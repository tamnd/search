package page

import "errors"

// Format and geometry constants (doc 02 §1, §2).

// Magic is the first 16 bytes of every .sx file: the ASCII string
// "tamndsearch fmt1" exactly, no NUL and no newline. Any byte deviation means
// the file is not a .sx file.
var Magic = [16]byte{'t', 'a', 'm', 'n', 'd', 's', 'e', 'a', 'r', 'c', 'h', ' ', 'f', 'm', 't', '1'}

// FormatVersion is the on-disk format version. A reader that does not recognize
// the file's version must refuse to open it (doc 02 §2.5 step 3).
const FormatVersion uint32 = 1

// EngineVersion is this build's engine version, encoded as the high byte for the
// major and the low byte for the minor (0x0100 = 1.0). It is compared against a
// file's FormatVersionCompatMin to decide whether the build is new enough to open
// it (doc 02 §13, the 1.0 format freeze).
const EngineVersion uint16 = 0x0100

// FormatCompatMin is the FormatVersionCompatMin a freshly created file records:
// the minimum engine version that can open it. At the 1.0 freeze this is 0x0100.
// It increments only on a breaking format change (changing an existing page-type
// layout or removing one); backward-compatible extensions leave it unchanged.
const FormatCompatMin uint16 = 0x0100

// Page-size bounds. A page is a power of two in [MinPageSize, MaxPageSize]; the
// default is 16384 (16 KiB). The size is fixed at file creation and recorded in
// the header as a log2 code (doc 02 §2.1 page_size_code).
const (
	MinPageSize     = 4096
	MaxPageSize     = 65536
	DefaultPageSize = 16384
)

// HeaderSize is the size of the structured file header at the start of page 0.
// The remainder of page 0 is reserved zeros.
const HeaderSize = 128

// MetaSize is the size of the structured meta-page fields at the start of a meta
// page. The remainder of the page is reserved zeros.
const MetaSize = 96

// PageHeaderSize is the size of the common page header carried by every page
// except page 0. The page body starts immediately after it.
const PageHeaderSize = 32

// NoPage32 is the 32-bit sentinel for an absent page pointer (doc 02 §3.2).
const NoPage32 uint32 = 0xFFFFFFFF

// PageID is a zero-based page index into the main file. Page 0 is the file
// header, pages 1 and 2 are the meta pages.
type PageID uint64

// PageType tags what a page holds. The numeric values are part of the storage
// contract (doc 02 §5.1).
type PageType uint8

const (
	// PageMeta is a meta page (pages 1 and 2).
	PageMeta PageType = 0x01
	// PageBTreeInterior is an interior node of the catalog B+tree.
	PageBTreeInterior PageType = 0x02
	// PageBTreeLeaf is a leaf node of the catalog B+tree.
	PageBTreeLeaf PageType = 0x03
	// PageFreelistTrunk is a freelist trunk page.
	PageFreelistTrunk PageType = 0x04
	// PageFreelistLeaf is a freelist leaf page.
	PageFreelistLeaf PageType = 0x05
	// PageOverflowHead is the head of an overflow chain.
	PageOverflowHead PageType = 0x06
	// PageOverflowCont is a continuation of an overflow chain.
	PageOverflowCont PageType = 0x07
	// PageSegmentExtent is the first page of a segment extent.
	PageSegmentExtent PageType = 0x08
	// PageExtentCont is a continuation of a segment extent.
	PageExtentCont PageType = 0x09
	// PageFSTBlock is an FST block (term dictionary).
	PageFSTBlock PageType = 0x0A
	// PagePostingsBlock is a postings block.
	PagePostingsBlock PageType = 0x0B
	// PagePositionsBlock is a positions block.
	PagePositionsBlock PageType = 0x0C
	// PageDocValuesBlock is a doc-values column block.
	PageDocValuesBlock PageType = 0x0D
	// PageDocStoreBlock is a stored-field / doc-store block.
	PageDocStoreBlock PageType = 0x0E
	// PageHNSWNodeBlock is an HNSW graph node block.
	PageHNSWNodeBlock PageType = 0x0F
	// PageWALFrame is an inline WAL frame page.
	PageWALFrame PageType = 0x10
	// PageRoaringBlock is a roaring bitmap block (live-doc filter).
	PageRoaringBlock PageType = 0x11
	// PageSkipList is a skip-list index block.
	PageSkipList PageType = 0x12
	// PageCompactionTomb is a transient compaction tombstone.
	PageCompactionTomb PageType = 0x13
	// PageReserved is reserved for pager internal use.
	PageReserved PageType = 0xFE
	// PageZero is an uninitialized / zeroed page.
	PageZero PageType = 0xFF
)

// Page flags (doc 02 §4.2), carried in the common header's page_flags byte.
const (
	PFDirty        uint8 = 1 << 0 // in the write buffer, not yet on disk (in-memory only)
	PFCOW          uint8 = 1 << 1 // a copy-on-write clone of an older page
	PFOverflowHead uint8 = 1 << 2 // head page of an overflow chain
	PFContinuation uint8 = 1 << 3 // continuation page of an overflow chain
	PFCompressed   uint8 = 1 << 4 // page body is compressed
)

// File flags (doc 02 §2.2), carried in the header's file_flags field.
const (
	FlagWALInline uint16 = 1 << 0 // WAL stored inline in the main file
	FlagReadOnly  uint16 = 1 << 1 // file is a read-only distributable
	FlagEncrypted uint16 = 1 << 2 // page bodies are encrypted (reserved, v2)
	FlagNoMmap    uint16 = 1 << 3 // hint: do not mmap this file
)

// Errors returned by the format layer. All are typed and none panic on bad data
// (panics are reserved for caller bugs such as a too-small buffer).
var (
	ErrShortBuffer        = errors.New("search/page: short buffer")
	ErrBadVarint          = errors.New("search/page: malformed varint")
	ErrFileTooShort       = errors.New("search/page: file shorter than the header")
	ErrNotSxFile          = errors.New("search/page: not a .sx file (bad magic)")
	ErrUnsupportedVersion = errors.New("search/page: unsupported format version")
	ErrInvalidPageSize    = errors.New("search/page: invalid page size")
	ErrHeaderCorrupt      = errors.New("search/page: header self-consistency check failed")
	ErrHeaderChecksumFail = errors.New("search/page: header checksum mismatch")
	ErrIncompatibleFormat = errors.New("search/page: file requires an unsupported feature")
	ErrTooNew             = errors.New("search/page: file requires a newer engine version")
	ErrPageChecksumFail   = errors.New("search/page: page checksum mismatch")
	ErrBothMetaInvalid    = errors.New("search/page: both meta pages are invalid")
)

// ValidPageSize reports whether s is a legal page size: a power of two within
// the bounds.
func ValidPageSize(s uint32) bool {
	if s < MinPageSize || s > MaxPageSize {
		return false
	}
	return s&(s-1) == 0
}

// PageSizeCode encodes a page size as log2(pageSize) - 12 (doc 02 §2.1). It
// returns the code and true for a legal size, or 0 and false otherwise.
func PageSizeCode(pageSize uint32) (uint16, bool) {
	if !ValidPageSize(pageSize) {
		return 0, false
	}
	code := 0
	for s := pageSize; s > 4096; s >>= 1 {
		code++
	}
	return uint16(code), true
}

// PageSizeFromCode decodes a page_size_code back to bytes: 4096 << code. It
// returns the size and true for a code in [0, 4], or 0 and false otherwise.
func PageSizeFromCode(code uint16) (uint32, bool) {
	if code > 4 {
		return 0, false
	}
	return uint32(4096) << code, true
}

// BodySize is the usable page-body length for a page of the given size: the
// bytes after the 32-byte common header.
func BodySize(pageSize uint32) int { return int(pageSize) - PageHeaderSize }
