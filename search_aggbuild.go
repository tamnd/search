package search

import (
	"github.com/tamnd/search/agg"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/segment"
)

// buildAgg compiles one AggSpec into a live accumulator backed by the field's
// doc-values columns (spec 2063 doc 14 §7). Terms and keyword cardinality read
// the keyword columns; the numeric aggregations read the numeric columns,
// widening every numeric type to float64 for bucketing and metrics.
func buildAgg(kv segment.KV, set *segment.SegmentSet, s *schema.Schema, spec AggSpec) (agg.Agg, error) {
	f, ok := s.Lookup(spec.Field)
	if !ok {
		return nil, &query.Error{Msg: "unknown agg field: " + spec.Field}
	}
	fc, err := openFieldCols(kv, set, spec.Field)
	if err != nil {
		return nil, err
	}
	isDouble := f.Type == schema.TypeDouble
	num := func(docID uint32) (float64, bool) { return fc.numValue(docID, isDouble) }
	keys := func(docID uint32) [][]byte { return fc.keys(docID) }

	switch spec.Kind {
	case "terms":
		var sub agg.SubFactory
		if len(spec.Sub) > 0 {
			subSpecs := spec.Sub
			sub = func() map[string]agg.Agg {
				m := make(map[string]agg.Agg, len(subSpecs))
				for name, ss := range subSpecs {
					a, berr := buildAgg(kv, set, s, ss)
					if berr != nil {
						// A sub-agg spec is validated when its parent is built below,
						// so a build error here cannot happen; skip defensively.
						continue
					}
					m[name] = a
				}
				return m
			}
			// Validate the sub-specs once up front so a bad sub-agg is reported.
			for _, ss := range subSpecs {
				if _, verr := buildAgg(kv, set, s, ss); verr != nil {
					return nil, verr
				}
			}
		}
		return agg.NewTerms(keys, spec.Size, spec.ByKey, sub), nil
	case "histogram":
		return agg.NewHistogram(num, spec.Interval, spec.Offset), nil
	case "range":
		return agg.NewRange(num, spec.Ranges), nil
	case "min", "max", "sum", "avg", "count", "stats":
		return agg.NewStats(num, spec.Kind), nil
	case "cardinality":
		if f.Type == schema.TypeKeyword {
			return agg.NewCardinalityKeyword(keys), nil
		}
		return agg.NewCardinalityNumeric(num), nil
	case "percentiles":
		return agg.NewPercentiles(num, spec.Percents), nil
	default:
		return nil, &query.Error{Msg: "unknown agg kind: " + spec.Kind}
	}
}
