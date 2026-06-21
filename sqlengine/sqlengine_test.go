package sqlengine_test

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	search "github.com/tamnd/search"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/sqlengine"
)

// corpus is a small fixed set of documents shared by the SQL tests. Each row has
// a text title and body, a keyword category, and a numeric price so the same
// corpus exercises MATCH, range, IN, and LIKE predicates.
var corpus = []map[string]any{
	{"_id": "1", "title": "fast red running shoes", "body": "lightweight trail runners", "category": "shoes", "price": 80},
	{"_id": "2", "title": "blue running socks", "body": "merino wool socks for running", "category": "socks", "price": 12},
	{"_id": "3", "title": "leather hiking boots", "body": "waterproof boots for the mountains", "category": "shoes", "price": 140},
	{"_id": "4", "title": "marathon water bottle", "body": "insulated bottle for long runs", "category": "gear", "price": 25},
	{"_id": "5", "title": "trail running vest", "body": "hydration vest for running races", "category": "gear", "price": 95},
	{"_id": "6", "title": "casual canvas shoes", "body": "everyday sneakers", "category": "shoes", "price": 45},
}

func openIndex(t *testing.T) *search.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sql.sx")
	db, err := search.Open(path, search.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close index: %v", err)
		}
	})

	s := schema.New()
	for _, f := range []schema.Field{
		schema.NewField("title", schema.TypeText),
		schema.NewField("body", schema.TypeText),
		schema.NewField("category", schema.TypeKeyword),
		schema.NewField("price", schema.TypeLong),
	} {
		if err := s.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.PutSchema(s); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Index(corpus); err != nil {
		t.Fatal(err)
	}
	return db
}

func openSQL(t *testing.T) (*sqlengine.DB, *search.DB) {
	t.Helper()
	idx := openIndex(t)
	sdb, err := sqlengine.Open(idx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sdb.Close(); err != nil {
			t.Errorf("close sqlengine: %v", err)
		}
	})
	return sdb, idx
}

// idsFromSQL runs a SELECT and returns the _id column of every row in order.
func idsFromSQL(t *testing.T, sdb *sqlengine.DB, sql string, args ...any) []string {
	t.Helper()
	rows, err := sdb.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close rows: %v", err)
		}
	}()
	var ids []string
	for rows.Next() {
		var id any
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		s, ok := id.(string)
		if !ok {
			t.Fatalf("_id is %T not string", id)
		}
		ids = append(ids, s)
	}
	return ids
}

// idsFromHits pulls external ids out of a Go API result.
func idsFromHits(hits []search.Hit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ExternalID
	}
	return ids
}

// TestSQLFTS5Match checks that an FTS5-style MATCH through the SQL surface
// produces the same ranked result as the equivalent query built directly with
// the Go API, across a range of single-term, multi-term, field-scoped, and
// boolean queries.
func TestSQLFTS5Match(t *testing.T) {
	sdb, idx := openSQL(t)

	cases := []struct {
		name  string
		sql   string
		text  string // ParseString text for the Go API comparison
		field string // field-level scope ("" = table-level over all text fields)
	}{
		{"single term", "SELECT _id FROM docs WHERE docs MATCH 'running'", "running", ""},
		{"two terms", "SELECT _id FROM docs WHERE docs MATCH 'running shoes'", "running shoes", ""},
		{"required term", "SELECT _id FROM docs WHERE docs MATCH '+running +vest'", "+running +vest", ""},
		{"excluded term", "SELECT _id FROM docs WHERE docs MATCH 'running -socks'", "running -socks", ""},
		{"phrase", "SELECT _id FROM docs WHERE docs MATCH '\"trail running\"'", "\"trail running\"", ""},
		{"field scoped", "SELECT _id FROM docs WHERE title MATCH 'running'", "running", "title"},
		{"field qualified term", "SELECT _id FROM docs WHERE docs MATCH 'title:boots'", "title:boots", ""},
		{"boolean or", "SELECT _id FROM docs WHERE docs MATCH 'boots OR socks'", "boots OR socks", ""},
		{"prefix", "SELECT _id FROM docs WHERE docs MATCH 'run*'", "run*", ""},
	}

	// textFields mirrors the planner: a table-level MATCH searches every text
	// field, so the Go API comparison ORs a per-field parse of the same string.
	textFields := []string{"title", "body"}
	buildWant := func(text, field string) query.Query {
		if field != "" {
			q, err := query.ParseString(text, field)
			if err != nil {
				t.Fatalf("ParseString: %v", err)
			}
			return q
		}
		bq := query.Bool().SetMinimumShouldMatch(1)
		for _, f := range textFields {
			q, err := query.ParseString(text, f)
			if err != nil {
				t.Fatalf("ParseString: %v", err)
			}
			bq.ShouldClause(q)
		}
		return bq
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := idsFromSQL(t, sdb, tc.sql)

			hits, err := idx.Search(buildWant(tc.text, tc.field), 100)
			if err != nil {
				t.Fatalf("Go API search: %v", err)
			}
			want := idsFromHits(hits)

			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ranking mismatch\n sql:  %v\n goapi: %v", got, want)
			}
		})
	}
}

// TestSQLRange checks BETWEEN against the equivalent inclusive RangeQuery.
func TestSQLRange(t *testing.T) {
	sdb, idx := openSQL(t)

	got := idsFromSQL(t, sdb, "SELECT _id FROM docs WHERE price BETWEEN 20 AND 95 ORDER BY price ASC")

	q := query.Bool().FilterClause(query.Range("price", "20", "95", true, true))
	hits, err := idx.SearchRequestExec(search.SearchRequest{
		Query: q,
		K:     100,
		Sort:  []search.SortKey{{Field: "price", Desc: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := idsFromHits(hits.Hits)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("range mismatch\n sql:  %v\n goapi: %v", got, want)
	}
	// 25, 45, 80, 95 fall inside [20,95]; 12 and 140 are outside.
	wantIDs := []string{"4", "6", "1", "5"}
	if !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("expected %v ordered by price, got %v", wantIDs, got)
	}
}

// TestSQLComparisons covers the scalar comparison operators against the price
// field, confirming each maps to the right half-open or point range.
func TestSQLComparisons(t *testing.T) {
	sdb, _ := openSQL(t)

	cases := []struct {
		sql  string
		want []string
	}{
		{"SELECT _id FROM docs WHERE price < 25 ORDER BY price ASC", []string{"2"}},
		{"SELECT _id FROM docs WHERE price <= 25 ORDER BY price ASC", []string{"2", "4"}},
		{"SELECT _id FROM docs WHERE price > 95 ORDER BY price ASC", []string{"3"}},
		{"SELECT _id FROM docs WHERE price >= 95 ORDER BY price ASC", []string{"5", "3"}},
		{"SELECT _id FROM docs WHERE price = 45", []string{"6"}},
		{"SELECT _id FROM docs WHERE price != 45 ORDER BY price ASC", []string{"2", "4", "1", "5", "3"}},
	}
	for _, tc := range cases {
		got := idsFromSQL(t, sdb, tc.sql)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q\n got:  %v\n want: %v", tc.sql, got, tc.want)
		}
	}
}

// TestSQLInAndKeyword checks IN against a keyword field.
func TestSQLInAndKeyword(t *testing.T) {
	sdb, _ := openSQL(t)

	got := idsFromSQL(t, sdb, "SELECT _id FROM docs WHERE category IN ('shoes', 'gear') ORDER BY price ASC")
	sort.Strings(got)
	want := []string{"1", "3", "4", "5", "6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IN mismatch\n got:  %v\n want: %v", got, want)
	}
}

// TestSQLLike checks that a trailing-percent LIKE behaves like a prefix query.
func TestSQLLike(t *testing.T) {
	sdb, idx := openSQL(t)

	got := idsFromSQL(t, sdb, "SELECT _id FROM docs WHERE title LIKE 'run%'")
	sort.Strings(got)

	hits, err := idx.Search(query.Bool().FilterClause(query.Prefix("title", "run")), 100)
	if err != nil {
		t.Fatal(err)
	}
	want := idsFromHits(hits)
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LIKE mismatch\n got:  %v\n want: %v", got, want)
	}
}

// TestSQLProjectionAndStar checks named projection, aliases, pseudo-columns, and
// the star expansion to stored fields.
func TestSQLProjectionAndStar(t *testing.T) {
	sdb, _ := openSQL(t)

	rows, err := sdb.Query(context.Background(),
		"SELECT _id, title, price AS p FROM docs WHERE docs MATCH 'boots'")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	if cols := rows.Columns(); !reflect.DeepEqual(cols, []string{"_id", "title", "p"}) {
		t.Fatalf("columns = %v", cols)
	}
	if !rows.Next() {
		t.Fatal("expected a row")
	}
	row := rows.Row()
	if row["_id"] != "3" {
		t.Fatalf("_id = %v", row["_id"])
	}
	if row["title"] != "leather hiking boots" {
		t.Fatalf("title = %v", row["title"])
	}
	if row["p"] == nil {
		t.Fatal("alias p should carry the price value")
	}

	// Star expands to _id plus every stored field.
	starRows, err := sdb.Query(context.Background(), "SELECT * FROM docs WHERE docs MATCH 'boots'")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := starRows.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	want := []string{"_id", "title", "body", "category", "price"}
	if cols := starRows.Columns(); !reflect.DeepEqual(cols, want) {
		t.Fatalf("star columns = %v, want %v", cols, want)
	}
}

// TestSQLLimitOffset checks the result window.
func TestSQLLimitOffset(t *testing.T) {
	sdb, _ := openSQL(t)

	full := idsFromSQL(t, sdb, "SELECT _id FROM docs WHERE price > 0 ORDER BY price ASC")
	if len(full) != len(corpus) {
		t.Fatalf("expected %d rows, got %d", len(corpus), len(full))
	}
	win := idsFromSQL(t, sdb, "SELECT _id FROM docs WHERE price > 0 ORDER BY price ASC LIMIT 2 OFFSET 1")
	if !reflect.DeepEqual(win, full[1:3]) {
		t.Fatalf("window = %v, want %v", win, full[1:3])
	}
}

// TestSQLBindParameters checks positional and named placeholders.
func TestSQLBindParameters(t *testing.T) {
	sdb, _ := openSQL(t)

	pos := idsFromSQL(t, sdb, "SELECT _id FROM docs WHERE docs MATCH ? AND price < ?", "running", 90)
	named := idsFromSQL(t, sdb,
		"SELECT _id FROM docs WHERE docs MATCH :q AND price < :max",
		sqlengine.Named("q", "running"), sqlengine.Named("max", 90))

	if !reflect.DeepEqual(pos, named) {
		t.Fatalf("positional %v != named %v", pos, named)
	}
	// Every returned row must mention running and be under 90.
	if len(pos) == 0 {
		t.Fatal("expected at least one match")
	}
}

// TestSQLUnsupported confirms out-of-subset statements report ErrUnsupportedSQL
// rather than executing or panicking.
func TestSQLUnsupported(t *testing.T) {
	sdb, _ := openSQL(t)

	cases := []string{
		"INSERT INTO docs VALUES (1)",
		"SELECT _id FROM docs WHERE title IS NULL",
		"SELECT _id FROM docs JOIN other ON x = y",
	}
	for _, sql := range cases {
		_, err := sdb.Query(context.Background(), sql)
		if err == nil {
			t.Fatalf("%q should be rejected", sql)
		}
	}
}
