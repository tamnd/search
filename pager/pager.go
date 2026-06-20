// Package pager turns a single file into an atomically versioned array of
// fixed-size pages (spec 2063 doc 03). It owns the file handle, page allocation,
// page I/O, per-page checksums, and the two meta pages that make commit atomic.
//
// At S0 the pager is a complete page store: it creates and opens a .sx file,
// allocates pages by extending the high-water mark, reads and writes pages with
// CRC-32C verification on every read, and selects the live meta page on open.
// The freelist, the WAL, the COW B+tree, and transactions arrive at S1. The
// pager runs over a vfs.VFS rather than the os package directly so the whole
// durability path can be exercised by the in-memory fault-injecting backend.
package pager

import (
	"errors"
	"sync"

	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/page"
	"github.com/tamnd/search/vfs"
	"github.com/tamnd/search/wal"
)

// BuildTag is written into the file header's creator_string on creation. It is
// a var, not a const, so a release build can stamp it via -ldflags.
var BuildTag = "search/0.1.0"

// Errors specific to the pager layer.
var (
	// ErrReadOnly is returned by mutating operations on a read-only pager.
	ErrReadOnly = errors.New("search/pager: database is read-only")
	// ErrPageOutOfRange is returned when a page id is past the high-water mark.
	ErrPageOutOfRange = errors.New("search/pager: page id out of range")
	// ErrZeroPage is returned by ReadPage/WritePage for page 0, which is the
	// file header and is not a common page; use the header accessors instead.
	ErrZeroPage = errors.New("search/pager: page 0 is the file header, not a data page")
	// ErrExists is returned by Create when the file already exists.
	ErrExists = errors.New("search/pager: file already exists")
)

// Options configure how a pager opens or creates a file. The zero value is the
// production default: full synchronous durability, read-write, default page
// size, OS clock, and a wall-clock-seeded WAL salt.
type Options struct {
	// PageSize is used only when creating a new file; 0 means the default.
	PageSize uint32
	// Sync is the durability level (consumed by the WAL at S1).
	Sync wal.SyncLevel
	// ReadOnly opens the file for reads only.
	ReadOnly bool
	// SaltSeed makes the WAL salt deterministic for tests; 0 derives the salt
	// from the clock.
	SaltSeed uint64
	// Clock is the time source; nil uses the OS clock.
	Clock determ.Clock
}

// Pager is an open page store over one file.
type Pager struct {
	fsys     vfs.VFS
	f        vfs.File
	path     string
	pageSize uint32
	readOnly bool
	sync     wal.SyncLevel
	clock    determ.Clock

	mu        sync.Mutex // serializes writers (single-writer discipline)
	header    page.Header
	meta      page.Meta
	metaSlot  page.PageID // 1 or 2: which slot the live meta occupies
	pageCount uint32      // high-water mark (number of pages in the file)
}

// Create makes a new .sx file at path through fsys and returns an open pager
// positioned at the initial empty state. It writes page 0 (the header), meta
// page 1 (the creation commit, txn id 1), and a zeroed meta page 2 (an invalid
// slot the first real commit will flip into). It returns ErrExists if the file
// is already present.
func Create(fsys vfs.VFS, path string, opt Options) (*Pager, error) {
	if fsys == nil {
		fsys = vfs.NewOS()
	}
	if fsys.Exists(path) {
		return nil, ErrExists
	}
	pageSize := opt.PageSize
	if pageSize == 0 {
		pageSize = page.DefaultPageSize
	}
	if !page.ValidPageSize(pageSize) {
		return nil, page.ErrInvalidPageSize
	}
	clock := opt.Clock
	if clock == nil {
		clock = determ.OSClock{}
	}

	f, err := fsys.Open(path, true)
	if err != nil {
		return nil, err
	}

	p := &Pager{
		fsys:     fsys,
		f:        f,
		path:     path,
		pageSize: pageSize,
		readOnly: false,
		sync:     opt.Sync,
		clock:    clock,
	}

	// Page 0: the file header. creation_txn_id is the first commit, 1.
	epoch := clock.Now() / 1e9
	hdr, err := page.NewHeader(pageSize, 1, epoch, sectorGuess(), page.CreatorString(BuildTag))
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	p.header = hdr
	if err := p.writeRawPage0(); err != nil {
		_ = f.Close()
		return nil, err
	}

	// Meta page 1: the creation commit. Empty catalog and freelist.
	salt := opt.SaltSeed
	if salt == 0 {
		salt = determ.NewPRNG(uint64(clock.Now())).Uint64()
	}
	p.pageCount = 3 // pages 0, 1, 2 exist
	p.meta = page.NewMeta(1, p.pageCount, salt)
	p.metaSlot = 1
	if err := p.writeMeta(1, p.meta); err != nil {
		_ = f.Close()
		return nil, err
	}
	// Meta page 2: a zeroed (invalid) slot. Writing zeros makes the file the
	// full three pages long and leaves slot 2 failing its CRC check, so the
	// selection algorithm picks slot 1 on the next open.
	if err := p.writeZeroPage(2); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return p, nil
}

// Open opens an existing .sx file at path through fsys. It validates the header,
// adopts the file's page size, selects the live meta page, and returns the open
// pager. It returns the typed format errors from package page on a bad header or
// two invalid meta pages.
func Open(fsys vfs.VFS, path string, opt Options) (*Pager, error) {
	if fsys == nil {
		fsys = vfs.NewOS()
	}
	clock := opt.Clock
	if clock == nil {
		clock = determ.OSClock{}
	}
	f, err := fsys.Open(path, false)
	if err != nil {
		return nil, err
	}

	// Read page 0 (the header). We do not yet know the page size, so read the
	// fixed 128-byte header first.
	hb := make([]byte, page.HeaderSize)
	if _, err := f.ReadAt(hb, 0); err != nil {
		_ = f.Close()
		return nil, page.ErrFileTooShort
	}
	hdr, err := page.ParseHeader(hb)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !opt.ReadOnly && hdr.FileFlags&page.FlagReadOnly != 0 {
		_ = f.Close()
		return nil, ErrReadOnly
	}

	p := &Pager{
		fsys:     fsys,
		f:        f,
		path:     path,
		pageSize: hdr.PageSize(),
		readOnly: opt.ReadOnly,
		sync:     opt.Sync,
		clock:    clock,
		header:   hdr,
	}

	// Select the live meta page.
	m1, ok1 := p.readMetaSlot(1)
	m2, ok2 := p.readMetaSlot(2)
	meta, slot, err := page.SelectMeta(m1, ok1, m2, ok2)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	p.meta = meta
	p.metaSlot = slot
	p.pageCount = meta.PageCount
	return p, nil
}

// PageSize returns the file's page size in bytes.
func (p *Pager) PageSize() uint32 { return p.pageSize }

// PageCount returns the current high-water mark (number of pages in the file).
func (p *Pager) PageCount() uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pageCount
}

// Header returns a copy of the file header.
func (p *Pager) Header() page.Header { return p.header }

// Meta returns a copy of the current live meta page.
func (p *Pager) Meta() page.Meta {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.meta
}

// ReadOnly reports whether the pager was opened read-only.
func (p *Pager) ReadOnly() bool { return p.readOnly }

// Sync flushes the file durably.
func (p *Pager) Sync() error { return p.f.Sync() }

// Close flushes and closes the file. It is idempotent.
func (p *Pager) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.f == nil {
		return nil
	}
	err := p.f.Close()
	p.f = nil
	return err
}

func sectorGuess() uint32 { return 512 }
