package search

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// opsDB builds a small indexed database for the ops tests: three documents over
// a text+keyword schema, with one deleted so the live-doc paths are exercised.
func opsDB(t *testing.T) *DB {
	t.Helper()
	db := openDB(t)
	if err := db.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	docs := []map[string]any{
		{"_id": "a", "title": "the quick brown fox", "tag": "x"},
		{"_id": "b", "title": "a slow green turtle", "tag": "y"},
		{"_id": "c", "title": "lazy yellow dog", "tag": "x"},
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.Delete("c"); err != nil || !ok {
		t.Fatalf("delete c: ok=%v err=%v", ok, err)
	}
	return db
}

func TestInfo(t *testing.T) {
	db := opsDB(t)
	defer mustClose(t, db)

	fi, err := db.Info()
	if err != nil {
		t.Fatal(err)
	}
	if fi.Path != "idx.sx" {
		t.Fatalf("path = %q, want idx.sx", fi.Path)
	}
	if fi.PageSize == 0 || fi.PageCount == 0 {
		t.Fatalf("geometry not populated: %+v", fi)
	}
	if fi.FileBytes != int64(fi.PageSize)*int64(fi.PageCount) {
		t.Fatalf("file bytes = %d, want %d", fi.FileBytes, int64(fi.PageSize)*int64(fi.PageCount))
	}
	if fi.FormatVersion != FormatVersion {
		t.Fatalf("format version = %d, want %d", fi.FormatVersion, FormatVersion)
	}
	if fi.EngineVersionMin == 0 {
		t.Fatalf("engine version min should be set, got 0")
	}
}

func TestStats(t *testing.T) {
	db := opsDB(t)
	defer mustClose(t, db)

	// A read snapshot held open while Stats runs must show up in the live
	// reader counters, and its txn must be reported as the oldest pinned reader.
	tx, err := db.Begin(false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	st, err := db.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.SegmentCount == 0 || len(st.Segments) == 0 {
		t.Fatalf("no segments: %+v", st)
	}
	if st.DocCount != 3 || st.DeletedDocCount != 1 {
		t.Fatalf("counts: docs=%d deleted=%d, want 3/1", st.DocCount, st.DeletedDocCount)
	}
	if st.TotalTerms == 0 {
		t.Fatalf("no terms counted: %+v", st)
	}
	if st.ActiveReaders < 1 {
		t.Fatalf("active readers = %d, want at least 1", st.ActiveReaders)
	}
	if st.OldestReaderTxn == 0 {
		t.Fatalf("oldest reader txn should be set with a snapshot open")
	}
	// The per-field term counts must sum to the reported total.
	var sum uint64
	for _, s := range st.Segments {
		for _, f := range s.Fields {
			sum += f.TermCount
		}
	}
	if sum != st.TotalTerms {
		t.Fatalf("term total %d != sum of fields %d", st.TotalTerms, sum)
	}
}

func TestVerifyClean(t *testing.T) {
	db := opsDB(t)
	defer mustClose(t, db)

	rep, err := db.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Fatalf("clean index reported errors: %v", rep.Errors)
	}
	if rep.Segments == 0 {
		t.Fatalf("expected at least one segment, got %d", rep.Segments)
	}
	if rep.Terms == 0 {
		t.Fatalf("expected terms to be scanned, got 0")
	}
	if rep.PostingsRead == 0 {
		t.Fatalf("deep verify should read postings, got 0")
	}
	// Two live documents remain after deleting c.
	if rep.LiveDocs != 2 {
		t.Fatalf("live docs = %d, want 2", rep.LiveDocs)
	}
}

func TestExportRoundTrips(t *testing.T) {
	db := opsDB(t)
	defer mustClose(t, db)

	var buf bytes.Buffer
	n, err := db.Export(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("exported %d docs, want 2 (c was deleted)", n)
	}

	got := map[string]map[string]any{}
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Fatalf("export line not valid json: %v", err)
		}
		id, _ := d["_id"].(string)
		got[id] = d
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["a"] == nil || got["b"] == nil {
		t.Fatalf("export ids = %v, want a and b", keysOf(got))
	}
	if got["c"] != nil {
		t.Fatalf("deleted doc c should not be exported")
	}
	if got["a"]["title"] != "the quick brown fox" {
		t.Fatalf("exported doc a title = %v", got["a"]["title"])
	}

	// The exported JSONL feeds straight back into a fresh index.
	db2 := openDB(t)
	defer mustClose(t, db2)
	if err := db2.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	var reload []map[string]any
	sc2 := bufio.NewScanner(strings.NewReader(bufExport(t, db)))
	for sc2.Scan() {
		var d map[string]any
		if err := json.Unmarshal(sc2.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		reload = append(reload, d)
	}
	if got, err := db2.Index(reload); err != nil || got != 2 {
		t.Fatalf("reindex exported docs: n=%d err=%v", got, err)
	}
}

func TestVerifyCatchesCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.sx")
	db, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	docs := make([]map[string]any, 0, 200)
	for i := range 200 {
		docs = append(docs, map[string]any{
			"_id":   fmt.Sprintf("d%d", i),
			"title": fmt.Sprintf("document number %d about foxes and turtles", i),
			"tag":   "t",
		})
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	ps := int(db.PageSize())
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Flip a byte inside the body of every page past the meta pages. Some of
	// those pages are live and reachable from the catalog, so a checksum failure
	// must surface either at open or in the verify report.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for off := 3 * ps; off+200 < len(raw); off += ps {
		raw[off+200] ^= 0xFF
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(path, Options{ReadOnly: true})
	if err != nil {
		return // corruption rejected at open is an acceptable outcome
	}
	defer mustClose(t, db2)
	rep, err := db2.Verify(true)
	if err != nil {
		return // a fatal read error is also acceptable
	}
	if rep.OK() {
		t.Fatalf("verify passed a corrupted file: %+v", rep)
	}
}

// bufExport returns the JSONL export of db as a string.
func bufExport(t *testing.T, db *DB) string {
	t.Helper()
	var b bytes.Buffer
	if _, err := db.Export(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func keysOf[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
