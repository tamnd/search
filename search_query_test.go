package search

import (
	"math"
	"testing"

	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
)

// searchSchema is a text + keyword + numeric mapping used by the query tests.
func searchSchema(t *testing.T) *schema.Schema {
	t.Helper()
	s := schema.New()
	for _, f := range []schema.Field{
		schema.NewField("title", schema.TypeText),
		schema.NewField("tag", schema.TypeKeyword),
		schema.NewField("year", schema.TypeLong),
	} {
		if err := s.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// indexCorpus opens a DB with the search schema and indexes docs as one batch.
func indexCorpus(t *testing.T, docs []map[string]any) *DB {
	t.Helper()
	db := openDB(t)
	if err := db.PutSchema(searchSchema(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	return db
}

// extIDs returns the external ids of a hit list in rank order.
func extIDs(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ExternalID
	}
	return out
}

func sampleDocs() []map[string]any {
	return []map[string]any{
		{"_id": "a", "title": "the quick brown fox", "tag": "anim", "year": 2001},
		{"_id": "b", "title": "the lazy dog sleeps", "tag": "anim", "year": 2010},
		{"_id": "c", "title": "quick foxes are quick", "tag": "anim", "year": 1999},
		{"_id": "d", "title": "a slow green turtle", "tag": "plant", "year": 2020},
		{"_id": "e", "title": "brown bears and brown foxes", "tag": "anim", "year": 2005},
	}
}

func TestTermQuery_SingleSegment(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	hits, err := db.Search(query.Term("title", "fox"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := extIDs(hits); len(got) != 1 || got[0] != "a" {
		t.Fatalf("term fox = %v, want [a]", got)
	}
}

func TestMatchQuery_OR(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	// "quick dog" with the default OR matches a, b, c.
	hits, err := db.Search(query.Match("title", "quick dog"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("match OR = %v, want 3 hits", extIDs(hits))
	}
	// Both a and c match only "quick" (same IDF and length); c has the term twice
	// so its higher term frequency must rank it above a.
	rank := map[string]int{}
	for i, h := range hits {
		rank[h.ExternalID] = i
	}
	if rank["c"] >= rank["a"] {
		t.Fatalf("expected c to outrank a, got order %v", extIDs(hits))
	}
}

func TestBoolQuery_AND(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	q := query.Bool().
		MustClause(query.Term("title", "brown")).
		MustClause(query.Term("title", "fox"))
	hits, err := db.Search(q, 10)
	if err != nil {
		t.Fatal(err)
	}
	// a ("brown ... fox") and e ("brown ... foxes" -> "foxes" != "fox") :
	// only a has both "brown" and "fox" as exact terms.
	if got := extIDs(hits); len(got) != 1 || got[0] != "a" {
		t.Fatalf("bool AND = %v, want [a]", got)
	}
}

func TestBoolQuery_MustNot(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	q := query.Bool().
		MustClause(query.Term("title", "quick")).
		MustNotClause(query.Term("title", "brown"))
	hits, err := db.Search(q, 10)
	if err != nil {
		t.Fatal(err)
	}
	// "quick" is in a and c; exclude the one with "brown" (a) -> only c.
	if got := extIDs(hits); len(got) != 1 || got[0] != "c" {
		t.Fatalf("bool MUST_NOT = %v, want [c]", got)
	}
}

func TestBoolQuery_Filter(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	q := query.Bool().
		MustClause(query.Term("title", "fox")).
		FilterClause(query.Term("tag", "anim"))
	hits, err := db.Search(q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := extIDs(hits); len(got) != 1 || got[0] != "a" {
		t.Fatalf("bool filter = %v, want [a]", got)
	}
}

func TestPhraseQuery(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	hits, err := db.Search(query.Phrase("title", "brown fox"), 10)
	if err != nil {
		t.Fatal(err)
	}
	// "brown fox" adjacent and in order only in a.
	if got := extIDs(hits); len(got) != 1 || got[0] != "a" {
		t.Fatalf("phrase = %v, want [a]", got)
	}

	// "quick fox" is not adjacent in a ("quick brown fox"); slop 1 admits it.
	sloppy := query.Phrase("title", "quick fox")
	sloppy.Slop = 1
	hits, err = db.Search(sloppy, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := extIDs(hits); len(got) != 1 || got[0] != "a" {
		t.Fatalf("phrase slop 1 = %v, want [a]", got)
	}
}

func TestPrefixQuery(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	hits, err := db.Search(query.Prefix("title", "fox"), 10)
	if err != nil {
		t.Fatal(err)
	}
	// "fox" (a) and "foxes" (c, e) start with "fox".
	if got := extIDs(hits); len(got) != 3 {
		t.Fatalf("prefix fox = %v, want 3 hits", got)
	}
}

func TestRangeQuery_Numeric(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	// year >= 2005: b (2010), d (2020), e (2005).
	q := query.Range("year", "2005", "", true, false)
	hits, err := db.Search(q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("year >= 2005 = %v, want 3 hits", extIDs(hits))
	}
	for _, h := range hits {
		if y, _ := toInt(h.Document["year"]); y < 2005 {
			t.Fatalf("hit %q has year %d < 2005", h.ExternalID, y)
		}
	}

	// 2000 <= year <= 2010: a (2001), b (2010), e (2005).
	q = query.Range("year", "2000", "2010", true, true)
	hits, err = db.Search(q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("2000..2010 = %v, want 3 hits", extIDs(hits))
	}
}

func TestRangeQuery_Keyword(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	// tag in [anim, anin): only "anim".
	q := query.Range("tag", "anim", "anim", true, true)
	hits, err := db.Search(q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 4 {
		t.Fatalf("tag=anim range = %v, want 4 hits", extIDs(hits))
	}
}

func TestMatchAll(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	hits, err := db.Search(query.MatchAll(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 5 {
		t.Fatalf("match_all = %v, want 5 hits", extIDs(hits))
	}
}

func TestTopK_Accuracy(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	all, err := db.Search(query.Match("title", "quick brown fox"), 10)
	if err != nil {
		t.Fatal(err)
	}
	top2, err := db.Search(query.Match("title", "quick brown fox"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(top2) != 2 {
		t.Fatalf("top-2 returned %d hits", len(top2))
	}
	// The top-2 must be the two highest-scoring of the full result, in order.
	for i := range top2 {
		if top2[i].ExternalID != all[i].ExternalID {
			t.Fatalf("top-2[%d] = %q, want %q (from full %v)", i, top2[i].ExternalID, all[i].ExternalID, extIDs(all))
		}
	}
}

func TestMultiSegmentMerge(t *testing.T) {
	docs := sampleDocs()

	// One batch -> one segment.
	single := indexCorpus(t, docs)
	defer mustClose(t, single)

	// Same docs, one batch each -> five segments.
	multi := openDB(t)
	defer mustClose(t, multi)
	if err := multi.PutSchema(searchSchema(t)); err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if _, err := multi.Index([]map[string]any{d}); err != nil {
			t.Fatal(err)
		}
	}

	for _, q := range []query.Query{
		query.Match("title", "quick brown fox"),
		query.Term("title", "fox"),
		query.Bool().MustClause(query.Term("title", "quick")).MustNotClause(query.Term("title", "brown")),
		query.Range("year", "2005", "", true, false),
		query.MatchAll(),
	} {
		a, err := single.Search(q, 10)
		if err != nil {
			t.Fatal(err)
		}
		b, err := multi.Search(q, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(a) != len(b) {
			t.Fatalf("%T: single %v vs multi %v differ in length", q, extIDs(a), extIDs(b))
		}
		for i := range a {
			if a[i].ExternalID != b[i].ExternalID {
				t.Fatalf("%T rank %d: single %q vs multi %q", q, i, a[i].ExternalID, b[i].ExternalID)
			}
			if math.Abs(float64(a[i].Score-b[i].Score)) > 1e-5 {
				t.Fatalf("%T %q: single score %v vs multi score %v", q, a[i].ExternalID, a[i].Score, b[i].Score)
			}
		}
	}
}

func TestSearchString_EndToEnd(t *testing.T) {
	db := indexCorpus(t, sampleDocs())
	defer mustClose(t, db)

	hits, err := db.SearchString("title:fox", "title", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := extIDs(hits); len(got) != 1 || got[0] != "a" {
		t.Fatalf("query string = %v, want [a]", got)
	}
}

// toInt coerces a JSON-decoded numeric value to int64 for test assertions.
func toInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}
