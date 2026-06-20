package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes content to a temp file and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSXCreateIndexGet(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(dir, "t.sx")
	schemaJSON := `{
	  "id_field": "_id",
	  "fields": [
	    {"name": "title", "type": "text", "analyzer": "english"},
	    {"name": "tag", "type": "keyword"}
	  ]
	}`
	schemaPath := writeFile(t, dir, "schema.json", schemaJSON)

	if code := cmdCreate([]string{idx, "--schema", schemaPath}); code != 0 {
		t.Fatalf("create exit %d", code)
	}

	docs := `{"_id":"a","title":"the quick fox","tag":"x"}
{"_id":"b","title":"lazy dog","tag":"y"}`
	docPath := writeFile(t, dir, "docs.jsonl", docs)
	if code := cmdIndex([]string{idx, "--file", docPath}); code != 0 {
		t.Fatalf("index exit %d", code)
	}

	// get by internal doc-id
	out := captureStdout(t, func() int { return cmdGet([]string{idx, "1"}) })
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("get doc-id output not json: %v\n%s", err, out)
	}
	if got["title"] != "the quick fox" {
		t.Fatalf("doc 1 = %+v", got)
	}

	// get by external id
	out = captureStdout(t, func() int { return cmdGet([]string{idx, "--id", "b"}) })
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("get ext output not json: %v\n%s", err, out)
	}
	if got["tag"] != "y" {
		t.Fatalf("doc b = %+v", got)
	}
}

func TestSXSchema(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(dir, "t.sx")
	schemaPath := writeFile(t, dir, "schema.json",
		`{"fields":[{"name":"title","type":"text","analyzer":"english"}]}`)
	if code := cmdCreate([]string{idx, "--schema", schemaPath}); code != 0 {
		t.Fatalf("create exit %d", code)
	}
	out := captureStdout(t, func() int { return cmdSchema([]string{idx}) })
	var sf schemaFile
	if err := json.Unmarshal([]byte(out), &sf); err != nil {
		t.Fatalf("schema output not json: %v\n%s", err, out)
	}
	if sf.IDField != "_id" || len(sf.Fields) != 1 || sf.Fields[0].Analyzer != "english" {
		t.Fatalf("schema = %+v", sf)
	}
}

func TestSXInspect(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(dir, "t.sx")
	schemaPath := writeFile(t, dir, "schema.json",
		`{"fields":[{"name":"title","type":"text"},{"name":"tag","type":"keyword"}]}`)
	if code := cmdCreate([]string{idx, "--schema", schemaPath}); code != 0 {
		t.Fatalf("create exit %d", code)
	}
	docs := `{"_id":"a","title":"quick brown fox","tag":"x"}
{"_id":"b","title":"lazy dog","tag":"x"}`
	docPath := writeFile(t, dir, "docs.jsonl", docs)
	if code := cmdIndex([]string{idx, "--file", docPath}); code != 0 {
		t.Fatalf("index exit %d", code)
	}

	out := captureStdout(t, func() int { return cmdInspect([]string{idx, "--format", "json"}) })
	var segs []segmentJSON
	if err := json.Unmarshal([]byte(out), &segs); err != nil {
		t.Fatalf("inspect output not json: %v\n%s", err, out)
	}
	if len(segs) != 1 || segs[0].DocCount != 2 {
		t.Fatalf("segments = %+v", segs)
	}
	var titleTerms uint64
	for _, f := range segs[0].Fields {
		if f.Name == "title" {
			titleTerms = f.TermCount
		}
		if f.Name == "tag" && f.TermCount != 1 {
			t.Fatalf("tag term count = %d, want 1", f.TermCount)
		}
	}
	// quick, brown, fox, lazy, dog
	if titleTerms != 5 {
		t.Fatalf("title term count = %d, want 5", titleTerms)
	}

	// Table output names the segment.
	out = captureStdout(t, func() int { return cmdInspect([]string{idx}) })
	if !strings.Contains(out, "segment 1") {
		t.Fatalf("table output = %q", out)
	}
}

func TestSXAnalyze(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(dir, "t.sx")
	if code := cmdCreate([]string{idx}); code != 0 {
		t.Fatalf("create exit %d", code)
	}

	// JSON output for a built-in analyzer.
	out := captureStdout(t, func() int {
		return cmdAnalyze([]string{idx, "--analyzer", "english", "--format", "json", "The cats are running"})
	})
	var toks []tokenJSON
	if err := json.Unmarshal([]byte(out), &toks); err != nil {
		t.Fatalf("analyze output not json: %v\n%s", err, out)
	}
	var terms []string
	for _, tk := range toks {
		terms = append(terms, tk.Term)
	}
	if strings.Join(terms, ",") != "cat,run" {
		t.Fatalf("english terms = %v", terms)
	}

	// Table output is the default and lists the same terms.
	out = captureStdout(t, func() int {
		return cmdAnalyze([]string{idx, "--analyzer", "standard", "Hello World"})
	})
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Fatalf("table output = %q", out)
	}
}

func TestSXUpdateDeleteCompact(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(dir, "t.sx")
	schemaPath := writeFile(t, dir, "schema.json",
		`{"fields":[{"name":"title","type":"text"}]}`)
	if code := cmdCreate([]string{idx, "--schema", schemaPath}); code != 0 {
		t.Fatalf("create exit %d", code)
	}

	// Two batches so there is something to compact.
	b1 := writeFile(t, dir, "b1.jsonl", `{"_id":"a","title":"alpha one"}
{"_id":"b","title":"alpha two"}`)
	b2 := writeFile(t, dir, "b2.jsonl", `{"_id":"c","title":"alpha three"}`)
	if code := cmdIndex([]string{idx, "--file", b1}); code != 0 {
		t.Fatalf("index b1 exit %d", code)
	}
	if code := cmdIndex([]string{idx, "--file", b2}); code != 0 {
		t.Fatalf("index b2 exit %d", code)
	}

	// Update replaces document a in place.
	up := writeFile(t, dir, "up.jsonl", `{"_id":"a","title":"alpha updated"}`)
	out := captureStdout(t, func() int { return cmdUpdate([]string{idx, "--file", up}) })
	if !strings.Contains(out, "updated 1") {
		t.Fatalf("update output = %q", out)
	}
	got := captureStdout(t, func() int { return cmdGet([]string{idx, "--id", "a"}) })
	var doc map[string]any
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("get a not json: %v\n%s", err, got)
	}
	if doc["title"] != "alpha updated" {
		t.Fatalf("doc a after update = %+v", doc)
	}

	// Delete drops document b; a second delete of the same id reports not found.
	out = captureStdout(t, func() int { return cmdDelete([]string{idx, "b"}) })
	if !strings.Contains(out, "deleted 1") || !strings.Contains(out, "0 not found") {
		t.Fatalf("delete output = %q", out)
	}
	if code := cmdDelete([]string{idx, "b"}); code != 0 {
		t.Fatalf("re-delete exit %d", code)
	}

	// Compact --all merges every segment into one and reaps the deleted document.
	out = captureStdout(t, func() int { return cmdCompact([]string{idx, "--all"}) })
	if !strings.Contains(out, "merged") {
		t.Fatalf("compact output = %q", out)
	}
	out = captureStdout(t, func() int { return cmdInspect([]string{idx, "--format", "json"}) })
	var segs []segmentJSON
	if err := json.Unmarshal([]byte(out), &segs); err != nil {
		t.Fatalf("inspect not json: %v\n%s", err, out)
	}
	if len(segs) != 1 {
		t.Fatalf("after compact: %d segments, want 1", len(segs))
	}
	// a (updated) and c survive; b was deleted.
	if segs[0].DocCount != 2 {
		t.Fatalf("after compact: doc count %d, want 2", segs[0].DocCount)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// wrote. The pipe is drained concurrently so output larger than the pipe buffer
// does not deadlock.
func captureStdout(t *testing.T, fn func() int) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	code := fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = orig
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("command exit %d, output:\n%s", code, out)
	}
	return out
}
