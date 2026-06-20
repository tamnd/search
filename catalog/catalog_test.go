package catalog

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/tamnd/search/btree"
	"github.com/tamnd/search/page"
)

// memPages is a minimal in-memory page seam for exercising the catalog over a
// real B+tree without a file. It never reclaims freed pages, which is fine for a
// unit test.
type memPages struct {
	pages map[page.PageID][]byte
	next  page.PageID
}

func newMemPages() *memPages {
	return &memPages{pages: make(map[page.PageID][]byte), next: 3}
}

func (m *memPages) PageSize() uint32 { return 4096 }

func (m *memPages) Get(id page.PageID) ([]byte, error) {
	b, ok := m.pages[id]
	if !ok {
		return nil, fmt.Errorf("page %d not found", id)
	}
	return b, nil
}

func (m *memPages) New(typ page.PageType) (page.PageID, []byte, error) {
	id := m.next
	m.next++
	b := make([]byte, page.BodySize(4096))
	m.pages[id] = b
	return id, b, nil
}

func (m *memPages) Free(id page.PageID) { delete(m.pages, id) }

func TestCatalogNamespaceIsolation(t *testing.T) {
	root := btree.EmptyRoot
	c := New(newMemPages(), &root)

	// The same raw key in two namespaces is two distinct entries.
	if err := c.Put(NSMeta, []byte("k"), []byte("meta")); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(NSSchema, []byte("k"), []byte("schema")); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := c.Get(NSMeta, []byte("k")); !ok || !bytes.Equal(v, []byte("meta")) {
		t.Fatalf("NSMeta k = %q ok=%v", v, ok)
	}
	if v, ok, _ := c.Get(NSSchema, []byte("k")); !ok || !bytes.Equal(v, []byte("schema")) {
		t.Fatalf("NSSchema k = %q ok=%v", v, ok)
	}

	// Deleting one namespace's key leaves the other untouched.
	if err := c.Delete(NSMeta, []byte("k")); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Get(NSMeta, []byte("k")); ok {
		t.Fatal("NSMeta k should be gone")
	}
	if _, ok, _ := c.Get(NSSchema, []byte("k")); !ok {
		t.Fatal("NSSchema k should survive a delete in another namespace")
	}
}

func TestCatalogScanStripsTag(t *testing.T) {
	root := btree.EmptyRoot
	c := New(newMemPages(), &root)

	for i := range 20 {
		if err := c.Put(NSFieldMeta, fmt.Appendf(nil, "f-%03d", i), fmt.Appendf(nil, "v%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// A neighbouring namespace must not leak into the scan.
	if err := c.Put(NSSegmentManifest, []byte("f-000"), []byte("other")); err != nil {
		t.Fatal(err)
	}

	var got int
	err := c.Scan(NSFieldMeta, func(k, v []byte) bool {
		want := fmt.Appendf(nil, "f-%03d", got)
		if !bytes.Equal(k, want) {
			t.Fatalf("scan key %d = %q, want %q (tag not stripped?)", got, k, want)
		}
		got++
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != 20 {
		t.Fatalf("scanned %d entries, want 20", got)
	}
}
