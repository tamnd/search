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

func TestSXStats(t *testing.T) {
	idx := opsIndex(t)
	out := captureStdout(t, func() int { return cmdStats([]string{idx, "--format", "json"}) })
	var st search.IndexStats
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("stats output not json: %v\n%s", err, out)
	}
	if st.SegmentCount == 0 || len(st.Segments) == 0 {
		t.Fatalf("stats reported no segments: %+v", st)
	}
	if st.TotalTerms == 0 {
		t.Fatalf("stats reported no terms: %+v", st)
	}
	if st.DeletedDocCount != 1 {
		t.Fatalf("stats deleted = %d, want 1", st.DeletedDocCount)
	}

	tbl := captureStdout(t, func() int { return cmdStats([]string{idx}) })
	if !strings.Contains(tbl, "free pages:") || !strings.Contains(tbl, "segment ") {
		t.Fatalf("table stats missing fields:\n%s", tbl)
	}
}

func TestSXCheckpoint(t *testing.T) {
	idx := opsIndex(t)
	out := captureStdout(t, func() int { return cmdCheckpoint([]string{idx}) })
	if !strings.Contains(out, "self-contained") {
		t.Fatalf("checkpoint output unexpected:\n%s", out)
	}
	// No sidecar is ever produced by this engine.
	if _, err := os.Stat(idx + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("checkpoint left or expected a WAL sidecar: %v", err)
	}
}

func TestSXRestore(t *testing.T) {
	idx := opsIndex(t)
	dir := filepath.Dir(idx)
	backup := filepath.Join(dir, "b.sx")
	if code := cmdBackup([]string{idx, backup}); code != 0 {
		t.Fatalf("backup exit %d", code)
	}

	dst := filepath.Join(dir, "restored.sx")
	if code := cmdRestore([]string{dst, "--from", backup}); code != 0 {
		t.Fatalf("restore exit %d", code)
	}
	got := captureStdout(t, func() int { return cmdGet([]string{dst, "--id", "b"}) })
	var doc map[string]any
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("get from restored not json: %v\n%s", err, got)
	}
	if doc["tag"] != "y" {
		t.Fatalf("restored doc b = %+v", doc)
	}

	// Restoring over an existing file needs --force.
	if code := cmdRestore([]string{dst, "--from", backup}); code != 1 {
		t.Fatalf("restore over existing file should fail without --force, got %d", code)
	}
	if code := cmdRestore([]string{dst, "--from", backup, "--force"}); code != 0 {
		t.Fatalf("restore --force exit %d", code)
	}
}

func TestSXRepair(t *testing.T) {
	idx := opsIndex(t)
	dst := idx + ".repaired"
	code := cmdRepair([]string{idx})
	if code != 0 {
		t.Fatalf("repair of a healthy file exit %d, want 0", code)
	}
	// The rebuilt file carries exactly the live documents and serves them.
	v := captureStdout(t, func() int { return cmdVerify([]string{dst, "--deep"}) })
	if !strings.Contains(v, "live docs:    2") {
		t.Fatalf("repaired file should hold 2 live docs:\n%s", v)
	}
	got := captureStdout(t, func() int { return cmdGet([]string{dst, "--id", "a"}) })
	if !strings.Contains(got, "the quick brown fox") {
		t.Fatalf("repaired file missing doc a:\n%s", got)
	}
	// The deleted document does not come back.
	if code := cmdGet([]string{dst, "--id", "c"}); code == 0 {
		t.Fatalf("repaired file resurrected the deleted doc c")
	}
}

func TestSXRepairMissingSource(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.sx")
	if code := cmdRepair([]string{missing}); code != 4 {
		t.Fatalf("repair of a missing source = %d, want 4", code)
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
