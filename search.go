// Package search is an embedded, single-file, full-text and vector search engine
// for Go (spec 2063). It is pure Go (CGO_ENABLED=0), stores a whole index in one
// self-describing .sx file, and gives the SQLite "open a file, get a database"
// feel for search.
//
// At S0 the public surface is the file lifecycle: Open creates or opens a .sx
// file, validates its header, mounts the pager over a VFS, and Close leaves a
// quiescent, checksum-valid file. Document indexing arrives at S2 and query
// execution at S4. The full library API is doc 16.
package search

import (
	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/page"
	"github.com/tamnd/search/pager"
	"github.com/tamnd/search/vfs"
	"github.com/tamnd/search/wal"
)

// FormatVersion is the on-disk format version this build reads and writes.
const FormatVersion = page.FormatVersion

// DB is an open search index.
type DB struct {
	path string
	pgr  *pager.Pager
}

// Options configure how an index is opened. The zero value is the default: the
// OS filesystem, the default page size, and full synchronous durability.
type Options struct {
	// VFS is the virtual filesystem to open the file through; nil uses the real
	// OS filesystem. Tests pass an in-memory, fault-injecting VFS here.
	VFS vfs.VFS
	// PageSize is used only when creating a new file; 0 means the default.
	PageSize uint32
	// Sync is the durability level; the zero value is full synchronous.
	Sync wal.SyncLevel
	// ReadOnly opens the index for queries only.
	ReadOnly bool
	// SaltSeed makes WAL salt generation deterministic for tests; 0 is fine in
	// production where determinism is not required.
	SaltSeed uint64
	// Clock is the time source; nil uses the OS clock.
	Clock determ.Clock
}

// Open opens the index at path, creating it with a fresh header if it does not
// exist and validating the header if it does. From S1 onward the WAL is
// recovered on open so a crash before this call leaves the index at its last
// committed state.
func Open(path string, opt Options) (*DB, error) {
	fsys := opt.VFS
	if fsys == nil {
		fsys = vfs.NewOS()
	}
	popt := pager.Options{
		PageSize: opt.PageSize,
		Sync:     opt.Sync,
		ReadOnly: opt.ReadOnly,
		SaltSeed: opt.SaltSeed,
		Clock:    opt.Clock,
	}
	var pgr *pager.Pager
	var err error
	if fsys.Exists(path) {
		pgr, err = pager.Open(fsys, path, popt)
	} else {
		if opt.ReadOnly {
			return nil, vfs.ErrNotExist
		}
		pgr, err = pager.Create(fsys, path, popt)
	}
	if err != nil {
		return nil, err
	}
	return &DB{path: path, pgr: pgr}, nil
}

// Path returns the index file path.
func (db *DB) Path() string { return db.path }

// PageSize returns the file's page size in bytes.
func (db *DB) PageSize() uint32 { return db.pgr.PageSize() }

// Close flushes and closes the index. It is idempotent.
func (db *DB) Close() error {
	if db.pgr == nil {
		return nil
	}
	err := db.pgr.Close()
	db.pgr = nil
	return err
}
