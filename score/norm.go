package score

import "math/bits"

// Length norms (doc 13 §3). A norm is a single byte stored per document per field
// that encodes the field's token count in a lossy floating-point form, so that
// 10M documents across several fields cost megabytes rather than hundreds of
// megabytes. Scoring decodes the byte back to an approximate length through a
// 256-entry table.
//
// The spec's §3.2 snippet pairs a 5-bit-mantissa/3-bit-exponent decode with an
// encode that is not its inverse, and that layout caps the decoded length at 252
// tokens, which cannot represent the long fields the same section describes. We
// implement instead the established Lucene SmallFloat.byte4 scheme: a 3-bit
// mantissa with a 5-bit exponent and an implicit leading bit. It is an exact,
// self-inverse encoding for lengths 0..15, quantizes larger lengths with a
// relative step of about 1/8, and represents lengths up to roughly 2^31, which is
// what the doc's stated quantization table and length range actually require.

// EncodeNorm maps a field length in tokens to its one-byte norm. Lengths 0..7 are
// stored exactly; 8..15 are also exact; larger lengths keep three significant
// bits. Negative lengths clamp to 0.
func EncodeNorm(fieldLen int32) byte {
	if fieldLen <= 0 {
		return 0
	}
	if fieldLen < 8 {
		return byte(fieldLen)
	}
	// numBits is the position of the highest set bit, counting from 1.
	numBits := 32 - bits.LeadingZeros32(uint32(fieldLen))
	mantissa := (uint32(fieldLen) >> (numBits - 4)) & 0x07
	exponent := uint32(numBits - 4)
	return byte(((exponent + 1) << 3) | mantissa)
}

// DecodeNorm maps a norm byte back to its approximate field length. It is the
// inverse of EncodeNorm on the values EncodeNorm can produce.
func DecodeNorm(b byte) uint32 {
	bitsv := uint32(b)
	mantissa := bitsv & 0x07
	exponent := (bitsv >> 3) & 0x1F
	if exponent == 0 {
		return mantissa
	}
	return (mantissa | 0x08) << (exponent - 1)
}

// normDecodeTable is the precomputed byte->length table used on the scoring hot
// path to avoid the bit math per document.
var normDecodeTable [256]uint32

func init() {
	for i := range normDecodeTable {
		normDecodeTable[i] = DecodeNorm(byte(i))
	}
}

// NormLength returns the decoded length for a norm byte via the precomputed
// table.
func NormLength(b byte) uint32 { return normDecodeTable[b] }
