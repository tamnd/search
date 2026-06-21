package docvalues

import (
	"encoding/binary"
	"slices"
)

// Block codec tags (doc 14 §3.1). The encoder computes the byte cost of each
// applicable codec for a block's values and writes the cheapest one.
const (
	codecConstant = 0x01 // all values identical
	codecDelta    = 0x02 // first value verbatim, rest delta-from-previous, bit-packed
	codecGCD      = 0x03 // subtract block-min, divide by gcd, bit-pack
	codecTable    = 0x04 // dictionary of <=256 distinct values + one ordinal byte per doc
	codecBitpack  = 0x05 // frame of reference: subtract block-min, bit-pack at min width
)

// blockHeaderLen is the fixed prefix every encoded block carries:
// codec(1) + bitWidth(1) + docCount(2) + blockMin(8) + gcd(8).
const blockHeaderLen = 20

// encodeBlock serializes one block of values with the cheapest applicable codec
// and returns the header-plus-payload bytes (doc 14 §3.3). vals must hold at
// least one and at most BlockSize entries.
func encodeBlock(vals []int64) []byte {
	n := len(vals)
	bmin, bmax := vals[0], vals[0]
	for _, v := range vals {
		if v < bmin {
			bmin = v
		}
		if v > bmax {
			bmax = v
		}
	}

	if bmin == bmax {
		return blockHeader(codecConstant, 0, n, bmin, 1)
	}

	rang := uint64(bmax - bmin)
	bw := bitWidth(rang)

	// Candidate 1: frame-of-reference bit-pack.
	bestCodec := byte(codecBitpack)
	bestBW := bw
	bestGCD := int64(1)
	bestCost := blockHeaderLen + packedLen(n, bw)

	// Candidate 2: GCD scaling.
	if g := blockGCD(vals, bmin); g > 1 {
		gbw := bitWidth(rang / uint64(g))
		if cost := blockHeaderLen + packedLen(n, gbw); cost < bestCost {
			bestCodec, bestBW, bestGCD, bestCost = codecGCD, gbw, g, cost
		}
	}

	// Candidate 3: dictionary table when the distinct count is small.
	if dict, ok := distinctTable(vals); ok {
		if cost := blockHeaderLen + 1 + 8*len(dict) + n; cost < bestCost {
			return encodeTable(vals, dict, n)
		}
	}

	// Candidate 4: delta-from-previous, useful for monotone sequences whose
	// value range is wide but whose step range is narrow.
	if dbw, ok := deltaWidth(vals); ok && dbw < bestBW {
		if cost := blockHeaderLen + packedLen(n, dbw); cost < bestCost {
			return encodeDelta(vals, dbw, n)
		}
	}

	out := blockHeader(bestCodec, bestBW, n, bmin, bestGCD)
	packed := make([]byte, packedLen(n, bestBW))
	scaled := make([]uint64, n)
	for i, v := range vals {
		scaled[i] = uint64(v-bmin) / uint64(bestGCD)
	}
	packBits(packed, scaled, bestBW)
	return append(out, packed...)
}

// blockHeader writes the fixed block header.
func blockHeader(codec byte, bw uint, n int, bmin, gcd int64) []byte {
	out := make([]byte, blockHeaderLen)
	out[0] = codec
	out[1] = byte(bw)
	binary.LittleEndian.PutUint16(out[2:], uint16(n))
	binary.LittleEndian.PutUint64(out[4:], uint64(bmin))
	binary.LittleEndian.PutUint64(out[12:], uint64(gcd))
	return out
}

// encodeTable writes a dictionary-coded block: a sorted dictionary of distinct
// values followed by one ordinal byte per document.
func encodeTable(vals []int64, dict []int64, n int) []byte {
	out := blockHeader(codecTable, 8, n, dict[0], 1)
	out = append(out, byte(len(dict)))
	for _, d := range dict {
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], uint64(d))
		out = append(out, b[:]...)
	}
	index := make(map[int64]byte, len(dict))
	for i, d := range dict {
		index[d] = byte(i)
	}
	for _, v := range vals {
		out = append(out, index[v])
	}
	return out
}

// encodeDelta writes a delta-from-previous block: the first value lives in
// blockMin, every later value is stored as (V[i]-V[i-1]) bit-packed at dbw bits.
// Deltas are non-negative because the encoder only chooses DELTA for monotone
// non-decreasing sequences.
func encodeDelta(vals []int64, dbw uint, n int) []byte {
	out := blockHeader(codecDelta, dbw, n, vals[0], 1)
	packed := make([]byte, packedLen(n-1, dbw))
	deltas := make([]uint64, n-1)
	for i := 1; i < n; i++ {
		deltas[i-1] = uint64(vals[i] - vals[i-1])
	}
	packBits(packed, deltas, dbw)
	return append(out, packed...)
}

// blockGCD returns the greatest common divisor of {V[i]-blockMin}, or 0 when
// every difference is zero (which never happens once a block is non-constant).
func blockGCD(vals []int64, bmin int64) int64 {
	var g int64
	for _, v := range vals {
		g = gcd64(g, v-bmin)
		if g == 1 {
			return 1
		}
	}
	return g
}

func gcd64(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

// distinctTable returns the sorted distinct values of vals when there are at
// most 256 of them, the condition for the TABLE codec.
func distinctTable(vals []int64) ([]int64, bool) {
	seen := make(map[int64]struct{}, 64)
	for _, v := range vals {
		seen[v] = struct{}{}
		if len(seen) > 256 {
			return nil, false
		}
	}
	out := make([]int64, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sortInt64(out)
	return out, true
}

// deltaWidth reports the bit width of the largest step when vals is monotone
// non-decreasing, and whether DELTA applies at all.
func deltaWidth(vals []int64) (uint, bool) {
	var maxDelta uint64
	for i := 1; i < len(vals); i++ {
		if vals[i] < vals[i-1] {
			return 0, false
		}
		d := uint64(vals[i] - vals[i-1])
		if d > maxDelta {
			maxDelta = d
		}
	}
	return bitWidth(maxDelta), true
}

// blockValueAt decodes a single value from an encoded block at in-block index i.
func blockValueAt(block []byte, i int) int64 {
	codec := block[0]
	bw := uint(block[1])
	bmin := int64(binary.LittleEndian.Uint64(block[4:]))
	gcd := int64(binary.LittleEndian.Uint64(block[12:]))
	switch codec {
	case codecConstant:
		return bmin
	case codecTable:
		dictLen := int(block[blockHeaderLen])
		dictStart := blockHeaderLen + 1
		ordStart := dictStart + 8*dictLen
		ord := int(block[ordStart+i])
		return int64(binary.LittleEndian.Uint64(block[dictStart+8*ord:]))
	case codecDelta:
		return decodeBlock(block)[i]
	default: // GCD and BITPACK
		payload := block[blockHeaderLen:]
		return bmin + int64(unpackSingle(payload, bw, uint32(i))*uint64(gcd))
	}
}

// decodeBlock decodes every value in an encoded block. The leading u16 of the
// header gives the count.
func decodeBlock(block []byte) []int64 {
	codec := block[0]
	bw := uint(block[1])
	n := int(binary.LittleEndian.Uint16(block[2:]))
	bmin := int64(binary.LittleEndian.Uint64(block[4:]))
	gcd := int64(binary.LittleEndian.Uint64(block[12:]))
	out := make([]int64, n)
	switch codec {
	case codecConstant:
		for i := range out {
			out[i] = bmin
		}
	case codecTable:
		dictLen := int(block[blockHeaderLen])
		dictStart := blockHeaderLen + 1
		ordStart := dictStart + 8*dictLen
		for i := 0; i < n; i++ {
			ord := int(block[ordStart+i])
			out[i] = int64(binary.LittleEndian.Uint64(block[dictStart+8*ord:]))
		}
	case codecDelta:
		payload := block[blockHeaderLen:]
		out[0] = bmin
		for i := 1; i < n; i++ {
			out[i] = out[i-1] + int64(unpackSingle(payload, bw, uint32(i-1)))
		}
	default: // GCD and BITPACK
		payload := block[blockHeaderLen:]
		for i := 0; i < n; i++ {
			out[i] = bmin + int64(unpackSingle(payload, bw, uint32(i))*uint64(gcd))
		}
	}
	return out
}

// sortInt64 sorts a slice of values ascending.
func sortInt64(a []int64) { slices.Sort(a) }
