package search

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/vfs"
)

// TestMultiProcess exercises the advisory file lock through two open handles to
// the same on-disk file. Because the lock is an open-file-description lock, two
// opens in this one test process conflict exactly as two separate processes
// would, so the table below is the multi-process contract: one writer excludes
// every other opener, and readers share.
func TestMultiProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.sx")

	// Create and close so the file exists for the open-vs-open cases below.
	seed, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	// A writer holds an exclusive lock.
	w, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("first writer open: %v", err)
	}

	// A second writer is refused while the first holds the file.
	if _, err := Open(path, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second writer err = %v, want ErrLocked", err)
	}
	// A reader is refused too: an exclusive lock excludes shared ones.
	if _, err := Open(path, Options{ReadOnly: true}); !errors.Is(err, ErrLocked) {
		t.Fatalf("reader against writer err = %v, want ErrLocked", err)
	}

	// --unsafe-no-lock opens regardless; it is the caller's promise of no
	// concurrent opener.
	bypass, err := Open(path, Options{ReadOnly: true, UnsafeNoLock: true})
	if err != nil {
		t.Fatalf("unsafe-no-lock open should succeed: %v", err)
	}
	if err := bypass.Close(); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// With the writer gone, two readers share the file.
	r1, err := Open(path, Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("first reader open: %v", err)
	}
	r2, err := Open(path, Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("second reader open (shared) err = %v, want nil", err)
	}
	// A writer cannot break in while readers hold shared locks.
	if _, err := Open(path, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("writer against readers err = %v, want ErrLocked", err)
	}
	if err := r1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r2.Close(); err != nil {
		t.Fatal(err)
	}

	// All locks released: a writer opens cleanly again.
	w2, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("writer after readers closed: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestMemVFSNoLock confirms the in-memory backend, which does not implement the
// lock capability, opens a second handle to the same file without contention.
// The lock is a no-op there because a single process owns the memory.
func TestMemVFSNoLock(t *testing.T) {
	mem := vfs.NewMem()
	db1, err := Open("idx.sx", Options{VFS: mem, Clock: determ.NewFakeClock(0), SaltSeed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db1.Close() }()
	// The same mem VFS and path again: a Locker-backed file would refuse this,
	// but the mem backend carries no Locker, so it opens.
	db2, err := Open("idx.sx", Options{VFS: mem, Clock: determ.NewFakeClock(0), SaltSeed: 1})
	if err != nil {
		t.Fatalf("second mem open should not lock: %v", err)
	}
	_ = db2.Close()
}
