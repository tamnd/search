package page

import "github.com/tamnd/search/checksum"

// Meta is the 96-byte structured meta page (doc 02 §3.2) that lives at pages 1
// and 2. The two meta pages are double-buffered: a commit writes the new state
// into the stale slot and fsyncs, which atomically publishes the new version.
// On open, the meta page with the higher valid txn id is current.
//
// Unlike the file header, a meta page is a normal page: it carries the common
// 32-byte page header, and the 96 structured bytes begin at the page body
// (offset 32). The meta's own CRC over its first 92 bytes (offset 92) is
// independent of, and in addition to, the common page header's page_crc32c.
type Meta struct {
	TxnID               uint64 // offset 0: 0 means the slot is empty/invalid
	CatalogRoot         uint32 // offset 8: NoPage32 if empty
	FreelistRoot        uint32 // offset 12: NoPage32 if empty
	FreelistCount       uint64 // offset 16
	SegmentManifestRoot uint32 // offset 24
	PageCount           uint32 // offset 28: high-water mark
	DocCount            uint64 // offset 32
	LastDocID           uint64 // offset 40
	SchemaVersion       uint32 // offset 48
	SegmentCount        uint32 // offset 52
	WALSalt             uint64 // offset 56
	WALFrameCount       uint64 // offset 64
	DeletedDocCount     uint64 // offset 72
	WriteTxnCounter     uint64 // offset 80
	ReservedA           uint32 // offset 88
	// MetaCRC32C is at offset 92 over bytes 0..91; computed by Marshal, checked
	// by ParseMeta.
}

const (
	mTxnID         = 0
	mCatalogRoot   = 8
	mFreelistRoot  = 12
	mFreelistCount = 16
	mSegManRoot    = 24
	mPageCount     = 28
	mDocCount      = 32
	mLastDocID     = 40
	mSchemaVer     = 48
	mSegCount      = 52
	mWALSalt       = 56
	mWALFrameCount = 64
	mDeletedDocs   = 72
	mWriteTxnCtr   = 80
	mReservedA     = 88
	mCRC           = 92
)

// NewMeta returns the initial meta for a freshly created, empty file: the first
// commit (txn id 1), an empty catalog, and the given high-water mark and WAL
// salt. CatalogRoot and FreelistRoot default to NoPage32 (empty); the caller
// overrides CatalogRoot once the empty catalog root page is allocated.
func NewMeta(txnID uint64, pageCount uint32, walSalt uint64) Meta {
	return Meta{
		TxnID:           txnID,
		CatalogRoot:     NoPage32,
		FreelistRoot:    NoPage32,
		PageCount:       pageCount,
		WALSalt:         walSalt,
		WriteTxnCounter: txnID,
	}
}

// MarshalInto writes the 96 structured meta bytes into the front of dst (which
// must be at least MetaSize bytes), including the meta CRC at offset 92. dst is
// the page body; the caller is responsible for the surrounding common page
// header and for zeroing the rest of the body.
func (m Meta) MarshalInto(dst []byte) {
	clear(dst[:MetaSize])
	PutU64(dst[mTxnID:], m.TxnID)
	PutU32(dst[mCatalogRoot:], m.CatalogRoot)
	PutU32(dst[mFreelistRoot:], m.FreelistRoot)
	PutU64(dst[mFreelistCount:], m.FreelistCount)
	PutU32(dst[mSegManRoot:], m.SegmentManifestRoot)
	PutU32(dst[mPageCount:], m.PageCount)
	PutU64(dst[mDocCount:], m.DocCount)
	PutU64(dst[mLastDocID:], m.LastDocID)
	PutU32(dst[mSchemaVer:], m.SchemaVersion)
	PutU32(dst[mSegCount:], m.SegmentCount)
	PutU64(dst[mWALSalt:], m.WALSalt)
	PutU64(dst[mWALFrameCount:], m.WALFrameCount)
	PutU64(dst[mDeletedDocs:], m.DeletedDocCount)
	PutU64(dst[mWriteTxnCtr:], m.WriteTxnCounter)
	PutU32(dst[mReservedA:], m.ReservedA)
	PutU32(dst[mCRC:], checksum.Sum(dst[:mCRC]))
}

// ParseMeta parses a meta page from the 96 structured bytes at the front of b
// (the page body). It returns the meta and true if the meta CRC validates, or a
// zero meta and false if the slot is corrupt or uninitialized. A false result
// is not an error: the meta-selection algorithm expects to see invalid slots
// (an unwritten meta page 2, a torn commit) and falls back to the other slot.
func ParseMeta(b []byte) (Meta, bool) {
	if len(b) < MetaSize {
		return Meta{}, false
	}
	if !checksum.Verify(b[:mCRC], U32(b[mCRC:])) {
		return Meta{}, false
	}
	var m Meta
	m.TxnID = U64(b[mTxnID:])
	m.CatalogRoot = U32(b[mCatalogRoot:])
	m.FreelistRoot = U32(b[mFreelistRoot:])
	m.FreelistCount = U64(b[mFreelistCount:])
	m.SegmentManifestRoot = U32(b[mSegManRoot:])
	m.PageCount = U32(b[mPageCount:])
	m.DocCount = U64(b[mDocCount:])
	m.LastDocID = U64(b[mLastDocID:])
	m.SchemaVersion = U32(b[mSchemaVer:])
	m.SegmentCount = U32(b[mSegCount:])
	m.WALSalt = U64(b[mWALSalt:])
	m.WALFrameCount = U64(b[mWALFrameCount:])
	m.DeletedDocCount = U64(b[mDeletedDocs:])
	m.WriteTxnCounter = U64(b[mWriteTxnCtr:])
	m.ReservedA = U32(b[mReservedA:])
	return m, true
}

// SelectMeta implements the meta-page selection algorithm (doc 02 §3.3): pick
// the valid slot with the higher txn id. It returns the winning meta, the page
// number it came from (1 or 2), and an error only if both slots are invalid.
func SelectMeta(m1 Meta, ok1 bool, m2 Meta, ok2 bool) (Meta, PageID, error) {
	switch {
	case !ok1 && !ok2:
		return Meta{}, 0, ErrBothMetaInvalid
	case !ok1:
		return m2, 2, nil
	case !ok2:
		return m1, 1, nil
	case m1.TxnID >= m2.TxnID:
		return m1, 1, nil
	default:
		return m2, 2, nil
	}
}
