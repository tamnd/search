// Package postings is the block-PFOR postings codec (spec 2063 doc 09). A
// term's postings list is a sequence of (doc-id, term-frequency, positions)
// records. Doc-ids are delta-encoded and packed in fixed blocks of BlockSize
// documents using Patched Frame Of Reference (PFOR): values are packed at a
// chosen bit width and the few outliers ("exceptions") are patched in
// separately. When exceptions would dominate, the block falls back to a plain
// variable-length-integer stream. A per-block skip list lets a reader advance
// past whole blocks without decoding them.
//
// Positions are stored in a parallel byte stream as per-document delta runs; the
// doc block records, for each document, the byte offset of its positions, so a
// reader that has skipped to a block can still random-access any document's
// positions (doc 09 §6-7).
package postings

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

// BlockSize is the number of documents per PFOR block (doc 09 §3.1).
const BlockSize = 128

// SkipInterval is the number of blocks between higher-level skip entries
// (doc 09 §5.1). Level-0 records one entry per block; this is the fan-out used
// for the coarse level.
const SkipInterval = 64

// exceptionFallbackNum/Den express the exception-rate threshold above which a
// block uses the variable-length fallback instead of PFOR (doc 09 §3.6): more
// than 30% exceptions.
const (
	exceptionFallbackNum = 30
	exceptionFallbackDen = 100
)

// block codec modes, stored as the leading byte of an encoded block.
const (
	modePFOR   byte = 0
	modeVarint byte = 1
)

// pforEncode encodes up to BlockSize uint32 values. It chooses the cheaper of a
// PFOR frame (best bit width plus patched exceptions) and a plain varint stream.
func pforEncode(vals []uint32) []byte {
	if len(vals) == 0 {
		return []byte{modeVarint, 0}
	}
	b, exc := bestWidth(vals)
	pforCost := 2 + packedLen(len(vals), b) + exc + exceptionVarintCost(vals, b)
	varintCost := 1 + varintLen(vals)

	overThreshold := exc*exceptionFallbackDen > len(vals)*exceptionFallbackNum
	if overThreshold || varintCost < pforCost {
		return encodeVarint(vals)
	}
	return encodePFOR(vals, b)
}

// pforDecode decodes n values produced by pforEncode.
func pforDecode(data []byte, n int) ([]uint32, error) {
	if n == 0 {
		return nil, nil
	}
	if len(data) < 1 {
		return nil, fmt.Errorf("postings: empty block")
	}
	switch data[0] {
	case modePFOR:
		return decodePFOR(data[1:], n)
	case modeVarint:
		return decodeVarint(data[1:], n)
	default:
		return nil, fmt.Errorf("postings: unknown block mode 0x%02x", data[0])
	}
}

// bestWidth returns the bit width minimizing total PFOR cost and the resulting
// exception count at that width.
func bestWidth(vals []uint32) (width, exceptions int) {
	bestB := 32
	bestCost := 1 << 62
	for b := 0; b <= 32; b++ {
		exc := 0
		for _, v := range vals {
			if b < 32 && v >= (uint32(1)<<uint(b)) {
				exc++
			}
		}
		cost := packedLen(len(vals), b) + exc + excHighBitsCost(vals, b)
		if cost < bestCost {
			bestCost = cost
			bestB = b
		}
	}
	exc := 0
	for _, v := range vals {
		if bestB < 32 && v >= (uint32(1)<<uint(bestB)) {
			exc++
		}
	}
	return bestB, exc
}

// encodePFOR writes a PFOR block: [mode][bitWidth][excCount][packed][excPos...][excHigh varints].
func encodePFOR(vals []uint32, b int) []byte {
	out := []byte{modePFOR, byte(b)}
	var excPos []byte
	var excHigh []byte
	excCount := 0
	for i, v := range vals {
		if b < 32 && v >= (uint32(1)<<uint(b)) {
			excCount++
			excPos = append(excPos, byte(i))
			high := v >> uint(b)
			excHigh = binary.AppendUvarint(excHigh, uint64(high))
		}
	}
	out = append(out, byte(excCount))
	out = append(out, packBits(vals, b)...)
	out = append(out, excPos...)
	out = append(out, excHigh...)
	return out
}

// decodePFOR reverses encodePFOR. data excludes the leading mode byte.
func decodePFOR(data []byte, n int) ([]uint32, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("postings: short pfor header")
	}
	b := int(data[0])
	if b > 32 {
		return nil, fmt.Errorf("postings: bad bit width %d", b)
	}
	excCount := int(data[1])
	p := 2
	pl := packedLen(n, b)
	if p+pl > len(data) {
		return nil, fmt.Errorf("postings: truncated packed data")
	}
	out := unpackBits(data[p:p+pl], n, b)
	p += pl
	if p+excCount > len(data) {
		return nil, fmt.Errorf("postings: truncated exception positions")
	}
	positions := data[p : p+excCount]
	p += excCount
	for _, pos := range positions {
		high, m := binary.Uvarint(data[p:])
		if m <= 0 {
			return nil, fmt.Errorf("postings: bad exception high bits")
		}
		p += m
		if int(pos) >= n {
			return nil, fmt.Errorf("postings: exception position %d out of range %d", pos, n)
		}
		out[pos] |= uint32(high) << uint(b)
	}
	return out, nil
}

// encodeVarint writes a plain varint block.
func encodeVarint(vals []uint32) []byte {
	out := []byte{modeVarint}
	for _, v := range vals {
		out = binary.AppendUvarint(out, uint64(v))
	}
	return out
}

// decodeVarint reverses encodeVarint. data excludes the leading mode byte.
func decodeVarint(data []byte, n int) ([]uint32, error) {
	out := make([]uint32, n)
	p := 0
	for i := range n {
		v, m := binary.Uvarint(data[p:])
		if m <= 0 {
			return nil, fmt.Errorf("postings: truncated varint at value %d", i)
		}
		out[i] = uint32(v)
		p += m
	}
	return out, nil
}

// packBits packs the low b bits of each value into a byte slice.
func packBits(vals []uint32, b int) []byte {
	if b == 0 {
		return nil
	}
	out := make([]byte, packedLen(len(vals), b))
	bitPos := 0
	for _, v := range vals {
		val := uint64(v) & ((uint64(1) << uint(b)) - 1)
		for i := range b {
			if val&(uint64(1)<<uint(i)) != 0 {
				out[bitPos>>3] |= 1 << uint(bitPos&7)
			}
			bitPos++
		}
	}
	return out
}

// unpackBits reverses packBits, reading n values of width b.
func unpackBits(data []byte, n, b int) []uint32 {
	out := make([]uint32, n)
	if b == 0 {
		return out
	}
	bitPos := 0
	for i := range n {
		var val uint64
		for j := range b {
			if data[bitPos>>3]&(1<<uint(bitPos&7)) != 0 {
				val |= uint64(1) << uint(j)
			}
			bitPos++
		}
		out[i] = uint32(val)
	}
	return out
}

// packedLen returns the byte length of n values packed at b bits each.
func packedLen(n, b int) int {
	return (n*b + 7) / 8
}

// excHighBitsCost estimates the byte cost of exception high bits at width b.
func excHighBitsCost(vals []uint32, b int) int {
	if b >= 32 {
		return 0
	}
	total := 0
	for _, v := range vals {
		if v >= (uint32(1) << uint(b)) {
			total += uvarintLen(uint64(v >> uint(b)))
		}
	}
	return total
}

// exceptionVarintCost is excHighBitsCost reused under its call-site name.
func exceptionVarintCost(vals []uint32, b int) int { return excHighBitsCost(vals, b) }

// varintLen returns the total byte length of vals as a varint stream.
func varintLen(vals []uint32) int {
	total := 0
	for _, v := range vals {
		total += uvarintLen(uint64(v))
	}
	return total
}

func uvarintLen(v uint64) int {
	if v == 0 {
		return 1
	}
	return (bits.Len64(v) + 6) / 7
}
