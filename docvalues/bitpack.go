// Package docvalues is the columnar doc-values store (spec 2063 doc 14): a
// per-segment, per-field column keyed by doc-id that answers "what is the value
// of field F for document D" in O(1), the access pattern the inverted index
// cannot serve. Columns back sorting, faceting, aggregations, grouping, and
// numeric range filtering.
//
// Like the rest of the engine the store rides the catalog key/value seam rather
// than raw page extents (the same documented deviation the segment layer makes):
// each column serializes to one self-contained byte blob stored under
// NSSegDocValues keyed by (segment id, field name), and readers operate on that
// blob. The page-aligned column directory and footer of doc 14 §5 are folded
// into the blob header; the on-disk codecs, ordinal encoding, and BKD structure
// follow the spec.
package docvalues

import "math/bits"

// BlockSize is the number of documents per encoded numeric block (doc 14 §3.1).
const BlockSize = 16384

// packBits writes len(src) values into dst, each at bw bits, packed
// little-endian: the first value occupies bits [0,bw) of byte 0, the next bits
// [bw,2*bw), wrapping across byte boundaries without gaps (doc 14 §15.4). dst
// must hold at least ceil(len(src)*bw/8) bytes. A bit width of zero writes
// nothing (the values are all zero and recovered from context).
func packBits(dst []byte, src []uint64, bw uint) {
	if bw == 0 {
		return
	}
	var bitPos uint
	for _, v := range src {
		v &= (1 << bw) - 1
		byteIdx := bitPos >> 3
		shift := bitPos & 7
		dst[byteIdx] |= byte(v << shift)
		// Spill into the following bytes when the value crosses a boundary.
		written := 8 - shift
		for written < bw {
			byteIdx++
			dst[byteIdx] |= byte(v >> written)
			written += 8
		}
		bitPos += bw
	}
}

// unpackSingle extracts the value at index idx from a little-endian bit-packed
// array at bit width bw (doc 14 §15.4). It reads bit by bit so it stays correct
// for any width up to 64 and never runs off the end of src.
func unpackSingle(src []byte, bw uint, idx uint32) uint64 {
	if bw == 0 {
		return 0
	}
	bitPos := uint64(idx) * uint64(bw)
	var out uint64
	for i := range bw {
		p := bitPos + uint64(i)
		byteIdx := int(p >> 3)
		if byteIdx >= len(src) {
			break
		}
		bit := (src[byteIdx] >> (uint(p) & 7)) & 1
		out |= uint64(bit) << i
	}
	return out
}

// packedLen returns the byte length needed to pack count values at bw bits.
func packedLen(count int, bw uint) int {
	return (count*int(bw) + 7) / 8
}

// bitWidth returns the number of bits needed to represent the unsigned value v.
func bitWidth(v uint64) uint {
	return uint(bits.Len64(v))
}
