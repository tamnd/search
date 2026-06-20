package btree

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/tamnd/search/page"
)

// memPages is an in-memory Pages for unit-testing the tree in isolation. It
// never reclaims a freed page (it only records the id), so a test can still read
// an older root and confirm copy-on-write immutability.
type memPages struct {
	pageSize uint32
	pages    map[page.PageID][]byte
	next     page.PageID
	freed    []page.PageID
}

func newMemPages(pageSize uint32) *memPages {
	return &memPages{
		pageSize: pageSize,
		pages:    make(map[page.PageID][]byte),
		next:     3, // pages 0,1,2 are the header and meta pages
	}
}

func (m *memPages) PageSize() uint32 { return m.pageSize }

func (m *memPages) Get(id page.PageID) ([]byte, error) {
	b, ok := m.pages[id]
	if !ok {
		return nil, fmt.Errorf("memPages: page %d not found", id)
	}
	return b, nil
}

func (m *memPages) New(_ page.PageType) (page.PageID, []byte, error) {
	id := m.next
	m.next++
	b := make([]byte, page.BodySize(m.pageSize))
	m.pages[id] = b
	return id, b, nil
}

func (m *memPages) Free(id page.PageID) { m.freed = append(m.freed, id) }

func key(i int) []byte { return fmt.Appendf(nil, "key-%06d", i) }
func val(i int) []byte { return fmt.Appendf(nil, "val-%06d", i) }

func mustGet(t *testing.T, tr *Tree, root page.PageID, k []byte) []byte {
	t.Helper()
	v, ok, err := tr.Get(root, k)
	if err != nil {
		t.Fatalf("get %q: %v", k, err)
	}
	if !ok {
		t.Fatalf("get %q: not found", k)
	}
	return v
}

func TestBtreePutGet(t *testing.T) {
	tr := New(newMemPages(4096))
	root := EmptyRoot

	// Insert enough entries to force several levels of splits.
	const n = 2000
	var err error
	for i := range n {
		root, err = tr.Put(root, key(i), val(i))
		if err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	for i := range n {
		got := mustGet(t, tr, root, key(i))
		if !bytes.Equal(got, val(i)) {
			t.Fatalf("get key %d = %q, want %q", i, got, val(i))
		}
	}
	// A missing key reports not found.
	if _, ok, err := tr.Get(root, []byte("nope")); err != nil || ok {
		t.Fatalf("get missing: ok=%v err=%v", ok, err)
	}

	// Overwrite every value and confirm the new value reads back.
	for i := range n {
		root, err = tr.Put(root, key(i), fmt.Appendf(nil, "new-%06d", i))
		if err != nil {
			t.Fatalf("overwrite %d: %v", i, err)
		}
	}
	for i := range n {
		got := mustGet(t, tr, root, key(i))
		if want := fmt.Appendf(nil, "new-%06d", i); !bytes.Equal(got, want) {
			t.Fatalf("after overwrite key %d = %q, want %q", i, got, want)
		}
	}
}

func TestBtreeScan(t *testing.T) {
	tr := New(newMemPages(4096))
	root := EmptyRoot
	const n = 500
	var err error
	for i := range n {
		root, err = tr.Put(root, key(i), val(i))
		if err != nil {
			t.Fatal(err)
		}
	}

	// Full scan returns keys in ascending order.
	var got []string
	if err := tr.Scan(root, nil, nil, func(k, _ []byte) bool {
		got = append(got, string(k))
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("full scan returned %d entries, want %d", len(got), n)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatal("scan did not return keys in ascending order")
	}

	// Bounded scan [key(100), key(200)) returns exactly 100 entries.
	count := 0
	if err := tr.Scan(root, key(100), key(200), func(k, _ []byte) bool {
		if bytes.Compare(k, key(100)) < 0 || bytes.Compare(k, key(200)) >= 0 {
			t.Fatalf("scan returned out-of-range key %q", k)
		}
		count++
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if count != 100 {
		t.Fatalf("bounded scan returned %d entries, want 100", count)
	}
}

func TestBtreeDelete(t *testing.T) {
	tr := New(newMemPages(4096))
	root := EmptyRoot
	const n = 1500
	var err error
	for i := range n {
		root, err = tr.Put(root, key(i), val(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	// Delete every third key.
	for i := 0; i < n; i += 3 {
		root, err = tr.Delete(root, key(i))
		if err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	for i := range n {
		_, ok, err := tr.Get(root, key(i))
		if err != nil {
			t.Fatal(err)
		}
		want := i%3 != 0
		if ok != want {
			t.Fatalf("key %d present=%v, want %v", i, ok, want)
		}
	}
	// Deleting an absent key leaves the root unchanged.
	before := root
	root, err = tr.Delete(root, []byte("absent"))
	if err != nil {
		t.Fatal(err)
	}
	if root != before {
		t.Fatalf("deleting absent key changed root %d -> %d", before, root)
	}

	// Delete everything and the tree collapses back to empty.
	for i := range n {
		root, err = tr.Delete(root, key(i))
		if err != nil {
			t.Fatalf("final delete %d: %v", i, err)
		}
	}
	if root != EmptyRoot {
		t.Fatalf("after deleting all keys root = %d, want EmptyRoot", root)
	}
}

func TestBtreeCOW(t *testing.T) {
	mp := newMemPages(4096)
	tr := New(mp)
	root := EmptyRoot
	const n = 800
	var err error
	for i := range n {
		root, err = tr.Put(root, key(i), val(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot := root

	// Mutate the tree: overwrite and delete. The snapshot root must keep
	// observing the original values because COW never touches its pages.
	for i := range n {
		root, err = tr.Put(root, key(i), []byte("changed"))
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i += 2 {
		root, err = tr.Delete(root, key(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	if root == snapshot {
		t.Fatal("mutation did not change the root (no COW happened)")
	}
	if len(mp.freed) == 0 {
		t.Fatal("expected freed pages from COW, got none")
	}

	// The snapshot still sees the original tree, untouched.
	for i := range n {
		got := mustGet(t, tr, snapshot, key(i))
		if !bytes.Equal(got, val(i)) {
			t.Fatalf("snapshot key %d = %q, want %q (COW violated immutability)", i, got, val(i))
		}
	}
	// The live tree sees the mutations.
	for i := range n {
		_, ok, err := tr.Get(root, key(i))
		if err != nil {
			t.Fatal(err)
		}
		if want := i%2 != 0; ok != want {
			t.Fatalf("live key %d present=%v, want %v", i, ok, want)
		}
	}
}

func TestBtreeKeyTooLarge(t *testing.T) {
	tr := New(newMemPages(4096))
	big := make([]byte, MaxKeySize+1)
	if _, err := tr.Put(EmptyRoot, big, []byte("x")); !errors.Is(err, ErrKeyTooLarge) {
		t.Fatalf("put oversized key err = %v, want ErrKeyTooLarge", err)
	}
}

func BenchmarkBtreeScan(b *testing.B) {
	tr := New(newMemPages(4096))
	root := EmptyRoot
	for i := range 10000 {
		nr, err := tr.Put(root, key(i), val(i))
		if err != nil {
			b.Fatal(err)
		}
		root = nr
	}
	b.ResetTimer()
	for range b.N {
		n := 0
		if err := tr.Scan(root, nil, nil, func(_, _ []byte) bool {
			n++
			return true
		}); err != nil {
			b.Fatal(err)
		}
		if n != 10000 {
			b.Fatalf("scanned %d, want 10000", n)
		}
	}
}
