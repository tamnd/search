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

func TestParseJSONFuzzy(t *testing.T) {
	q, err := ParseJSON([]byte(`{"fuzzy": {"field": "title", "term": "serch", "max_edits": 1}}`))
	if err != nil {
		t.Fatal(err)
	}
	fq, ok := q.(*FuzzyQuery)
	if !ok {
		t.Fatalf("got %T, want *FuzzyQuery", q)
	}
	if fq.Field != "title" || fq.Term != "serch" || fq.MaxEdits != 1 || fq.AutoEdits {
		t.Fatalf("got %+v", fq)
	}
}

func TestParseJSONWildcardRegexp(t *testing.T) {
	w, err := ParseJSON([]byte(`{"wildcard": {"field": "title", "value": "qu*ck"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if wq, ok := w.(*WildcardQuery); !ok || wq.Pattern != "qu*ck" {
		t.Fatalf("wildcard got %+v", w)
	}
	r, err := ParseJSON([]byte(`{"regexp": {"field": "code", "value": "[0-9]{4}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if rq, ok := r.(*RegexpQuery); !ok || rq.Pattern != "[0-9]{4}" {
		t.Fatalf("regexp got %+v", r)
	}
}

func TestParseJSONGeoDistance(t *testing.T) {
	q, err := ParseJSON([]byte(`{"geo_distance": {"field": "loc", "lat": 40.7, "lon": -74.0, "distance": 5000}}`))
	if err != nil {
		t.Fatal(err)
	}
	g, ok := q.(*GeoDistanceQuery)
	if !ok {
		t.Fatalf("got %T", q)
	}
	if g.Field != "loc" || g.Lat != 40.7 || g.Lon != -74.0 || g.Meters != 5000 {
		t.Fatalf("got %+v", g)
	}
}

func TestParseJSONSpanNear(t *testing.T) {
	q, err := ParseJSON([]byte(`{"span_near": {"field": "body", "terms": ["quick","fox"], "slop": 2, "in_order": false}}`))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := q.(*SpanNearQuery)
	if !ok {
		t.Fatalf("got %T", q)
	}
	if s.Field != "body" || len(s.Terms) != 2 || s.Slop != 2 || s.InOrder {
		t.Fatalf("got %+v", s)
	}
}

func TestParseJSONFunctionScore(t *testing.T) {
	data := `{"function_score": {
		"query": {"match": {"field": "title", "query": "fox"}},
		"functions": [
			{"field_value_factor": {"field": "views", "modifier": "ln1p", "missing": 1}, "weight": 2},
			{"filter": {"term": {"field": "tag", "value": "hot"}}, "weight": 3}
		],
		"score_mode": "sum",
		"boost_mode": "replace",
		"max_boost": 10
	}}`
	q, err := ParseJSON([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	fs, ok := q.(*FunctionScoreQuery)
	if !ok {
		t.Fatalf("got %T", q)
	}
	if fs.ScoreMode != ScoreSum || fs.BoostMode != BoostReplace || fs.MaxBoost != 10 {
		t.Fatalf("modes wrong: %+v", fs)
	}
	if len(fs.Functions) != 2 {
		t.Fatalf("functions = %d, want 2", len(fs.Functions))
	}
	if fs.Functions[0].FieldValue == nil || fs.Functions[0].FieldValue.Modifier != ModLn1p {
		t.Fatalf("field_value_factor wrong: %+v", fs.Functions[0])
	}
	if fs.Functions[1].Filter == nil {
		t.Fatalf("filter not parsed: %+v", fs.Functions[1])
	}
}

func TestParseJSONBM25F(t *testing.T) {
	data := `{"bm25f": {"terms": ["fox"], "fields": [{"name": "title", "boost": 2}, {"name": "body"}], "k1": 1.2}}`
	q, err := ParseJSON([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	b, ok := q.(*BM25FQuery)
	if !ok {
		t.Fatalf("got %T", q)
	}
	if len(b.Terms) != 1 || len(b.Fields) != 2 || b.K1 != 1.2 {
		t.Fatalf("got %+v", b)
	}
	if b.Fields[0].Name != "title" || b.Fields[0].Boost != 2 {
		t.Fatalf("field 0 wrong: %+v", b.Fields[0])
	}
}

func TestParseJSONRescore(t *testing.T) {
	data := `{"rescore": {
		"query": {"match": {"field": "title", "query": "fox"}},
		"rescore": {"match_phrase": {"field": "title", "query": "quick fox"}},
		"window_size": 100,
		"query_weight": 0.7,
		"rescore_weight": 1.3
	}}`
	q, err := ParseJSON([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := q.(*RescoreQuery)
	if !ok {
		t.Fatalf("got %T", q)
	}
	if r.WindowSize != 100 || r.QueryWeight != 0.7 || r.RescoreWeight != 1.3 {
		t.Fatalf("got %+v", r)
	}
	if r.Query == nil || r.Rescore == nil {
		t.Fatalf("sub-queries not parsed: %+v", r)
	}
}
