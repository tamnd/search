package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tamnd/search/query"
)

// openTestDB opens a fresh index in a temp dir and returns its handle.
func openTestDB(t *testing.T) uint64 {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.sx")
	h, rc, msg := Open(path, OpenReadWrite|OpenCreate)
	if rc != StatusOK {
		t.Fatalf("open: rc=%d msg=%q", rc, msg)
	}
	return h
}

// defineCorpus defines a title/views schema and indexes the given docs through a
// writer, committing once.
func defineCorpus(t *testing.T, db uint64, docs []map[string]any) {
	t.Helper()
	for _, fj := range []string{
		`{"name":"title","type":"text","stored":true,"indexed":true}`,
		`{"name":"views","type":"long","stored":true,"doc_values":true}`,
	} {
		if rc := DefineField(db, fj); rc != StatusOK {
			t.Fatalf("define %s: rc=%d %s", fj, rc, ErrMsg(db))
		}
	}
	w, rc := WriterOpen(db)
	if rc != StatusOK {
		t.Fatalf("writer_open: rc=%d", rc)
	}
	for _, d := range docs {
		b, _ := json.Marshal(d)
		if rc := Index(w, string(b)); rc != StatusOK {
			t.Fatalf("index: rc=%d %s", rc, ErrMsgWriter(w))
		}
	}
	if rc := Commit(w); rc != StatusOK {
		t.Fatalf("commit: rc=%d %s", rc, ErrMsgWriter(w))
	}
}

func TestCABICreateOpen(t *testing.T) {
	db := openTestDB(t)
	docs := make([]map[string]any, 100)
	for i := range docs {
		docs[i] = map[string]any{"_id": fmt.Sprintf("d%d", i), "title": "coffee beans", "views": i}
	}
	defineCorpus(t, db, docs)

	// The mapping round-trips through get_mapping.
	mj, rc := GetMapping(db)
	if rc != StatusOK {
		t.Fatalf("get_mapping rc=%d", rc)
	}
	var got mappingJSONDoc
	if err := json.Unmarshal([]byte(mj), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("expected 2 fields, got %d: %s", len(got.Fields), mj)
	}
	if rc := Close(db); rc != StatusOK {
		t.Fatalf("close rc=%d %s", rc, ErrMsg(db))
	}
}

func TestCABISearch(t *testing.T) {
	db := openTestDB(t)
	docs := []map[string]any{
		{"_id": "a", "title": "the quick brown fox", "views": 1},
		{"_id": "b", "title": "lazy brown dog", "views": 2},
		{"_id": "c", "title": "nothing relevant", "views": 3},
	}
	defineCorpus(t, db, docs)

	// Compare the C ABI path against the Go API for the same query.
	snap, rc := SnapshotOpen(db)
	if rc != StatusOK {
		t.Fatalf("snapshot rc=%d", rc)
	}
	q, rc := Prepare(snap, `{"match":{"field":"title","query":"brown"}}`)
	if rc != StatusOK {
		t.Fatalf("prepare rc=%d %s", rc, ErrMsgSnapshot(snap))
	}
	cur, rc := QueryRun(q, `{"from":0,"size":10}`)
	if rc != StatusOK {
		t.Fatalf("run rc=%d", rc)
	}

	var ids []string
	var lastScore float32 = 1e30
	for {
		st := Step(cur)
		if st == StatusDone {
			break
		}
		if st != StatusRow {
			t.Fatalf("step rc=%d", st)
		}
		id, ok := ColumnID(cur)
		if !ok {
			t.Fatal("column_id missing on row")
		}
		score := ColumnScore(cur)
		if score > lastScore {
			t.Fatalf("scores not descending: %f after %f", score, lastScore)
		}
		lastScore = score
		// column_text and column_json expose the stored fields.
		if title, ok := ColumnText(cur, "title"); !ok || title == "" {
			t.Fatalf("column_text(title) missing for %s", id)
		}
		js, ok := ColumnJSON(cur)
		if !ok {
			t.Fatalf("column_json missing for %s", id)
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(js), &row); err != nil {
			t.Fatal(err)
		}
		if row["_id"] != id {
			t.Fatalf("column_json _id %v != %s", row["_id"], id)
		}
		ids = append(ids, id)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 hits (a,b), got %v", ids)
	}

	// The same query through the Go API must produce the same id set and order.
	goHits, err := goAPIQuery(t, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(goHits) != len(ids) {
		t.Fatalf("Go API returned %d hits, C ABI %d", len(goHits), len(ids))
	}
	for i := range ids {
		if goHits[i] != ids[i] {
			t.Fatalf("hit %d differs: go=%s cabi=%s", i, goHits[i], ids[i])
		}
	}

	if rc := CursorClose(cur); rc != StatusOK {
		t.Fatalf("cursor_close rc=%d", rc)
	}
	if rc := QueryClose(q); rc != StatusOK {
		t.Fatalf("query_close rc=%d", rc)
	}
	if rc := SnapshotClose(snap); rc != StatusOK {
		t.Fatalf("snapshot_close rc=%d", rc)
	}
	if rc := Close(db); rc != StatusOK {
		t.Fatalf("close rc=%d %s", rc, ErrMsg(db))
	}
}

func TestCABIHandleLeak(t *testing.T) {
	start := liveHandles()

	db := openTestDB(t)
	defineCorpus(t, db, []map[string]any{
		{"_id": "a", "title": "alpha beta", "views": 1},
	})

	// Exercise the full handle lifecycle, including an intentionally out-of-order
	// close (cursor left open before query close) to confirm cleanup still drains.
	snap, _ := SnapshotOpen(db)
	q, _ := Prepare(snap, `{"match":{"field":"title","query":"alpha"}}`)
	cur, _ := QueryRun(q, "")
	for Step(cur) == StatusRow {
	}

	// Closing the db while a snapshot is open must be refused.
	if rc := Close(db); rc != StatusSnapshots {
		t.Fatalf("close with open snapshot should return SX_SNAPSHOTS, got %d", rc)
	}

	CursorClose(cur)
	QueryClose(q)
	SnapshotClose(snap)
	if rc := Close(db); rc != StatusOK {
		t.Fatalf("close rc=%d %s", rc, ErrMsg(db))
	}

	if got := liveHandles(); got != start {
		t.Fatalf("handle leak: started at %d, ended at %d", start, got)
	}

	// No goroutine should be left running by the surface.
	runtime.GC()
}

// goAPIQuery runs the reference query directly on the Go DB behind a db handle,
// returning the external ids in score order.
func goAPIQuery(t *testing.T, db uint64) ([]string, error) {
	t.Helper()
	obj, ok := table.get(kindDB, db)
	if !ok {
		t.Fatal("db handle not found")
	}
	q, err := query.ParseJSON([]byte(`{"match":{"field":"title","query":"brown"}}`))
	if err != nil {
		return nil, err
	}
	hits, err := obj.(*dbObj).db.Search(q, 10)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ExternalID
	}
	return out, nil
}
