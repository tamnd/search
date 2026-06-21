package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSXSQL_Match(t *testing.T) {
	idx := buildQueryIndex(t)

	out := captureStdout(t, func() int {
		return cmdSQL([]string{idx, "--format", "jsonl", "SELECT _id, title FROM docs WHERE docs MATCH 'fox'"})
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("no rows returned:\n%s", out)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("jsonl not json: %v\n%s", err, out)
	}
	if row["_id"] != "a" {
		t.Fatalf("top row = %+v, want _id a", row)
	}
}

func TestSXSQL_RangeJSON(t *testing.T) {
	idx := buildQueryIndex(t)

	out := captureStdout(t, func() int {
		return cmdSQL([]string{idx, "--format", "json",
			"SELECT _id FROM docs WHERE year BETWEEN 2000 AND 2010 ORDER BY year ASC"})
	})
	var res struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("json not parseable: %v\n%s", err, out)
	}
	got := make([]string, len(res.Rows))
	for i, r := range res.Rows {
		got[i], _ = r["_id"].(string)
	}
	want := []string{"a", "b"} // 2001 and 2010 fall in [2000,2010]; 1999 does not
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("range rows = %v, want %v", got, want)
	}
}

func TestSXSQL_NamedBind(t *testing.T) {
	idx := buildQueryIndex(t)

	out := captureStdout(t, func() int {
		return cmdSQL([]string{idx, "--format", "csv", "-v", "q=quick",
			"SELECT _id FROM docs WHERE docs MATCH :q"})
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected header plus rows, got:\n%s", out)
	}
	if lines[0] != "_id" {
		t.Fatalf("csv header = %q, want _id", lines[0])
	}
}

func TestSXSQL_Unsupported(t *testing.T) {
	idx := buildQueryIndex(t)
	if code := cmdSQL([]string{idx, "DELETE FROM docs"}); code == 0 {
		t.Fatal("DELETE should be rejected with a non-zero exit")
	}
}
