package search

import (
	"testing"

	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/vfs"
)

func mustClose(t *testing.T, db *DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestOpenCreateAndReopen(t *testing.T) {
	fs := vfs.NewMem()
	opt := Options{VFS: fs, Clock: determ.NewFakeClock(0), SaltSeed: 1}

	db, err := Open("idx.sx", opt)
	if err != nil {
		t.Fatal(err)
	}
	if db.Path() != "idx.sx" {
		t.Fatalf("path = %q", db.Path())
	}
	if db.PageSize() != 16384 {
		t.Fatalf("page size = %d, want 16384", db.PageSize())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Close is idempotent.
	if err := db.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	// Reopen the existing file.
	db2, err := Open("idx.sx", Options{VFS: fs})
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db2)
	if db2.PageSize() != 16384 {
		t.Fatalf("reopened page size = %d", db2.PageSize())
	}
}

func TestOpenReadOnlyMissing(t *testing.T) {
	fs := vfs.NewMem()
	if _, err := Open("nope.sx", Options{VFS: fs, ReadOnly: true}); err != vfs.ErrNotExist {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
}

func TestCustomPageSize(t *testing.T) {
	fs := vfs.NewMem()
	db, err := Open("p.sx", Options{VFS: fs, PageSize: 4096, Clock: determ.NewFakeClock(0), SaltSeed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db)
	if db.PageSize() != 4096 {
		t.Fatalf("page size = %d, want 4096", db.PageSize())
	}
}
