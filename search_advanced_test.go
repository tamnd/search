package search

import (
	"testing"

	"github.com/tamnd/search/docvalues"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
)

// advSchema is a text + keyword + geo mapping for the advanced-query tests.
func advSchema(t *testing.T) *schema.Schema {
	t.Helper()
	s := schema.New()
	for _, f := range []schema.Field{
		schema.NewField("title", schema.TypeText),
		schema.NewField("code", schema.TypeKeyword),
		schema.NewField("loc", schema.TypeGeoPoint),
	} {
		if err := s.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func indexAdv(t *testing.T, docs []map[string]any) *DB {
	t.Helper()
	db := openDB(t)
	if err := db.PutSchema(advSchema(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	return db
}

func hitSet(hits []Hit) map[string]bool {
	m := map[string]bool{}
	for _, h := range hits {
		m[h.ExternalID] = true
	}
	return m
}

func TestFuzzyQuery(t *testing.T) {
	db := indexAdv(t, []map[string]any{
		{"_id": "a", "title": "search engine"},
		{"_id": "b", "title": "research paper"},
		{"_id": "c", "title": "surgery room"},
	})
	defer mustClose(t, db)

	// "searh" within one edit matches "search".
	hits, err := db.Search(&query.FuzzyQuery{Field: "title", Term: "searh", MaxEdits: 1}, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := hitSet(hits)
	if !got["a"] {
		t.Fatalf("fuzzy searh~1 should match a (search), got %v", extIDs(hits))
	}
	if got["c"] {
		t.Fatalf("fuzzy searh~1 should not match c (surgery), got %v", extIDs(hits))
	}
}

func TestWildcardQuery(t *testing.T) {
	db := indexAdv(t, []map[string]any{
		{"_id": "a", "title": "prefix premium"},
		{"_id": "b", "title": "preface present"},
		{"_id": "c", "title": "post hoc"},
	})
	defer mustClose(t, db)

	hits, err := db.Search(query.Wildcard("title", "pre*"), 10)
	if err != nil {
		t.Fatal(err)
	}
	got := hitSet(hits)
	if !got["a"] || !got["b"] {
		t.Fatalf("wildcard pre* should match a and b, got %v", extIDs(hits))
	}
	if got["c"] {
		t.Fatalf("wildcard pre* should not match c, got %v", extIDs(hits))
	}
}

func TestRegexpQuery(t *testing.T) {
	db := indexAdv(t, []map[string]any{
		{"_id": "a", "code": "AB1234"},
		{"_id": "b", "code": "XY42"},
		{"_id": "c", "code": "ZZ9999"},
	})
	defer mustClose(t, db)

	// Keyword terms holding exactly four digits somewhere: match the digit run.
	hits, err := db.Search(query.Regexp("code", "[A-Z]{2}[0-9]{4}"), 10)
	if err != nil {
		t.Fatal(err)
	}
	got := hitSet(hits)
	if !got["a"] || !got["c"] {
		t.Fatalf("regexp should match a and c, got %v", extIDs(hits))
	}
	if got["b"] {
		t.Fatalf("regexp should not match b (only two digits), got %v", extIDs(hits))
	}
}

func TestSpanNearQuery(t *testing.T) {
	db := indexAdv(t, []map[string]any{
		{"_id": "a", "title": "quick brown fox jumps"},
		{"_id": "b", "title": "quick green and brown leaf"},
		{"_id": "c", "title": "brown quick reversed order"},
	})
	defer mustClose(t, db)

	// "quick" then "brown" in order within slop 2: a (adjacent) and b (gap 2).
	hits, err := db.Search(query.SpanNear("title", []string{"quick", "brown"}, 2), 10)
	if err != nil {
		t.Fatal(err)
	}
	got := hitSet(hits)
	if !got["a"] || !got["b"] {
		t.Fatalf("span_near in order should match a and b, got %v", extIDs(hits))
	}
	if got["c"] {
		t.Fatalf("span_near in order should not match c (reversed), got %v", extIDs(hits))
	}
}

func TestGeoDistance_Correctness(t *testing.T) {
	// Build a ring of points at increasing distance from a center and verify the
	// query keeps exactly those within the radius, matching a reference haversine.
	center := struct{ lat, lon float64 }{37.7749, -122.4194} // San Francisco
	docs := []map[string]any{
		{"_id": "near", "loc": map[string]any{"lat": 37.78, "lon": -122.42}},    // ~1 km
		{"_id": "mid", "loc": map[string]any{"lat": 37.80, "lon": -122.27}},     // ~14 km
		{"_id": "far", "loc": map[string]any{"lat": 38.5814, "lon": -121.4944}}, // ~120 km (Sacramento)
	}
	db := indexAdv(t, docs)
	defer mustClose(t, db)

	const radius = 50000 // 50 km
	hits, err := db.Search(query.GeoDistance("loc", center.lat, center.lon, radius), 10)
	if err != nil {
		t.Fatal(err)
	}
	got := hitSet(hits)
	want := map[string]bool{}
	for _, d := range docs {
		g := d["loc"].(map[string]any)
		if docvalues.Haversine(center.lat, center.lon, g["lat"].(float64), g["lon"].(float64)) <= radius {
			want[d["_id"].(string)] = true
		}
	}
	if len(want) == 0 {
		t.Fatal("test setup: no points within radius")
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("geo_distance should include %s, got %v", id, extIDs(hits))
		}
	}
	if got["far"] {
		t.Fatalf("geo_distance 50km should exclude far, got %v", extIDs(hits))
	}
}

func TestGeoDistance_InBool(t *testing.T) {
	db := indexAdv(t, []map[string]any{
		{"_id": "a", "title": "coffee shop", "loc": map[string]any{"lat": 37.78, "lon": -122.42}},
		{"_id": "b", "title": "coffee house", "loc": map[string]any{"lat": 40.0, "lon": -120.0}},
	})
	defer mustClose(t, db)

	q := query.Bool().
		MustClause(query.Match("title", "coffee")).
		FilterClause(query.GeoDistance("loc", 37.7749, -122.4194, 50000))
	hits, err := db.Search(q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := hitSet(hits); !got["a"] || got["b"] {
		t.Fatalf("bool match+geo filter should be just a, got %v", extIDs(hits))
	}
}
