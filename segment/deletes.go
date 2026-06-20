package segment

import (
	"encoding/binary"
	"fmt"

	"github.com/tamnd/search/catalog"
)

// DeleteBitmap records which documents of one segment have been deleted. A
// segment is immutable once flushed, so a delete cannot touch it; the deletion
// state lives beside the segment under NSDeletionState, keyed by segment id, and
// is rewritten in its own write transaction. The bitmap is a dense bitset with
// one bit per doc-id in the segment's [baseDoc, maxDoc) range, indexed by
// (doc-id - baseDoc).
//
// The bitset is dense rather than a Roaring bitmap because a segment's doc-id
// range is bounded by the batch that produced it: a 1000-document batch needs
// 125 bytes, and a one-million-document compacted segment needs 125 KB, both of
// which are small next to the postings they describe. This matches the dense
// per-segment norm array and keeps the code simple. See spec 2063 doc 10 §8; the
// dense form is a documented deviation from the Roaring bitmap named there.
type DeleteBitmap struct {
	baseDoc uint32
	bits    []byte
	count   uint32
}

// NewDeleteBitmap returns an empty bitmap over the doc-id range [baseDoc,
// baseDoc+span).
func NewDeleteBitmap(baseDoc, span uint32) *DeleteBitmap {
	return &DeleteBitmap{baseDoc: baseDoc, bits: make([]byte, (span+7)/8)}
}

// Add marks docID deleted and reports whether the bit changed from clear to set.
// A doc-id outside the segment's range is ignored.
func (d *DeleteBitmap) Add(docID uint32) bool {
	if docID < d.baseDoc {
		return false
	}
	i := docID - d.baseDoc
	if int(i>>3) >= len(d.bits) {
		return false
	}
	mask := byte(1) << (i & 7)
	if d.bits[i>>3]&mask != 0 {
		return false
	}
	d.bits[i>>3] |= mask
	d.count++
	return true
}

// Contains reports whether docID is marked deleted.
func (d *DeleteBitmap) Contains(docID uint32) bool {
	if docID < d.baseDoc {
		return false
	}
	i := docID - d.baseDoc
	if int(i>>3) >= len(d.bits) {
		return false
	}
	return d.bits[i>>3]&(byte(1)<<(i&7)) != 0
}

// Count returns the number of deleted documents.
func (d *DeleteBitmap) Count() uint32 { return d.count }

// Empty reports whether no document is marked deleted.
func (d *DeleteBitmap) Empty() bool { return d.count == 0 }

// AppendTo appends every deleted global doc-id in ascending order to dst.
func (d *DeleteBitmap) AppendTo(dst []uint32) []uint32 {
	for i, b := range d.bits {
		for b != 0 {
			bit := b & -b
			pos := bitIndex(bit)
			dst = append(dst, d.baseDoc+uint32(i)*8+uint32(pos))
			b &^= bit
		}
	}
	return dst
}

// bitIndex returns the position of the single set bit in b (0..7).
func bitIndex(b byte) int {
	n := 0
	for b > 1 {
		b >>= 1
		n++
	}
	return n
}

// encode serializes the bitmap as the deleted count followed by the raw bits.
func (d *DeleteBitmap) encode() []byte {
	out := binary.AppendUvarint(nil, uint64(d.count))
	return append(out, d.bits...)
}

// decodeDeleteBitmap reverses encode, given the segment's base doc-id.
func decodeDeleteBitmap(baseDoc uint32, b []byte) (*DeleteBitmap, error) {
	count, m := binary.Uvarint(b)
	if m <= 0 {
		return nil, fmt.Errorf("segment: bad delete count")
	}
	bits := make([]byte, len(b)-m)
	copy(bits, b[m:])
	return &DeleteBitmap{baseDoc: baseDoc, bits: bits, count: uint32(count)}, nil
}

// LoadDeletes reads the delete bitmap for a segment, returning an empty bitmap
// over the segment's range when none has been written yet.
func LoadDeletes(kv KV, m *Meta) (*DeleteBitmap, error) {
	b, ok, err := kv.Get(catalog.NSDeletionState, metaKey(m.ID))
	if err != nil {
		return nil, err
	}
	span := uint32(0)
	if m.MaxDoc > m.BaseDoc {
		span = m.MaxDoc - m.BaseDoc
	}
	if !ok {
		return NewDeleteBitmap(m.BaseDoc, span), nil
	}
	return decodeDeleteBitmap(m.BaseDoc, b)
}

// StoreDeletes writes a segment's delete bitmap.
func StoreDeletes(kv KV, m *Meta, d *DeleteBitmap) error {
	return kv.Put(catalog.NSDeletionState, metaKey(m.ID), d.encode())
}
