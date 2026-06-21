package search

import (
	"math"
	"reflect"
	"testing"

	"github.com/tamnd/search/agg"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
)

// facetSchema is a mapping with a keyword, a long, and a double field, all with
// doc-values, used by the sort and aggregation tests.
func facetSchema(t *testing.T) *schema.Schema {
	t.Helper()
	s := schema.New()
	for _, f := range []schema.Field{
		schema.NewField("title", schema.TypeText),
		schema.NewField("tag", schema.TypeKeyword),
		schema.NewField("year", schema.TypeLong),
		schema.NewField("price", schema.TypeDouble),
	} {
		if err := s.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func facetDocs() []map[string]any {
	return []map[string]any{
		{"_id": "a", "title": "alpha shoe", "tag": "shoe", "year": 2001, "price": 50.0},
		{"_id": "b", "title": "beta shoe", "tag": "shoe", "year": 2010, "price": 80.0},
		{"_id": "c", "title": "gamma hat", "tag": "hat", "year": 1999, "price": 20.0},
		{"_id": "d", "title": "delta hat", "tag": "hat", "year": 2020, "price": 35.0},
		{"_id": "e", "title": "eps bag", "tag": "bag", "year": 2005, "price": 120.0},
	}
}

func indexFacetCorpus(t *testing.T, docs []map[string]any) *DB {
	t.Helper()
	db := openDB(t)
	if err := db.PutSchema(facetSchema(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	return db
}

// matchAll returns a query that matches every document via a present-term scan on
// the title text field, which every doc in the corpus carries.
func matchAll() query.Query {
	return query.Bool().
		ShouldClause(query.Term("title", "shoe")).
		ShouldClause(query.Term("title", "hat")).
		ShouldClause(query.Term("title", "bag"))
}

func TestSorting_Numeric(t *testing.T) {
	db := indexFacetCorpus(t, facetDocs())
	defer mustClose(t, db)

	res, err := db.SearchRequestExec(SearchRequest{
		Query: matchAll(),
		K:     10,
		Sort:  []SortKey{{Field: "year", Desc: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := extIDs(res.Hits)
	want := []string{"d", "b", "e", "a", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("year desc = %v, want %v", got, want)
	}
}

func TestSorting_NumericAsc(t *testing.T) {
	db := indexFacetCorpus(t, facetDocs())
	defer mustClose(t, db)

	res, err := db.SearchRequestExec(SearchRequest{
		Query: matchAll(),
		K:     10,
		Sort:  []SortKey{{Field: "price", Desc: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := extIDs(res.Hits)
	want := []string{"c", "d", "a", "b", "e"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("price asc = %v, want %v", got, want)
	}
}

func TestSorting_MultiKey(t *testing.T) {
	db := indexFacetCorpus(t, facetDocs())
	defer mustClose(t, db)

	// Sort by tag ascending, then year descending within a tag.
	res, err := db.SearchRequestExec(SearchRequest{
		Query: matchAll(),
		K:     10,
		Sort: []SortKey{
			{Field: "tag", Desc: false},
			{Field: "year", Desc: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := extIDs(res.Hits)
	// tags ascending: bag(e), hat(d=2020,c=1999), shoe(b=2010,a=2001)
	want := []string{"e", "d", "c", "b", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tag asc, year desc = %v, want %v", got, want)
	}
}

func TestTermsFacet_DB(t *testing.T) {
	db := indexFacetCorpus(t, facetDocs())
	defer mustClose(t, db)

	res, err := db.SearchRequestExec(SearchRequest{
		Query: matchAll(),
		K:     10,
		Aggs:  map[string]AggSpec{"by_tag": {Kind: "terms", Field: "tag", Size: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := bucketCounts(res.Aggs["by_tag"])
	want := map[string]uint64{"shoe": 2, "hat": 2, "bag": 1}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("terms by_tag = %v, want %v", counts, want)
	}
}

func TestRangeFacet_DB(t *testing.T) {
	db := indexFacetCorpus(t, facetDocs())
	defer mustClose(t, db)

	res, err := db.SearchRequestExec(SearchRequest{
		Query: matchAll(),
		K:     10,
		Aggs: map[string]AggSpec{"price_bands": {
			Kind:  "range",
			Field: "price",
			Ranges: []agg.NumRange{
				{Key: "cheap", From: math.NaN(), To: 40},
				{Key: "mid", From: 40, To: 100},
				{Key: "lux", From: 100, To: math.NaN()},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := bucketCounts(res.Aggs["price_bands"])
	// cheap: c(20), d(35); mid: a(50), b(80); lux: e(120)
	want := map[string]uint64{"cheap": 2, "mid": 2, "lux": 1}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("range price_bands = %v, want %v", counts, want)
	}
}

func TestHistogramFacet_DB(t *testing.T) {
	db := indexFacetCorpus(t, facetDocs())
	defer mustClose(t, db)

	res, err := db.SearchRequestExec(SearchRequest{
		Query: matchAll(),
		K:     10,
		Aggs:  map[string]AggSpec{"by_decade": {Kind: "histogram", Field: "year", Interval: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := bucketCounts(res.Aggs["by_decade"])
	// 1990: c(1999); 2000: a(2001),e(2005); 2010: b(2010); 2020: d(2020)
	want := map[string]uint64{"1990": 1, "2000": 2, "2010": 1, "2020": 1}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("histogram by_decade = %v, want %v", counts, want)
	}
}

func TestMetricAgg_DB(t *testing.T) {
	db := indexFacetCorpus(t, facetDocs())
	defer mustClose(t, db)

	res, err := db.SearchRequestExec(SearchRequest{
		Query: matchAll(),
		K:     10,
		Aggs: map[string]AggSpec{
			"max_price": {Kind: "max", Field: "price"},
			"min_price": {Kind: "min", Field: "price"},
			"sum_price": {Kind: "sum", Field: "price"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Aggs["max_price"].Value; got != 120 {
		t.Fatalf("max price = %v, want 120", got)
	}
	if got := res.Aggs["min_price"].Value; got != 20 {
		t.Fatalf("min price = %v, want 20", got)
	}
	if got := res.Aggs["sum_price"].Value; got != 305 {
		t.Fatalf("sum price = %v, want 305", got)
	}
}

func TestNestedFacet_DB(t *testing.T) {
	db := indexFacetCorpus(t, facetDocs())
	defer mustClose(t, db)

	res, err := db.SearchRequestExec(SearchRequest{
		Query: matchAll(),
		K:     10,
		Aggs: map[string]AggSpec{"by_tag": {
			Kind:  "terms",
			Field: "tag",
			Size:  10,
			Sub:   map[string]AggSpec{"avg_price": {Kind: "avg", Field: "price"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range res.Aggs["by_tag"].Buckets {
		sub, ok := b.Subs["avg_price"]
		if !ok {
			t.Fatalf("bucket %q missing avg_price sub", b.Key)
		}
		switch b.Key {
		case "shoe":
			if sub.Value != 65 { // (50+80)/2
				t.Fatalf("shoe avg = %v, want 65", sub.Value)
			}
		case "hat":
			if sub.Value != 27.5 { // (20+35)/2
				t.Fatalf("hat avg = %v, want 27.5", sub.Value)
			}
		case "bag":
			if sub.Value != 120 {
				t.Fatalf("bag avg = %v, want 120", sub.Value)
			}
		}
	}
}

func TestCollapse_DB(t *testing.T) {
	db := indexFacetCorpus(t, facetDocs())
	defer mustClose(t, db)

	// Collapse on tag, keeping the highest-price hit per tag.
	res, err := db.SearchRequestExec(SearchRequest{
		Query:    matchAll(),
		K:        10,
		Sort:     []SortKey{{Field: "price", Desc: true}},
		Collapse: "tag",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := extIDs(res.Hits)
	// One per tag, ranked by the kept hit's price desc:
	// bag e(120), shoe b(80), hat d(35)
	want := []string{"e", "b", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collapse tag = %v, want %v", got, want)
	}
}

func TestSorting_MissingLast(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)
	if err := db.PutSchema(facetSchema(t)); err != nil {
		t.Fatal(err)
	}
	docs := []map[string]any{
		{"_id": "a", "title": "one shoe", "tag": "shoe", "year": 2001},
		{"_id": "b", "title": "two shoe", "tag": "shoe", "year": 2010, "price": 80.0},
		{"_id": "c", "title": "three hat", "tag": "hat", "price": 35.0},
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	res, err := db.SearchRequestExec(SearchRequest{
		Query: matchAll(),
		K:     10,
		Sort:  []SortKey{{Field: "price", Desc: false, MissingLast: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := extIDs(res.Hits)
	// c(35), b(80), then a (no price) last
	want := []string{"c", "b", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missing_last = %v, want %v", got, want)
	}
}

// bucketCounts reduces a bucketed aggregation result to a key/count map.
func bucketCounts(r agg.Result) map[string]uint64 {
	out := make(map[string]uint64, len(r.Buckets))
	for _, b := range r.Buckets {
		out[b.Key] = b.Count
	}
	return out
}
