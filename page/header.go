package page

import (
	"github.com/tamnd/search/checksum"
)

// Header is the 128-byte structured file header at the start of page 0 (doc 02
// §2.1). It is the single source of truth for the file's geometry. It does not
// carry the common page header; it is its own top-level structure at byte 0,
// with its own CRC-32C over bytes 0..123 stored at offset 124. It round-trips
// exactly through Marshal and ParseHeader.
type Header struct {
	Magic             [16]byte
	FormatVersion     uint32   // offset 16
	PageSizeCode      uint16   // offset 20: log2(pageSize) - 12
	FileFlags         uint16   // offset 22: see Flag* constants
	CreationTxnID     uint64   // offset 24: txn id of the initial commit (usually 1)
	CompatFeatures    uint64   // offset 32: required feature flags
	OptionalFeatures  uint64   // offset 40: optional feature flags
	DefaultTextCodec  uint32   // offset 48
	DefaultVecCodec   uint32   // offset 52
	DefaultStoreCodec uint32   // offset 56
	DefaultDVCodec    uint32   // offset 60
	SchemaVersion     uint32   // offset 64
	PageSizeActual    uint32   // offset 68: redundant byte count
	FileCreateEpoch   uint64   // offset 72: unix seconds, informational
	ApplicationID     uint32   // offset 80
	SectorSize        uint32   // offset 84
	Reserved          [16]byte // offset 88
	CreatorString     [20]byte // offset 104: ASCII, NUL-padded
	// HeaderCRC32C is at offset 124, computed over bytes 0..123. It is not a
	// struct input; Marshal computes it and ParseHeader validates it.
}

// header field offsets, named so the layout is auditable against the spec table.
const (
	hMagic        = 0
	hFormatVer    = 16
	hPageSizeCode = 20
	hFileFlags    = 22
	hCreationTxn  = 24
	hCompatFeat   = 32
	hOptionalFeat = 40
	hTextCodec    = 48
	hVecCodec     = 52
	hStoreCodec   = 56
	hDVCodec      = 60
	hSchemaVer    = 64
	hPageSizeAct  = 68
	hCreateEpoch  = 72
	hAppID        = 80
	hSectorSize   = 84
	hReserved     = 88
	hCreatorStr   = 104
	hCRC          = 124
)

// CreatorString returns the conventional creator string for a given build tag,
// NUL-padded to 20 bytes. Used at file creation.
func CreatorString(buildTag string) [20]byte {
	var s [20]byte
	copy(s[:], buildTag)
	return s
}

// NewHeader builds a header for a freshly created file with the given page size,
// codecs at their defaults, and the supplied creation metadata. It returns
// ErrInvalidPageSize for an illegal page size.
func NewHeader(pageSize uint32, creationTxnID uint64, epoch int64, sectorSize uint32, creator [20]byte) (Header, error) {
	code, ok := PageSizeCode(pageSize)
	if !ok {
		return Header{}, ErrInvalidPageSize
	}
	return Header{
		Magic:             Magic,
		FormatVersion:     FormatVersion,
		PageSizeCode:      code,
		CreationTxnID:     creationTxnID,
		DefaultTextCodec:  1,
		DefaultVecCodec:   1,
		DefaultStoreCodec: 1,
		DefaultDVCodec:    1,
		SchemaVersion:     0,
		PageSizeActual:    pageSize,
		FileCreateEpoch:   uint64(epoch),
		SectorSize:        sectorSize,
		CreatorString:     creator,
	}, nil
}

// PageSize returns the page size in bytes implied by the header's code.
func (h Header) PageSize() uint32 {
	sz, _ := PageSizeFromCode(h.PageSizeCode)
	return sz
}

// Marshal serializes the header into a HeaderSize-byte slice with the CRC-32C
// computed over bytes 0..123 and stored at offset 124.
func (h Header) Marshal() []byte {
	b := make([]byte, HeaderSize)
	copy(b[hMagic:], h.Magic[:])
	PutU32(b[hFormatVer:], h.FormatVersion)
	PutU16(b[hPageSizeCode:], h.PageSizeCode)
	PutU16(b[hFileFlags:], h.FileFlags)
	PutU64(b[hCreationTxn:], h.CreationTxnID)
	PutU64(b[hCompatFeat:], h.CompatFeatures)
	PutU64(b[hOptionalFeat:], h.OptionalFeatures)
	PutU32(b[hTextCodec:], h.DefaultTextCodec)
	PutU32(b[hVecCodec:], h.DefaultVecCodec)
	PutU32(b[hStoreCodec:], h.DefaultStoreCodec)
	PutU32(b[hDVCodec:], h.DefaultDVCodec)
	PutU32(b[hSchemaVer:], h.SchemaVersion)
	PutU32(b[hPageSizeAct:], h.PageSizeActual)
	PutU64(b[hCreateEpoch:], h.FileCreateEpoch)
	PutU32(b[hAppID:], h.ApplicationID)
	PutU32(b[hSectorSize:], h.SectorSize)
	copy(b[hReserved:], h.Reserved[:])
	copy(b[hCreatorStr:], h.CreatorString[:])
	PutU32(b[hCRC:], checksum.Sum(b[:hCRC]))
	return b
}

// ParseHeader parses and validates a header from b, which must be at least
// HeaderSize bytes. It performs the verification policy of doc 02 §2.5 in order,
// returning the first failure as a typed error and never panicking on bad data.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, ErrFileTooShort
	}
	var h Header
	copy(h.Magic[:], b[hMagic:hMagic+16])
	if h.Magic != Magic {
		return Header{}, ErrNotSxFile
	}
	h.FormatVersion = U32(b[hFormatVer:])
	if h.FormatVersion > FormatVersion {
		return Header{}, ErrUnsupportedVersion
	}
	h.PageSizeCode = U16(b[hPageSizeCode:])
	pageSize, ok := PageSizeFromCode(h.PageSizeCode)
	if !ok {
		return Header{}, ErrInvalidPageSize
	}
	h.FileFlags = U16(b[hFileFlags:])
	h.CreationTxnID = U64(b[hCreationTxn:])
	h.CompatFeatures = U64(b[hCompatFeat:])
	h.OptionalFeatures = U64(b[hOptionalFeat:])
	h.DefaultTextCodec = U32(b[hTextCodec:])
	h.DefaultVecCodec = U32(b[hVecCodec:])
	h.DefaultStoreCodec = U32(b[hStoreCodec:])
	h.DefaultDVCodec = U32(b[hDVCodec:])
	h.SchemaVersion = U32(b[hSchemaVer:])
	h.PageSizeActual = U32(b[hPageSizeAct:])
	if h.PageSizeActual != pageSize {
		return Header{}, ErrHeaderCorrupt
	}
	h.FileCreateEpoch = U64(b[hCreateEpoch:])
	h.ApplicationID = U32(b[hAppID:])
	h.SectorSize = U32(b[hSectorSize:])
	copy(h.Reserved[:], b[hReserved:hReserved+16])
	copy(h.CreatorString[:], b[hCreatorStr:hCreatorStr+20])
	if !checksum.Verify(b[:hCRC], U32(b[hCRC:])) {
		return Header{}, ErrHeaderChecksumFail
	}
	// compat_features: any bit we do not recognize forces a refusal.
	if h.CompatFeatures&^knownCompatFeatures != 0 {
		return Header{}, ErrIncompatibleFormat
	}
	return h, nil
}

// knownCompatFeatures is the set of compat_features bits this build can honor.
// A set bit the build cannot honor forces ErrIncompatibleFormat (doc 02 §2.3).
// At S0 the engine serves neither HNSW (CF_HNSW) nor inline WAL (CF_INLINE_WAL),
// so the honored set is empty: a freshly created file writes compat_features=0
// and opens cleanly, while any file demanding a not-yet-built feature is
// refused rather than silently mishandled. Later milestones widen this set.
const knownCompatFeatures uint64 = 0
