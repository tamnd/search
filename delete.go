package search

import (
	"encoding/binary"

	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/docstore"
	"github.com/tamnd/search/segment"
)

// deleter soft-deletes documents within one write transaction. A delete sets the
// document's bit in its segment's delete bitmap and drops the stored body; the
// segment's postings are immutable and stay until compaction reaps them, so the
// query path filters deleted doc-ids out of every result. Bitmaps are loaded
// once per segment and written back together by flush.
type deleter struct {
	c     *catalog.Catalog
	set   *segment.SegmentSet
	store *docstore.Store
	dirty map[*segment.Segment]*segment.DeleteBitmap
}

func newDeleter(c *catalog.Catalog, set *segment.SegmentSet, store *docstore.Store) *deleter {
	return &deleter{c: c, set: set, store: store, dirty: map[*segment.Segment]*segment.DeleteBitmap{}}
}

// mark soft-deletes docID and reports whether it belonged to a live segment. A
// doc-id with no owning segment (it was indexed but carried no inverted field,
// so no segment holds it) is still removed from the store.
func (d *deleter) mark(docID uint64) (bool, error) {
	if err := d.store.Delete(docID); err != nil {
		return false, err
	}
	seg, ok := d.set.Find(uint32(docID))
	if !ok {
		return false, nil
	}
	bm, ok := d.dirty[seg]
	if !ok {
		var err error
		bm, err = segment.LoadDeletes(d.c, seg.Meta())
		if err != nil {
			return false, err
		}
		d.dirty[seg] = bm
	}
	bm.Add(uint32(docID))
	return true, nil
}

// flush writes every modified delete bitmap.
func (d *deleter) flush() error {
	for seg, bm := range d.dirty {
		if err := segment.StoreDeletes(d.c, seg.Meta(), bm); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes the document with the given external id and reports whether it
// existed. The delete is soft: the document's postings remain in their immutable
// segment until compaction, but it is no longer returned by queries or by
// GetByExternalID, and its stored body is dropped.
func (db *DB) Delete(extID string) (bool, error) {
	var existed bool
	err := db.Update(func(t *Txn) error {
		c := t.Catalog()
		b, ok, err := c.Get(catalog.NSExternalID, []byte(extID))
		if err != nil || !ok {
			return err
		}
		docID := binary.BigEndian.Uint64(b)
		set, err := segment.LoadSet(c)
		if err != nil {
			return err
		}
		del := newDeleter(c, set, docstore.New(c, catalog.NSDocStore))
		if _, err := del.mark(docID); err != nil {
			return err
		}
		if err := del.flush(); err != nil {
			return err
		}
		if err := c.Delete(catalog.NSExternalID, []byte(extID)); err != nil {
			return err
		}
		existed = true
		return nil
	})
	return existed, err
}
