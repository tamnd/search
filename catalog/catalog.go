// Package catalog is the namespaced key/value view of the COW B+tree (spec 2063
// doc 04 §4). The catalog is the index's table of contents: format and schema
// metadata, field definitions, the segment manifest, deletion state, and the
// doc-id counters all live here as small entries in one tree. Each logical table
// is a one-byte namespace prefixed onto the key, so a single ordered tree holds
// every kind of record and a namespace is a contiguous key range.
//
// A Catalog is bound to a transaction's root pointer: every mutation rewrites
// the tree copy-on-write and stores the new root back through that pointer, so
// the transaction always holds the live root and a commit publishes it with one
// meta flip. Read-only callers pass a snapshot root and never mutate.
package catalog

import (
	"github.com/tamnd/search/btree"
	"github.com/tamnd/search/page"
)

// Namespace tags (doc 04 §4.2, §13). Each tag is the first byte of a catalog
// key, partitioning the single tree into ordered logical tables.
const (
	NSMeta            byte = 0x01 // format version, generation, timestamps
	NSFieldMeta       byte = 0x02 // per-field metadata, keyed by field id
	NSSegmentManifest byte = 0x03 // live segment descriptors, keyed by generation id
	NSDocID           byte = 0x05 // doc-id allocation counters
	NSDeletionState   byte = 0x06 // per-segment deletion state
	NSStats           byte = 0x09 // index-wide statistics
	NSSchema          byte = 0x0A // serialized field schema (doc 06 §2)
	NSAnalyzer        byte = 0x0B // serialized custom analyzer configs (doc 07 §7)
	NSDocStore        byte = 0x0C // stored-field document blocks (doc 06 §6)
	NSExternalID      byte = 0x0D // external id -> internal doc id (doc 06 §1.5)
	NSSegFST          byte = 0x0E // per-segment, per-field term dictionary FST (doc 08)
	NSSegPostings     byte = 0x0F // per-segment, per-field postings records (doc 09)
	NSSegNorms        byte = 0x10 // per-segment, per-field length norms (doc 13 §3)
	NSSegDocValues    byte = 0x11 // per-segment, per-field columnar doc-values (doc 14)
	NSSegPoints       byte = 0x12 // per-segment, per-field BKD points index (doc 14 §8)
	NSSegVectors      byte = 0x13 // per-segment, per-field HNSW graph + quantized vectors (doc 15)
)

// Catalog is a namespaced view of one B+tree. It mutates through the bound root
// pointer so the owning transaction always sees the latest root.
type Catalog struct {
	tree *btree.Tree
	root *page.PageID
}

// New returns a catalog over the page seam, reading and writing the root through
// root. The pointee may be btree.EmptyRoot for a fresh, empty catalog.
func New(p btree.Pages, root *page.PageID) *Catalog {
	return &Catalog{tree: btree.New(p), root: root}
}

// Root returns the current tree root.
func (c *Catalog) Root() page.PageID { return *c.root }

// nsKey returns the namespaced key []byte{ns} ++ key.
func nsKey(ns byte, key []byte) []byte {
	out := make([]byte, 1+len(key))
	out[0] = ns
	copy(out[1:], key)
	return out
}

// Put inserts or replaces the value for (ns, key), advancing the root.
func (c *Catalog) Put(ns byte, key, val []byte) error {
	nr, err := c.tree.Put(*c.root, nsKey(ns, key), val)
	if err != nil {
		return err
	}
	*c.root = nr
	return nil
}

// Get returns the value for (ns, key), or nil and false if absent.
func (c *Catalog) Get(ns byte, key []byte) ([]byte, bool, error) {
	return c.tree.Get(*c.root, nsKey(ns, key))
}

// Delete removes (ns, key), advancing the root. Deleting an absent key is not an
// error and leaves the root unchanged.
func (c *Catalog) Delete(ns byte, key []byte) error {
	nr, err := c.tree.Delete(*c.root, nsKey(ns, key))
	if err != nil {
		return err
	}
	*c.root = nr
	return nil
}

// Scan calls fn for every entry in namespace ns, in ascending key order, with
// the namespace tag stripped from the key passed to fn. fn returning false
// stops the scan.
func (c *Catalog) Scan(ns byte, fn func(key, val []byte) bool) error {
	start := []byte{ns}
	var end []byte
	if ns < 0xFF {
		end = []byte{ns + 1}
	}
	return c.tree.Scan(*c.root, start, end, func(k, v []byte) bool {
		return fn(k[1:], v)
	})
}
