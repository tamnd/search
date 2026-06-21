package query

// This file carries the scoring-shaping query nodes from spec 2063 doc 13:
// function score, multi-field BM25F, and two-phase rescoring. They wrap a base
// query and adjust how its matches are scored rather than which documents match.

// ScoreMode controls how the individual functions of a FunctionScoreQuery combine
// into one function score (doc 13 §8.1).
type ScoreMode int

const (
	// ScoreMultiply takes the product of the matching functions (the default).
	ScoreMultiply ScoreMode = iota
	// ScoreSum takes the sum of the matching functions.
	ScoreSum
	// ScoreAvg takes the mean of the matching functions.
	ScoreAvg
	// ScoreMax takes the largest matching function value.
	ScoreMax
	// ScoreMin takes the smallest matching function value.
	ScoreMin
	// ScoreFirst takes the first function whose filter matches.
	ScoreFirst
)

// BoostMode controls how the combined function score interacts with the base
// query score (doc 13 §8.1).
type BoostMode int

const (
	// BoostMultiply multiplies the query score by the function score (default).
	BoostMultiply BoostMode = iota
	// BoostReplace ignores the query score and uses the function score.
	BoostReplace
	// BoostSum adds the function score to the query score.
	BoostSum
	// BoostAvg averages the query and function scores.
	BoostAvg
	// BoostMax takes the larger of the two.
	BoostMax
	// BoostMin takes the smaller of the two.
	BoostMin
)

// FieldModifier is the math function applied to a field value in a
// field_value_factor function (doc 13 §8.2).
type FieldModifier int

const (
	// ModNone applies no modifier.
	ModNone FieldModifier = iota
	// ModLog applies log10(factor*value).
	ModLog
	// ModLog1p applies log10(1 + factor*value).
	ModLog1p
	// ModLog2p applies log10(2 + factor*value).
	ModLog2p
	// ModLn applies ln(factor*value).
	ModLn
	// ModLn1p applies ln(1 + factor*value).
	ModLn1p
	// ModLn2p applies ln(2 + factor*value).
	ModLn2p
	// ModSquare applies (factor*value)^2.
	ModSquare
	// ModSqrt applies sqrt(factor*value).
	ModSqrt
	// ModReciprocal applies 1/(factor*value).
	ModReciprocal
)

// DecayKind selects the shape of a decay function (doc 13 §8.3).
type DecayKind int

const (
	// DecayGauss is a Gaussian (bell-curve) decay.
	DecayGauss DecayKind = iota
	// DecayExp is an exponential decay.
	DecayExp
	// DecayLinear is a linear decay clamped at zero.
	DecayLinear
)

// ScoreFunction is one component of a FunctionScoreQuery. Exactly one of the
// value-producing fields (FieldValue, Decay, Random) is set; when none is set the
// function contributes its Weight alone. An optional Filter restricts the function
// to the documents it matches; a nil filter applies to every matched document.
type ScoreFunction struct {
	Filter     Query
	Weight     float32
	FieldValue *FieldValueFactor
	Decay      *DecayFunction
	Random     *RandomScore
}

// FieldValueFactor reads a numeric doc-values field and shapes it with a modifier
// (doc 13 §8.2): modifier(factor * value), with Missing substituted when the field
// is absent.
type FieldValueFactor struct {
	Field    string
	Factor   float32
	Modifier FieldModifier
	Missing  float32
}

// DecayFunction reduces the score as a numeric field value moves away from Origin
// (doc 13 §8.3). A value within Offset of the origin scores 1; at Scale beyond the
// offset it scores Decay.
type DecayFunction struct {
	Field  string
	Kind   DecayKind
	Origin float64
	Scale  float64
	Offset float64
	Decay  float64
}

// RandomScore assigns each document a stable pseudo-random score in [0,1) seeded
// by Seed (doc 13 §8.4).
type RandomScore struct {
	Seed int64
}

// FunctionScoreQuery modifies the score of a base query with one or more functions
// (doc 13 §8). The functions combine per ScoreMode, then merge with the query
// score per BoostMode. MinScore drops documents whose final score falls below it;
// MaxBoost caps the combined function score; the node boost scales the final score.
type FunctionScoreQuery struct {
	base
	Query     Query
	Functions []ScoreFunction
	ScoreMode ScoreMode
	BoostMode BoostMode
	MinScore  float32
	MaxBoost  float32
}

// FunctionScore wraps a base query so its score can be reshaped by functions.
func FunctionScore(q Query, fns ...ScoreFunction) *FunctionScoreQuery {
	return &FunctionScoreQuery{Query: q, Functions: fns}
}

func (*FunctionScoreQuery) queryNode() {}

// Boost returns the query-level boost factor (default 1).
func (q *FunctionScoreQuery) Boost() float32 { return q.boostOr1() }

// WithBoost returns a copy of the node with the boost replaced.
func (q *FunctionScoreQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}

// Validate requires a base query and checks every function's filter and fields.
func (q *FunctionScoreQuery) Validate(s Schema) error {
	if q.Query == nil {
		return &Error{Msg: "function_score needs a query"}
	}
	if err := q.Query.Validate(s); err != nil {
		return err
	}
	for _, fn := range q.Functions {
		if fn.Filter != nil {
			if err := fn.Filter.Validate(s); err != nil {
				return err
			}
		}
		if fn.FieldValue != nil {
			if err := requireField(s, fn.FieldValue.Field, "field_value_factor"); err != nil {
				return err
			}
		}
		if fn.Decay != nil {
			if fn.Decay.Scale == 0 {
				return &Error{Msg: "decay function needs a non-zero scale"}
			}
			if err := requireField(s, fn.Decay.Field, "decay"); err != nil {
				return err
			}
		}
	}
	return nil
}

// Rewrite rewrites the wrapped base query and keeps the functions as is.
func (q *FunctionScoreQuery) Rewrite() Query {
	c := *q
	c.Query = q.Query.Rewrite()
	return &c
}

// BM25FField names a field that takes part in a BM25F query along with its boost
// and length-normalization strength (doc 13 §4.3). A negative B means use the
// default 0.75.
type BM25FField struct {
	Name  string
	Boost float32
	B     float32
}

// BM25FQuery scores documents with multi-field BM25F, combining per-field evidence
// before the term-frequency saturation rather than after (doc 13 §4). The terms
// are matched as indexed (not re-analyzed).
type BM25FQuery struct {
	base
	Terms  []string
	Fields []BM25FField
	K1     float32
}

// BM25F builds a BM25F query over the given fields.
func BM25F(terms []string, fields ...BM25FField) *BM25FQuery {
	return &BM25FQuery{Terms: terms, Fields: fields}
}

func (*BM25FQuery) queryNode() {}

// Boost returns the query-level boost factor (default 1).
func (q *BM25FQuery) Boost() float32 { return q.boostOr1() }

// WithBoost returns a copy of the node with the boost replaced.
func (q *BM25FQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}

// Validate requires at least one term and one field, and checks each field exists.
func (q *BM25FQuery) Validate(s Schema) error {
	if len(q.Terms) == 0 {
		return &Error{Msg: "bm25f needs at least one term"}
	}
	if len(q.Fields) == 0 {
		return &Error{Msg: "bm25f needs at least one field"}
	}
	for _, f := range q.Fields {
		if err := requireField(s, f.Name, "bm25f"); err != nil {
			return err
		}
	}
	return nil
}

// Rewrite returns the node unchanged.
func (q *BM25FQuery) Rewrite() Query { return q }

// RescoreQuery re-ranks the top window of a cheap base query with a second,
// usually more expensive, query and blends the two scores (doc 13 §10). The base
// query supplies the candidate set; Rescore recomputes a score for each candidate
// it matches, and the final score is QueryWeight*base + RescoreWeight*rescore.
type RescoreQuery struct {
	base
	Query         Query
	Rescore       Query
	WindowSize    int
	QueryWeight   float32
	RescoreWeight float32
}

// Rescore wraps a base query with a rescoring query over the given window. The
// default blend replaces the base score with the rescore score.
func Rescore(q, rescore Query, window int) *RescoreQuery {
	return &RescoreQuery{Query: q, Rescore: rescore, WindowSize: window, QueryWeight: 1, RescoreWeight: 1}
}

func (*RescoreQuery) queryNode() {}

// Boost returns the query-level boost factor (default 1).
func (q *RescoreQuery) Boost() float32 { return q.boostOr1() }

// WithBoost returns a copy of the node with the boost replaced.
func (q *RescoreQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}

// Validate requires both queries, rejects a negative window, and validates each query.
func (q *RescoreQuery) Validate(s Schema) error {
	if q.Query == nil || q.Rescore == nil {
		return &Error{Msg: "rescore needs a base query and a rescore query"}
	}
	if q.WindowSize < 0 {
		return &Error{Msg: "rescore window must be non-negative"}
	}
	if err := q.Query.Validate(s); err != nil {
		return err
	}
	return q.Rescore.Validate(s)
}

// Rewrite rewrites both the base query and the rescore query.
func (q *RescoreQuery) Rewrite() Query {
	c := *q
	c.Query = q.Query.Rewrite()
	c.Rescore = q.Rescore.Rewrite()
	return &c
}
