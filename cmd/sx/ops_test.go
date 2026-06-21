package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/search"
)

// opsIndex creates a small indexed file for the ops-command tests and returns
// its path. One document is deleted so the live-doc paths are covered.
func opsIndex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	idx := filepath.Join(dir, "t.sx")
	schemaPath := writeFile(t, dir, "schema.json",
		`{"id_field":"_id","fields":[{"name":"title","type":"text","analyzer":"english"},{"name":"tag","type":"keyword"}]}`)
	if code := cmdCreate([]string{idx, "--schema", schemaPath}); code != 0 {
		t.Fatalf("create exit %d", code)
	}
	docs := `{"_id":"a","title":"the quick brown fox","tag":"x"}
{"_id":"b","title":"a slow green turtle","tag":"y"}
{"_id":"c","title":"lazy yellow dog","tag":"x"}`
	docPath := writeFile(t, dir, "docs.jsonl", docs)
	if code := cmdIndex([]string{idx, "--file", docPath}); code != 0 {
		t.Fatalf("index exit %d", code)
	}
	if code := cmdDelete([]string{idx, "c"}); code != 0 {
		t.Fatalf("delete exit %d", code)
	}
	return idx
}

func TestSXInfo(t *testing.T) {
	idx := opsIndex(t)
	out := captureStdout(t, func() int { return cmdInfo([]string{idx, "--format", "json"}) })
	var fi search.FileInfo
	if err := json.Unmarshal([]byte(out), &fi); err != nil {
		t.Fatalf("info output not json: %v\n%s", err, out)
	}
	if fi.PageSize == 0 || fi.SegmentCount == 0 {
		t.Fatalf("info = %+v", fi)
	}
	if fi.FormatVersion != search.FormatVersion {
		t.Fatalf("format version = %d", fi.FormatVersion)
	}

	// The table form should also render without error.
	tbl := captureStdout(t, func() int { return cmdInfo([]string{idx}) })
	if !strings.Contains(tbl, "page size:") {
		t.Fatalf("table info missing fields:\n%s", tbl)
	}
}

func TestSXVerify(t *testing.T) {
	idx := opsIndex(t)
	out := captureStdout(t, func() int { return cmdVerify([]string{idx, "--deep"}) })
	if !strings.Contains(out, "result:       OK") {
		t.Fatalf("verify did not report OK:\n%s", out)
	}
	if !strings.Contains(out, "postings:") {
		t.Fatalf("deep verify should report postings:\n%s", out)
	}
}

func TestSXVerifyDetectsCorruption(t *testing.T) {
	idx := opsIndex(t)
	raw, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	// Flip bytes across every page past the meta pages; page size is the default.
	const ps = 16384
	for off := 3 * ps; off+200 < len(raw); off += ps {
		raw[off+200] ^= 0xFF
	}
	if err := os.WriteFile(idx, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// Either open fails or verify reports a non-zero exit; both are acceptable.
	code := cmdVerify([]string{idx, "--deep"})
	if code == 0 {
		t.Fatalf("verify passed a corrupted file")
	}
}

func TestSXExportImport(t *testing.T) {
	idx := opsIndex(t)
	dir := filepath.Dir(idx)
	dump := filepath.Join(dir, "dump.jsonl")

	if code := cmdExport([]string{idx, "--out", dump}); code != 0 {
		t.Fatalf("export exit %d", code)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	lines := nonEmptyLines(string(b))
	if len(lines) != 2 {
		t.Fatalf("export wrote %d lines, want 2:\n%s", len(lines), b)
	}

	// Import the dump into a fresh index and confirm both documents land.
	idx2 := filepath.Join(dir, "t2.sx")
	schemaPath := writeFile(t, dir, "schema2.json",
		`{"id_field":"_id","fields":[{"name":"title","type":"text","analyzer":"english"},{"name":"tag","type":"keyword"}]}`)
	if code := cmdCreate([]string{idx2, "--schema", schemaPath}); code != 0 {
		t.Fatalf("create2 exit %d", code)
	}
	if code := cmdImport([]string{idx2, "--file", dump, "--batch", "1"}); code != 0 {
		t.Fatalf("import exit %d", code)
	}
	got := captureStdout(t, func() int { return cmdGet([]string{idx2, "--id", "a"}) })
	var doc map[string]any
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("get after import not json: %v\n%s", err, got)
	}
	if doc["title"] != "the quick brown fox" {
		t.Fatalf("imported doc a = %+v", doc)
	}
}

func TestSXBackup(t *testing.T) {
	idx := opsIndex(t)
	dst := filepath.Join(filepath.Dir(idx), "backup.sx")
	if code := cmdBackup([]string{idx, dst}); code != 0 {
		t.Fatalf("backup exit %d", code)
	}
	// The backup opens on its own and serves the same documents.
	got := captureStdout(t, func() int { return cmdGet([]string{dst, "--id", "b"}) })
	var doc map[string]any
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("get from backup not json: %v\n%s", err, got)
	}
	if doc["tag"] != "y" {
		t.Fatalf("backup doc b = %+v", doc)
	}
}

func TestSXVacuum(t *testing.T) {
	idx := opsIndex(t)
	out := captureStdout(t, func() int { return cmdVacuum([]string{idx}) })
	if !strings.Contains(out, "vacuumed") {
		t.Fatalf("vacuum output unexpected:\n%s", out)
	}
	// After a vacuum the deleted document is gone and the live ones remain.
	v := captureStdout(t, func() int { return cmdVerify([]string{idx, "--deep"}) })
	if !strings.Contains(v, "live docs:    2") {
		t.Fatalf("expected 2 live docs after vacuum:\n%s", v)
	}
}

// nonEmptyLines splits s into its non-blank lines.
func nonEmptyLines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
