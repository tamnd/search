package docvalues

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Numeric value encodings stored in the column header so the reader knows how to
// interpret Int64 vs Float64.
const (
	encRawInt   = 0 // value is a plain int64 (long, date, boolean)
	encSortable = 1 // value is the order-preserving transform of a float64
)

// NumericWriter accumulates one int64 per document during segment flush and
// serializes a block-encoded NUMERIC column (doc 14 §3).
type NumericWriter struct {
	values   []int64
	present  []bool
	encoding byte
}

// NewNumericWriter returns a writer for a span of docCount documents. If float
// is true, values handed to AddFloat are stored with the sortable-float
// transform so the column orders and ranges like a real float64.
func NewNumericWriter(docCount uint32, float bool) *NumericWriter {
	enc := byte(encRawInt)
	if float {
		enc = encSortable
	}
	return &NumericWriter{
		values:   make([]int64, docCount),
		present:  make([]bool, docCount),
		encoding: enc,
	}
}

// Set records a raw int64 value for doc index i (long, date, boolean).
func (w *NumericWriter) Set(i uint32, v int64) {
	w.values[i] = v
	w.present[i] = true
}

// SetFloat records a float64 value for doc index i, applying the sortable
// transform.
func (w *NumericWriter) SetFloat(i uint32, v float64) {
	w.values[i] = floatToSortable(v)
	w.present[i] = true
}

// Bytes serializes the column.
func (w *NumericWriter) Bytes() []byte {
	out := []byte{byte(KindNumeric), w.encoding}
	out = binary.AppendUvarint(out, uint64(len(w.values)))
	out = append(out, encodePresence(w.present)...)

	n := len(w.values)
	blockCount := (n + BlockSize - 1) / BlockSize
	out = binary.AppendUvarint(out, uint64(blockCount))
	for b := 0; b < n; b += BlockSize {
		end := min(b+BlockSize, n)
		block := encodeBlock(w.values[b:end])
		out = binary.AppendUvarint(out, uint64(len(block)))
		out = append(out, block...)
	}
	return out
}

// numericColumn is the read side of a NUMERIC column.
type numericColumn struct {
	docCount uint32
	encoding byte
	pres     presence
	blocks   [][]byte // one decoded-on-demand block slice per BlockSize docs
}

func openNumeric(blob []byte) (*numericColumn, error) {
	if len(blob) < 2 {
		return nil, fmt.Errorf("docvalues: truncated numeric header")
	}
	c := &numericColumn{encoding: blob[1]}
	p := 2
	dc, m := binary.Uvarint(blob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("docvalues: bad numeric doc count")
	}
	p += m
	c.docCount = uint32(dc)
	pres, used := decodePresenceN(blob[p:], c.docCount)
	c.pres = pres
	p += used
	bc, m := binary.Uvarint(blob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("docvalues: bad numeric block count")
	}
	p += m
	c.blocks = make([][]byte, 0, bc)
	for range bc {
		bl, m := binary.Uvarint(blob[p:])
		if m <= 0 {
			return nil, fmt.Errorf("docvalues: bad numeric block length")
		}
		p += m
		if p+int(bl) > len(blob) {
			return nil, fmt.Errorf("docvalues: truncated numeric block")
		}
		c.blocks = append(c.blocks, blob[p:p+int(bl)])
		p += int(bl)
	}
	return c, nil
}

// Kind reports the column's structural kind, KindNumeric.
func (c *numericColumn) Kind() ColumnKind { return KindNumeric }

// DocCount returns the number of documents the column spans.
func (c *numericColumn) DocCount() uint32 { return c.docCount }

// HasValue reports whether doc index i has a value.
func (c *numericColumn) HasValue(i uint32) bool { return c.pres.has(i) }

// Int64 returns the stored value for doc index i, or 0 for a missing doc.
func (c *numericColumn) Int64(i uint32) int64 {
	if !c.pres.has(i) {
		return 0
	}
	b := int(i) / BlockSize
	within := int(i) % BlockSize
	if b >= len(c.blocks) {
		return 0
	}
	return blockValueAt(c.blocks[b], within)
}

// Float64 returns the value decoded from the sortable-float transform, or the
// plain int64 value as a float64 for a raw-int column.
func (c *numericColumn) Float64(i uint32) float64 {
	v := c.Int64(i)
	if c.encoding == encSortable {
		return sortableToFloat(v)
	}
	return float64(v)
}

// FloatToSortable exposes the order-preserving float-to-int64 transform so the
// query layer can encode range bounds for a double field's BKD points index the
// same way the column stored them.
func FloatToSortable(v float64) int64 { return floatToSortable(v) }

// SortableToFloat reverses FloatToSortable.
func SortableToFloat(v int64) float64 { return sortableToFloat(v) }

// floatToSortable maps a float64 to an int64 whose signed order matches the
// float's numeric order (doc 14 §3.4). The doc-values column and the BKD index
// compare values as signed int64, so a non-negative float keeps its bit pattern
// (already a non-negative int64) and a negative float maps to the negation of
// its magnitude bits, which puts every negative below zero in magnitude order.
// NaN sorts before all finite values; math.MinInt64 is unreachable by any finite
// float, so it is reserved for NaN.
func floatToSortable(v float64) int64 {
	if math.IsNaN(v) {
		return math.MinInt64
	}
	b := math.Float64bits(v)
	if b>>63 == 0 {
		return int64(b)
	}
	return -int64(b & 0x7FFFFFFFFFFFFFFF)
}

// sortableToFloat reverses floatToSortable.
func sortableToFloat(v int64) float64 {
	if v == math.MinInt64 {
		return math.NaN()
	}
	if v >= 0 {
		return math.Float64frombits(uint64(v))
	}
	return math.Float64frombits(uint64(-v) | (1 << 63))
}
