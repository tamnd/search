package search

import (
	"reflect"
	"testing"

	"github.com/tamnd/search/analysis"
	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/vfs"
)

// openDB opens a fresh in-memory index for a test.
func openDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open("idx.sx", Options{VFS: vfs.NewMem(), Clock: determ.NewFakeClock(0), SaltSeed: 1})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// sampleSchema is a small text+keyword mapping used across document tests.
func sampleSchema(t *testing.T) *schema.Schema {
	t.Helper()
	s := schema.New()
	for _, f := range []schema.Field{
		schema.NewField("title", schema.TypeText),
		schema.NewField("tag", schema.TypeKeyword),
	} {
		if err := s.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestSchemaRoundTrip(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)

	if _, err := db.Schema(); err != ErrNoSchema {
		t.Fatalf("Schema() before PutSchema = %v, want ErrNoSchema", err)
	}
	if err := db.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	got, err := db.Schema()
	if err != nil {
		t.Fatal(err)
	}
	if got.PrimaryKey() != "_id" || len(got.Fields) != 2 {
		t.Fatalf("schema = %+v", got)
	}
	if f, ok := got.Lookup("title"); !ok || f.Type != schema.TypeText {
		t.Fatalf("title field = %+v ok=%v", f, ok)
	}
}

func TestSchemaFrozenAfterIndex(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)

	if err := db.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	// Replacing the schema freely before any document is allowed.
	s2 := sampleSchema(t)
	if err := s2.Add(schema.NewField("body", schema.TypeText)); err != nil {
		t.Fatal(err)
	}
	if err := db.PutSchema(s2); err != nil {
		t.Fatalf("pre-index replace: %v", err)
	}

	if _, err := db.Index([]map[string]any{{"_id": "a", "title": "hello"}}); err != nil {
		t.Fatal(err)
	}

	// Adding a field after documents exist is still fine.
	s3 := sampleSchema(t)
	for _, f := range []schema.Field{
		schema.NewField("body", schema.TypeText),
		schema.NewField("year", schema.TypeLong),
	} {
		if err := s3.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.PutSchema(s3); err != nil {
		t.Fatalf("additive change after index: %v", err)
	}

	// Changing an existing field's type is rejected.
	bad := schema.New()
	if err := bad.Add(schema.NewField("title", schema.TypeKeyword)); err != nil {
		t.Fatal(err)
	}
	if err := db.PutSchema(bad); err == nil {
		t.Fatal("expected ErrSchemaFrozen for type change")
	}
}

func TestIndexAndGet(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)

	if err := db.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	docs := []map[string]any{
		{"_id": "doc-a", "title": "the quick fox", "tag": "x"},
		{"_id": "doc-b", "title": "lazy dog", "tag": "y"},
	}
	n, err := db.Index(docs)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("indexed %d, want 2", n)
	}

	// doc-ids are assigned monotonically from 1.
	got, err := db.GetByDocID(1)
	if err != nil {
		t.Fatal(err)
	}
	if got["title"] != "the quick fox" {
		t.Fatalf("doc 1 = %+v", got)
	}

	byExt, err := db.GetByExternalID("doc-b")
	if err != nil {
		t.Fatal(err)
	}
	if byExt["tag"] != "y" {
		t.Fatalf("doc-b = %+v", byExt)
	}

	if _, err := db.GetByDocID(99); err != ErrNoDoc {
		t.Fatalf("missing doc-id = %v, want ErrNoDoc", err)
	}
	if _, err := db.GetByExternalID("nope"); err != ErrNoDoc {
		t.Fatalf("missing ext id = %v, want ErrNoDoc", err)
	}
}

func TestReindexReplacesDocument(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)

	if err := db.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Index([]map[string]any{{"_id": "k", "title": "v1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Index([]map[string]any{{"_id": "k", "title": "v2"}}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetByExternalID("k")
	if err != nil {
		t.Fatal(err)
	}
	if got["title"] != "v2" {
		t.Fatalf("after reindex = %+v, want v2", got)
	}
	// Re-indexing soft-deletes the old version and writes the new one under a
	// fresh doc-id, so doc 1 is gone and doc 2 holds the current version.
	if _, err := db.GetByDocID(1); err != ErrNoDoc {
		t.Fatalf("doc 1 = %v, want ErrNoDoc (old version deleted)", err)
	}
	d2, err := db.GetByDocID(2)
	if err != nil {
		t.Fatal(err)
	}
	if d2["title"] != "v2" {
		t.Fatalf("doc 2 = %+v, want v2", d2)
	}
}

func TestIndexWithoutSchema(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)
	if _, err := db.Index([]map[string]any{{"_id": "a"}}); err != ErrNoSchema {
		t.Fatalf("Index without schema = %v, want ErrNoSchema", err)
	}
}

func TestIndexAutoExternalID(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)

	if err := db.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"title": "no id here"}
	if _, err := db.Index([]map[string]any{doc}); err != nil {
		t.Fatal(err)
	}
	// A document without the primary key gets an external id equal to its doc-id.
	if doc["_id"] != "1" {
		t.Fatalf("auto _id = %v, want \"1\"", doc["_id"])
	}
	got, err := db.GetByExternalID("1")
	if err != nil {
		t.Fatal(err)
	}
	if got["title"] != "no id here" {
		t.Fatalf("auto-id doc = %+v", got)
	}
}

func TestPersistAcrossReopen(t *testing.T) {
	fs := vfs.NewMem()
	db, err := Open("idx.sx", Options{VFS: fs, Clock: determ.NewFakeClock(0), SaltSeed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Index([]map[string]any{{"_id": "x", "title": "kept"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open("idx.sx", Options{VFS: fs})
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db2)
	got, err := db2.GetByExternalID("x")
	if err != nil {
		t.Fatal(err)
	}
	if got["title"] != "kept" {
		t.Fatalf("after reopen = %+v", got)
	}
}

func TestAnalyzeNamed(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)

	toks, err := db.Analyze("english", "The cats are RUNNING")
	if err != nil {
		t.Fatal(err)
	}
	got := termList(toks)
	if !reflect.DeepEqual(got, []string{"cat", "run"}) {
		t.Fatalf("english analyze = %v", got)
	}

	if _, err := db.Analyze("bogus", "x"); err == nil {
		t.Fatal("expected error for unknown analyzer")
	}
}

func TestAnalyzeField(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)

	s := schema.New()
	body := schema.NewField("body", schema.TypeText)
	body.Opts.Analyzer = "english"
	for _, f := range []schema.Field{
		body,
		schema.NewField("title", schema.TypeText),
		schema.NewField("tag", schema.TypeKeyword),
	} {
		if err := s.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.PutSchema(s); err != nil {
		t.Fatal(err)
	}

	// body uses english (stemmed, stopword-stripped).
	toks, err := db.AnalyzeField("body", "the running dogs")
	if err != nil {
		t.Fatal(err)
	}
	if got := termList(toks); !reflect.DeepEqual(got, []string{"run", "dog"}) {
		t.Fatalf("body analyze = %v", got)
	}

	// title has no analyzer set: standard (lowercased, no stemming).
	toks, err = db.AnalyzeField("title", "the running dogs")
	if err != nil {
		t.Fatal(err)
	}
	if got := termList(toks); !reflect.DeepEqual(got, []string{"the", "running", "dogs"}) {
		t.Fatalf("title analyze = %v", got)
	}

	// keyword fields are emitted whole.
	toks, err = db.AnalyzeField("tag", "Hello World")
	if err != nil {
		t.Fatal(err)
	}
	if got := termList(toks); !reflect.DeepEqual(got, []string{"Hello World"}) {
		t.Fatalf("tag analyze = %v", got)
	}

	if _, err := db.AnalyzeField("nope", "x"); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestCustomAnalyzerStored(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)

	cfg := analysis.AnalyzerConfig{
		Name:      "edge",
		Tokenizer: analysis.TokenizerConfig{Type: "edge_ngram", Min: 1, Max: 3},
		TokenFilters: []analysis.TokenFilterConfig{
			{Type: "lowercase"},
		},
	}
	if err := db.PutAnalyzer(cfg); err != nil {
		t.Fatal(err)
	}
	toks, err := db.Analyze("edge", "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if got := termList(toks); !reflect.DeepEqual(got, []string{"h", "he", "hel"}) {
		t.Fatalf("custom edge = %v", got)
	}

	if err := db.PutAnalyzer(analysis.AnalyzerConfig{Name: ""}); err == nil {
		t.Fatal("expected error for unnamed analyzer")
	}
}

// termList extracts term strings from a token slice.
func termList(toks []analysis.Token) []string {
	if len(toks) == 0 {
		return nil
	}
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.Term
	}
	return out
}
