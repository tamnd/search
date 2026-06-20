// Package agg builds the faceting and aggregation accumulators that run in a
// single pass over a query's matching documents (spec 2063 doc 14 §7). An
// aggregation reads a field's doc-values column for each matching doc and folds
// the value into an accumulator: a terms aggregation counts documents per
// keyword, a histogram buckets a numeric field, and a metric aggregation tracks
// a running min, max, sum, average, distinct count, or percentile.
//
// The accumulators here are deliberately decoupled from the column readers in
// package docvalues. The caller binds each aggregation to a value reader (a
// closure that reads the field for a doc) and drives every accumulator from one
// loop over the matching doc-ids. This keeps the package free of the segment and
// catalog machinery and makes every accumulator unit-testable on its own.
//
// Cross-segment merge is handled by keying the terms accumulator on the keyword
// bytes themselves rather than on per-segment ordinals. The spec's global-ordinal
// table (doc 14 §7.7) is an optimization over this; the byte-keyed map is the
// behavior it accelerates, and it is correct across any number of segments
// without a separate merge pass. This is a documented deviation.
package agg

import (
	"math"
	"slices"
	"sort"
	"strconv"
)

// Bucket is one bucket of a bucket aggregation: a key, the number of documents
// that fell in it, and the results of any nested sub-aggregations.
type Bucket struct {
	Key   string
	Count uint64
	Subs  map[string]Result
}

// Result is the output of one aggregation. A bucket aggregation fills Buckets; a
// single-value metric fills Value; a multi-value metric (stats, percentiles)
// fills Values.
type Result struct {
	Buckets []Bucket
	Value   float64
	Values  map[string]float64
}

// Agg is one bound aggregation: Collect folds the matching doc into the
// accumulator and Result reads the final answer. Collect is called once per
// matching doc in ascending doc-id order.
type Agg interface {
	Collect(docID uint32)
	Result() Result
}

// KeysReader returns the keyword values of a field for a doc. A single-valued
// field returns a one-element slice; a missing value returns nil.
type KeysReader func(docID uint32) [][]byte

// NumReader returns the numeric value of a field for a doc and whether it is
// present.
type NumReader func(docID uint32) (float64, bool)

// SubFactory builds a fresh set of sub-aggregations for one bucket. The returned
// map is keyed by sub-aggregation name.
type SubFactory func() map[string]Agg

// TermsAgg counts documents per distinct keyword value and, optionally, runs a
// set of sub-aggregations within each bucket (doc 14 §7.2, §7.5).
type TermsAgg struct {
	keys    KeysReader
	size    int
	byKey   bool // order by key ascending instead of by count descending
	counts  map[string]uint64
	subs    map[string]map[string]Agg
	subFact SubFactory
}

// NewTerms returns a terms aggregation over keys. size caps the number of
// buckets returned; byKey orders buckets by key ascending instead of by count
// descending. sub may be nil for a leaf aggregation.
func NewTerms(keys KeysReader, size int, byKey bool, sub SubFactory) *TermsAgg {
	return &TermsAgg{
		keys:    keys,
		size:    size,
		byKey:   byKey,
		counts:  make(map[string]uint64),
		subs:    make(map[string]map[string]Agg),
		subFact: sub,
	}
}

// Collect folds one doc into every bucket its keyword values name.
func (a *TermsAgg) Collect(docID uint32) {
	for _, kb := range a.keys(docID) {
		key := string(kb)
		a.counts[key]++
		if a.subFact != nil {
			s, ok := a.subs[key]
			if !ok {
				s = a.subFact()
				a.subs[key] = s
			}
			for _, sub := range s {
				sub.Collect(docID)
			}
		}
	}
}

// Result returns the top buckets in the configured order.
func (a *TermsAgg) Result() Result {
	type kc struct {
		key string
		cnt uint64
	}
	pairs := make([]kc, 0, len(a.counts))
	for k, c := range a.counts {
		pairs = append(pairs, kc{k, c})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if a.byKey {
			return pairs[i].key < pairs[j].key
		}
		if pairs[i].cnt != pairs[j].cnt {
			return pairs[i].cnt > pairs[j].cnt
		}
		return pairs[i].key < pairs[j].key // stable tiebreak
	})
	if a.size > 0 && len(pairs) > a.size {
		pairs = pairs[:a.size]
	}
	out := Result{Buckets: make([]Bucket, 0, len(pairs))}
	for _, p := range pairs {
		b := Bucket{Key: p.key, Count: p.cnt}
		if subs, ok := a.subs[p.key]; ok {
			b.Subs = make(map[string]Result, len(subs))
			for name, sub := range subs {
				b.Subs[name] = sub.Result()
			}
		}
		out.Buckets = append(out.Buckets, b)
	}
	return out
}

// HistogramAgg partitions a numeric field into fixed-width buckets (doc 14 §7.3).
type HistogramAgg struct {
	num      NumReader
	interval float64
	offset   float64
	counts   map[int64]uint64
}

// NewHistogram returns a histogram aggregation with the given bucket width and
// baseline offset.
func NewHistogram(num NumReader, interval, offset float64) *HistogramAgg {
	if interval <= 0 {
		interval = 1
	}
	return &HistogramAgg{num: num, interval: interval, offset: offset, counts: make(map[int64]uint64)}
}

// Collect places one doc in its bucket.
func (a *HistogramAgg) Collect(docID uint32) {
	v, ok := a.num(docID)
	if !ok {
		return
	}
	idx := int64(math.Floor((v - a.offset) / a.interval))
	a.counts[idx]++
}

// Result returns the buckets in ascending key order. Each bucket's key is the
// bucket's lower bound (offset + idx*interval), formatted by the caller.
func (a *HistogramAgg) Result() Result {
	idxs := make([]int64, 0, len(a.counts))
	for i := range a.counts {
		idxs = append(idxs, i)
	}
	slices.Sort(idxs)
	out := Result{Buckets: make([]Bucket, 0, len(idxs))}
	for _, i := range idxs {
		lo := a.offset + float64(i)*a.interval
		out.Buckets = append(out.Buckets, Bucket{Key: formatNum(lo), Count: a.counts[i]})
	}
	return out
}

// RangeAgg counts documents into a fixed set of half-open numeric ranges
// [From, To); an open bound is represented by NaN (doc 14 §7, range facet).
type RangeAgg struct {
	num    NumReader
	ranges []NumRange
	counts []uint64
}

// NumRange is one named numeric range. From is inclusive, To is exclusive; a NaN
// bound is open on that side.
type NumRange struct {
	Key  string
	From float64
	To   float64
}

// NewRange returns a range aggregation over the given ranges.
func NewRange(num NumReader, ranges []NumRange) *RangeAgg {
	return &RangeAgg{num: num, ranges: ranges, counts: make([]uint64, len(ranges))}
}

// Collect adds one doc to every range it falls in.
func (a *RangeAgg) Collect(docID uint32) {
	v, ok := a.num(docID)
	if !ok {
		return
	}
	for i, r := range a.ranges {
		if (math.IsNaN(r.From) || v >= r.From) && (math.IsNaN(r.To) || v < r.To) {
			a.counts[i]++
		}
	}
}

// Result returns one bucket per configured range, in declaration order.
func (a *RangeAgg) Result() Result {
	out := Result{Buckets: make([]Bucket, len(a.ranges))}
	for i, r := range a.ranges {
		out.Buckets[i] = Bucket{Key: r.Key, Count: a.counts[i]}
	}
	return out
}

// StatsAgg tracks the count, sum, min, max, and average of a numeric field in a
// single pass (doc 14 §7.4). The metric parameter selects which scalar Result
// reports; "stats" reports them all in Values.
type StatsAgg struct {
	num    NumReader
	metric string
	count  uint64
	sum    float64
	min    float64
	max    float64
}

// NewStats returns a metric aggregation. metric is one of min, max, sum, avg,
// count, or stats.
func NewStats(num NumReader, metric string) *StatsAgg {
	return &StatsAgg{num: num, metric: metric, min: math.Inf(1), max: math.Inf(-1)}
}

// Collect folds one doc's value into the running statistics.
func (a *StatsAgg) Collect(docID uint32) {
	v, ok := a.num(docID)
	if !ok {
		return
	}
	a.count++
	a.sum += v
	if v < a.min {
		a.min = v
	}
	if v > a.max {
		a.max = v
	}
}

// Result returns the selected metric.
func (a *StatsAgg) Result() Result {
	avg := 0.0
	if a.count > 0 {
		avg = a.sum / float64(a.count)
	}
	switch a.metric {
	case "min":
		return Result{Value: a.minOr0()}
	case "max":
		return Result{Value: a.maxOr0()}
	case "sum":
		return Result{Value: a.sum}
	case "count":
		return Result{Value: float64(a.count)}
	case "avg":
		return Result{Value: avg}
	default: // stats
		return Result{Values: map[string]float64{
			"count": float64(a.count),
			"sum":   a.sum,
			"min":   a.minOr0(),
			"max":   a.maxOr0(),
			"avg":   avg,
		}}
	}
}

func (a *StatsAgg) minOr0() float64 {
	if a.count == 0 {
		return 0
	}
	return a.min
}

func (a *StatsAgg) maxOr0() float64 {
	if a.count == 0 {
		return 0
	}
	return a.max
}

// CardinalityAgg estimates the number of distinct values of a field using a
// HyperLogLog sketch (doc 14 §7.4).
type CardinalityAgg struct {
	keys KeysReader
	num  NumReader
	hll  *hll
}

// NewCardinalityKeyword returns a cardinality aggregation over a keyword field.
func NewCardinalityKeyword(keys KeysReader) *CardinalityAgg {
	return &CardinalityAgg{keys: keys, hll: newHLL()}
}

// NewCardinalityNumeric returns a cardinality aggregation over a numeric field.
func NewCardinalityNumeric(num NumReader) *CardinalityAgg {
	return &CardinalityAgg{num: num, hll: newHLL()}
}

// Collect inserts the doc's value(s) into the sketch.
func (a *CardinalityAgg) Collect(docID uint32) {
	if a.keys != nil {
		for _, kb := range a.keys(docID) {
			a.hll.add(hashBytes(kb))
		}
		return
	}
	if v, ok := a.num(docID); ok {
		a.hll.add(hashU64(math.Float64bits(v)))
	}
}

// Result returns the estimated distinct count.
func (a *CardinalityAgg) Result() Result {
	return Result{Value: a.hll.estimate()}
}

// PercentilesAgg estimates quantiles of a numeric field with a t-digest (doc 14
// §7.4).
type PercentilesAgg struct {
	num    NumReader
	percs  []float64
	digest *tdigest
}

// NewPercentiles returns a percentiles aggregation reporting the given
// percentiles (each in [0,100]).
func NewPercentiles(num NumReader, percs []float64) *PercentilesAgg {
	return &PercentilesAgg{num: num, percs: percs, digest: newTDigest(100)}
}

// Collect inserts the doc's value into the digest.
func (a *PercentilesAgg) Collect(docID uint32) {
	if v, ok := a.num(docID); ok {
		a.digest.add(v)
	}
}

// Result returns one entry per requested percentile, keyed by the percentile
// formatted as a string (e.g. "95").
func (a *PercentilesAgg) Result() Result {
	vals := make(map[string]float64, len(a.percs))
	for _, p := range a.percs {
		vals[formatNum(p)] = a.digest.quantile(p / 100)
	}
	return Result{Values: vals}
}

// formatNum formats a float bucket key without a trailing ".0" for integers.
func formatNum(v float64) string {
	if v == math.Trunc(v) && !math.IsInf(v, 0) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
