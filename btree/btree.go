// Package btree is the copy-on-write B+tree that backs the catalog (spec 2063
// doc 04). Keys and values are opaque byte strings; keys are compared as
// unsigned bytes. Every mutation is copy-on-write: a changed node is written to
// a freshly allocated page and the old page is scheduled for reclamation, so a
// reader holding an older root sees a stable, immutable tree. The tree never
// mutates a live page in place, which is what makes the single atomic meta flip
// at commit (doc 05) a crash-safe publish of a whole new tree version.
//
// The tree is pure node logic: it allocates, reads, and frees pages through the
// Pages seam and never touches the file, the WAL, or the meta pages directly.
// The transaction layer implements Pages, buffering dirty node bodies until it
// flips the meta page. This keeps page lifecycle (staging, the freelist, the
// reader table) out of the tree and the tree's split/merge logic out of the
// transaction.
//
// Node layout deviates from doc 04 §3 in one documented way: a node is fully
// decoded and re-encoded on every visit rather than edited in a slotted page
// with an in-page free list. The catalog is small (tens of entries), so the
// re-encode cost is irrelevant and the code is far less error prone. The
// on-disk body is still a compact, deterministic, checksummed layout; the
// slotted free-space bookkeeping is simply absent because nothing reads it. The
// interior node uses the standard children[n+1]/keys[n] representation rather
// than doc 04's (key, left_child)+rightmost_child cells; the two are
// isomorphic.
package btree

import (
	"bytes"
	"errors"

	"github.com/tamnd/search/page"
)

// MaxKeySize is the hard cap on a key (doc 04 §14.3). The catalog is not a
// general key/value store; oversized keys are a misuse and are rejected.
const MaxKeySize = 512

// Errors returned by the tree.
var (
	// ErrKeyTooLarge is returned by Put when a key exceeds MaxKeySize.
	ErrKeyTooLarge = errors.New("search/btree: key exceeds 512 bytes")
	// ErrEntryTooLarge is returned when a single key/value pair cannot fit in an
	// empty leaf even after a split. Overflow pages for large values arrive with
	// the document store; the catalog stores only small values.
	ErrEntryTooLarge = errors.New("search/btree: entry too large for a page")
	// ErrCorruptNode is returned when a node body fails to decode. It indicates
	// corruption that slipped past the page checksum, or a logic bug.
	ErrCorruptNode = errors.New("search/btree: corrupt node body")
)

// Pages is the page seam the tree allocates and reads through. The transaction
// layer implements it: Get returns a freshly staged body if the page was
// written in this transaction, otherwise the committed body from the pager; New
// reserves a page and hands back a zeroed body to fill; Free schedules a page
// for reader-gated reclamation.
type Pages interface {
	// PageSize is the file's page size in bytes.
	PageSize() uint32
	// Get returns the body (the bytes after the 32-byte common header) of page
	// id. The returned slice must not be mutated by the tree.
	Get(id page.PageID) ([]byte, error)
	// New allocates a page of typ and returns its id and a zeroed body to fill.
	// The body is owned by the transaction; the tree writes the node into it.
	New(typ page.PageType) (page.PageID, []byte, error)
	// Free schedules page id for reclamation once no reader can still see it.
	Free(id page.PageID)
}

// node is a decoded B+tree node. A leaf holds keys and values; an interior node
// holds keys and len(keys)+1 child pointers. The node is decoded from a page
// body, mutated in memory, and re-encoded into a fresh page body on write.
type node struct {
	leaf bool
	keys [][]byte
	vals [][]byte      // leaf only
	kids []page.PageID // interior only, len == len(keys)+1
}

// node body offsets within the page body.
const (
	nbType  = 0 // u8: page type tag (leaf or interior)
	nbCount = 2 // u16: number of keys
	nbHead  = 8 // payload starts here (8-byte aligned)
)

// Tree is a handle to a COW B+tree rooted at a page. The root is not stored in
// the Tree: every mutation returns the new root id, which the caller threads
// through the transaction and ultimately records in the meta page. A NoPage32
// root denotes an empty tree.
type Tree struct {
	pages Pages
}

// New returns a tree over the given page seam.
func New(p Pages) *Tree { return &Tree{pages: p} }

// EmptyRoot is the root value of an empty tree.
const EmptyRoot = page.PageID(page.NoPage32)

// readNode decodes the node at id.
func (t *Tree) readNode(id page.PageID) (*node, error) {
	body, err := t.pages.Get(id)
	if err != nil {
		return nil, err
	}
	return decodeNode(body)
}

// decodeNode parses a node from a page body.
func decodeNode(body []byte) (*node, error) {
	if len(body) < nbHead {
		return nil, ErrCorruptNode
	}
	n := &node{}
	switch page.PageType(body[nbType]) {
	case page.PageBTreeLeaf:
		n.leaf = true
	case page.PageBTreeInterior:
		n.leaf = false
	default:
		return nil, ErrCorruptNode
	}
	count := int(page.U16(body[nbCount:]))
	off := nbHead
	if n.leaf {
		n.keys = make([][]byte, count)
		n.vals = make([][]byte, count)
		for i := range count {
			k, no, err := readChunk(body, off)
			if err != nil {
				return nil, err
			}
			v, no2, err := readChunk(body, no)
			if err != nil {
				return nil, err
			}
			n.keys[i] = k
			n.vals[i] = v
			off = no2
		}
		return n, nil
	}
	// Interior: count+1 child ids, then count keys.
	n.kids = make([]page.PageID, count+1)
	for i := range count + 1 {
		if off+4 > len(body) {
			return nil, ErrCorruptNode
		}
		n.kids[i] = page.PageID(page.U32(body[off:]))
		off += 4
	}
	n.keys = make([][]byte, count)
	for i := range count {
		k, no, err := readChunk(body, off)
		if err != nil {
			return nil, err
		}
		n.keys[i] = k
		off = no
	}
	return n, nil
}

// readChunk reads a uvarint length prefix and that many bytes at off, returning
// the bytes and the new offset. The returned slice aliases body; callers that
// retain it past the page's lifetime must copy.
func readChunk(body []byte, off int) ([]byte, int, error) {
	if off > len(body) {
		return nil, 0, ErrCorruptNode
	}
	v, n, err := page.Uvarint(body[off:])
	if err != nil {
		return nil, 0, ErrCorruptNode
	}
	start := off + n
	end := start + int(v)
	if end > len(body) {
		return nil, 0, ErrCorruptNode
	}
	return body[start:end], end, nil
}

// encodedSize returns the number of body bytes the node serializes to.
func (n *node) encodedSize() int {
	sz := nbHead
	if n.leaf {
		for i := range n.keys {
			sz += chunkLen(len(n.keys[i])) + chunkLen(len(n.vals[i]))
		}
		return sz
	}
	sz += 4 * (len(n.keys) + 1)
	for i := range n.keys {
		sz += chunkLen(len(n.keys[i]))
	}
	return sz
}

// chunkLen returns the encoded length of a uvarint-prefixed blob of n bytes.
func chunkLen(n int) int { return uvarintLen(uint64(n)) + n }

// uvarintLen returns how many bytes a uvarint encoding of v occupies.
func uvarintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

// fits reports whether the node serializes within the page body.
func (n *node) fits(pageSize uint32) bool {
	return n.encodedSize() <= page.BodySize(pageSize)
}

// write encodes the node into a freshly allocated page and returns its id.
func (t *Tree) write(n *node) (page.PageID, error) {
	typ := page.PageBTreeInterior
	if n.leaf {
		typ = page.PageBTreeLeaf
	}
	id, body, err := t.pages.New(typ)
	if err != nil {
		return 0, err
	}
	n.encodeInto(body)
	return id, nil
}

// encodeInto serializes the node into body, which must be at least encodedSize
// bytes and is assumed zeroed.
func (n *node) encodeInto(body []byte) {
	if n.leaf {
		body[nbType] = byte(page.PageBTreeLeaf)
	} else {
		body[nbType] = byte(page.PageBTreeInterior)
	}
	page.PutU16(body[nbCount:], uint16(len(n.keys)))
	off := nbHead
	if n.leaf {
		for i := range n.keys {
			off = writeChunk(body, off, n.keys[i])
			off = writeChunk(body, off, n.vals[i])
		}
		return
	}
	for i := range n.kids {
		page.PutU32(body[off:], uint32(n.kids[i]))
		off += 4
	}
	for i := range n.keys {
		off = writeChunk(body, off, n.keys[i])
	}
}

// writeChunk writes a uvarint length prefix and the bytes, returning the new
// offset.
func writeChunk(body []byte, off int, b []byte) int {
	tmp := page.AppendUvarint(body[off:off], uint64(len(b)))
	off += len(tmp)
	copy(body[off:], b)
	return off + len(b)
}

// searchLeaf returns the index of key in a leaf and whether it is present. When
// absent, the index is the insertion point (the first key greater than key).
func (n *node) searchLeaf(key []byte) (int, bool) {
	lo, hi := 0, len(n.keys)
	for lo < hi {
		mid := (lo + hi) / 2
		switch bytes.Compare(n.keys[mid], key) {
		case 0:
			return mid, true
		case -1:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return lo, false
}

// childIndex returns the child slot to descend into for key in an interior
// node: the first child whose subtree may contain key.
func (n *node) childIndex(key []byte) int {
	lo, hi := 0, len(n.keys)
	for lo < hi {
		mid := (lo + hi) / 2
		if bytes.Compare(key, n.keys[mid]) < 0 {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

// Get returns the value for key, or nil and false if absent. It reads the tree
// rooted at root without allocating or freeing any page.
func (t *Tree) Get(root page.PageID, key []byte) ([]byte, bool, error) {
	if root == EmptyRoot {
		return nil, false, nil
	}
	id := root
	for {
		n, err := t.readNode(id)
		if err != nil {
			return nil, false, err
		}
		if n.leaf {
			i, ok := n.searchLeaf(key)
			if !ok {
				return nil, false, nil
			}
			out := make([]byte, len(n.vals[i]))
			copy(out, n.vals[i])
			return out, true, nil
		}
		id = n.kids[n.childIndex(key)]
	}
}

// split is the result of a node that overflowed: a separator key whose subtree
// to the right is the newly created right node.
type split struct {
	sep   []byte
	right page.PageID
}

// Put inserts or replaces key with val and returns the new root. The tree is
// copy-on-write: every node on the path from the root to the leaf is rewritten
// to a fresh page and the old pages are freed.
func (t *Tree) Put(root page.PageID, key, val []byte) (page.PageID, error) {
	if len(key) > MaxKeySize {
		return 0, ErrKeyTooLarge
	}
	if root == EmptyRoot {
		leaf := &node{leaf: true, keys: [][]byte{cloneBytes(key)}, vals: [][]byte{cloneBytes(val)}}
		if !leaf.fits(t.pages.PageSize()) {
			return 0, ErrEntryTooLarge
		}
		return t.write(leaf)
	}
	newRoot, sp, err := t.insert(root, key, val)
	if err != nil {
		return 0, err
	}
	if sp == nil {
		return newRoot, nil
	}
	// The root split; build a new root one level taller.
	nr := &node{leaf: false, keys: [][]byte{sp.sep}, kids: []page.PageID{newRoot, sp.right}}
	return t.write(nr)
}

// insert recursively inserts into the subtree at id, returning the rewritten
// node's new id and a non-nil split if the node overflowed.
func (t *Tree) insert(id page.PageID, key, val []byte) (page.PageID, *split, error) {
	n, err := t.readNode(id)
	if err != nil {
		return 0, nil, err
	}
	if n.leaf {
		i, found := n.searchLeaf(key)
		if found {
			n.vals[i] = cloneBytes(val)
		} else {
			n.keys = insertAt(n.keys, i, cloneBytes(key))
			n.vals = insertAt(n.vals, i, cloneBytes(val))
		}
		return t.finishLeaf(id, n)
	}
	ci := n.childIndex(key)
	childNew, sp, err := t.insert(n.kids[ci], key, val)
	if err != nil {
		return 0, nil, err
	}
	n.kids[ci] = childNew
	if sp != nil {
		n.keys = insertAt(n.keys, ci, sp.sep)
		n.kids = insertAt(n.kids, ci+1, sp.right)
	}
	return t.finishInterior(id, n)
}

// finishLeaf frees the old leaf page and writes n, splitting it first if it no
// longer fits.
func (t *Tree) finishLeaf(oldID page.PageID, n *node) (page.PageID, *split, error) {
	t.pages.Free(oldID)
	if n.fits(t.pages.PageSize()) {
		id, err := t.write(n)
		return id, nil, err
	}
	if len(n.keys) < 2 {
		return 0, nil, ErrEntryTooLarge
	}
	mid := len(n.keys) / 2
	right := &node{leaf: true, keys: n.keys[mid:], vals: n.vals[mid:]}
	left := &node{leaf: true, keys: n.keys[:mid], vals: n.vals[:mid]}
	if !left.fits(t.pages.PageSize()) || !right.fits(t.pages.PageSize()) {
		return 0, nil, ErrEntryTooLarge
	}
	rightID, err := t.write(right)
	if err != nil {
		return 0, nil, err
	}
	leftID, err := t.write(left)
	if err != nil {
		return 0, nil, err
	}
	return leftID, &split{sep: cloneBytes(right.keys[0]), right: rightID}, nil
}

// finishInterior frees the old interior page and writes n, splitting it first
// if it no longer fits.
func (t *Tree) finishInterior(oldID page.PageID, n *node) (page.PageID, *split, error) {
	t.pages.Free(oldID)
	if n.fits(t.pages.PageSize()) {
		id, err := t.write(n)
		return id, nil, err
	}
	if len(n.keys) < 3 {
		return 0, nil, ErrEntryTooLarge
	}
	mid := len(n.keys) / 2
	sep := cloneBytes(n.keys[mid])
	left := &node{leaf: false, keys: n.keys[:mid], kids: n.kids[:mid+1]}
	right := &node{leaf: false, keys: n.keys[mid+1:], kids: n.kids[mid+1:]}
	rightID, err := t.write(right)
	if err != nil {
		return 0, nil, err
	}
	leftID, err := t.write(left)
	if err != nil {
		return 0, nil, err
	}
	return leftID, &split{sep: sep, right: rightID}, nil
}

// Delete removes key and returns the new root. Deletion never rebalances or
// merges (the LMDB policy, doc 04 §11): a node may become under-full and is
// left as is. Only a node that becomes completely empty is removed from its
// parent, and the root collapses when it drops to a single child. If key is
// absent the tree is unchanged and the original root is returned.
func (t *Tree) Delete(root page.PageID, key []byte) (page.PageID, error) {
	if root == EmptyRoot {
		return root, nil
	}
	newRoot, deleted, err := t.del(root, key)
	if err != nil {
		return 0, err
	}
	if !deleted {
		return root, nil
	}
	// Collapse the root if it became an empty leaf or a single-child interior.
	n, err := t.readNode(newRoot)
	if err != nil {
		return 0, err
	}
	if n.leaf {
		if len(n.keys) == 0 {
			t.pages.Free(newRoot)
			return EmptyRoot, nil
		}
		return newRoot, nil
	}
	if len(n.keys) == 0 {
		only := n.kids[0]
		t.pages.Free(newRoot)
		return only, nil
	}
	return newRoot, nil
}

// del recursively deletes key from the subtree at id, returning the rewritten
// node id and whether anything was deleted. When nothing is deleted the subtree
// is untouched and the original id is returned (no COW).
func (t *Tree) del(id page.PageID, key []byte) (page.PageID, bool, error) {
	n, err := t.readNode(id)
	if err != nil {
		return 0, false, err
	}
	if n.leaf {
		i, found := n.searchLeaf(key)
		if !found {
			return id, false, nil
		}
		n.keys = removeAt(n.keys, i)
		n.vals = removeAt(n.vals, i)
		t.pages.Free(id)
		newID, err := t.write(n)
		return newID, true, err
	}
	ci := n.childIndex(key)
	childNew, deleted, err := t.del(n.kids[ci], key)
	if err != nil {
		return 0, false, err
	}
	if !deleted {
		return id, false, nil
	}
	n.kids[ci] = childNew
	// If the rewritten child became empty, drop it from this node.
	child, err := t.readNode(childNew)
	if err != nil {
		return 0, false, err
	}
	if childEmpty(child) {
		t.pages.Free(childNew)
		if child.leaf || len(child.keys) == 0 {
			n.kids = removeAt(n.kids, ci)
			if ci < len(n.keys) {
				n.keys = removeAt(n.keys, ci)
			} else if len(n.keys) > 0 {
				n.keys = removeAt(n.keys, len(n.keys)-1)
			}
		}
	}
	t.pages.Free(id)
	newID, err := t.write(n)
	return newID, true, err
}

// childEmpty reports whether a node holds no entries: a leaf with no keys, or
// an interior node collapsed to a single child.
func childEmpty(n *node) bool {
	if n.leaf {
		return len(n.keys) == 0
	}
	return len(n.keys) == 0 && len(n.kids) <= 1
}

// cloneBytes returns a copy of b so the tree never retains a caller's or a
// page's backing array.
func cloneBytes(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

// insertAt inserts v at index i in s, shifting the tail right.
func insertAt[T any](s []T, i int, v T) []T {
	s = append(s, v)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// removeAt removes the element at index i in s.
func removeAt[T any](s []T, i int) []T {
	return append(s[:i], s[i+1:]...)
}
