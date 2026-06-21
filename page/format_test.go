package page

import (
	"bytes"
	"testing"

	"github.com/tamnd/search/checksum"
)

// crc32cOf is a test shorthand for the format CRC over a byte range.
func crc32cOf(b []byte) uint32 { return checksum.Sum(b) }

func TestHeaderRoundTrip(t *testing.T) {
	h, err := NewHeader(DefaultPageSize, 1, 0, 512, CreatorString(BuildTagForTest))
	if err != nil {
		t.Fatal(err)
	}
	b := h.Marshal()
	if len(b) != HeaderSize {
		t.Fatalf("header length = %d, want %d", len(b), HeaderSize)
	}
	got, err := ParseHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != h {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, h)
	}
	if got.PageSize() != DefaultPageSize {
		t.Fatalf("PageSize() = %d, want %d", got.PageSize(), DefaultPageSize)
	}
}

// BuildTagForTest is the creator tag used by format tests so they do not depend
// on the pager's BuildTag var.
const BuildTagForTest = "search/0.1.0"

// TestHeaderByteLayout pins the documented offsets (doc 02 §2.1 field table).
// Note: the spec's hex dump in §2.4 places the creator string four bytes late
// and would collide with the CRC; the field table (creator at 104, len 20, CRC
// at 124) is internally consistent and is the layout implemented here.
func TestHeaderByteLayout(t *testing.T) {
	h, err := NewHeader(DefaultPageSize, 1, 0, 512, CreatorString("search/0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	b := h.Marshal()

	wantMagic := []byte("tamndsearch fmt1")
	if !bytes.Equal(b[0:16], wantMagic) {
		t.Fatalf("magic = %q, want %q", b[0:16], wantMagic)
	}
	if U32(b[16:]) != 1 {
		t.Fatalf("format_version = %d, want 1", U32(b[16:]))
	}
	if U16(b[20:]) != 2 {
		t.Fatalf("page_size_code = %d, want 2 (16384)", U16(b[20:]))
	}
	if U16(b[22:]) != 0 {
		t.Fatalf("file_flags = %d, want 0", U16(b[22:]))
	}
	if U64(b[24:]) != 1 {
		t.Fatalf("creation_txn_id = %d, want 1", U64(b[24:]))
	}
	if U32(b[48:]) != 1 || U32(b[52:]) != 1 || U32(b[56:]) != 1 || U32(b[60:]) != 1 {
		t.Fatalf("default codecs not all 1")
	}
	if U32(b[68:]) != DefaultPageSize {
		t.Fatalf("page_size_actual = %d, want %d", U32(b[68:]), DefaultPageSize)
	}
	if U32(b[84:]) != 512 {
		t.Fatalf("sector_size = %d, want 512", U32(b[84:]))
	}
	if U16(b[88:]) != 0x0100 {
		t.Fatalf("format_version_compat_min = %#x, want 0x0100", U16(b[88:]))
	}
	if !bytes.HasPrefix(b[104:124], []byte("search/0.1.0")) {
		t.Fatalf("creator_string = %q, want prefix search/0.1.0", b[104:124])
	}
	// header CRC must validate over bytes 0..123.
	if got, want := U32(b[124:]), crc32cOf(b[:124]); got != want {
		t.Fatalf("header_crc32c = %#x, want %#x", got, want)
	}
}

func TestHeaderBadMagic(t *testing.T) {
	h, _ := NewHeader(DefaultPageSize, 1, 0, 512, CreatorString("x"))
	b := h.Marshal()
	b[3] ^= 0xFF
	// Recompute CRC so the magic check, not the checksum check, is what fires.
	PutU32(b[124:], crc32cOf(b[:124]))
	if _, err := ParseHeader(b); err != ErrNotSxFile {
		t.Fatalf("err = %v, want ErrNotSxFile", err)
	}
}

func TestHeaderUnsupportedVersion(t *testing.T) {
	h, _ := NewHeader(DefaultPageSize, 1, 0, 512, CreatorString("x"))
	b := h.Marshal()
	PutU32(b[16:], 0xFF)
	PutU32(b[124:], crc32cOf(b[:124]))
	if _, err := ParseHeader(b); err != ErrUnsupportedVersion {
		t.Fatalf("err = %v, want ErrUnsupportedVersion", err)
	}
}

func TestHeaderChecksumFail(t *testing.T) {
	h, _ := NewHeader(DefaultPageSize, 1, 0, 512, CreatorString("x"))
	b := h.Marshal()
	b[80] ^= 0x01 // flip a byte inside the CRC-covered region, leave CRC stale
	if _, err := ParseHeader(b); err != ErrHeaderChecksumFail {
		t.Fatalf("err = %v, want ErrHeaderChecksumFail", err)
	}
}

func TestHeaderIncompatibleFeature(t *testing.T) {
	h, _ := NewHeader(DefaultPageSize, 1, 0, 512, CreatorString("x"))
	h.CompatFeatures = 1 // CF_HNSW, not honored at S0
	b := h.Marshal()
	if _, err := ParseHeader(b); err != ErrIncompatibleFormat {
		t.Fatalf("err = %v, want ErrIncompatibleFormat", err)
	}
}

func TestHeaderTooNew(t *testing.T) {
	h, _ := NewHeader(DefaultPageSize, 1, 0, 512, CreatorString("x"))
	b := h.Marshal()
	// Demand an engine one minor version past this build and re-seal the header.
	PutU16(b[88:], EngineVersion+1)
	PutU32(b[124:], crc32cOf(b[:124]))
	if _, err := ParseHeader(b); err != ErrTooNew {
		t.Fatalf("err = %v, want ErrTooNew", err)
	}
}

func TestHeaderCompatMinRoundTrip(t *testing.T) {
	h, _ := NewHeader(DefaultPageSize, 1, 0, 512, CreatorString("x"))
	if h.FormatVersionCompatMin != FormatCompatMin {
		t.Fatalf("compat_min = %#x, want %#x", h.FormatVersionCompatMin, FormatCompatMin)
	}
	got, err := ParseHeader(h.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got.FormatVersionCompatMin != FormatCompatMin {
		t.Fatalf("parsed compat_min = %#x, want %#x", got.FormatVersionCompatMin, FormatCompatMin)
	}
}

// TestHeaderOldFileOpens confirms a file written before the compat field existed
// (those bytes zero) still opens: zero is below every engine version.
func TestHeaderOldFileOpens(t *testing.T) {
	h, _ := NewHeader(DefaultPageSize, 1, 0, 512, CreatorString("x"))
	b := h.Marshal()
	PutU16(b[88:], 0)
	PutU32(b[124:], crc32cOf(b[:124]))
	if _, err := ParseHeader(b); err != nil {
		t.Fatalf("zero compat_min should open, got %v", err)
	}
}

func TestPageSizeCodes(t *testing.T) {
	cases := []struct {
		size uint32
		code uint16
	}{
		{4096, 0}, {8192, 1}, {16384, 2}, {32768, 3}, {65536, 4},
	}
	for _, c := range cases {
		got, ok := PageSizeCode(c.size)
		if !ok || got != c.code {
			t.Fatalf("PageSizeCode(%d) = %d,%v want %d", c.size, got, ok, c.code)
		}
		back, ok := PageSizeFromCode(c.code)
		if !ok || back != c.size {
			t.Fatalf("PageSizeFromCode(%d) = %d,%v want %d", c.code, back, ok, c.size)
		}
	}
	if _, ok := PageSizeCode(3000); ok {
		t.Fatal("PageSizeCode(3000) should be invalid")
	}
	if _, ok := PageSizeCode(12288); ok {
		t.Fatal("PageSizeCode(12288) should be invalid (not power of two)")
	}
}

func TestMetaRoundTripAndSelect(t *testing.T) {
	body := make([]byte, DefaultPageSize)
	m := NewMeta(7, 42, 0xDEADBEEF)
	m.CatalogRoot = 3
	m.MarshalInto(body)
	got, ok := ParseMeta(body)
	if !ok {
		t.Fatal("ParseMeta returned not-ok for a freshly written meta")
	}
	if got != m {
		t.Fatalf("meta round-trip mismatch:\n got %+v\nwant %+v", got, m)
	}

	// A zeroed slot is invalid.
	zero := make([]byte, DefaultPageSize)
	if _, ok := ParseMeta(zero); ok {
		t.Fatal("ParseMeta of zeros should be invalid")
	}

	// Selection picks the higher txn id; an invalid slot is skipped.
	hi := NewMeta(9, 50, 1)
	_, slot, err := SelectMeta(m, true, hi, true)
	if err != nil || slot != 2 {
		t.Fatalf("SelectMeta picked slot %d (err %v), want slot 2 (higher txn)", slot, err)
	}
	_, slot, err = SelectMeta(m, true, Meta{}, false)
	if err != nil || slot != 1 {
		t.Fatalf("SelectMeta picked slot %d (err %v), want slot 1 (other invalid)", slot, err)
	}
	if _, _, err := SelectMeta(Meta{}, false, Meta{}, false); err != ErrBothMetaInvalid {
		t.Fatalf("both invalid: err = %v, want ErrBothMetaInvalid", err)
	}
}

func TestPageHeaderRoundTripAndChecksum(t *testing.T) {
	const ps = DefaultPageSize
	buf := make([]byte, ps)
	hdr := NewPageHeader(PageBTreeLeaf, ps, 5)
	hdr.SlotCount = 3
	hdr.FreeSpaceOffset = 100
	// write a body pattern
	body := Body(buf)
	for i := range body {
		body[i] = byte(i)
	}
	WritePage(buf, hdr)

	got, err := ReadHeader(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != PageBTreeLeaf || got.PageTxnID != 5 || got.SlotCount != 3 || got.FreeSpaceOffset != 100 {
		t.Fatalf("page header round-trip mismatch: %+v", got)
	}
	if !VerifyBody(buf) {
		t.Fatal("VerifyBody failed on a freshly written page")
	}

	// Flip a body byte: body checksum must fail, header checksum still ok.
	buf[PageHeaderSize+10] ^= 0xFF
	if _, err := ReadHeader(buf); err != nil {
		t.Fatalf("header read should still succeed after body corruption: %v", err)
	}
	if VerifyBody(buf) {
		t.Fatal("VerifyBody should fail after body corruption")
	}

	// Flip a header byte: header checksum must fail.
	WritePage(buf, hdr) // restore
	buf[1] ^= 0xFF      // corrupt flags, leave header CRC stale
	if _, err := ReadHeader(buf); err != ErrPageChecksumFail {
		t.Fatalf("ReadHeader err = %v, want ErrPageChecksumFail", err)
	}
}
