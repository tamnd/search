package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// vecDoc renders a JSONL line for a document with an embedding.
func vecDoc(id string, vec []float64, extra string) string {
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = fmt.Sprintf("%g", v)
	}
	embed := "[" + strings.Join(parts, ",") + "]"
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf(`{"_id":%q,"embed":%s%s}`, id, embed, extra)
}

func TestSXKNNAndHybrid(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(dir, "v.sx")
	schemaJSON := `{
	  "id_field": "_id",
	  "fields": [
	    {"name": "title", "type": "text"},
	    {"name": "embed", "type": "dense_vector", "dims": 4, "metric": "l2"}
	  ]
	}`
	schemaPath := writeFile(t, dir, "schema.json", schemaJSON)
	if code := cmdCreate([]string{idx, "--schema", schemaPath}); code != 0 {
		t.Fatalf("create exit %d", code)
	}

	docs := strings.Join([]string{
		vecDoc("a", []float64{1, 0, 0, 0}, `"title":"alpha"`),
		vecDoc("b", []float64{0, 1, 0, 0}, `"title":"beta"`),
		vecDoc("c", []float64{0, 0, 1, 0}, `"title":"gamma unicorn"`),
		vecDoc("d", []float64{0, 0, 0, 1}, `"title":"delta"`),
	}, "\n")
	docPath := writeFile(t, dir, "docs.jsonl", docs)
	if code := cmdIndex([]string{idx, "--file", docPath}); code != 0 {
		t.Fatalf("index exit %d", code)
	}

	// kNN: nearest to (1,0,0,0) is doc a.
	out := captureStdout(t, func() int {
		return cmdKNN([]string{idx, "--field", "embed", "--vector", "1,0,0,0", "--k", "2", "--format", "json"})
	})
	var res struct {
		Hits []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("knn output not json: %v\n%s", err, out)
	}
	if len(res.Hits) == 0 || res.Hits[0]["_id"] != "a" {
		t.Fatalf("knn nearest should be a, got %v", res.Hits)
	}

	// Hybrid: text "unicorn" favors c, vector (1,0,0,0) favors a; both surface.
	out = captureStdout(t, func() int {
		return cmdHybrid([]string{idx, "--field", "embed", "--vector", "1,0,0,0", "--k", "4", "--format", "json", "unicorn"})
	})
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("hybrid output not json: %v\n%s", err, out)
	}
	got := map[string]bool{}
	for _, h := range res.Hits {
		got[fmt.Sprint(h["_id"])] = true
	}
	if !got["a"] || !got["c"] {
		t.Fatalf("hybrid should include a (vector) and c (text), got %v", res.Hits)
	}
}
