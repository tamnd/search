package btree

import (
	"bytes"

	"github.com/tamnd/search/page"
)

// Cursor iterates the tree in ascending key order (doc 04 §12). It is bound to
// a fixed root, so it observes a stable immutable snapshot: concurrent writers
// produce a new root and never disturb the pages this cursor walks. A cursor
// holds only a small stack of decoded nodes; it must not outlive the
// transaction whose root it was opened on.
type Cursor struct {
	t     *Tree
	root  page.PageID
	stack []cursorFrame
	err   error
}

type cursorFrame struct {
	n   *node
	idx int
}

// Cursor returns a cursor over the tree rooted at root, not yet positioned.
// Call Seek or First before reading.
func (t *Tree) Cursor(root page.PageID) *Cursor {
	return &Cursor{t: t, root: root}
}

// Err returns the first error the cursor encountered, if any.
func (c *Cursor) Err() error { return c.err }

// Seek positions the cursor at the first entry with key >= target. If no such
// entry exists the cursor becomes invalid (Valid returns false).
func (c *Cursor) Seek(target []byte) {
	c.stack = c.stack[:0]
	if c.root == EmptyRoot {
		return
	}
	id := c.root
	for {
		n, err := c.t.readNode(id)
		if err != nil {
			c.err = err
			c.stack = c.stack[:0]
			return
		}
		if n.leaf {
			i, _ := n.searchLeaf(target)
			c.stack = append(c.stack, cursorFrame{n: n, idx: i})
			return
		}
		ci := n.childIndex(target)
		c.stack = append(c.stack, cursorFrame{n: n, idx: ci})
		id = n.kids[ci]
	}
}

// First positions the cursor at the smallest key in the tree.
func (c *Cursor) First() {
	c.stack = c.stack[:0]
	if c.root == EmptyRoot {
		return
	}
	c.descendLeft(c.root)
}

// descendLeft pushes the leftmost path from id down to a leaf.
func (c *Cursor) descendLeft(id page.PageID) {
	for {
		n, err := c.t.readNode(id)
		if err != nil {
			c.err = err
			c.stack = c.stack[:0]
			return
		}
		c.stack = append(c.stack, cursorFrame{n: n, idx: 0})
		if n.leaf {
			return
		}
		id = n.kids[0]
	}
}

// Valid reports whether the cursor is positioned at a live entry.
func (c *Cursor) Valid() bool {
	if c.err != nil || len(c.stack) == 0 {
		return false
	}
	top := c.stack[len(c.stack)-1]
	return top.n.leaf && top.idx < len(top.n.keys)
}

// Key returns a copy of the key at the cursor, or nil if invalid.
func (c *Cursor) Key() []byte {
	if !c.Valid() {
		return nil
	}
	top := c.stack[len(c.stack)-1]
	return cloneBytes(top.n.keys[top.idx])
}

// Value returns a copy of the value at the cursor, or nil if invalid.
func (c *Cursor) Value() []byte {
	if !c.Valid() {
		return nil
	}
	top := c.stack[len(c.stack)-1]
	return cloneBytes(top.n.vals[top.idx])
}

// Next advances to the next entry in ascending order.
func (c *Cursor) Next() {
	if c.err != nil || len(c.stack) == 0 {
		return
	}
	c.stack[len(c.stack)-1].idx++
	if c.Valid() {
		return
	}
	// Exhausted this leaf; ascend to a parent with a remaining child.
	c.stack = c.stack[:len(c.stack)-1]
	for len(c.stack) > 0 {
		parent := &c.stack[len(c.stack)-1]
		parent.idx++
		if parent.idx < len(parent.n.kids) {
			next := parent.n.kids[parent.idx]
			c.descendLeft(next)
			return
		}
		c.stack = c.stack[:len(c.stack)-1]
	}
}

// Scan calls fn for every entry with start <= key < end, in ascending order. A
// nil start begins at the smallest key; a nil end runs to the largest. fn
// returning false stops the scan early. The keys and values passed to fn are
// freshly copied and owned by the caller.
func (t *Tree) Scan(root page.PageID, start, end []byte, fn func(key, val []byte) bool) error {
	c := t.Cursor(root)
	if start == nil {
		c.First()
	} else {
		c.Seek(start)
	}
	for c.Valid() {
		k := c.Key()
		if end != nil && bytes.Compare(k, end) >= 0 {
			break
		}
		if !fn(k, c.Value()) {
			break
		}
		c.Next()
	}
	return c.Err()
}
