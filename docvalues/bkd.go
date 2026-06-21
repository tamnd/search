package docvalues

import (
	"encoding/binary"
	"fmt"
	"slices"
	"sort"
)

// The BKD/points index (doc 14 §8) answers numeric range predicates without
// scanning every document. For the 1D case the structure degenerates to the
// (value, docID) pair set sorted by value: a range [min,max] is the contiguous
// slice between the lower and upper bound, found by binary search in
// O(log N + K). Float and date fields feed their order-preserving int64 form, so
// one structure serves every numeric type.

const bkdPairLen = 12 // i64 value + u32 docID

// BKDWriter accumulates (value, docID) points during segment flush.
type BKDWriter struct {
	points []bkdPoint
}

type bkdPoint struct {
	value int64
	docID uint32
}

// NewBKDWriter returns an empty points writer.
func NewBKDWriter() *BKDWriter { return &BKDWriter{} }

// Add records one point. The docID is the segment-local doc index.
func (w *BKDWriter) Add(docID uint32, value int64) {
	w.points = append(w.points, bkdPoint{value: value, docID: docID})
}

// Len returns the number of points added.
func (w *BKDWriter) Len() int { return len(w.points) }

// Bytes serializes the points sorted ascending by value.
func (w *BKDWriter) Bytes() []byte {
	sort.Slice(w.points, func(i, j int) bool {
		if w.points[i].value != w.points[j].value {
			return w.points[i].value < w.points[j].value
		}
		return w.points[i].docID < w.points[j].docID
	})
	out := make([]byte, 0, 8+len(w.points)*bkdPairLen)
	out = binary.AppendUvarint(out, uint64(len(w.points)))
	for _, p := range w.points {
		var b [bkdPairLen]byte
		binary.LittleEndian.PutUint64(b[:8], uint64(p.value))
		binary.LittleEndian.PutUint32(b[8:], p.docID)
		out = append(out, b[:]...)
	}
	return out
}

// BKD is a read handle over a serialized points index.
type BKD struct {
	data  []byte // the bkdPairLen-stride pair array
	count int
}

// OpenBKD parses a points index written by BKDWriter.Bytes.
func OpenBKD(blob []byte) (*BKD, error) {
	n, m := binary.Uvarint(blob)
	if m <= 0 {
		return nil, fmt.Errorf("docvalues: bad bkd point count")
	}
	data := blob[m:]
	if len(data) < int(n)*bkdPairLen {
		return nil, fmt.Errorf("docvalues: truncated bkd points")
	}
	return &BKD{data: data, count: int(n)}, nil
}

// valueAt returns the value of the i-th sorted point.
func (t *BKD) valueAt(i int) int64 {
	return int64(binary.LittleEndian.Uint64(t.data[i*bkdPairLen:]))
}

// docAt returns the docID of the i-th sorted point.
func (t *BKD) docAt(i int) uint32 {
	return binary.LittleEndian.Uint32(t.data[i*bkdPairLen+8:])
}

// RangeSearch returns the doc indices whose value lies in [minVal, maxVal],
// sorted ascending by doc index (doc 14 §8.3). Pass math.MinInt64 / math.MaxInt64
// for a half-open bound.
func (t *BKD) RangeSearch(minVal, maxVal int64) []uint32 {
	if minVal > maxVal || t.count == 0 {
		return nil
	}
	lo := sort.Search(t.count, func(i int) bool { return t.valueAt(i) >= minVal })
	hi := sort.Search(t.count, func(i int) bool { return t.valueAt(i) > maxVal })
	if lo >= hi {
		return nil
	}
	out := make([]uint32, 0, hi-lo)
	for i := lo; i < hi; i++ {
		out = append(out, t.docAt(i))
	}
	slices.Sort(out)
	return out
}

// Count returns the number of points in the index.
func (t *BKD) Count() int { return t.count }
