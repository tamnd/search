package pager

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tamnd/search/checksum"
	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/page"
	"github.com/tamnd/search/vfs"
)

// fixedOpts returns options that make file creation byte-deterministic: a fake
// clock pinned at epoch 0 and a fixed WAL salt.
func fixedOpts() Options {
	return Options{Clock: determ.NewFakeClock(0), SaltSeed: 0x0123456789ABCDEF}
}

func TestCreateFile(t *testing.T) {
	fs := vfs.NewMem()
	p, err := Create(fs, "t.sx", fixedOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, p)

	if p.PageSize() != page.DefaultPageSize {
		t.Fatalf("page size = %d, want %d", p.PageSize(), page.DefaultPageSize)
	}
	if p.PageCount() != 3 {
		t.Fatalf("page count = %d, want 3", p.PageCount())
	}
	// File length must be exactly 3 pages.
	f, _ := fs.Open("t.sx", false)
	sz, _ := f.Size()
	if sz != int64(3*page.DefaultPageSize) {
		t.Fatalf("file size = %d, want %d", sz, 3*page.DefaultPageSize)
	}
	// Header magic and meta selection.
	if p.Header().Magic != page.Magic {
		t.Fatal("header magic mismatch")
	}
	m := p.Meta()
	if m.TxnID != 1 || m.PageCount != 3 || m.CatalogRoot != page.NoPage32 {
		t.Fatalf("unexpected initial meta: %+v", m)
	}
	if m.WALSalt != 0x0123456789ABCDEF {
		t.Fatalf("wal salt = %#x, want fixed seed", m.WALSalt)
	}
}

func TestCreateExists(t *testing.T) {
	fs := vfs.NewMem()
	p, err := Create(fs, "t.sx", fixedOpts())
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, p)
	if _, err := Create(fs, "t.sx", fixedOpts()); err != ErrExists {
		t.Fatalf("second Create err = %v, want ErrExists", err)
	}
}

func TestOpenExistingFile(t *testing.T) {
	fs := vfs.NewMem()
	p, err := Create(fs, "t.sx", fixedOpts())
	if err != nil {
		t.Fatal(err)
	}
	wantHdr := p.Header()
	mustClose(t, p)

	p2, err := Open(fs, "t.sx", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, p2)
	if p2.Header() != wantHdr {
		t.Fatalf("reopened header mismatch:\n got %+v\nwant %+v", p2.Header(), wantHdr)
	}
	if p2.Meta().TxnID != 1 {
		t.Fatalf("reopened meta txn = %d, want 1", p2.Meta().TxnID)
	}
}

func TestBadMagic(t *testing.T) {
	fs := vfs.NewMem()
	p, _ := Create(fs, "t.sx", fixedOpts())
	mustClose(t, p)
	// Corrupt the magic in page 0.
	f, _ := fs.Open("t.sx", false)
	mustWriteAt(t, f, []byte{0x00}, 0)
	if _, err := Open(fs, "t.sx", Options{}); err != page.ErrNotSxFile {
		t.Fatalf("Open err = %v, want ErrNotSxFile", err)
	}
}

func TestUnsupportedVersion(t *testing.T) {
	fs := vfs.NewMem()
	p, _ := Create(fs, "t.sx", fixedOpts())
	mustClose(t, p)
	// Rewrite the header with a bumped version and a valid CRC so the version
	// check, not the checksum check, fires.
	f, _ := fs.Open("t.sx", false)
	hb := make([]byte, page.HeaderSize)
	mustReadAt(t, f, hb, 0)
	page.PutU32(hb[16:], 0xFF)
	page.PutU32(hb[124:], crc32cOf(hb[:124]))
	mustWriteAt(t, f, hb, 0)
	if _, err := Open(fs, "t.sx", Options{}); err != page.ErrUnsupportedVersion {
		t.Fatalf("Open err = %v, want ErrUnsupportedVersion", err)
	}
}

func TestAllocPage(t *testing.T) {
	fs := vfs.NewMem()
	p, _ := Create(fs, "t.sx", fixedOpts())
	defer mustClose(t, p)

	const n = 1000
	prev := page.PageID(2)
	for range n {
		id, err := p.AllocPage(page.PageBTreeLeaf)
		if err != nil {
			t.Fatal(err)
		}
		if id <= prev {
			t.Fatalf("alloc id %d not monotonic after %d", id, prev)
		}
		prev = id
	}
	if p.PageCount() != 3+n {
		t.Fatalf("page count = %d, want %d", p.PageCount(), 3+n)
	}
	f, _ := fs.Open("t.sx", false)
	sz, _ := f.Size()
	if sz != int64((3+n)*page.DefaultPageSize) {
		t.Fatalf("file size = %d, want %d", sz, (3+n)*page.DefaultPageSize)
	}
}

func TestReadWritePage(t *testing.T) {
	fs := vfs.NewMem()
	p, _ := Create(fs, "t.sx", fixedOpts())
	defer mustClose(t, p)

	id, _ := p.AllocPage(page.PageDocStoreBlock)
	buf := make([]byte, p.PageSize())
	body := page.Body(buf)
	for i := range body {
		body[i] = byte((i * 7) & 0xFF)
	}
	hdr := page.NewPageHeader(page.PageDocStoreBlock, p.PageSize(), p.Meta().TxnID)
	if err := p.WritePage(id, buf, hdr); err != nil {
		t.Fatal(err)
	}

	got, err := p.ReadPage(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(page.Body(got), body) {
		t.Fatal("read-back body differs from written body")
	}
}

func TestChecksumVerify(t *testing.T) {
	fs := vfs.NewMem()
	p, _ := Create(fs, "t.sx", fixedOpts())
	defer mustClose(t, p)

	id, _ := p.AllocPage(page.PageDocStoreBlock)
	buf := make([]byte, p.PageSize())
	hdr := page.NewPageHeader(page.PageDocStoreBlock, p.PageSize(), 1)
	if err := p.WritePage(id, buf, hdr); err != nil {
		t.Fatal(err)
	}

	// Flip one byte in the body directly on the media.
	f, _ := fs.Open("t.sx", false)
	off := int64(id)*int64(p.PageSize()) + page.PageHeaderSize + 10
	one := make([]byte, 1)
	mustReadAt(t, f, one, off)
	one[0] ^= 0xFF
	mustWriteAt(t, f, one, off)

	if _, err := p.ReadPage(id); err != page.ErrPageChecksumFail {
		t.Fatalf("ReadPage err = %v, want ErrPageChecksumFail", err)
	}
}

func TestReadPageRejectsHeaderAndRange(t *testing.T) {
	fs := vfs.NewMem()
	p, _ := Create(fs, "t.sx", fixedOpts())
	defer mustClose(t, p)
	if _, err := p.ReadPage(0); err != ErrZeroPage {
		t.Fatalf("ReadPage(0) err = %v, want ErrZeroPage", err)
	}
	if _, err := p.ReadPage(999); err != ErrPageOutOfRange {
		t.Fatalf("ReadPage(999) err = %v, want ErrPageOutOfRange", err)
	}
}

func TestReadOnlyRejectsWrites(t *testing.T) {
	fs := vfs.NewMem()
	p, _ := Create(fs, "t.sx", fixedOpts())
	mustClose(t, p)
	ro, err := Open(fs, "t.sx", Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, ro)
	if _, err := ro.AllocPage(page.PageDocStoreBlock); err != ErrReadOnly {
		t.Fatalf("AllocPage on read-only err = %v, want ErrReadOnly", err)
	}
}

func TestConcurrentReads(t *testing.T) {
	fs := vfs.NewMem()
	p, _ := Create(fs, "t.sx", fixedOpts())
	defer mustClose(t, p)

	// Populate a handful of pages with distinct content.
	const pages = 8
	ids := make([]page.PageID, pages)
	for i := range pages {
		id, _ := p.AllocPage(page.PageDocStoreBlock)
		ids[i] = id
		buf := make([]byte, p.PageSize())
		page.Body(buf)[0] = byte(i)
		if err := p.WritePage(id, buf, page.NewPageHeader(page.PageDocStoreBlock, p.PageSize(), 1)); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for i, id := range ids {
				got, err := p.ReadPage(id)
				if err != nil {
					t.Errorf("read %d: %v", id, err)
					return
				}
				if page.Body(got)[0] != byte(i) {
					t.Errorf("page %d content mismatch", id)
					return
				}
			}
		})
	}
	wg.Wait()
}

// TestGoldenFile pins the exact bytes of a freshly created, deterministic .sx
// file. If the golden fixture is absent it is written and the test fails asking
// the author to commit it; otherwise the created bytes must match byte for byte.
func TestGoldenFile(t *testing.T) {
	fs := vfs.NewMem()
	p, err := Create(fs, "g.sx", fixedOpts())
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, p)

	f, _ := fs.Open("g.sx", false)
	sz, _ := f.Size()
	got := make([]byte, sz)
	mustReadAt(t, f, got, 0)

	golden := filepath.Join("testdata", "golden.sx")
	want, err := os.ReadFile(golden)
	if os.IsNotExist(err) {
		if mkErr := os.MkdirAll("testdata", 0o755); mkErr != nil {
			t.Fatal(mkErr)
		}
		if wErr := os.WriteFile(golden, got, 0o644); wErr != nil {
			t.Fatal(wErr)
		}
		t.Fatalf("golden file %s written (%d bytes); please commit it", golden, len(got))
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("created file differs from golden fixture (got %d bytes, want %d)", len(got), len(want))
	}
}

func crc32cOf(b []byte) uint32 { return checksum.Sum(b) }

// Test helpers that fail the test on an unexpected I/O error, keeping the test
// bodies free of repetitive error plumbing.

func mustClose(t *testing.T, p *Pager) {
	t.Helper()
	if err := p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func mustWriteAt(t *testing.T, f vfs.File, b []byte, off int64) {
	t.Helper()
	if _, err := f.WriteAt(b, off); err != nil {
		t.Fatalf("writeat off=%d: %v", off, err)
	}
}

func mustReadAt(t *testing.T, f vfs.File, b []byte, off int64) {
	t.Helper()
	if _, err := f.ReadAt(b, off); err != nil {
		t.Fatalf("readat off=%d: %v", off, err)
	}
}
