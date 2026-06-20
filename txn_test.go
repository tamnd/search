package search

import (
	"fmt"
	"sync"
	"testing"

	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/vfs"
)

// openMem opens a fresh in-memory index with a small page size so a modest entry
// count forces B+tree splits and real freelist churn.
func openMem(t *testing.T) (*DB, vfs.VFS) {
	t.Helper()
	fs := vfs.NewMem()
	db, err := Open("idx.sx", Options{VFS: fs, PageSize: 4096, Clock: determ.NewFakeClock(0), SaltSeed: 1})
	if err != nil {
		t.Fatal(err)
	}
	return db, fs
}

func putCat(t *testing.T, db *DB, key, val string) {
	t.Helper()
	if err := db.Update(func(tx *Txn) error {
		return tx.Catalog().Put(catalog.NSMeta, []byte(key), []byte(val))
	}); err != nil {
		t.Fatalf("put %q: %v", key, err)
	}
}

func getCat(t *testing.T, tx *Txn, key string) (string, bool) {
	t.Helper()
	v, ok, err := tx.Catalog().Get(catalog.NSMeta, []byte(key))
	if err != nil {
		t.Fatalf("get %q: %v", key, err)
	}
	return string(v), ok
}

func TestTransactionRoundTrip(t *testing.T) {
	db, fs := openMem(t)
	for i := range 500 {
		putCat(t, db, fmt.Sprintf("k-%05d", i), fmt.Sprintf("v-%05d", i))
	}
	mustClose(t, db)

	// Reopen: every committed entry survives the meta flip and reload.
	db2, err := Open("idx.sx", Options{VFS: fs})
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db2)
	if err := db2.View(func(tx *Txn) error {
		for i := range 500 {
			v, ok := getCat(t, tx, fmt.Sprintf("k-%05d", i))
			if !ok || v != fmt.Sprintf("v-%05d", i) {
				t.Fatalf("k-%05d = %q ok=%v", i, v, ok)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionRollback(t *testing.T) {
	db, _ := openMem(t)
	defer mustClose(t, db)
	putCat(t, db, "keep", "1")

	// A transaction that returns an error is rolled back and leaves no trace.
	wantErr := fmt.Errorf("boom")
	if err := db.Update(func(tx *Txn) error {
		if err := tx.Catalog().Put(catalog.NSMeta, []byte("ghost"), []byte("x")); err != nil {
			return err
		}
		return wantErr
	}); err != wantErr {
		t.Fatalf("Update err = %v, want %v", err, wantErr)
	}
	if err := db.View(func(tx *Txn) error {
		if _, ok := getCat(t, tx, "ghost"); ok {
			t.Fatal("rolled-back key is visible")
		}
		if v, ok := getCat(t, tx, "keep"); !ok || v != "1" {
			t.Fatalf("keep = %q ok=%v", v, ok)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionIsolation(t *testing.T) {
	db, _ := openMem(t)
	defer mustClose(t, db)
	putCat(t, db, "a", "1")

	// Open a read snapshot, then commit a new version over it.
	r, err := db.Begin(false)
	if err != nil {
		t.Fatal(err)
	}
	putCat(t, db, "a", "2")
	// Churn enough to force copy-on-write and freelist activity under the reader.
	for i := range 300 {
		putCat(t, db, fmt.Sprintf("c-%05d", i), "x")
	}

	// The snapshot still sees its own version, never the later write.
	if v, ok := getCat(t, r, "a"); !ok || v != "1" {
		t.Fatalf("snapshot a = %q ok=%v, want 1", v, ok)
	}
	if err := r.Rollback(); err != nil {
		t.Fatal(err)
	}

	// A fresh snapshot sees the committed value.
	if err := db.View(func(tx *Txn) error {
		if v, ok := getCat(t, tx, "a"); !ok || v != "2" {
			t.Fatalf("fresh a = %q ok=%v, want 2", v, ok)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReaderTable(t *testing.T) {
	db, _ := openMem(t)
	defer mustClose(t, db)
	putCat(t, db, "k", "v0")

	// While a reader pins version V, pages freed by later commits are deferred,
	// not reclaimed.
	r, err := db.Begin(false)
	if err != nil {
		t.Fatal(err)
	}
	pinned := r.TxnID()

	for i := range 50 {
		putCat(t, db, "k", fmt.Sprintf("v%d", i))
	}
	db.rmu.Lock()
	if db.readers[pinned] != 1 {
		t.Fatalf("reader refcount = %d, want 1", db.readers[pinned])
	}
	pendWithReader := len(db.pendingFree)
	if pendWithReader == 0 {
		t.Fatal("expected deferred frees while a reader is open")
	}
	if db.minReaderLocked() != pinned {
		t.Fatalf("minReader = %d, want %d", db.minReaderLocked(), pinned)
	}
	db.rmu.Unlock()

	if err := r.Rollback(); err != nil {
		t.Fatal(err)
	}
	db.rmu.Lock()
	if _, ok := db.readers[pinned]; ok {
		t.Fatal("reader still registered after rollback")
	}
	db.rmu.Unlock()

	// With the reader gone, the next commit promotes the frees it was pinning.
	// Each commit still defers its own freed pages until a later commit, so the
	// reader-era backlog is what must drain, not the deferred set as a whole.
	putCat(t, db, "k", "final")
	db.rmu.Lock()
	pend := len(db.pendingFree)
	free := len(db.freelist)
	db.rmu.Unlock()
	if pend >= pendWithReader {
		t.Fatalf("pendingFree = %d after reader left, want fewer than %d", pend, pendWithReader)
	}
	if free == 0 {
		t.Fatal("expected reclaimed pages in the freelist")
	}
}

func TestFreelistReclamation(t *testing.T) {
	db, fs := openMem(t)
	putCat(t, db, "seed", "1")

	// Build a tree, then delete most of it so many pages are freed and persisted
	// to the freelist chain.
	if err := db.Update(func(tx *Txn) error {
		for i := range 400 {
			if err := tx.Catalog().Put(catalog.NSMeta, fmt.Appendf(nil, "d-%05d", i), []byte("x")); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *Txn) error {
		for i := range 400 {
			if err := tx.Catalog().Delete(catalog.NSMeta, fmt.Appendf(nil, "d-%05d", i)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	db.rmu.Lock()
	freeAfter := len(db.freelist)
	db.rmu.Unlock()
	if freeAfter == 0 {
		t.Fatal("expected free pages after deleting the tree")
	}
	hwmBefore := db.pgr.PageCount()
	mustClose(t, db)

	// The freelist survives reopen: the in-memory list is rebuilt from the chain.
	db2, err := Open("idx.sx", Options{VFS: fs})
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db2)
	db2.rmu.Lock()
	reloaded := len(db2.freelist)
	db2.rmu.Unlock()
	if reloaded == 0 {
		t.Fatal("freelist not reloaded from the chain on open")
	}

	// New allocations draw from the freelist rather than growing the file.
	if err := db2.Update(func(tx *Txn) error {
		for i := range 100 {
			if err := tx.Catalog().Put(catalog.NSMeta, fmt.Appendf(nil, "r-%05d", i), []byte("y")); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := db2.pgr.PageCount(); got > hwmBefore+8 {
		t.Fatalf("high-water mark grew from %d to %d; freelist not reused", hwmBefore, got)
	}
}

func TestConcurrentReadersWriter(t *testing.T) {
	db, _ := openMem(t)
	defer mustClose(t, db)
	putCat(t, db, "counter", "0")

	const writes = 200
	done := make(chan struct{})
	// One writer advances a counter while readers take snapshots concurrently.
	go func() {
		defer close(done)
		for i := 1; i <= writes; i++ {
			if err := db.Update(func(tx *Txn) error {
				return tx.Catalog().Put(catalog.NSMeta, []byte("counter"), fmt.Appendf(nil, "%d", i))
			}); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				// A snapshot is internally consistent: the counter always parses.
				if err := db.View(func(tx *Txn) error {
					v, ok, err := tx.Catalog().Get(catalog.NSMeta, []byte("counter"))
					if err != nil {
						return err
					}
					if !ok || len(v) == 0 {
						return fmt.Errorf("counter missing in snapshot")
					}
					return nil
				}); err != nil {
					t.Errorf("read: %v", err)
					return
				}
			}
		}()
	}
	<-done
	wg.Wait()

	if err := db.View(func(tx *Txn) error {
		v, _, _ := tx.Catalog().Get(catalog.NSMeta, []byte("counter"))
		if string(v) != fmt.Sprintf("%d", writes) {
			t.Fatalf("final counter = %q, want %d", v, writes)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkGroupCommit(b *testing.B) {
	fs := vfs.NewMem()
	db, err := Open("bench.sx", Options{VFS: fs, PageSize: 4096, Clock: determ.NewFakeClock(0), SaltSeed: 1})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	b.ResetTimer()
	for i := range b.N {
		if err := db.Update(func(tx *Txn) error {
			return tx.Catalog().Put(catalog.NSMeta, fmt.Appendf(nil, "b-%08d", i), []byte("v"))
		}); err != nil {
			b.Fatal(err)
		}
	}
}
