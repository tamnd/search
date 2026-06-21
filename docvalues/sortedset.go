package docvalues

import (
	"encoding/binary"
	"fmt"
	"slices"
)

// SortedSetWriter accumulates a set of keyword values per document and
// serializes a SORTED_SET column: a shared sorted ordinal dictionary, a packed
// concatenation of each doc's sorted ordinals, and the per-doc cumulative count
// for random access (doc 14 §4.5).
type SortedSetWriter struct {
	docCount uint32
	values   [][][]byte // values[i] is the set of keywords for doc i
}

// NewSortedSetWriter returns a writer for docCount documents.
func NewSortedSetWriter(docCount uint32) *SortedSetWriter {
	return &SortedSetWriter{docCount: docCount, values: make([][][]byte, docCount)}
}

// Add records one keyword value for doc index i. Duplicate values for the same
// doc collapse to one ordinal at serialization time.
func (w *SortedSetWriter) Add(i uint32, term []byte) {
	w.values[i] = append(w.values[i], append([]byte(nil), term...))
}

// Bytes serializes the column.
func (w *SortedSetWriter) Bytes() []byte {
	flat := make([][]byte, 0)
	for _, set := range w.values {
		flat = append(flat, set...)
	}
	dict, ordOf := buildOrdDict(flat)
	bw := ordBitWidth(len(dict))

	present := make([]bool, w.docCount)
	counts := make([]uint32, w.docCount)
	var allOrds []uint64
	for i, set := range w.values {
		if len(set) == 0 {
			continue
		}
		present[i] = true
		// Deduplicate and sort the ordinals for this doc.
		uniq := make([]uint32, 0, len(set))
		seen := make(map[uint32]struct{}, len(set))
		for _, t := range set {
			o := ordOf[string(t)]
			if _, ok := seen[o]; ok {
				continue
			}
			seen[o] = struct{}{}
			uniq = append(uniq, o)
		}
		slices.Sort(uniq)
		counts[i] = uint32(len(uniq))
		for _, o := range uniq {
			allOrds = append(allOrds, uint64(o))
		}
	}

	out := []byte{byte(KindSortedSet)}
	out = binary.AppendUvarint(out, uint64(w.docCount))
	out = append(out, encodePresence(present)...)
	out = binary.AppendUvarint(out, uint64(len(dict)))
	out = append(out, byte(bw))
	// Cumulative counts: docCount+1 entries, cum[0]=0.
	out = binary.AppendUvarint(out, uint64(len(allOrds)))
	var cum uint32
	for _, c := range counts {
		out = binary.AppendUvarint(out, uint64(cum))
		cum += c
	}
	out = binary.AppendUvarint(out, uint64(cum))
	packed := make([]byte, packedLen(len(allOrds), bw))
	packBits(packed, allOrds, bw)
	out = append(out, packed...)
	out = appendDict(out, dict)
	return out
}

// sortedSetColumn is the read side of a SORTED_SET column.
type sortedSetColumn struct {
	docCount uint32
	pres     presence
	ordCount uint32
	bw       uint
	cum      []uint32 // cumulative ordinal counts, docCount+1 entries
	packed   []byte
	dict     ordDict
}

func openSortedSet(blob []byte) (*sortedSetColumn, error) {
	c := &sortedSetColumn{}
	p := 1
	dc, m := binary.Uvarint(blob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("docvalues: bad sorted-set doc count")
	}
	p += m
	c.docCount = uint32(dc)
	pres, used := decodePresenceN(blob[p:], c.docCount)
	c.pres = pres
	p += used
	oc, m := binary.Uvarint(blob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("docvalues: bad sorted-set ord count")
	}
	p += m
	c.ordCount = uint32(oc)
	if p >= len(blob) {
		return nil, fmt.Errorf("docvalues: truncated sorted-set header")
	}
	c.bw = uint(blob[p])
	p++
	total, m := binary.Uvarint(blob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("docvalues: bad sorted-set total")
	}
	p += m
	c.cum = make([]uint32, c.docCount+1)
	for i := range c.cum {
		v, m := binary.Uvarint(blob[p:])
		if m <= 0 {
			return nil, fmt.Errorf("docvalues: bad sorted-set cum count")
		}
		p += m
		c.cum[i] = uint32(v)
	}
	plen := packedLen(int(total), c.bw)
	if p+plen > len(blob) {
		return nil, fmt.Errorf("docvalues: truncated sorted-set ordinals")
	}
	c.packed = blob[p : p+plen]
	p += plen
	d, err := openDict(blob[p:])
	if err != nil {
		return nil, err
	}
	c.dict = d
	return c, nil
}

func (c *sortedSetColumn) Kind() ColumnKind       { return KindSortedSet }
func (c *sortedSetColumn) DocCount() uint32       { return c.docCount }
func (c *sortedSetColumn) HasValue(i uint32) bool { return c.pres.has(i) }
func (c *sortedSetColumn) OrdCount() uint32       { return c.ordCount }
func (c *sortedSetColumn) LookupOrd(ord uint32) []byte {
	return c.dict.lookup(ord)
}

func (c *sortedSetColumn) OrdinalsFor(i uint32) []uint32 {
	if !c.pres.has(i) || int(i)+1 >= len(c.cum) {
		return nil
	}
	start, end := c.cum[i], c.cum[i+1]
	out := make([]uint32, 0, end-start)
	for j := start; j < end; j++ {
		out = append(out, uint32(unpackSingle(c.packed, c.bw, j)))
	}
	return out
}
