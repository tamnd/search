// Package cabi is the C-callable surface of the engine (spec 2063 doc 16 §7).
// The cgo shim in cabi.go (built only with CGO_ENABLED=1) exports the sx_*
// symbols; every shim converts its C arguments to Go values, calls into this
// file, and converts the result back. All of the engine logic lives here in
// plain Go so the surface is unit-testable without a C compiler: the parity
// tests drive these functions directly.
//
// Handles. Go objects the C caller holds onto (a DB, writer, snapshot, prepared
// query, or cursor) are kept in a process-global handle table and addressed by
// an opaque uint64. The high 8 bits of a handle encode the object kind so a
// handle passed to the wrong function is rejected instead of mis-cast; the low
// 56 bits index the table. A released slot is removed immediately, so handle
// values are not monotone and must not be treated as ordered.
//
// Errors. Each call returns one of the Status codes below. The last error
// string for a handle is retained on the handle's object and read back with the
// errmsg accessors, mirroring sqlite3_errmsg.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"

	search "github.com/tamnd/search"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
)

// Status is a C ABI result code. It mirrors the SX_* constants in search.h.
const (
	StatusOK        = 0
	StatusNotFound  = 1
	StatusCorrupt   = 2
	StatusReadOnly  = 3
	StatusClosed    = 4
	StatusConflict  = 5
	StatusTimeout   = 6
	StatusSchema    = 7
	StatusTooBig    = 8
	StatusBusy      = 9
	StatusSnapshots = 10
	StatusCapacity  = 11
	StatusVersion   = 12
	StatusError     = 99
	StatusRow       = 100
	StatusDone      = 101
)

// Open flags mirror the SX_OPEN_* constants in search.h.
const (
	OpenReadWrite = 0x00000001
	OpenReadOnly  = 0x00000002
	OpenCreate    = 0x00000004
	OpenMemory    = 0x00000008
)

// LibVersion is the library semantic version reported by sx_libversion.
const LibVersion = "0.1.0"

// LibVersionNumber encodes LibVersion as MAJOR*1000000 + MINOR*1000 + PATCH.
const LibVersionNumber = 0*1000000 + 1*1000 + 0

// ABIVersion is the C ABI contract version, frozen at 1 for the 1.0 release. It
// increments only on a breaking ABI change, independent of LibVersion.
const ABIVersion = 1

// kind tags a handle with the object type it refers to, stored in the high byte
// of the handle so a misrouted handle is caught at lookup time.
type kind uint8

const (
	kindDB kind = iota + 1
	kindWriter
	kindSnapshot
	kindQuery
	kindCursor
)

const kindShift = 56
const indexMask = (uint64(1) << kindShift) - 1

// table is the process-global handle table. It is sharded by the low bits of the
// handle index to spread lock contention across concurrent callers.
type handleTable struct {
	shards [256]struct {
		mu sync.Mutex
		m  map[uint64]any
	}
	next atomic.Uint64
}

var table = newHandleTable()

func newHandleTable() *handleTable {
	t := &handleTable{}
	for i := range t.shards {
		t.shards[i].m = make(map[uint64]any)
	}
	return t
}

func shardOf(idx uint64) int { return int(idx & 0xff) }

// put registers obj under a fresh handle tagged with k.
func (t *handleTable) put(k kind, obj any) uint64 {
	idx := t.next.Add(1) & indexMask
	h := uint64(k)<<kindShift | idx
	sh := &t.shards[shardOf(idx)]
	sh.mu.Lock()
	sh.m[h] = obj
	sh.mu.Unlock()
	return h
}

// get looks up a handle, checking that its kind tag matches the expected kind.
func (t *handleTable) get(k kind, h uint64) (any, bool) {
	if kind(h>>kindShift) != k {
		return nil, false
	}
	sh := &t.shards[shardOf(h&indexMask)]
	sh.mu.Lock()
	obj, ok := sh.m[h]
	sh.mu.Unlock()
	return obj, ok
}

// remove drops a handle from the table, returning the object it held.
func (t *handleTable) remove(k kind, h uint64) (any, bool) {
	if kind(h>>kindShift) != k {
		return nil, false
	}
	sh := &t.shards[shardOf(h&indexMask)]
	sh.mu.Lock()
	obj, ok := sh.m[h]
	if ok {
		delete(sh.m, h)
	}
	sh.mu.Unlock()
	return obj, ok
}

// liveHandles reports how many handles are registered; the leak test asserts it
// returns to its starting value after a clean teardown.
func liveHandles() int {
	n := 0
	for i := range table.shards {
		sh := &table.shards[i]
		sh.mu.Lock()
		n += len(sh.m)
		sh.mu.Unlock()
	}
	return n
}

// dbObj is the object behind an sx_db handle.
type dbObj struct {
	db      *search.DB
	lastErr string
	mapping string // cached for getMapping's library-owned lifetime
	stats   string // cached for statsJSON's library-owned lifetime
}

func (o *dbObj) fail(err error) int {
	if err == nil {
		o.lastErr = ""
		return StatusOK
	}
	o.lastErr = err.Error()
	return statusFor(err)
}

// writerObj batches indexing and deletes until commit, matching the C ABI's
// "publish all changes since writer_open" contract.
type writerObj struct {
	owner   *dbObj
	pending []map[string]any
	deletes []string
	lastErr string
	done    bool
}

func (o *writerObj) fail(err error) int {
	if err == nil {
		o.lastErr = ""
		return StatusOK
	}
	o.lastErr = err.Error()
	return statusFor(err)
}

// snapObj is a read view. The engine's queries already run against a consistent
// committed state per call, so the snapshot records the txid at open time.
type snapObj struct {
	owner   *dbObj
	txid    uint64
	lastErr string
}

// queryObj is a parsed query plus any bound parameters.
type queryObj struct {
	snap   *snapObj
	q      query.Query
	params map[string]any
}

// cursorObj holds the materialized hits a query run produced and the iteration
// cursor over them.
type cursorObj struct {
	hits  []search.Hit
	pos   int // -1 before the first step
	total int64
	curID string
	curJS string
}

// statusFor maps a Go error to the closest C status code.
func statusFor(err error) int {
	switch {
	case err == nil:
		return StatusOK
	case errors.Is(err, search.ErrNoSchema):
		return StatusSchema
	default:
		return StatusError
	}
}

// Open opens or creates an index and returns a db handle. flags currently
// selects read-only; create-if-missing is implicit, matching Open's behavior.
func Open(path string, flags int) (uint64, int, string) {
	opt := search.Options{}
	if flags&OpenReadOnly != 0 && flags&OpenReadWrite == 0 {
		opt.ReadOnly = true
	}
	db, err := search.Open(path, opt)
	if err != nil {
		return 0, statusFor(err), err.Error()
	}
	return table.put(kindDB, &dbObj{db: db}), StatusOK, ""
}

// Close closes a db handle. It refuses to close while writers or snapshots are
// still registered against it, returning StatusSnapshots.
func Close(h uint64) int {
	obj, ok := table.get(kindDB, h)
	if !ok {
		return StatusClosed
	}
	o := obj.(*dbObj)
	if n := dependents(o); n > 0 {
		o.lastErr = fmt.Sprintf("close: %d open writer/snapshot handles remain", n)
		return StatusSnapshots
	}
	if err := o.db.Close(); err != nil {
		return o.fail(err)
	}
	table.remove(kindDB, h)
	return StatusOK
}

// dependents counts the live writer and snapshot handles owned by o.
func dependents(o *dbObj) int {
	n := 0
	for i := range table.shards {
		sh := &table.shards[i]
		sh.mu.Lock()
		for _, v := range sh.m {
			switch t := v.(type) {
			case *writerObj:
				if t.owner == o && !t.done {
					n++
				}
			case *snapObj:
				if t.owner == o {
					n++
				}
			}
		}
		sh.mu.Unlock()
	}
	return n
}

// ErrMsg returns the last error string recorded on a db handle.
func ErrMsg(h uint64) string {
	if obj, ok := table.get(kindDB, h); ok {
		return obj.(*dbObj).lastErr
	}
	return ""
}

// ErrMsgWriter returns the last error string recorded on a writer handle.
func ErrMsgWriter(h uint64) string {
	if obj, ok := table.get(kindWriter, h); ok {
		return obj.(*writerObj).lastErr
	}
	return ""
}

// ErrMsgSnapshot returns the last error string recorded on a snapshot handle.
func ErrMsgSnapshot(h uint64) string {
	if obj, ok := table.get(kindSnapshot, h); ok {
		return obj.(*snapObj).lastErr
	}
	return ""
}

// DefineField adds or updates one field from a JSON field definition and
// republishes the schema.
func DefineField(h uint64, fieldJSON string) int {
	obj, ok := table.get(kindDB, h)
	if !ok {
		return StatusClosed
	}
	o := obj.(*dbObj)
	f, err := fieldFromJSON([]byte(fieldJSON))
	if err != nil {
		return o.fail(err)
	}
	s, err := currentSchema(o)
	if err != nil {
		return o.fail(err)
	}
	// Replace an existing field of the same name so define is idempotent.
	replaced := false
	for i := range s.Fields {
		if s.Fields[i].Name == f.Name {
			s.Fields[i] = f
			replaced = true
			break
		}
	}
	if !replaced {
		if err := s.Add(f); err != nil {
			return o.fail(err)
		}
	}
	if err := o.db.PutSchema(s); err != nil {
		return o.fail(err)
	}
	o.mapping = ""
	return StatusOK
}

// SetMapping replaces the entire mapping from a JSON document with a "fields"
// array (or a bare array of field objects).
func SetMapping(h uint64, mappingJSON string) int {
	obj, ok := table.get(kindDB, h)
	if !ok {
		return StatusClosed
	}
	o := obj.(*dbObj)
	s, err := schemaFromMapping([]byte(mappingJSON))
	if err != nil {
		return o.fail(err)
	}
	if err := o.db.PutSchema(s); err != nil {
		return o.fail(err)
	}
	o.mapping = ""
	return StatusOK
}

// GetMapping returns the current mapping as JSON. The string is cached on the
// handle so the returned pointer stays valid until the next GetMapping or close.
func GetMapping(h uint64) (string, int) {
	obj, ok := table.get(kindDB, h)
	if !ok {
		return "", StatusClosed
	}
	o := obj.(*dbObj)
	s, err := currentSchema(o)
	if err != nil {
		return "", o.fail(err)
	}
	b, err := json.Marshal(mappingDTO(s))
	if err != nil {
		return "", o.fail(err)
	}
	o.mapping = string(b)
	return o.mapping, StatusOK
}

// WriterOpen opens a batching writer against a db handle.
func WriterOpen(dbh uint64) (uint64, int) {
	obj, ok := table.get(kindDB, dbh)
	if !ok {
		return 0, StatusClosed
	}
	w := &writerObj{owner: obj.(*dbObj)}
	return table.put(kindWriter, w), StatusOK
}

// Index buffers a document for the next commit.
func Index(wh uint64, docJSON string) int {
	obj, ok := table.get(kindWriter, wh)
	if !ok {
		return StatusClosed
	}
	w := obj.(*writerObj)
	if w.done {
		w.lastErr = "writer is closed"
		return StatusClosed
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(docJSON), &doc); err != nil {
		return w.fail(err)
	}
	w.pending = append(w.pending, doc)
	return StatusOK
}

// DeleteDoc buffers an external-id delete for the next commit.
func DeleteDoc(wh uint64, id string) int {
	obj, ok := table.get(kindWriter, wh)
	if !ok {
		return StatusClosed
	}
	w := obj.(*writerObj)
	if w.done {
		w.lastErr = "writer is closed"
		return StatusClosed
	}
	w.deletes = append(w.deletes, id)
	return StatusOK
}

// Commit applies the buffered deletes then the buffered documents. On success
// the writer is marked done and its handle is invalid for further calls.
func Commit(wh uint64) int {
	obj, ok := table.get(kindWriter, wh)
	if !ok {
		return StatusClosed
	}
	w := obj.(*writerObj)
	if w.done {
		return StatusOK
	}
	for _, id := range w.deletes {
		if _, err := w.owner.db.Delete(id); err != nil {
			return w.fail(err)
		}
	}
	if len(w.pending) > 0 {
		if _, err := w.owner.db.Index(w.pending); err != nil {
			return w.fail(err)
		}
	}
	w.pending = nil
	w.deletes = nil
	w.done = true
	table.remove(kindWriter, wh)
	return StatusOK
}

// WriterClose discards any buffered changes and releases the writer.
func WriterClose(wh uint64) int {
	if _, ok := table.remove(kindWriter, wh); !ok {
		return StatusOK
	}
	return StatusOK
}

// SnapshotOpen pins a read view of the current committed state.
func SnapshotOpen(dbh uint64) (uint64, int) {
	obj, ok := table.get(kindDB, dbh)
	if !ok {
		return 0, StatusClosed
	}
	o := obj.(*dbObj)
	var txid uint64
	if err := o.db.View(func(t *search.Txn) error {
		txid = t.TxnID()
		return nil
	}); err != nil {
		return 0, o.fail(err)
	}
	return table.put(kindSnapshot, &snapObj{owner: o, txid: txid}), StatusOK
}

// SnapshotClose releases a snapshot handle.
func SnapshotClose(sh uint64) int {
	if _, ok := table.remove(kindSnapshot, sh); !ok {
		return StatusClosed
	}
	return StatusOK
}

// SnapshotTxid returns the transaction id a snapshot was opened at.
func SnapshotTxid(sh uint64) uint64 {
	if obj, ok := table.get(kindSnapshot, sh); ok {
		return obj.(*snapObj).txid
	}
	return 0
}

// Prepare compiles a JSON query against a snapshot's schema.
func Prepare(sh uint64, queryJSON string) (uint64, int) {
	obj, ok := table.get(kindSnapshot, sh)
	if !ok {
		return 0, StatusClosed
	}
	snap := obj.(*snapObj)
	q, err := query.ParseJSON([]byte(queryJSON))
	if err != nil {
		snap.lastErr = err.Error()
		return 0, StatusError
	}
	return table.put(kindQuery, &queryObj{snap: snap, q: q, params: map[string]any{}}), StatusOK
}

// QueryClose releases a prepared query handle.
func QueryClose(qh uint64) int {
	if _, ok := table.remove(kindQuery, qh); !ok {
		return StatusClosed
	}
	return StatusOK
}

// runOptions is the subset of the run options JSON the surface honors at S8.
type runOptions struct {
	From int `json:"from"`
	Size int `json:"size"`
}

// QueryRun executes a prepared query and returns a cursor over the hit window.
func QueryRun(qh uint64, optionsJSON string) (uint64, int) {
	obj, ok := table.get(kindQuery, qh)
	if !ok {
		return 0, StatusClosed
	}
	q := obj.(*queryObj)
	opts := runOptions{From: 0, Size: 10}
	if optionsJSON != "" {
		if err := json.Unmarshal([]byte(optionsJSON), &opts); err != nil {
			q.snap.lastErr = err.Error()
			return 0, StatusError
		}
	}
	if opts.Size < 0 {
		opts.Size = 0
	}
	if opts.From < 0 {
		opts.From = 0
	}
	hits, err := q.snap.owner.db.Search(q.q, opts.From+opts.Size)
	if err != nil {
		q.snap.lastErr = err.Error()
		return 0, statusFor(err)
	}
	total := int64(len(hits))
	if opts.From < len(hits) {
		hits = hits[opts.From:]
	} else {
		hits = nil
	}
	cur := &cursorObj{hits: hits, pos: -1, total: total}
	return table.put(kindCursor, cur), StatusOK
}

// Step advances a cursor, returning StatusRow when a hit is current and
// StatusDone when the window is exhausted.
func Step(ch uint64) int {
	obj, ok := table.get(kindCursor, ch)
	if !ok {
		return StatusClosed
	}
	c := obj.(*cursorObj)
	c.pos++
	if c.pos >= len(c.hits) {
		c.curID, c.curJS = "", ""
		return StatusDone
	}
	c.curID = c.hits[c.pos].ExternalID
	c.curJS = ""
	return StatusRow
}

// current returns the hit the cursor points at, if any.
func (c *cursorObj) current() (search.Hit, bool) {
	if c.pos < 0 || c.pos >= len(c.hits) {
		return search.Hit{}, false
	}
	return c.hits[c.pos], true
}

// ColumnID returns the external id of the current row.
func ColumnID(ch uint64) (string, bool) {
	obj, ok := table.get(kindCursor, ch)
	if !ok {
		return "", false
	}
	c := obj.(*cursorObj)
	h, ok := c.current()
	if !ok {
		return "", false
	}
	return h.ExternalID, true
}

// ColumnScore returns the score of the current row.
func ColumnScore(ch uint64) float32 {
	obj, ok := table.get(kindCursor, ch)
	if !ok {
		return 0
	}
	h, ok := obj.(*cursorObj).current()
	if !ok {
		return 0
	}
	return h.Score
}

// ColumnText returns a stored field of the current row as a string.
func ColumnText(ch uint64, field string) (string, bool) {
	obj, ok := table.get(kindCursor, ch)
	if !ok {
		return "", false
	}
	h, ok := obj.(*cursorObj).current()
	if !ok {
		return "", false
	}
	v, ok := h.Document[field]
	if !ok || v == nil {
		return "", false
	}
	if s, ok := v.(string); ok {
		return s, true
	}
	return fmt.Sprintf("%v", v), true
}

// ColumnJSON returns the current row serialized as a JSON object including its
// id and score. The string is cached on the cursor for the row's lifetime.
func ColumnJSON(ch uint64) (string, bool) {
	obj, ok := table.get(kindCursor, ch)
	if !ok {
		return "", false
	}
	c := obj.(*cursorObj)
	h, ok := c.current()
	if !ok {
		return "", false
	}
	if c.curJS != "" {
		return c.curJS, true
	}
	row := make(map[string]any, len(h.Document)+2)
	maps.Copy(row, h.Document)
	row["_id"] = h.ExternalID
	row["_score"] = h.Score
	b, err := json.Marshal(row)
	if err != nil {
		return "", false
	}
	c.curJS = string(b)
	return c.curJS, true
}

// ResultTotal returns the number of matching documents in the result window. It
// is a lower bound: only the requested window is materialized.
func ResultTotal(ch uint64) int64 {
	if obj, ok := table.get(kindCursor, ch); ok {
		return obj.(*cursorObj).total
	}
	return 0
}

// CursorClose releases a cursor handle.
func CursorClose(ch uint64) int {
	table.remove(kindCursor, ch)
	return StatusOK
}

// Analyze runs a field's analyzer over text and returns the tokens as JSON.
func Analyze(sh uint64, field, text string) (string, int) {
	obj, ok := table.get(kindSnapshot, sh)
	if !ok {
		return "", StatusClosed
	}
	snap := obj.(*snapObj)
	toks, err := snap.owner.db.AnalyzeField(field, text)
	if err != nil {
		snap.lastErr = err.Error()
		return "", statusFor(err)
	}
	type tokDTO struct {
		Term     string `json:"term"`
		Start    int    `json:"start"`
		End      int    `json:"end"`
		Position int    `json:"position"`
	}
	out := make([]tokDTO, 0, len(toks))
	pos := 0
	for _, t := range toks {
		pos += t.PositionIncr
		out = append(out, tokDTO{Term: t.Term, Start: t.StartOffset, End: t.EndOffset, Position: pos})
	}
	b, err := json.Marshal(out)
	if err != nil {
		snap.lastErr = err.Error()
		return "", StatusError
	}
	return string(b), StatusOK
}

// Compact runs a synchronous full compaction.
func Compact(dbh uint64) int {
	obj, ok := table.get(kindDB, dbh)
	if !ok {
		return StatusClosed
	}
	o := obj.(*dbObj)
	if _, err := o.db.CompactAll(); err != nil {
		return o.fail(err)
	}
	return StatusOK
}

// StatsJSON returns engine statistics as JSON, cached on the handle.
func StatsJSON(dbh uint64) (string, int) {
	obj, ok := table.get(kindDB, dbh)
	if !ok {
		return "", StatusClosed
	}
	o := obj.(*dbObj)
	segs, err := o.db.Segments()
	if err != nil {
		return "", o.fail(err)
	}
	var docs uint64
	for _, s := range segs {
		docs += uint64(s.DocCount)
	}
	b, err := json.Marshal(map[string]any{
		"path":           o.db.Path(),
		"page_size":      o.db.PageSize(),
		"segments":       len(segs),
		"document_count": docs,
	})
	if err != nil {
		return "", o.fail(err)
	}
	o.stats = string(b)
	return o.stats, StatusOK
}

// currentSchema returns the live schema, or a fresh empty one when none is set.
func currentSchema(o *dbObj) (*schema.Schema, error) {
	s, err := o.db.Schema()
	if errors.Is(err, search.ErrNoSchema) {
		return schema.New(), nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}
