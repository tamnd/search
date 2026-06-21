package docvalues

import (
	"encoding/binary"
	"fmt"
	"slices"
)

// SortedNumericWriter accumulates a set of int64 values per document and
// serializes a SORTED_NUMERIC column: each doc's values are stored sorted
// ascending, with a per-doc cumulative count for random access (doc 14 §2.1,
// §6.3). This is the numeric analogue of SORTED_SET and backs multi-valued
// numeric fields and the multi-value sort modes (min, max, avg, sum, median).
type SortedNumericWriter struct {
	docCount uint32
	values   [][]int64 // values[i] is the value set for doc i
	float    bool
}

// NewSortedNumericWriter returns a writer for docCount documents. When float is
// true, values handed to AddFloat are stored with the sortable-float transform.
func NewSortedNumericWriter(docCount uint32, float bool) *SortedNumericWriter {
	return &SortedNumericWriter{docCount: docCount, values: make([][]int64, docCount), float: float}
}

// Add records one int64 value for doc index i.
func (w *SortedNumericWriter) Add(i uint32, v int64) {
	w.values[i] = append(w.values[i], v)
}

// AddFloat records one float64 value for doc index i, applying the sortable
// transform so the stored int64 order matches the float order.
func (w *SortedNumericWriter) AddFloat(i uint32, v float64) {
	w.values[i] = append(w.values[i], floatToSortable(v))
}

// Bytes serializes the column.
func (w *SortedNumericWriter) Bytes() []byte {
	enc := byte(encRawInt)
	if w.float {
		enc = encSortable
	}
	present := make([]bool, w.docCount)
	counts := make([]uint32, w.docCount)
	var all []int64
	for i, set := range w.values {
		if len(set) == 0 {
			continue
		}
		present[i] = true
		s := append([]int64(nil), set...)
		slices.Sort(s)
		counts[i] = uint32(len(s))
		all = append(all, s...)
	}

	out := []byte{byte(KindSortedNum), enc}
	out = binary.AppendUvarint(out, uint64(w.docCount))
	out = append(out, encodePresence(present)...)
	out = binary.AppendUvarint(out, uint64(len(all)))
	var cum uint32
	for _, c := range counts {
		out = binary.AppendUvarint(out, uint64(cum))
		cum += c
	}
	out = binary.AppendUvarint(out, uint64(cum))
	// Values are delta-from-previous within the flat array is not used here; the
	// flat array is block-encoded with the same codec the NUMERIC column uses so
	// the per-block codec selection still applies.
	blockCount := (len(all) + BlockSize - 1) / BlockSize
	out = binary.AppendUvarint(out, uint64(blockCount))
	for b := 0; b < len(all); b += BlockSize {
		end := min(b+BlockSize, len(all))
		block := encodeBlock(all[b:end])
		out = binary.AppendUvarint(out, uint64(len(block)))
		out = append(out, block...)
	}
	return out
}

// sortedNumericColumn is the read side of a SORTED_NUMERIC column.
type sortedNumericColumn struct {
	docCount uint32
	encoding byte
	pres     presence
	cum      []uint32
	blocks   [][]byte
}

func openSortedNumeric(blob []byte) (*sortedNumericColumn, error) {
	if len(blob) < 2 {
		return nil, fmt.Errorf("docvalues: truncated sorted-numeric header")
	}
	c := &sortedNumericColumn{encoding: blob[1]}
	p := 2
	dc, m := binary.Uvarint(blob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("docvalues: bad sorted-numeric doc count")
	}
	p += m
	c.docCount = uint32(dc)
	pres, used := decodePresenceN(blob[p:], c.docCount)
	c.pres = pres
	p += used
	total, m := binary.Uvarint(blob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("docvalues: bad sorted-numeric total")
	}
	p += m
	c.cum = make([]uint32, c.docCount+1)
	for i := range c.cum {
		v, m := binary.Uvarint(blob[p:])
		if m <= 0 {
			return nil, fmt.Errorf("docvalues: bad sorted-numeric cum count")
		}
		p += m
		c.cum[i] = uint32(v)
	}
	_ = total
	bc, m := binary.Uvarint(blob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("docvalues: bad sorted-numeric block count")
	}
	p += m
	c.blocks = make([][]byte, 0, bc)
	for range bc {
		bl, m := binary.Uvarint(blob[p:])
		if m <= 0 {
			return nil, fmt.Errorf("docvalues: bad sorted-numeric block length")
		}
		p += m
		if p+int(bl) > len(blob) {
			return nil, fmt.Errorf("docvalues: truncated sorted-numeric block")
		}
		c.blocks = append(c.blocks, blob[p:p+int(bl)])
		p += int(bl)
	}
	return c, nil
}

// Kind reports the column's structural kind, KindSortedNum.
func (c *sortedNumericColumn) Kind() ColumnKind { return KindSortedNum }

// DocCount returns the number of documents the column spans.
func (c *sortedNumericColumn) DocCount() uint32 { return c.docCount }

// HasValue reports whether doc index i has at least one value.
func (c *sortedNumericColumn) HasValue(i uint32) bool { return c.pres.has(i) }

// valueAtFlat returns the j-th value of the flat value array.
func (c *sortedNumericColumn) valueAtFlat(j uint32) int64 {
	b := int(j) / BlockSize
	within := int(j) % BlockSize
	if b >= len(c.blocks) {
		return 0
	}
	return blockValueAt(c.blocks[b], within)
}

// Values returns the sorted int64 values for doc index i.
func (c *sortedNumericColumn) Values(i uint32) []int64 {
	if !c.pres.has(i) || int(i)+1 >= len(c.cum) {
		return nil
	}
	start, end := c.cum[i], c.cum[i+1]
	out := make([]int64, 0, end-start)
	for j := start; j < end; j++ {
		out = append(out, c.valueAtFlat(j))
	}
	return out
}

// Floats returns the values for doc index i decoded as float64 when the column
// holds the sortable-float transform.
func (c *sortedNumericColumn) Floats(i uint32) []float64 {
	vals := c.Values(i)
	out := make([]float64, len(vals))
	for k, v := range vals {
		if c.encoding == encSortable {
			out[k] = sortableToFloat(v)
		} else {
			out[k] = float64(v)
		}
	}
	return out
}
