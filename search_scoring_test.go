package search

import (
	"testing"

	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
)

// scoreSchema is a text title plus a numeric popularity field for the
// function-score tests.
func scoreSchema(t *testing.T) *schema.Schema {
	t.Helper()
	s := schema.New()
	for _, f := range []schema.Field{
		schema.NewField("title", schema.TypeText),
		schema.NewField("body", schema.TypeText),
		schema.NewField("views", schema.TypeLong),
	} {
		if err := s.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func indexScore(t *testing.T, docs []map[string]any) *DB {
	t.Helper()
	db := openDB(t)
	if err := db.PutSchema(scoreSchema(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	return db
}

func scoreOf(hits []Hit, id string) (float32, bool) {
	for _, h := range hits {
		if h.ExternalID == id {
			return h.Score, true
		}
	}
	return 0, false
}

func TestFunctionScore_FieldValueFactor(t *testing.T) {
	db := indexScore(t, []map[string]any{
		{"_id": "low", "title": "coffee beans", "views": 1},
		{"_id": "high", "title": "coffee beans", "views": 1000},
	})
	defer mustClose(t, db)

	// Both documents share the same text, so plain BM25 scores them equally. The
	// log1p view-count factor must lift the popular one above the other.
	fs := query.FunctionScore(
		query.Match("title", "coffee"),
		query.ScoreFunction{FieldValue: &query.FieldValueFactor{Field: "views", Modifier: query.ModLn1p, Missing: 1}},
	)
	hits, err := db.Search(fs, 10)
	if err != nil {
		t.Fatal(err)
	}
	hi, ok := scoreOf(hits, "high")
	if !ok {
		t.Fatalf("missing high in %v", extIDs(hits))
	}
	lo, ok := scoreOf(hits, "low")
	if !ok {
		t.Fatalf("missing low in %v", extIDs(hits))
	}
	if hi <= lo {
		t.Fatalf("popular doc should outscore unpopular: high=%f low=%f", hi, lo)
	}
	if hits[0].ExternalID != "high" {
		t.Fatalf("high should rank first, got %v", extIDs(hits))
	}
}

func TestFunctionScore_Replace(t *testing.T) {
	db := indexScore(t, []map[string]any{
		{"_id": "a", "title": "coffee", "views": 3},
		{"_id": "b", "title": "coffee", "views": 7},
	})
	defer mustClose(t, db)

	// boost_mode replace with a raw field value factor makes the score equal to the
	// view count exactly.
	fs := &query.FunctionScoreQuery{
		Query:     query.Match("title", "coffee"),
		Functions: []query.ScoreFunction{{FieldValue: &query.FieldValueFactor{Field: "views", Missing: 0}}},
		BoostMode: query.BoostReplace,
	}
	hits, err := db.Search(fs, 10)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := scoreOf(hits, "b"); s != 7 {
		t.Fatalf("replace score for b should be 7, got %f", s)
	}
	if s, _ := scoreOf(hits, "a"); s != 3 {
		t.Fatalf("replace score for a should be 3, got %f", s)
	}
}

func TestRescore_Precision(t *testing.T) {
	db := indexScore(t, []map[string]any{
		// Scattered occurrence: matches the terms but not adjacent.
		{"_id": "scattered", "title": "quick red animal and a lazy brown bear fox"},
		// Exact phrase: the rescore should promote this above the scattered match.
		{"_id": "phrase", "title": "the quick brown fox runs"},
	})
	defer mustClose(t, db)

	base := query.Match("title", "quick brown fox")
	rq := query.Rescore(base, query.Phrase("title", "quick brown fox"), 10)
	rq.QueryWeight = 1
	rq.RescoreWeight = 5
	hits, err := db.Search(rq, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 {
		t.Fatalf("expected both docs, got %v", extIDs(hits))
	}
	if hits[0].ExternalID != "phrase" {
		t.Fatalf("phrase rescore should promote the adjacent match first, got %v", extIDs(hits))
	}
}

func TestBM25F_CombinesFields(t *testing.T) {
	db := indexScore(t, []map[string]any{
		// Two occurrences in one field.
		{"_id": "onefield", "title": "alpha alpha", "body": "unrelated text here"},
		// One occurrence spread across two fields, with the title boosted.
		{"_id": "twofield", "title": "alpha", "body": "alpha appears here too"},
	})
	defer mustClose(t, db)

	q := query.BM25F([]string{"alpha"},
		query.BM25FField{Name: "title", Boost: 3, B: -1},
		query.BM25FField{Name: "body", Boost: 1, B: -1},
	)
	hits, err := db.Search(q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected both docs, got %v", extIDs(hits))
	}
	if _, ok := scoreOf(hits, "twofield"); !ok {
		t.Fatalf("twofield should match, got %v", extIDs(hits))
	}
	// The boosted title plus the body evidence should let the cross-field document
	// score above zero and participate in ranking.
	if s, _ := scoreOf(hits, "twofield"); s <= 0 {
		t.Fatalf("twofield should have a positive score, got %f", s)
	}
}
