package query

import "testing"

func TestParseStringSingleTerm(t *testing.T) {
	q, err := ParseString("fox", "title")
	if err != nil {
		t.Fatal(err)
	}
	m, ok := q.(*MatchQuery)
	if !ok {
		t.Fatalf("got %T, want *MatchQuery", q)
	}
	if m.Field != "title" || m.Text != "fox" {
		t.Fatalf("got field %q text %q", m.Field, m.Text)
	}
}

func TestParseStringFieldScope(t *testing.T) {
	q, err := ParseString("tag:animal", "title")
	if err != nil {
		t.Fatal(err)
	}
	m := q.(*MatchQuery)
	if m.Field != "tag" || m.Text != "animal" {
		t.Fatalf("got field %q text %q", m.Field, m.Text)
	}
}

func TestParseStringRequiredProhibited(t *testing.T) {
	q, err := ParseString("+quick -slow fox", "title")
	if err != nil {
		t.Fatal(err)
	}
	b, ok := q.(*BoolQuery)
	if !ok {
		t.Fatalf("got %T, want *BoolQuery", q)
	}
	if len(b.Clauses) != 3 {
		t.Fatalf("clauses = %d, want 3", len(b.Clauses))
	}
	wantOccur := []Occur{Must, MustNot, Should}
	for i, c := range b.Clauses {
		if c.Occur != wantOccur[i] {
			t.Fatalf("clause %d occur = %v, want %v", i, c.Occur, wantOccur[i])
		}
	}
}

func TestParseStringPhrase(t *testing.T) {
	q, err := ParseString(`title:"quick brown fox"`, "")
	if err != nil {
		t.Fatal(err)
	}
	ph, ok := q.(*MatchPhraseQuery)
	if !ok {
		t.Fatalf("got %T, want *MatchPhraseQuery", q)
	}
	if ph.Field != "title" || ph.Text != "quick brown fox" {
		t.Fatalf("got field %q text %q", ph.Field, ph.Text)
	}
}

func TestParseStringPrefix(t *testing.T) {
	q, err := ParseString("title:qui*", "")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := q.(*PrefixQuery)
	if !ok {
		t.Fatalf("got %T, want *PrefixQuery", q)
	}
	if p.Field != "title" || p.Prefix != "qui" {
		t.Fatalf("got field %q prefix %q", p.Field, p.Prefix)
	}
}

func TestParseStringRange(t *testing.T) {
	q, err := ParseString("price:[10 TO 100}", "")
	if err != nil {
		t.Fatal(err)
	}
	r, ok := q.(*RangeQuery)
	if !ok {
		t.Fatalf("got %T, want *RangeQuery", q)
	}
	if r.Field != "price" || r.Lower != "10" || r.Upper != "100" {
		t.Fatalf("got %+v", r)
	}
	if !r.IncludeLower || r.IncludeUpper {
		t.Fatalf("inclusivity wrong: %+v", r)
	}
}

func TestParseStringRangeOpenUpper(t *testing.T) {
	q, err := ParseString("price:[10 TO *]", "")
	if err != nil {
		t.Fatal(err)
	}
	r := q.(*RangeQuery)
	if r.Lower != "10" || r.Upper != "" {
		t.Fatalf("got lower %q upper %q", r.Lower, r.Upper)
	}
}

func TestParseStringEmpty(t *testing.T) {
	q, err := ParseString("   ", "title")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := q.(*MatchNoneQuery); !ok {
		t.Fatalf("got %T, want *MatchNoneQuery", q)
	}
}

func TestParseJSONTerm(t *testing.T) {
	q, err := ParseJSON([]byte(`{"term": {"field": "tag", "value": "animal"}}`))
	if err != nil {
		t.Fatal(err)
	}
	tq := q.(*TermQuery)
	if tq.Field != "tag" || tq.Term != "animal" {
		t.Fatalf("got %+v", tq)
	}
}

func TestParseJSONBool(t *testing.T) {
	data := `{"bool": {
		"must":   [{"match": {"field": "title", "query": "fox"}}],
		"should": [{"term":  {"field": "tag", "value": "fast"}}],
		"must_not":[{"term": {"field": "tag", "value": "slow"}}],
		"minimum_should_match": 1
	}}`
	q, err := ParseJSON([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	b := q.(*BoolQuery)
	if len(b.Clauses) != 3 {
		t.Fatalf("clauses = %d, want 3", len(b.Clauses))
	}
	if b.EffectiveMinShould() != 1 {
		t.Fatalf("min should = %d, want 1", b.EffectiveMinShould())
	}
}

func TestParseJSONRange(t *testing.T) {
	q, err := ParseJSON([]byte(`{"range": {"field": "price", "gte": 10, "lt": 100}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := q.(*RangeQuery)
	if r.Lower != "10" || !r.IncludeLower {
		t.Fatalf("lower wrong: %+v", r)
	}
	if r.Upper != "100" || r.IncludeUpper {
		t.Fatalf("upper wrong: %+v", r)
	}
}

func TestParseJSONMatchOperator(t *testing.T) {
	q, err := ParseJSON([]byte(`{"match": {"field": "title", "query": "quick fox", "operator": "and"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if q.(*MatchQuery).Operator != Must {
		t.Fatalf("operator not AND")
	}
}

func TestParseJSONBoost(t *testing.T) {
	q, err := ParseJSON([]byte(`{"term": {"field": "tag", "value": "x"}, "boost": 2.5}`))
	if err != nil {
		t.Fatal(err)
	}
	if q.Boost() != 2.5 {
		t.Fatalf("boost = %v, want 2.5", q.Boost())
	}
}

func TestBoolRewriteSingleClause(t *testing.T) {
	b := Bool().MustClause(Term("f", "x"))
	got := b.Rewrite()
	if _, ok := got.(*TermQuery); !ok {
		t.Fatalf("single-must bool should rewrite to its clause, got %T", got)
	}
}

// stubSchema implements query.Schema for validation tests.
type stubSchema map[string]string

func (s stubSchema) FieldType(name string) (string, bool) {
	t, ok := s[name]
	return t, ok
}

func TestValidateUnknownField(t *testing.T) {
	q := Term("missing", "x")
	if err := q.Validate(stubSchema{"present": "keyword"}); err == nil {
		t.Fatal("expected error for unknown field")
	}
	if err := Term("present", "x").Validate(stubSchema{"present": "keyword"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
