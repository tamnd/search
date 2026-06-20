// Package page defines the on-disk format of a .sx file (spec 2063 doc 02): the
// little-endian primitive encodings, the file header at page 0, the two meta
// pages at pages 1 and 2, the common 32-byte page header carried by every other
// page, and the page-type taxonomy. Everything here is normative; the byte
// layouts are the storage contract, and a second implementation must reproduce
// them byte for byte.
//
// Endianness is little-endian throughout (doc 02 §1.4), unconditionally, on
// every platform. A file written on arm64 macOS reads identically on amd64
// Linux. There is no host-endianness mode.
package page

import "encoding/binary"

// Fixed-width little-endian helpers. These never allocate and panic only on a
// caller bug (buffer too small), which is a programming error, not a data error.

// PutU16 writes v at the start of b in little-endian order.
func PutU16(b []byte, v uint16) { binary.LittleEndian.PutUint16(b, v) }

// PutU32 writes v at the start of b in little-endian order.
func PutU32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }

// PutU64 writes v at the start of b in little-endian order.
func PutU64(b []byte, v uint64) { binary.LittleEndian.PutUint64(b, v) }

// U16 reads a little-endian uint16 from the start of b.
func U16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }

// U32 reads a little-endian uint32 from the start of b.
func U32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

// U64 reads a little-endian uint64 from the start of b.
func U64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

// AppendUvarint appends an unsigned LEB128 varint (the encoding encoding/binary
// uses) to dst and returns the extended slice.
func AppendUvarint(dst []byte, v uint64) []byte {
	return binary.AppendUvarint(dst, v)
}

// Uvarint decodes an unsigned LEB128 varint from b, returning the value and the
// number of bytes consumed, or an error on a malformed or short input.
func Uvarint(b []byte) (uint64, int, error) {
	v, n := binary.Uvarint(b)
	if n == 0 {
		return 0, 0, ErrShortBuffer
	}
	if n < 0 {
		return 0, 0, ErrBadVarint
	}
	return v, n, nil
}
