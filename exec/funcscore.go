package exec

import (
	"math"

	"github.com/tamnd/search/docvalues"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/segment"
)

// numericResolver resolves a numeric doc-values field value for a global doc-id by
// finding the segment that owns the doc and reading its column. Columns are opened
// lazily and cached per segment. A doc with no value reports ok=false so the caller
// can substitute a configured missing value.
type numericResolver struct {
	se    *Searcher
	field string
	cols  map[uint64]docvalues.NumericColumn // segment id -> column (nil when absent)
}

func (se *Searcher) newNumericResolver(field string) *numericResolver {
	return &numericResolver{se: se, field: field, cols: make(map[uint64]docvalues.NumericColumn)}
}

// valueAt returns the field value for a global doc-id. ok is false when the doc has
// no value or the owning segment has no such column.
func (nr *numericResolver) valueAt(doc uint32) (float64, bool, error) {
	for _, seg := range nr.se.segs {
		base := seg.Meta().BaseDoc
		end := base + seg.Meta().DocCount
		if doc < base || doc >= end {
			continue
		}
		col, err := nr.column(seg.ID(), seg)
		if err != nil {
			return 0, false, err
		}
		if col == nil {
			return 0, false, nil
		}
		i := doc - base
		if !col.HasValue(i) {
			return 0, false, nil
		}
		return col.Float64(i), true, nil
	}
	return 0, false, nil
}

func (nr *numericResolver) column(id uint64, seg *segment.Segment) (docvalues.NumericColumn, error) {
	if c, ok := nr.cols[id]; ok {
		return c, nil
	}
	blob, ok, err := seg.DocValues(nr.se.kv, nr.field)
	if err != nil {
		return nil, err
	}
	if !ok {
		nr.cols[id] = nil
		return nil, nil
	}
	col, err := docvalues.OpenColumn(blob)
	if err != nil {
		return nil, err
	}
	nc, _ := col.(docvalues.NumericColumn)
	nr.cols[id] = nc
	return nc, nil
}

// docFunc computes one function's contribution for a document. ok reports whether
// the function's filter (if any) matched; a function that does not match is left
// out of the combination.
type docFunc struct {
	filter scorer
	weight float32
	value  func(doc uint32) (float32, error)
}

// match reports whether the function applies to doc, advancing its filter scorer.
func (f *docFunc) match(doc uint32) (bool, error) {
	if f.filter == nil {
		return true, nil
	}
	if f.filter.docID() == doc {
		return true, nil
	}
	if f.filter.docID() > doc {
		return false, nil
	}
	d, err := f.filter.advance(doc)
	if err != nil {
		return false, err
	}
	return d == doc, nil
}

// functionScoreScorer reshapes the score of an inner scorer with one or more
// functions (doc 13 §8). It iterates exactly the inner scorer's documents; the
// per-doc score blends the inner (query) score with the combined function score.
type functionScoreScorer struct {
	inner     scorer
	funcs     []docFunc
	scoreMode query.ScoreMode
	boostMode query.BoostMode
	maxBoost  float32
	boost     float32
}

func (f *functionScoreScorer) docID() uint32                    { return f.inner.docID() }
func (f *functionScoreScorer) next() (uint32, error)            { return f.inner.next() }
func (f *functionScoreScorer) advance(t uint32) (uint32, error) { return f.inner.advance(t) }
func (f *functionScoreScorer) cost() int                        { return f.inner.cost() }

func (f *functionScoreScorer) score() float32 {
	s, err := f.scoreErr()
	if err != nil {
		return 0
	}
	return s
}

// scoreErr computes the blended score, surfacing doc-values read errors. The
// scorer interface's score() cannot return an error, so a column read failure
// degrades to a zero contribution; in practice columns are validated at open.
func (f *functionScoreScorer) scoreErr() (float32, error) {
	doc := f.inner.docID()
	base := f.inner.score()

	fn, applied, err := f.combineFunctions(doc)
	if err != nil {
		return 0, err
	}
	if !applied {
		// No function matched: the function score is neutral (1 for multiply,
		// 0 otherwise) so the base score passes through unchanged where it should.
		fn = neutralFunctionScore(f.boostMode)
	}
	if f.maxBoost > 0 && fn > f.maxBoost {
		fn = f.maxBoost
	}
	return f.boost * blend(f.boostMode, base, fn), nil
}

// combineFunctions evaluates every function whose filter matches doc and folds
// them together per the score mode. applied reports whether any function matched.
func (f *functionScoreScorer) combineFunctions(doc uint32) (score float32, applied bool, err error) {
	var acc float32
	count := 0
	for i := range f.funcs {
		ok, err := f.funcs[i].match(doc)
		if err != nil {
			return 0, false, err
		}
		if !ok {
			continue
		}
		v, err := f.funcs[i].value(doc)
		if err != nil {
			return 0, false, err
		}
		v *= f.funcs[i].weight
		if count == 0 {
			acc = v
			if f.scoreMode == query.ScoreFirst {
				return acc, true, nil
			}
		} else {
			acc = foldScore(f.scoreMode, acc, v)
		}
		count++
	}
	if count == 0 {
		return 0, false, nil
	}
	if f.scoreMode == query.ScoreAvg {
		acc /= float32(count)
	}
	return acc, true, nil
}

// foldScore combines a running accumulator with the next function value per mode.
// Averaging divides by the count after the fold, so here it behaves as a sum.
func foldScore(mode query.ScoreMode, acc, v float32) float32 {
	switch mode {
	case query.ScoreSum, query.ScoreAvg:
		return acc + v
	case query.ScoreMax:
		if v > acc {
			return v
		}
		return acc
	case query.ScoreMin:
		if v < acc {
			return v
		}
		return acc
	case query.ScoreFirst:
		return acc
	default: // ScoreMultiply
		return acc * v
	}
}

// neutralFunctionScore is the function score that leaves the base score unchanged
// for a given boost mode when no function matched.
func neutralFunctionScore(mode query.BoostMode) float32 {
	if mode == query.BoostMultiply {
		return 1
	}
	return 0
}

// blend merges the query score qs with the function score fn per boost mode.
func blend(mode query.BoostMode, qs, fn float32) float32 {
	switch mode {
	case query.BoostReplace:
		return fn
	case query.BoostSum:
		return qs + fn
	case query.BoostAvg:
		return (qs + fn) / 2
	case query.BoostMax:
		if fn > qs {
			return fn
		}
		return qs
	case query.BoostMin:
		if fn < qs {
			return fn
		}
		return qs
	default: // BoostMultiply
		return qs * fn
	}
}

// compileFunctionScore builds the function-score scorer over the base query.
func (se *Searcher) compileFunctionScore(n *query.FunctionScoreQuery) (scorer, error) {
	inner, err := se.compile(n.Query)
	if err != nil {
		return nil, err
	}
	funcs := make([]docFunc, 0, len(n.Functions))
	for _, fn := range n.Functions {
		df, err := se.compileFunction(fn)
		if err != nil {
			return nil, err
		}
		funcs = append(funcs, df)
	}
	scorer := &functionScoreScorer{
		inner:     inner,
		funcs:     funcs,
		scoreMode: n.ScoreMode,
		boostMode: n.BoostMode,
		maxBoost:  n.MaxBoost,
		boost:     n.Boost(),
	}
	if n.MinScore != 0 {
		return &minScoreScorer{inner: scorer, min: n.MinScore}, nil
	}
	return scorer, nil
}

// compileFunction turns one score function spec into an evaluatable docFunc.
func (se *Searcher) compileFunction(fn query.ScoreFunction) (docFunc, error) {
	df := docFunc{weight: fn.Weight}
	if df.weight == 0 {
		df.weight = 1
	}
	if fn.Filter != nil {
		fs, err := se.compile(fn.Filter)
		if err != nil {
			return docFunc{}, err
		}
		df.filter = fs
	}
	switch {
	case fn.FieldValue != nil:
		df.value = se.fieldValueFunc(fn.FieldValue)
	case fn.Decay != nil:
		df.value = se.decayFunc(fn.Decay)
	case fn.Random != nil:
		seed := uint64(fn.Random.Seed)
		df.value = func(doc uint32) (float32, error) { return randomScore(seed, doc), nil }
	default:
		// Weight-only function: a constant contribution of 1 (scaled by weight).
		df.value = func(uint32) (float32, error) { return 1, nil }
	}
	return df, nil
}

// fieldValueFunc reads a numeric field and applies the configured modifier.
func (se *Searcher) fieldValueFunc(fv *query.FieldValueFactor) func(uint32) (float32, error) {
	nr := se.newNumericResolver(fv.Field)
	factor := float64(fv.Factor)
	if factor == 0 {
		factor = 1
	}
	return func(doc uint32) (float32, error) {
		v, ok, err := nr.valueAt(doc)
		if err != nil {
			return 0, err
		}
		if !ok {
			v = float64(fv.Missing)
		}
		return float32(applyModifier(fv.Modifier, factor*v)), nil
	}
}

// applyModifier applies a field-value modifier function (doc 13 §8.2).
func applyModifier(mod query.FieldModifier, x float64) float64 {
	switch mod {
	case query.ModLog:
		return math.Log10(x)
	case query.ModLog1p:
		return math.Log10(1 + x)
	case query.ModLog2p:
		return math.Log10(2 + x)
	case query.ModLn:
		return math.Log(x)
	case query.ModLn1p:
		return math.Log1p(x)
	case query.ModLn2p:
		return math.Log(2 + x)
	case query.ModSquare:
		return x * x
	case query.ModSqrt:
		return math.Sqrt(x)
	case query.ModReciprocal:
		return 1 / x
	default:
		return x
	}
}

// decayFunc builds a decay function over a numeric field (doc 13 §8.3).
func (se *Searcher) decayFunc(d *query.DecayFunction) func(uint32) (float32, error) {
	nr := se.newNumericResolver(d.Field)
	decay := d.Decay
	if decay <= 0 || decay >= 1 {
		decay = 0.5
	}
	scale := d.Scale
	if scale == 0 {
		scale = 1
	}
	offset := d.Offset
	var sigma2, lambda, linScale float64
	switch d.Kind {
	case query.DecayGauss:
		sigma2 = -scale * scale / (2 * math.Log(decay))
	case query.DecayExp:
		lambda = -math.Log(decay) / scale
	default: // linear
		linScale = scale / (1 - decay)
	}
	return func(doc uint32) (float32, error) {
		v, ok, err := nr.valueAt(doc)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, nil
		}
		dist := math.Abs(v-d.Origin) - offset
		if dist < 0 {
			dist = 0
		}
		switch d.Kind {
		case query.DecayGauss:
			return float32(math.Exp(-dist * dist / (2 * sigma2))), nil
		case query.DecayExp:
			return float32(math.Exp(-lambda * dist)), nil
		default:
			f := (linScale - dist) / linScale
			if f < 0 {
				f = 0
			}
			return float32(f), nil
		}
	}
}

// randomScore returns a stable pseudo-random value in [0,1) for a document, mixing
// the seed and the doc-id with a splitmix64 finalizer (doc 13 §8.4). It is
// deterministic, so repeating a query yields the same ordering.
func randomScore(seed uint64, doc uint32) float32 {
	x := seed ^ (uint64(doc) * 0x9e3779b97f4a7c15)
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return float32(x>>11) / float32(1<<53)
}

// minScoreScorer drops documents whose score falls below a floor (doc 13 §8.1).
type minScoreScorer struct {
	inner scorer
	min   float32
	cur   uint32
}

func (m *minScoreScorer) docID() uint32 { return m.cur }

func (m *minScoreScorer) next() (uint32, error) {
	return m.find(func() (uint32, error) { return m.inner.next() })
}

func (m *minScoreScorer) advance(t uint32) (uint32, error) {
	return m.find(func() (uint32, error) { return m.inner.advance(t) })
}

func (m *minScoreScorer) find(step func() (uint32, error)) (uint32, error) {
	for {
		d, err := step()
		if err != nil {
			return 0, err
		}
		if d == noMore {
			m.cur = noMore
			return noMore, nil
		}
		if m.inner.score() >= m.min {
			m.cur = d
			return d, nil
		}
	}
}

func (m *minScoreScorer) score() float32 { return m.inner.score() }
func (m *minScoreScorer) cost() int      { return m.inner.cost() }
