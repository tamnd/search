package search

import (
	"testing"

	"github.com/tamnd/search/analysis"
	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/vfs"
)

// repairOpts returns options bound to one in-memory filesystem so that a source
// file and the repaired output it produces live in the same VFS.
func repairOpts(mem vfs.VFS) Options {
	return Options{VFS: mem, Clock: determ.NewFakeClock(0), SaltSeed: 1}
}

func TestRepairRebuildsLiveDocuments(t *testing.T) {
	mem := vfs.NewMem()
	opt := repairOpts(mem)

	db, err := Open("src.sx", opt)
	if err != nil {
		t.Fatal(err)
	}

	// A field analyzed by a stored custom analyzer: if repair fails to carry the
	// analyzer to the rebuilt file, reindexing this field would error and the
	// documents would be dropped, so a clean repair proves the carry-over works.
	cfg := analysis.AnalyzerConfig{
		Name:      "edge",
		Tokenizer: analysis.TokenizerConfig{Type: "edge_ngram", Min: 1, Max: 3},
	}
	if err := db.PutAnalyzer(cfg); err != nil {
		t.Fatal(err)
	}
	s := schema.New()
	title := schema.NewField("title", schema.TypeText)
	title.Opts.Analyzer = "edge"
	if err := s.Add(title); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(schema.NewField("tag", schema.TypeKeyword)); err != nil {
		t.Fatal(err)
	}
	if err := db.PutSchema(s); err != nil {
		t.Fatal(err)
	}
	docs := []map[string]any{
		{"_id": "a", "title": "alpha", "tag": "x"},
		{"_id": "b", "title": "bravo", "tag": "y"},
		{"_id": "c", "title": "charlie", "tag": "x"},
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Delete("c"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	rep, err := Repair("src.sx", "out.sx", opt)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("repair not clean: %+v", rep)
	}
	if rep.Recovered != 2 || rep.Dropped != 0 {
		t.Fatalf("repair recovered=%d dropped=%d, want 2/0", rep.Recovered, rep.Dropped)
	}

	out, err := Open("out.sx", Options{VFS: mem, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()

	// The custom analyzer survived the rebuild.
	if _, err := out.Analyze("edge", "Hi"); err != nil {
		t.Fatalf("custom analyzer missing from repaired file: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := out.GetByExternalID(id); err != nil {
			t.Fatalf("repaired file missing live doc %q: %v", id, err)
		}
	}
	if _, err := out.GetByExternalID("c"); err != ErrNoDoc {
		t.Fatalf("repaired file resurrected deleted doc c: %v", err)
	}
}

func TestRepairMissingSourceErrors(t *testing.T) {
	mem := vfs.NewMem()
	if _, err := Repair("nope.sx", "out.sx", repairOpts(mem)); err == nil {
		t.Fatal("repair of a missing source should error")
	}
}
