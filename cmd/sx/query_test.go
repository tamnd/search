package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// buildQueryIndex creates an index with a small text+keyword+numeric corpus.
func buildQueryIndex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	idx := filepath.Join(dir, "q.sx")
	schemaPath := writeFile(t, dir, "schema.json",
		`{"fields":[{"name":"title","type":"text"},{"name":"tag","type":"keyword"},{"name":"year","type":"long"}]}`)
	if code := cmdCreate([]string{idx, "--schema", schemaPath}); code != 0 {
		t.Fatalf("create exit %d", code)
	}
	docs := `{"_id":"a","title":"the quick brown fox","tag":"anim","year":2001}
{"_id":"b","title":"the lazy dog sleeps","tag":"anim","year":2010}
{"_id":"c","title":"quick foxes are quick","tag":"anim","year":1999}`
	docPath := writeFile(t, dir, "docs.jsonl", docs)
	if code := cmdIndex([]string{idx, "--file", docPath}); code != 0 {
		t.Fatalf("index exit %d", code)
	}
	return idx
}

func TestSXQuery_String(t *testing.T) {
	idx := buildQueryIndex(t)

	out := captureStdout(t, func() int {
		return cmdQuery([]string{idx, "--field", "title", "--format", "jsonl", "fox"})
	})
	var hit map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &hit); err != nil {
		t.Fatalf("jsonl output not json: %v\n%s", err, out)
	}
	if hit["_id"] != "a" {
		t.Fatalf("query fox = %+v, want _id a", hit)
	}
	if _, ok := hit["_score"]; !ok {
		t.Fatalf("hit has no _score: %+v", hit)
	}
}

func TestSXQuery_FieldPrefix(t *testing.T) {
	idx := buildQueryIndex(t)

	out := captureStdout(t, func() int {
		return cmdQuery([]string{idx, "--format", "json", "title:quick"})
	})
	var res struct {
		Hits []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("json output not parseable: %v\n%s", err, out)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("title:quick = %d hits, want 2", len(res.Hits))
	}
}

func TestSXQuery_Table(t *testing.T) {
	idx := buildQueryIndex(t)

	out := captureStdout(t, func() int {
		return cmdQuery([]string{idx, "--field", "title", "fox"})
	})
	if !strings.Contains(out, "QUERY:") || !strings.Contains(out, "HITS: 1") {
		t.Fatalf("table header missing: %q", out)
	}
	if !strings.Contains(out, "a") {
		t.Fatalf("table missing hit id: %q", out)
	}
}

func TestSXQuery_Size(t *testing.T) {
	idx := buildQueryIndex(t)

	out := captureStdout(t, func() int {
		return cmdQuery([]string{idx, "--field", "title", "--format", "jsonl", "--size", "1", "quick fox"})
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("--size 1 returned %d lines:\n%s", len(lines), out)
	}
}

func TestSXQuery_JSONDSL(t *testing.T) {
	idx := buildQueryIndex(t)
	dir := filepath.Dir(idx)
	qPath := writeFile(t, dir, "q.json",
		`{"bool":{"must":[{"term":{"field":"title","value":"quick"}}],"must_not":[{"term":{"field":"title","value":"brown"}}]}}`)

	out := captureStdout(t, func() int {
		return cmdQuery([]string{idx, "--json", qPath, "--format", "jsonl"})
	})
	var hit map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &hit); err != nil {
		t.Fatalf("jsonl output not json: %v\n%s", err, out)
	}
	if hit["_id"] != "c" {
		t.Fatalf("bool query = %+v, want _id c", hit)
	}
}
