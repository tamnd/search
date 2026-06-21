package docvalues

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// SortedWriter accumulates one keyword value per document and serializes a
// SORTED column: a lexicographically sorted ordinal dictionary plus a bit-packed
// per-doc ordinal array (doc 14 §4).
type SortedWriter struct {
	docCount uint32
	values   [][]byte // values[i] is the keyword for doc i, nil if missing
}

// NewSortedWriter returns a writer for docCount documents.
func NewSortedWriter(docCount uint32) *SortedWriter {
	return &SortedWriter{docCount: docCount, values: make([][]byte, docCount)}
}

// Set records a keyword value for doc index i. The bytes are copied.
func (w *SortedWriter) Set(i uint32, term []byte) {
	w.values[i] = append([]byte(nil), term...)
}

// Bytes serializes the column.
func (w *SortedWriter) Bytes() []byte {
	dict, ordOf := buildOrdDict(w.values)
	bw := ordBitWidth(len(dict))

	present := make([]bool, w.docCount)
	ords := make([]uint64, w.docCount)
	for i, v := range w.values {
		if v != nil {
			present[i] = true
			ords[i] = uint64(ordOf[string(v)])
		}
	}

	out := []byte{byte(KindSorted)}
	out = binary.AppendUvarint(out, uint64(w.docCount))
	out = append(out, encodePresence(present)...)
	out = binary.AppendUvarint(out, uint64(len(dict)))
	out = append(out, byte(bw))
	packed := make([]byte, packedLen(int(w.docCount), bw))
	packBits(packed, ords, bw)
	out = append(out, packed...)
	out = appendDict(out, dict)
	return out
}

// sortedColumn is the read side of a SORTED column.
type sortedColumn struct {
	docCount uint32
	pres     presence
	ordCount uint32
	bw       uint
	packed   []byte
	dict     ordDict
}

func openSorted(blob []byte) (*sortedColumn, error) {
	c := &sortedColumn{}
	p := 1
	dc, m := binary.Uvarint(blob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("docvalues: bad sorted doc count")
	}
	p += m
	c.docCount = uint32(dc)
	pres, used := decodePresenceN(blob[p:], c.docCount)
	c.pres = pres
	p += used
	oc, m := binary.Uvarint(blob[p:])
	if m <= 0 {
		return nil, fmt.Errorf("docvalues: bad sorted ord count")
	}
	p += m
	c.ordCount = uint32(oc)
	if p >= len(blob) {
		return nil, fmt.Errorf("docvalues: truncated sorted header")
	}
	c.bw = uint(blob[p])
	p++
	plen := packedLen(int(c.docCount), c.bw)
	if p+plen > len(blob) {
		return nil, fmt.Errorf("docvalues: truncated sorted ordinals")
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

// Kind reports the column's structural kind, KindSorted.
func (c *sortedColumn) Kind() ColumnKind { return KindSorted }

// DocCount returns the number of documents the column spans.
func (c *sortedColumn) DocCount() uint32 { return c.docCount }

// HasValue reports whether doc index i has a value.
func (c *sortedColumn) HasValue(i uint32) bool { return c.pres.has(i) }

// OrdCount returns the number of distinct ordinals in the dictionary.
func (c *sortedColumn) OrdCount() uint32 { return c.ordCount }

// OrdAt returns the ordinal for doc index i, or -1 for a missing doc.
func (c *sortedColumn) OrdAt(i uint32) int32 {
	if !c.pres.has(i) {
		return -1
	}
	return int32(unpackSingle(c.packed, c.bw, i))
}

// LookupOrd returns the keyword bytes for an ordinal.
func (c *sortedColumn) LookupOrd(ord uint32) []byte { return c.dict.lookup(ord) }

// buildOrdDict returns the sorted distinct non-nil values and a map from value
// to its ordinal.
func buildOrdDict(values [][]byte) ([][]byte, map[string]uint32) {
	seen := make(map[string]struct{})
	for _, v := range values {
		if v != nil {
			seen[string(v)] = struct{}{}
		}
	}
	dict := make([][]byte, 0, len(seen))
	for k := range seen {
		dict = append(dict, []byte(k))
	}
	sort.Slice(dict, func(i, j int) bool { return bytes.Compare(dict[i], dict[j]) < 0 })
	ordOf := make(map[string]uint32, len(dict))
	for i, t := range dict {
		ordOf[string(t)] = uint32(i)
	}
	return dict, ordOf
}

// ordBitWidth returns the bit width needed to store ordinals for ordCount
// distinct values. A column with zero or one distinct value still needs one bit
// so the packed array has a definite length.
func ordBitWidth(ordCount int) uint {
	if ordCount <= 1 {
		return 1
	}
	return bitWidth(uint64(ordCount - 1))
}

// ordDict is a read-only view over a serialized ordinal dictionary.
type ordDict struct {
	offsets []uint32
	blob    []byte
}

// appendDict serializes a dictionary: the count, the cumulative offsets, then
// the concatenated term bytes.
func appendDict(out []byte, dict [][]byte) []byte {
	out = binary.AppendUvarint(out, uint64(len(dict)))
	var off uint32
	for _, t := range dict {
		out = binary.AppendUvarint(out, uint64(off))
		off += uint32(len(t))
	}
	out = binary.AppendUvarint(out, uint64(off)) // sentinel end offset
	for _, t := range dict {
		out = append(out, t...)
	}
	return out
}

// openDict parses a dictionary written by appendDict.
func openDict(buf []byte) (ordDict, error) {
	p := 0
	n, m := binary.Uvarint(buf[p:])
	if m <= 0 {
		return ordDict{}, fmt.Errorf("docvalues: bad dict count")
	}
	p += m
	offsets := make([]uint32, n+1)
	for i := range offsets {
		v, m := binary.Uvarint(buf[p:])
		if m <= 0 {
			return ordDict{}, fmt.Errorf("docvalues: bad dict offset")
		}
		p += m
		offsets[i] = uint32(v)
	}
	blob := buf[p:]
	if n > 0 && int(offsets[n]) > len(blob) {
		return ordDict{}, fmt.Errorf("docvalues: truncated dict blob")
	}
	return ordDict{offsets: offsets, blob: blob}, nil
}

func (d ordDict) lookup(ord uint32) []byte {
	if int(ord)+1 >= len(d.offsets) {
		return nil
	}
	return d.blob[d.offsets[ord]:d.offsets[ord+1]]
}
