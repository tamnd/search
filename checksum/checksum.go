// Package checksum provides the CRC-32C (Castagnoli) checksum used throughout
// the .sx format (spec 2063 doc 02 §10, doc 03 §7). Every page header, the file
// header, and the meta pages carry a CRC-32C over a defined byte range.
//
// CRC-32C is the Castagnoli polynomial (0x1EDC6F41), distinct from the older
// IEEE/Ethernet polynomial (0x04C11DB7). The Go standard library implements it
// with the SSE4.2 CRC32 instruction on amd64 and the ARMv8 crc32cb instruction
// on arm64 when the hardware advertises them, falling back to a slicing-by-8
// software table otherwise. That gives memory-bandwidth checksum throughput on
// modern targets with zero external dependencies, so the format's claim of a
// hardware-accelerated CRC-32C is satisfied by the standard library alone; the
// spec's note about vendoring a crc32c package predates the standard library
// exposing crc32.Castagnoli and is not needed.
package checksum

import "hash/crc32"

// table is the Castagnoli table, computed once. crc32.Update with this table
// uses the hardware instruction when available.
var table = crc32.MakeTable(crc32.Castagnoli)

// Sum returns the CRC-32C of b.
func Sum(b []byte) uint32 {
	return crc32.Checksum(b, table)
}

// Verify reports whether the CRC-32C of b equals want.
func Verify(b []byte, want uint32) bool {
	return Sum(b) == want
}

// New returns a fresh CRC-32C hash.Hash32 for streaming computation over data
// that does not live in one contiguous slice.
func New() interface {
	Write(p []byte) (int, error)
	Sum32() uint32
	Reset()
} {
	return crc32.New(table)
}
