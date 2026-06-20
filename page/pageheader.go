package page

import "github.com/tamnd/search/checksum"

// PageHeader is the common 32-byte header carried by every page except page 0
// (doc 02 §4.1). It is immediately followed by the page body, whose structure
// depends on PageType. The header carries two CRC-32C fields covering disjoint
// regions: page_crc32c over the body (bytes 32..end) and header_crc32c over the
// first 28 header bytes, so header corruption and body corruption are detected
// independently.
type PageHeader struct {
	Type            PageType // offset 0
	Flags           uint8    // offset 1: see PF* constants
	ReservedHdr     uint16   // offset 2: must be zero
	PageTxnID       uint64   // offset 4: txn that last modified this page
	OverflowNext    uint32   // offset 12: next overflow page, or NoPage32
	FreeSpaceOffset uint16   // offset 16: slotted-page first free byte
	SlotCount       uint16   // offset 18: slotted-page occupied slots
	BodyLength      uint32   // offset 20: used body bytes (excludes the 32B header)
	// PageCRC32C at offset 24 over bytes 32..(pageSize-1) and HeaderCRC32C at
	// offset 28 over bytes 0..27 are computed by WritePage and checked by
	// ReadHeader/VerifyPage.
}

const (
	phType         = 0
	phFlags        = 1
	phReservedHdr  = 2
	phPageTxnID    = 4
	phOverflowNext = 12
	phFreeSpaceOff = 16
	phSlotCount    = 18
	phBodyLength   = 20
	phPageCRC      = 24
	phHeaderCRC    = 28
)

// NewPageHeader returns a header for a page of the given type whose body spans
// the whole page body and carries no overflow link. Slotted-page callers adjust
// FreeSpaceOffset, SlotCount, and BodyLength before writing.
func NewPageHeader(typ PageType, pageSize uint32, txnID uint64) PageHeader {
	return PageHeader{
		Type:         typ,
		PageTxnID:    txnID,
		OverflowNext: NoPage32,
		BodyLength:   uint32(BodySize(pageSize)),
	}
}

// WritePage serializes h into the front of page p (a full pageSize-byte slice)
// and computes both checksums. The caller has already written the body bytes
// into p[32:]; any unused body bytes must be zero so the body checksum is
// deterministic (doc 02 §4.3). The header_crc32c is computed last, after
// page_crc32c is in place, matching the verification order.
func WritePage(p []byte, h PageHeader) {
	p[phType] = byte(h.Type)
	p[phFlags] = h.Flags
	PutU16(p[phReservedHdr:], h.ReservedHdr)
	PutU64(p[phPageTxnID:], h.PageTxnID)
	PutU32(p[phOverflowNext:], h.OverflowNext)
	PutU16(p[phFreeSpaceOff:], h.FreeSpaceOffset)
	PutU16(p[phSlotCount:], h.SlotCount)
	PutU32(p[phBodyLength:], h.BodyLength)
	PutU32(p[phPageCRC:], checksum.Sum(p[PageHeaderSize:]))
	PutU32(p[phHeaderCRC:], checksum.Sum(p[:phPageCRC]))
}

// ReadHeader parses the common page header from the front of page p and verifies
// the header_crc32c. It returns ErrPageChecksumFail if the header checksum does
// not validate; in that case the page type and flags cannot be trusted and the
// body must not be interpreted. Body verification is a separate VerifyPage call.
func ReadHeader(p []byte) (PageHeader, error) {
	if len(p) < PageHeaderSize {
		return PageHeader{}, ErrShortBuffer
	}
	if !checksum.Verify(p[:phPageCRC], U32(p[phHeaderCRC:])) {
		return PageHeader{}, ErrPageChecksumFail
	}
	return PageHeader{
		Type:            PageType(p[phType]),
		Flags:           p[phFlags],
		ReservedHdr:     U16(p[phReservedHdr:]),
		PageTxnID:       U64(p[phPageTxnID:]),
		OverflowNext:    U32(p[phOverflowNext:]),
		FreeSpaceOffset: U16(p[phFreeSpaceOff:]),
		SlotCount:       U16(p[phSlotCount:]),
		BodyLength:      U32(p[phBodyLength:]),
	}, nil
}

// VerifyBody checks the page_crc32c over the body bytes of page p. It is called
// after ReadHeader so the body is trusted only once both checksums pass.
func VerifyBody(p []byte) bool {
	if len(p) < PageHeaderSize {
		return false
	}
	return checksum.Verify(p[PageHeaderSize:], U32(p[phPageCRC:]))
}

// Body returns the page body slice (bytes 32..end) of page p for reading or
// writing in place.
func Body(p []byte) []byte { return p[PageHeaderSize:] }
