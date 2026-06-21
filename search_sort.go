package search

import (
	"bytes"

	"github.com/tamnd/search/docvalues"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/segment"
)

// fieldCols holds one field's doc-values columns across every live segment, so a
// global doc-id can be resolved to the right segment-local column entry (spec
// 2063 doc 14 §6.5). Segments hold disjoint ascending doc-id ranges, so at most
// one segment matches a given doc-id.
type fieldCols struct {
	segs []fieldColSeg
}

type fieldColSeg struct {
	base uint32
	end  uint32
	col  docvalues.Column
}

// openFieldCols opens the doc-values column for field in every segment that has
// one.
func openFieldCols(kv segment.KV, set *segment.SegmentSet, field string) (*fieldCols, error) {
	fc := &fieldCols{}
	for _, s := range set.Segments() {
		blob, ok, err := s.DocValues(kv, field)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		col, err := docvalues.OpenColumn(blob)
		if err != nil {
			return nil, err
		}
		m := s.Meta()
		fc.segs = append(fc.segs, fieldColSeg{base: m.BaseDoc, end: m.MaxDoc, col: col})
	}
	return fc, nil
}

// locate returns the column and the segment-local index for a global doc-id.
func (fc *fieldCols) locate(docID uint32) (docvalues.Column, uint32, bool) {
	for _, s := range fc.segs {
		if docID >= s.base && docID < s.end {
			return s.col, docID - s.base, true
		}
	}
	return nil, 0, false
}

// sortableInt returns the order-preserving int64 a numeric sort key compares on,
// reducing a multi-valued field by min (ascending) or max (descending).
func (fc *fieldCols) sortableInt(docID uint32, desc bool) (int64, bool) {
	col, i, ok := fc.locate(docID)
	if !ok {
		return 0, false
	}
	switch c := col.(type) {
	case docvalues.NumericColumn:
		if !c.HasValue(i) {
			return 0, false
		}
		return c.Int64(i), true
	case docvalues.SortedNumericColumn:
		vals := c.Values(i)
		if len(vals) == 0 {
			return 0, false
		}
		// Values are stored ascending, so min is first and max is last.
		if desc {
			return vals[len(vals)-1], true
		}
		return vals[0], true
	default:
		return 0, false
	}
}

// numValue returns the real numeric value of a field for an aggregation: the
// decoded float for a double field, the int64 widened to float otherwise. For a
// multi-valued field it returns the first value.
func (fc *fieldCols) numValue(docID uint32, isDouble bool) (float64, bool) {
	col, i, ok := fc.locate(docID)
	if !ok {
		return 0, false
	}
	switch c := col.(type) {
	case docvalues.NumericColumn:
		if !c.HasValue(i) {
			return 0, false
		}
		if isDouble {
			return c.Float64(i), true
		}
		return float64(c.Int64(i)), true
	case docvalues.SortedNumericColumn:
		if isDouble {
			fs := c.Floats(i)
			if len(fs) == 0 {
				return 0, false
			}
			return fs[0], true
		}
		vs := c.Values(i)
		if len(vs) == 0 {
			return 0, false
		}
		return float64(vs[0]), true
	default:
		return 0, false
	}
}

// keys returns the keyword values of a field for an aggregation.
func (fc *fieldCols) keys(docID uint32) [][]byte {
	col, i, ok := fc.locate(docID)
	if !ok {
		return nil
	}
	switch c := col.(type) {
	case docvalues.SortedColumn:
		ord := c.OrdAt(i)
		if ord < 0 {
			return nil
		}
		return [][]byte{c.LookupOrd(uint32(ord))}
	case docvalues.SortedSetColumn:
		ords := c.OrdinalsFor(i)
		if len(ords) == 0 {
			return nil
		}
		out := make([][]byte, len(ords))
		for k, o := range ords {
			out[k] = c.LookupOrd(o)
		}
		return out
	default:
		return nil
	}
}

// singleKey returns the single keyword value of a field for sort and collapse.
func (fc *fieldCols) singleKey(docID uint32) ([]byte, bool) {
	col, i, ok := fc.locate(docID)
	if !ok {
		return nil, false
	}
	if c, ok := col.(docvalues.SortedColumn); ok {
		ord := c.OrdAt(i)
		if ord < 0 {
			return nil, false
		}
		return c.LookupOrd(uint32(ord)), true
	}
	return nil, false
}

// latLon returns the geographic point of a geo_point field for a doc.
func (fc *fieldCols) latLon(docID uint32) (lat, lon float64, ok bool) {
	col, i, found := fc.locate(docID)
	if !found {
		return 0, 0, false
	}
	if c, ok := col.(docvalues.GeoColumn); ok {
		if !c.HasValue(i) {
			return 0, 0, false
		}
		lat, lon = c.LatLon(i)
		return lat, lon, true
	}
	return 0, 0, false
}

// sortVal is one sort key's value for one document. Exactly one of the three
// representations is meaningful, selected by kind; missing marks an absent value.
type sortVal struct {
	kind    byte // 0 = int64, 1 = bytes, 2 = float64
	i       int64
	b       []byte
	f       float64
	missing bool
}

const (
	svInt   = 0
	svBytes = 1
	svFloat = 2
)

// extractor produces a sort value for a doc given its score.
type extractor func(docID uint32, score float32) sortVal

// candidate is a matching document carrying its score and pre-read sort values.
type candidate struct {
	docID uint32
	score float32
	keys  []sortVal
}

// comparator ranks candidates by a chain of sort keys, breaking ties by
// ascending doc-id for a deterministic order.
type comparator struct {
	descs        []bool
	missingLasts []bool
}

// less reports whether a should rank before b.
func (c comparator) less(a, b candidate) bool {
	for i := range a.keys {
		r := compareSortVal(a.keys[i], b.keys[i], c.descs[i], c.missingLasts[i])
		if r != 0 {
			return r < 0
		}
	}
	return a.docID < b.docID
}

// compareSortVal compares two sort values honoring direction and missing policy.
// The missing policy is independent of direction: a missing value goes last
// (missingLast) or first regardless of ascending or descending.
func compareSortVal(a, b sortVal, desc, missingLast bool) int {
	if a.missing || b.missing {
		switch {
		case a.missing && b.missing:
			return 0
		case a.missing:
			if missingLast {
				return 1
			}
			return -1
		default:
			if missingLast {
				return -1
			}
			return 1
		}
	}
	var r int
	switch a.kind {
	case svBytes:
		r = bytes.Compare(a.b, b.b)
	case svFloat:
		switch {
		case a.f < b.f:
			r = -1
		case a.f > b.f:
			r = 1
		}
	default:
		switch {
		case a.i < b.i:
			r = -1
		case a.i > b.i:
			r = 1
		}
	}
	if desc {
		r = -r
	}
	return r
}

// buildSortExtractors compiles a sort specification into per-key value
// extractors plus the parallel direction and missing-policy slices the
// comparator needs.
func buildSortExtractors(kv segment.KV, set *segment.SegmentSet, s *schema.Schema, keys []SortKey) ([]extractor, []bool, []bool, error) {
	exs := make([]extractor, len(keys))
	descs := make([]bool, len(keys))
	missingLasts := make([]bool, len(keys))
	for i, k := range keys {
		descs[i] = k.Desc
		missingLasts[i] = k.MissingLast
		if k.Field == "" || k.Field == "_score" {
			exs[i] = func(_ uint32, score float32) sortVal {
				return sortVal{kind: svFloat, f: float64(score)}
			}
			continue
		}
		f, ok := s.Lookup(k.Field)
		if !ok {
			return nil, nil, nil, &query.Error{Msg: "unknown sort field: " + k.Field}
		}
		fc, err := openFieldCols(kv, set, k.Field)
		if err != nil {
			return nil, nil, nil, err
		}
		switch f.Type {
		case schema.TypeKeyword:
			fcc := fc
			exs[i] = func(docID uint32, _ float32) sortVal {
				kb, ok := fcc.singleKey(docID)
				if !ok {
					return sortVal{kind: svBytes, missing: true}
				}
				return sortVal{kind: svBytes, b: kb}
			}
		case schema.TypeGeoPoint:
			if k.Origin == nil {
				return nil, nil, nil, &query.Error{Msg: "geo_point sort needs an origin"}
			}
			fcc, origin := fc, *k.Origin
			exs[i] = func(docID uint32, _ float32) sortVal {
				lat, lon, ok := fcc.latLon(docID)
				if !ok {
					return sortVal{kind: svFloat, missing: true}
				}
				return sortVal{kind: svFloat, f: docvalues.Haversine(origin.Lat, origin.Lon, lat, lon)}
			}
		default:
			fcc, desc := fc, k.Desc
			exs[i] = func(docID uint32, _ float32) sortVal {
				v, ok := fcc.sortableInt(docID, desc)
				if !ok {
					return sortVal{kind: svInt, missing: true}
				}
				return sortVal{kind: svInt, i: v}
			}
		}
	}
	return exs, descs, missingLasts, nil
}
