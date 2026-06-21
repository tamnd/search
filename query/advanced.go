package query

import "regexp"

// FuzzyQuery matches documents whose field has a term within MaxEdits Levenshtein
// edits of Term (spec 2063 doc 11 §3.9). When MaxEdits is left at its zero value
// and AutoEdits is true the planner derives the edit distance from the term
// length. Matches are constant-scored like prefix and range queries.
type FuzzyQuery struct {
	base
	Field     string
	Term      string
	MaxEdits  int
	AutoEdits bool
}

// Fuzzy builds a FuzzyQuery with automatic edit distance based on term length.
func Fuzzy(field, term string) *FuzzyQuery {
	return &FuzzyQuery{Field: field, Term: term, AutoEdits: true}
}

func (*FuzzyQuery) queryNode()       {}
func (q *FuzzyQuery) Boost() float32 { return q.boostOr1() }
func (q *FuzzyQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *FuzzyQuery) Validate(s Schema) error {
	if q.Term == "" {
		return &Error{Msg: "fuzzy query needs a term"}
	}
	if q.MaxEdits < 0 {
		return &Error{Msg: "fuzzy edit distance must be non-negative"}
	}
	return requireField(s, q.Field, "fuzzy")
}
func (q *FuzzyQuery) Rewrite() Query { return q }

// WildcardQuery matches documents whose field has a term matching a glob pattern
// where * matches any run of characters and ? matches one character (doc 11
// §3.10). Constant-scored.
type WildcardQuery struct {
	base
	Field   string
	Pattern string
}

// Wildcard builds a WildcardQuery.
func Wildcard(field, pattern string) *WildcardQuery {
	return &WildcardQuery{Field: field, Pattern: pattern}
}

func (*WildcardQuery) queryNode()       {}
func (q *WildcardQuery) Boost() float32 { return q.boostOr1() }
func (q *WildcardQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *WildcardQuery) Validate(s Schema) error {
	if q.Pattern == "" {
		return &Error{Msg: "wildcard query needs a pattern"}
	}
	return requireField(s, q.Field, "wildcard")
}
func (q *WildcardQuery) Rewrite() Query { return q }

// RegexpQuery matches documents whose field has a term fully matching a Go
// regular expression (doc 11 §3.11). Constant-scored. The planner anchors the
// expression to the whole term and warns when an unanchored scan visits many
// terms.
type RegexpQuery struct {
	base
	Field   string
	Pattern string
}

// Regexp builds a RegexpQuery.
func Regexp(field, pattern string) *RegexpQuery {
	return &RegexpQuery{Field: field, Pattern: pattern}
}

func (*RegexpQuery) queryNode()       {}
func (q *RegexpQuery) Boost() float32 { return q.boostOr1() }
func (q *RegexpQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *RegexpQuery) Validate(s Schema) error {
	if q.Pattern == "" {
		return &Error{Msg: "regexp query needs a pattern"}
	}
	if _, err := regexp.Compile(q.Pattern); err != nil {
		return &Error{Msg: "regexp query: " + err.Error()}
	}
	return requireField(s, q.Field, "regexp")
}
func (q *RegexpQuery) Rewrite() Query { return q }

// GeoDistanceQuery matches documents whose geo_point field lies within Meters of
// the center (Lat, Lon), using the great-circle distance (doc 11 §3.14).
// Constant-scored; it is a filter over the geographic doc-values column.
type GeoDistanceQuery struct {
	base
	Field  string
	Lat    float64
	Lon    float64
	Meters float64
}

// GeoDistance builds a GeoDistanceQuery.
func GeoDistance(field string, lat, lon, meters float64) *GeoDistanceQuery {
	return &GeoDistanceQuery{Field: field, Lat: lat, Lon: lon, Meters: meters}
}

func (*GeoDistanceQuery) queryNode()       {}
func (q *GeoDistanceQuery) Boost() float32 { return q.boostOr1() }
func (q *GeoDistanceQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *GeoDistanceQuery) Validate(s Schema) error {
	if q.Meters <= 0 {
		return &Error{Msg: "geo_distance needs a positive distance"}
	}
	if q.Lat < -90 || q.Lat > 90 || q.Lon < -180 || q.Lon > 180 {
		return &Error{Msg: "geo_distance center out of range"}
	}
	if err := requireField(s, q.Field, "geo_distance"); err != nil {
		return err
	}
	if s != nil {
		if t, ok := s.FieldType(q.Field); ok && t != "geo_point" {
			return &Error{Msg: "geo_distance field must be geo_point"}
		}
	}
	return nil
}
func (q *GeoDistanceQuery) Rewrite() Query { return q }

// SpanNearQuery matches documents where the given terms appear in the field
// within Slop positions of one another, optionally requiring left-to-right order
// (doc 11 §3.12). It is the span building block; richer span operators compose on
// top of it. Scoring matches a phrase query.
type SpanNearQuery struct {
	base
	Field   string
	Terms   []string
	Slop    int
	InOrder bool
}

// SpanNear builds an ordered SpanNearQuery.
func SpanNear(field string, terms []string, slop int) *SpanNearQuery {
	return &SpanNearQuery{Field: field, Terms: terms, Slop: slop, InOrder: true}
}

func (*SpanNearQuery) queryNode()       {}
func (q *SpanNearQuery) Boost() float32 { return q.boostOr1() }
func (q *SpanNearQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *SpanNearQuery) Validate(s Schema) error {
	if len(q.Terms) == 0 {
		return &Error{Msg: "span_near needs at least one term"}
	}
	if q.Slop < 0 {
		return &Error{Msg: "span_near slop must be non-negative"}
	}
	return requireField(s, q.Field, "span_near")
}
func (q *SpanNearQuery) Rewrite() Query { return q }
