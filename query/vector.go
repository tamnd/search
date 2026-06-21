package query

// Dense-vector query nodes (spec 2063 doc 11 §4, doc 15 §9-§10). KNNQuery is
// approximate (or exact) nearest-neighbor search over a dense_vector field.
// HybridQuery fuses a text query with a kNN query using Reciprocal Rank Fusion.
// Both implement the sealed Query interface, but the planner in package exec does
// not turn them into the term iterators the text nodes use; the engine dispatches
// them to the dense-vector search path instead.

// KNNQuery searches a dense_vector field for the documents whose vector is
// nearest to Vector under the field's metric. K is how many neighbors to return;
// NumCandidates is the per-segment efSearch (0 uses the field default). Filter,
// when set, is a pre-filter: only documents it matches are eligible (filtered
// ANN, doc 15 §8).
type KNNQuery struct {
	base
	Field         string
	Vector        []float32
	K             int
	NumCandidates int
	Filter        Query
}

// KNN builds a KNNQuery for k neighbors of vec in field.
func KNN(field string, vec []float32, k int) *KNNQuery {
	return &KNNQuery{Field: field, Vector: vec, K: k}
}

func (*KNNQuery) queryNode()       {}
func (q *KNNQuery) Boost() float32 { return q.boostOr1() }
func (q *KNNQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}

func (q *KNNQuery) Validate(s Schema) error {
	if q.Field == "" {
		return &Error{Msg: "knn query needs a field"}
	}
	if len(q.Vector) == 0 {
		return &Error{Msg: "knn query needs a query vector"}
	}
	if q.K <= 0 {
		return &Error{Msg: "knn query k must be positive"}
	}
	if s != nil {
		typ, ok := s.FieldType(q.Field)
		if !ok {
			return &Error{Msg: "knn query references unknown field " + q.Field}
		}
		if typ != "dense_vector" {
			return &Error{Msg: "knn query field " + q.Field + " is not a dense_vector"}
		}
	}
	if q.Filter != nil {
		return q.Filter.Validate(s)
	}
	return nil
}

// Rewrite rewrites the filter sub-query, if any.
func (q *KNNQuery) Rewrite() Query {
	if q.Filter == nil {
		return q
	}
	c := *q
	c.Filter = q.Filter.Rewrite()
	return &c
}

// HybridQuery runs a text query and a kNN query independently and fuses their
// ranked results with Reciprocal Rank Fusion (doc 15 §10). K is the final number
// of hits; RRFK is the fusion constant (0 uses the default of 60).
type HybridQuery struct {
	base
	Text Query
	KNN  *KNNQuery
	K    int
	RRFK int
}

// Hybrid builds a HybridQuery from a text query and a kNN query.
func Hybrid(text Query, knn *KNNQuery, k int) *HybridQuery {
	return &HybridQuery{Text: text, KNN: knn, K: k}
}

func (*HybridQuery) queryNode()       {}
func (q *HybridQuery) Boost() float32 { return q.boostOr1() }
func (q *HybridQuery) WithBoost(b float32) Query {
	c := *q
	c.boost = b
	return &c
}

func (q *HybridQuery) Validate(s Schema) error {
	if q.Text == nil {
		return &Error{Msg: "hybrid query needs a text query"}
	}
	if q.KNN == nil {
		return &Error{Msg: "hybrid query needs a knn query"}
	}
	if err := q.Text.Validate(s); err != nil {
		return err
	}
	return q.KNN.Validate(s)
}

// Rewrite rewrites both sides of the hybrid query.
func (q *HybridQuery) Rewrite() Query {
	c := *q
	c.Text = q.Text.Rewrite()
	if knn, ok := q.KNN.Rewrite().(*KNNQuery); ok {
		c.KNN = knn
	}
	return &c
}
