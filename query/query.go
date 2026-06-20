// Package query is the typed query tree and its two textual front ends (spec 2063
// doc 11). A query is a tree of sealed nodes — TermQuery, MatchQuery,
// MatchPhraseQuery, PrefixQuery, RangeQuery, BoolQuery, MatchAllQuery, and
// MatchNoneQuery — each carrying a boost. Application code builds trees with the
// constructor functions, or parses one from the compact query string
// (ParseString) or the JSON DSL (ParseJSON). The planner in package exec turns a
// tree into iterators; this package stays free of the index internals so it can
// be validated and rewritten in isolation.
package query

// Occur is how a boolean clause participates in matching.
type Occur int

const (
	// Must clauses must match and contribute score.
	Must Occur = iota
	// Should clauses may match; a match contributes score. minimum_should_match
	// controls how many are required.
	Should
	// MustNot clauses must not match and never contribute score.
	MustNot
	// Filter clauses must match but contribute no score.
	Filter
)

// Schema is the read-only view the query layer needs for validation: it answers
// whether a field exists and what its type is. Package exec adapts the real index
// schema to this interface.
type Schema interface {
	FieldType(name string) (typ string, ok bool)
}

// Query is the sealed interface implemented by every node. The unexported
// queryNode method keeps the set of node types closed so the planner's type
// switch is exhaustive.
type Query interface {
	queryNode()
	// Boost returns the query-level boost factor (default 1).
	Boost() float32
	// WithBoost returns a copy of the node with the boost replaced.
	WithBoost(b float32) Query
	// Validate checks parameters and, where a schema is given, field existence.
	Validate(s Schema) error
	// Rewrite returns the canonical form of the node (constant folding, etc.).
	Rewrite() Query
}

// base carries the boost shared by every node. The zero value means boost 1.
type base struct {
	boost float32
}

func (b base) boostOr1() float32 {
	if b.boost == 0 {
		return 1
	}
	return b.boost
}

// TermQuery matches documents whose field contains exactly term (the term is not
// analyzed; it must already be in indexed form).
type TermQuery struct {
	base
	Field string
	Term  string
}

// Term builds a TermQuery.
func Term(field, term string) *TermQuery { return &TermQuery{Field: field, Term: term} }

func (*TermQuery) queryNode()       {}
func (q *TermQuery) Boost() float32 { return q.boostOr1() }
func (q *TermQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *TermQuery) Validate(s Schema) error {
	return requireField(s, q.Field, "term")
}
func (q *TermQuery) Rewrite() Query { return q }

// MatchQuery analyzes its text with the field's analyzer at plan time and
// combines the resulting terms with And or Or (Or by default).
type MatchQuery struct {
	base
	Field    string
	Text     string
	Operator Occur // Must (and) or Should (or); defaults to Should
}

// Match builds a MatchQuery with the default OR operator.
func Match(field, text string) *MatchQuery {
	return &MatchQuery{Field: field, Text: text, Operator: Should}
}

func (*MatchQuery) queryNode()       {}
func (q *MatchQuery) Boost() float32 { return q.boostOr1() }
func (q *MatchQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *MatchQuery) Validate(s Schema) error { return requireField(s, q.Field, "match") }
func (q *MatchQuery) Rewrite() Query          { return q }

// MatchPhraseQuery requires the analyzed tokens to appear in order within slop
// positions of each other.
type MatchPhraseQuery struct {
	base
	Field string
	Text  string
	Slop  int
}

// Phrase builds a MatchPhraseQuery with zero slop (exact adjacency).
func Phrase(field, text string) *MatchPhraseQuery {
	return &MatchPhraseQuery{Field: field, Text: text}
}

func (*MatchPhraseQuery) queryNode()       {}
func (q *MatchPhraseQuery) Boost() float32 { return q.boostOr1() }
func (q *MatchPhraseQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *MatchPhraseQuery) Validate(s Schema) error {
	if q.Slop < 0 {
		return &Error{Msg: "phrase slop must be non-negative"}
	}
	return requireField(s, q.Field, "match_phrase")
}
func (q *MatchPhraseQuery) Rewrite() Query { return q }

// PrefixQuery matches documents whose field has a term beginning with Prefix.
type PrefixQuery struct {
	base
	Field  string
	Prefix string
}

// Prefix builds a PrefixQuery.
func Prefix(field, prefix string) *PrefixQuery {
	return &PrefixQuery{Field: field, Prefix: prefix}
}

func (*PrefixQuery) queryNode()       {}
func (q *PrefixQuery) Boost() float32 { return q.boostOr1() }
func (q *PrefixQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *PrefixQuery) Validate(s Schema) error { return requireField(s, q.Field, "prefix") }
func (q *PrefixQuery) Rewrite() Query          { return q }

// RangeQuery matches documents whose field value falls in [Lower, Upper], with
// the inclusivity of each end configurable. An empty bound is open. The bounds
// are textual; the planner encodes them to the field's term form (lexicographic
// for keyword, order-preserving for numeric, date, and boolean fields).
type RangeQuery struct {
	base
	Field        string
	Lower        string
	Upper        string
	IncludeLower bool
	IncludeUpper bool
}

// Range builds a RangeQuery.
func Range(field, lower, upper string, includeLower, includeUpper bool) *RangeQuery {
	return &RangeQuery{
		Field:        field,
		Lower:        lower,
		Upper:        upper,
		IncludeLower: includeLower,
		IncludeUpper: includeUpper,
	}
}

func (*RangeQuery) queryNode()       {}
func (q *RangeQuery) Boost() float32 { return q.boostOr1() }
func (q *RangeQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *RangeQuery) Validate(s Schema) error {
	if q.Lower == "" && q.Upper == "" {
		return &Error{Msg: "range needs at least one bound"}
	}
	return requireField(s, q.Field, "range")
}
func (q *RangeQuery) Rewrite() Query { return q }

// MatchAllQuery matches every document with a constant score of 1 before boost.
type MatchAllQuery struct{ base }

// MatchAll builds a MatchAllQuery.
func MatchAll() *MatchAllQuery { return &MatchAllQuery{} }

func (*MatchAllQuery) queryNode()       {}
func (q *MatchAllQuery) Boost() float32 { return q.boostOr1() }
func (q *MatchAllQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *MatchAllQuery) Validate(Schema) error { return nil }
func (q *MatchAllQuery) Rewrite() Query        { return q }

// MatchNoneQuery matches no document.
type MatchNoneQuery struct{ base }

// MatchNone builds a MatchNoneQuery.
func MatchNone() *MatchNoneQuery { return &MatchNoneQuery{} }

func (*MatchNoneQuery) queryNode()       {}
func (q *MatchNoneQuery) Boost() float32 { return q.boostOr1() }
func (q *MatchNoneQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *MatchNoneQuery) Validate(Schema) error { return nil }
func (q *MatchNoneQuery) Rewrite() Query        { return q }

// Clause is one sub-query of a BoolQuery together with how it must occur.
type Clause struct {
	Occur Occur
	Query Query
}

// BoolQuery combines clauses by occurrence. A document matches when every Must
// and Filter clause matches, no MustNot clause matches, and at least
// MinimumShouldMatch of the Should clauses match (the default is 1 when there are
// only Should clauses, otherwise 0).
type BoolQuery struct {
	base
	Clauses            []Clause
	MinimumShouldMatch int
	minSet             bool
}

// Bool builds an empty BoolQuery.
func Bool() *BoolQuery { return &BoolQuery{} }

// Add appends a clause and returns the query for chaining.
func (q *BoolQuery) Add(o Occur, sub Query) *BoolQuery {
	q.Clauses = append(q.Clauses, Clause{Occur: o, Query: sub})
	return q
}

// MustClause, ShouldClause, MustNotClause, and FilterClause are convenience
// adders.
func (q *BoolQuery) MustClause(sub Query) *BoolQuery    { return q.Add(Must, sub) }
func (q *BoolQuery) ShouldClause(sub Query) *BoolQuery  { return q.Add(Should, sub) }
func (q *BoolQuery) MustNotClause(sub Query) *BoolQuery { return q.Add(MustNot, sub) }
func (q *BoolQuery) FilterClause(sub Query) *BoolQuery  { return q.Add(Filter, sub) }

// SetMinimumShouldMatch sets the minimum number of should clauses that must
// match.
func (q *BoolQuery) SetMinimumShouldMatch(n int) *BoolQuery {
	q.MinimumShouldMatch = n
	q.minSet = true
	return q
}

// EffectiveMinShould returns the minimum number of should clauses that must
// match, applying the default (1 when the query has only should clauses, else 0)
// when none was set explicitly.
func (q *BoolQuery) EffectiveMinShould() int {
	if q.minSet {
		return q.MinimumShouldMatch
	}
	hasReq := false
	hasShould := false
	for _, c := range q.Clauses {
		switch c.Occur {
		case Must, Filter:
			hasReq = true
		case Should:
			hasShould = true
		}
	}
	if hasShould && !hasReq {
		return 1
	}
	return 0
}

func (*BoolQuery) queryNode()       {}
func (q *BoolQuery) Boost() float32 { return q.boostOr1() }
func (q *BoolQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}
func (q *BoolQuery) Validate(s Schema) error {
	if len(q.Clauses) == 0 {
		return &Error{Msg: "bool query has no clauses"}
	}
	for _, c := range q.Clauses {
		if err := c.Query.Validate(s); err != nil {
			return err
		}
	}
	return nil
}

// Rewrite folds an empty bool to match-none and a single must/should clause to
// that clause, then recursively rewrites every clause.
func (q *BoolQuery) Rewrite() Query {
	c := *q
	c.Clauses = make([]Clause, len(q.Clauses))
	for i, cl := range q.Clauses {
		c.Clauses[i] = Clause{Occur: cl.Occur, Query: cl.Query.Rewrite()}
	}
	if len(c.Clauses) == 1 && !c.minSet {
		only := c.Clauses[0]
		if only.Occur == Must || only.Occur == Should {
			return only.Query.WithBoost(only.Query.Boost() * c.boostOr1())
		}
	}
	return &c
}

// requireField checks the field exists in the schema when a schema is provided.
func requireField(s Schema, field, qtype string) error {
	if field == "" {
		return &Error{Msg: qtype + " query needs a field"}
	}
	if s == nil {
		return nil
	}
	if _, ok := s.FieldType(field); !ok {
		return &Error{Msg: qtype + " query references unknown field " + field}
	}
	return nil
}

// Error is a query validation or parse error.
type Error struct{ Msg string }

func (e *Error) Error() string { return "query: " + e.Msg }
