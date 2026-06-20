package search

import (
	"errors"
	"math"

	"github.com/tamnd/search/btree"
	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/page"
)

// Errors from the transaction layer.
var (
	// ErrTxnDone is returned by operations on a transaction that has already
	// committed or rolled back.
	ErrTxnDone = errors.New("search: transaction already finished")
	// ErrReadOnlyTxn is returned by mutating catalog operations on a read txn.
	ErrReadOnlyTxn = errors.New("search: read-only transaction")
)

// Txn is a transaction over the index. A write transaction is exclusive (the
// single writer); a read transaction takes a stable snapshot of the catalog and
// runs concurrently with the writer and other readers, observing the committed
// state as of its start and nothing later. Every write is copy-on-write and
// becomes durable and visible atomically at Commit via the meta flip.
type Txn struct {
	db       *DB
	write    bool
	done     bool
	snapshot page.Meta
	txnID    uint64 // the version this write txn will publish (write only)
	root     page.PageID
	cat      *catalog.Catalog

	// write-transaction page staging.
	dirty     map[page.PageID]*dirtyPage // pages written this txn, keyed by id
	allocated map[page.PageID]struct{}   // pages allocated this txn (reusable on rollback)
	scratch   []page.PageID              // allocated-then-freed this txn (immediately reusable)
	freed     []page.PageID              // committed pages freed this txn (reader-gated)
}

// dirtyPage is a page body staged for writing at commit, with the page type to
// stamp into its common header.
type dirtyPage struct {
	typ  page.PageType
	body []byte
}

// Begin starts a transaction. A write transaction acquires the single-writer
// lock, held until Commit or Rollback. A read transaction registers a snapshot
// in the reader table so the pages of its version are not reclaimed under it.
func (db *DB) Begin(write bool) (*Txn, error) {
	if write {
		if db.pgr.ReadOnly() {
			return nil, ErrReadOnlyTxn
		}
		db.writeMu.Lock()
		snap := db.pgr.Meta()
		t := &Txn{
			db:        db,
			write:     true,
			snapshot:  snap,
			txnID:     snap.TxnID + 1,
			root:      page.PageID(snap.CatalogRoot),
			dirty:     make(map[page.PageID]*dirtyPage),
			allocated: make(map[page.PageID]struct{}),
		}
		t.cat = catalog.New(t, &t.root)
		return t, nil
	}
	db.rmu.Lock()
	snap := db.pgr.Meta()
	db.readers[snap.TxnID]++
	db.rmu.Unlock()
	t := &Txn{db: db, write: false, snapshot: snap, root: page.PageID(snap.CatalogRoot)}
	t.cat = catalog.New(t, &t.root)
	return t, nil
}

// Catalog returns the namespaced catalog view bound to this transaction.
func (t *Txn) Catalog() *catalog.Catalog { return t.cat }

// TxnID returns the version this transaction reads (for a read txn) or will
// publish (for a write txn).
func (t *Txn) TxnID() uint64 {
	if t.write {
		return t.txnID
	}
	return t.snapshot.TxnID
}

// --- btree.Pages implementation (write transactions stage; reads pass through) ---

// PageSize returns the file page size.
func (t *Txn) PageSize() uint32 { return t.db.pgr.PageSize() }

// Get returns the body of page id: the freshly staged copy if this transaction
// wrote it, otherwise the committed body read through the pager.
func (t *Txn) Get(id page.PageID) ([]byte, error) {
	if t.write {
		if dp, ok := t.dirty[id]; ok {
			return dp.body, nil
		}
	}
	buf, err := t.db.pgr.ReadPage(id)
	if err != nil {
		return nil, err
	}
	return page.Body(buf), nil
}

// New allocates a page of typ and returns its id and a zeroed body to fill. It
// prefers a page freed earlier in this same transaction (immediately safe),
// then a page from the durable freelist, then grows the file.
func (t *Txn) New(typ page.PageType) (page.PageID, []byte, error) {
	if !t.write {
		return 0, nil, ErrReadOnlyTxn
	}
	var id page.PageID
	if n := len(t.scratch); n > 0 {
		id = t.scratch[n-1]
		t.scratch = t.scratch[:n-1]
	} else {
		var err error
		id, err = t.db.allocPage(typ)
		if err != nil {
			return 0, nil, err
		}
	}
	body := make([]byte, page.BodySize(t.db.pgr.PageSize()))
	t.dirty[id] = &dirtyPage{typ: typ, body: body}
	t.allocated[id] = struct{}{}
	return id, body, nil
}

// Free schedules page id for reclamation. A page allocated within this
// transaction was never committed and is recycled immediately; a committed page
// is held until no reader can still see it.
func (t *Txn) Free(id page.PageID) {
	if !t.write {
		return
	}
	if _, ok := t.allocated[id]; ok {
		delete(t.allocated, id)
		delete(t.dirty, id)
		t.scratch = append(t.scratch, id)
		return
	}
	t.freed = append(t.freed, id)
}

// allocPage pops a page from the durable freelist or grows the file. The popped
// page already exists below the high-water mark, so no write is needed until
// commit; a grown page is materialized by the pager.
func (db *DB) allocPage(typ page.PageType) (page.PageID, error) {
	db.rmu.Lock()
	if n := len(db.freelist); n > 0 {
		id := db.freelist[n-1]
		db.freelist = db.freelist[:n-1]
		db.rmu.Unlock()
		return id, nil
	}
	db.rmu.Unlock()
	return db.pgr.AllocPage(typ)
}

// --- commit and rollback ---

// Commit makes a write transaction durable and visible, or ends a read
// transaction. For a write transaction it writes every staged page, fsyncs the
// data barrier, then flips the meta page (the second barrier), publishing the
// new catalog root, high-water mark, and freelist atomically.
func (t *Txn) Commit() error {
	if t.done {
		return ErrTxnDone
	}
	if !t.write {
		t.finishRead()
		return nil
	}
	defer t.finishWrite()

	db := t.db
	pageSize := db.pgr.PageSize()

	// 1. Write every staged page to its (already in-range) slot.
	for id, dp := range t.dirty {
		buf := make([]byte, pageSize)
		copy(page.Body(buf), dp.body)
		hdr := page.NewPageHeader(dp.typ, pageSize, t.txnID)
		if err := db.pgr.WritePage(id, buf, hdr); err != nil {
			return err
		}
	}

	// 2. Reconcile the freelist. Crash recovery always falls back to the current
	// on-disk meta, so this commit must never overwrite a page that meta still
	// reaches: not this transaction's just-freed pages (they are the old tree's
	// own pages) and not the old freelist trunk pages. Both are therefore added to
	// the deferred set tagged with this version and become reusable only from a
	// later commit. What this commit may reuse and persist is the set already free
	// under the on-disk meta (whose page contents that meta does not depend on)
	// plus this transaction's scratch (pages it both allocated and freed). Prior
	// deferred frees are promoted first, gated by the oldest live reader.
	db.rmu.Lock()
	db.freelist = append(db.freelist, t.scratch...)
	db.promoteLocked(db.minReaderLocked())
	flRoot, flCount, err := db.buildFreelistChainLocked(t.txnID)
	for _, id := range t.freed {
		db.pendingFree = append(db.pendingFree, pendingFree{id: id, version: t.txnID})
	}
	db.rmu.Unlock()
	if err != nil {
		return err
	}

	// 3. Data barrier: every page the new meta will reference is now durable.
	if err := db.pgr.SyncData(); err != nil {
		return err
	}

	// 4. Meta barrier: the single atomic flip publishing the new version.
	m := t.snapshot
	m.TxnID = t.txnID
	m.CatalogRoot = uint32(t.root)
	m.PageCount = db.pgr.PageCount()
	m.FreelistRoot = flRoot
	m.FreelistCount = flCount
	m.WriteTxnCounter = t.txnID
	if err := db.pgr.CommitMeta(m); err != nil {
		return err
	}
	t.done = true
	return nil
}

// Rollback discards a write transaction's changes or ends a read transaction.
// Pages allocated by an aborted write transaction were never published and are
// returned to the freelist for reuse.
func (t *Txn) Rollback() error {
	if t.done {
		return nil
	}
	if !t.write {
		t.finishRead()
		return nil
	}
	db := t.db
	db.rmu.Lock()
	for id := range t.allocated {
		db.freelist = append(db.freelist, id)
	}
	db.freelist = append(db.freelist, t.scratch...)
	db.rmu.Unlock()
	t.finishWrite()
	return nil
}

// finishWrite marks the transaction done and releases the writer lock. Commit
// sets done before calling this; Rollback relies on it to mark done.
func (t *Txn) finishWrite() {
	t.done = true
	t.db.writeMu.Unlock()
}

// finishRead unregisters the read snapshot from the reader table.
func (t *Txn) finishRead() {
	if t.done {
		return
	}
	t.done = true
	db := t.db
	db.rmu.Lock()
	v := t.snapshot.TxnID
	if db.readers[v] <= 1 {
		delete(db.readers, v)
	} else {
		db.readers[v]--
	}
	db.rmu.Unlock()
}

// --- freelist reclamation (rmu held by callers) ---

// minReaderLocked returns the oldest version pinned by an active reader, or
// math.MaxUint64 if there are no readers (every freed page is then reusable).
func (db *DB) minReaderLocked() uint64 {
	min := uint64(math.MaxUint64)
	for v := range db.readers {
		if v < min {
			min = v
		}
	}
	return min
}

// promoteLocked moves pages freed at a version no reader still pins into the
// reusable freelist. A page freed at version V is reusable once every reader
// observes version >= V (minReader >= V), because no surviving snapshot
// references it.
func (db *DB) promoteLocked(minReader uint64) {
	kept := db.pendingFree[:0]
	for _, pf := range db.pendingFree {
		if pf.version <= minReader {
			db.freelist = append(db.freelist, pf.id)
		} else {
			kept = append(kept, pf)
		}
	}
	db.pendingFree = kept
}

// freelist trunk page body layout (doc 02 §7.2).
const (
	flNextTrunk = 0 // u32: next trunk page or NoPage32
	flLeafCount = 4 // u32: number of page numbers in this trunk
	flEntries   = 8 // u32[]: the free page numbers
)

// trunkCapacity returns how many page numbers fit in one trunk page.
func (db *DB) trunkCapacity() int {
	return (page.BodySize(db.pgr.PageSize()) - flEntries) / 4
}

// buildFreelistChainLocked serializes the current freelist into a fresh chain of
// trunk pages and returns the chain root and the number of free pages recorded.
// The old chain's trunk pages are themselves freed (deferred), and the trunk
// pages of the new chain are drawn from the freelist itself, so a steady-state
// freelist needs no file growth. The chain is copy-on-write like every other
// structure: it is published only by the subsequent meta flip.
func (db *DB) buildFreelistChainLocked(version uint64) (uint32, uint64, error) {
	// The previous chain's pages become free once no reader is older than this
	// version. Readers never read the freelist, but gating uniformly is safe.
	for _, tp := range db.trunkPages {
		db.pendingFree = append(db.pendingFree, pendingFree{id: tp, version: version})
	}
	db.trunkPages = nil

	entries := db.freelist
	count := uint64(len(entries))
	if len(entries) == 0 {
		db.freelist = nil
		return page.NoPage32, 0, nil
	}

	capPer := db.trunkCapacity()
	// Each trunk page stores capPer entries and consumes one page (itself), so
	// nTrunks trunks account for nTrunks*(capPer+1) pages.
	nTrunks := (len(entries) + capPer) / (capPer + 1)
	nTrunks = max(nTrunks, 1)
	trunks := make([]page.PageID, nTrunks)
	copy(trunks, entries[:nTrunks])
	rest := entries[nTrunks:]

	pageSize := db.pgr.PageSize()
	idx := 0
	for ti, tp := range trunks {
		buf := make([]byte, pageSize)
		body := page.Body(buf)
		end := min(idx+capPer, len(rest))
		chunk := rest[idx:end]
		idx = end
		next := page.NoPage32
		if ti < len(trunks)-1 {
			next = uint32(trunks[ti+1])
		}
		page.PutU32(body[flNextTrunk:], next)
		page.PutU32(body[flLeafCount:], uint32(len(chunk)))
		for j, pg := range chunk {
			page.PutU32(body[flEntries+4*j:], uint32(pg))
		}
		hdr := page.NewPageHeader(page.PageFreelistTrunk, pageSize, version)
		if err := db.pgr.WritePage(tp, buf, hdr); err != nil {
			return 0, 0, err
		}
	}

	db.trunkPages = trunks
	db.freelist = append([]page.PageID(nil), rest...)
	return uint32(trunks[0]), count, nil
}

// loadFreelist walks the durable freelist chain into memory on open.
func (db *DB) loadFreelist() error {
	root := db.pgr.Meta().FreelistRoot
	for root != page.NoPage32 {
		buf, err := db.pgr.ReadPage(page.PageID(root))
		if err != nil {
			return err
		}
		body := page.Body(buf)
		next := page.U32(body[flNextTrunk:])
		cnt := int(page.U32(body[flLeafCount:]))
		for i := range cnt {
			db.freelist = append(db.freelist, page.PageID(page.U32(body[flEntries+4*i:])))
		}
		db.trunkPages = append(db.trunkPages, page.PageID(root))
		root = next
	}
	return nil
}

// Update runs fn inside a write transaction, committing if fn returns nil and
// rolling back otherwise.
func (db *DB) Update(fn func(*Txn) error) error {
	t, err := db.Begin(true)
	if err != nil {
		return err
	}
	if err := fn(t); err != nil {
		_ = t.Rollback()
		return err
	}
	return t.Commit()
}

// View runs fn inside a read transaction.
func (db *DB) View(fn func(*Txn) error) error {
	t, err := db.Begin(false)
	if err != nil {
		return err
	}
	defer func() { _ = t.Rollback() }()
	return fn(t)
}

// ensure Txn satisfies btree.Pages.
var _ btree.Pages = (*Txn)(nil)
