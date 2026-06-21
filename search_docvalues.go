package search

import (
	"fmt"

	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/docvalues"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/segment"
)

// schemaHasDocValues reports whether the batch carries a value for any
// doc-values field. A batch of geo_point-only documents produces no inverted
// terms, so flushBatch consults this to decide whether a segment must still be
// written so the columns are discoverable.
func schemaHasDocValues(s *schema.Schema, entries []docEntry) bool {
	for _, f := range s.Fields {
		if !f.Opts.DocValues {
			continue
		}
		for _, e := range entries {
			if v, ok := e.doc[f.Name]; ok && v != nil {
				return true
			}
		}
	}
	return false
}

// flushDocValues builds the columnar doc-values and the BKD points index for a
// freshly flushed segment and stores them under the segment's per-field
// namespaces (spec 2063 doc 14 §2-§9). Columns are built from the raw document
// bodies of the batch, indexed by the segment-local position docID-baseDoc, the
// same relative addressing the norm arrays use. A field carries a column only
// when its mapping enables doc_values; numeric, date, and boolean fields also get
// a BKD points index so a range query can resolve to sorted doc-ids in
// O(log N + K) instead of scanning the column.
func flushDocValues(c *catalog.Catalog, s *schema.Schema, segID uint64, entries []docEntry, baseDoc, maxDoc uint32) error {
	if maxDoc <= baseDoc {
		return nil
	}
	span := maxDoc - baseDoc

	for _, f := range s.Fields {
		if !f.Opts.DocValues {
			continue
		}
		multi := fieldIsMultiValued(entries, f.Name)
		var err error
		switch f.Type {
		case schema.TypeKeyword:
			err = buildKeywordColumn(c, segID, f.Name, entries, baseDoc, span, multi)
		case schema.TypeLong, schema.TypeDate, schema.TypeBoolean:
			err = buildIntColumn(c, segID, f, entries, baseDoc, span, multi)
		case schema.TypeDouble:
			err = buildDoubleColumn(c, segID, f.Name, entries, baseDoc, span, multi)
		case schema.TypeGeoPoint:
			err = buildGeoColumn(c, segID, f.Name, entries, baseDoc, span)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// fieldIsMultiValued reports whether any document in the batch carries more than
// one value for the field, which decides between the single- and multi-valued
// column kinds.
func fieldIsMultiValued(entries []docEntry, name string) bool {
	for _, e := range entries {
		if arr, ok := e.doc[name].([]any); ok && len(arr) > 1 {
			return true
		}
	}
	return false
}

// rawValues returns the scalar values of a field for one document: a single
// element for a scalar, one per element for an array, and nil when absent.
func rawValues(doc map[string]any, name string) []any {
	v, ok := doc[name]
	if !ok || v == nil {
		return nil
	}
	if arr, ok := v.([]any); ok {
		out := make([]any, 0, len(arr))
		for _, e := range arr {
			if e != nil {
				out = append(out, e)
			}
		}
		return out
	}
	return []any{v}
}

func buildKeywordColumn(c *catalog.Catalog, segID uint64, name string, entries []docEntry, baseDoc, span uint32, multi bool) error {
	if multi {
		w := docvalues.NewSortedSetWriter(span)
		for _, e := range entries {
			i := uint32(e.docID) - baseDoc
			for _, v := range rawValues(e.doc, name) {
				w.Add(i, []byte(keywordString(v)))
			}
		}
		return segment.WriteDocValues(c, segID, name, w.Bytes())
	}
	w := docvalues.NewSortedWriter(span)
	for _, e := range entries {
		vals := rawValues(e.doc, name)
		if len(vals) == 0 {
			continue
		}
		w.Set(uint32(e.docID)-baseDoc, []byte(keywordString(vals[0])))
	}
	return segment.WriteDocValues(c, segID, name, w.Bytes())
}

func buildIntColumn(c *catalog.Catalog, segID uint64, f schema.Field, entries []docEntry, baseDoc, span uint32, multi bool) error {
	toInt := func(v any) (int64, error) {
		switch f.Type {
		case schema.TypeDate:
			return schema.ToEpochNanos(v)
		case schema.TypeBoolean:
			b, err := schema.ToBool(v)
			if err != nil {
				return 0, err
			}
			if b {
				return 1, nil
			}
			return 0, nil
		default:
			return schema.ToInt64(v)
		}
	}
	bkd := docvalues.NewBKDWriter()
	if multi {
		w := docvalues.NewSortedNumericWriter(span, false)
		for _, e := range entries {
			i := uint32(e.docID) - baseDoc
			for _, v := range rawValues(e.doc, f.Name) {
				n, err := toInt(v)
				if err != nil {
					return err
				}
				w.Add(i, n)
				bkd.Add(i, n)
			}
		}
		if err := segment.WriteDocValues(c, segID, f.Name, w.Bytes()); err != nil {
			return err
		}
	} else {
		w := docvalues.NewNumericWriter(span, false)
		for _, e := range entries {
			vals := rawValues(e.doc, f.Name)
			if len(vals) == 0 {
				continue
			}
			i := uint32(e.docID) - baseDoc
			n, err := toInt(vals[0])
			if err != nil {
				return err
			}
			w.Set(i, n)
			bkd.Add(i, n)
		}
		if err := segment.WriteDocValues(c, segID, f.Name, w.Bytes()); err != nil {
			return err
		}
	}
	return segment.WritePoints(c, segID, f.Name, bkd.Bytes())
}

func buildDoubleColumn(c *catalog.Catalog, segID uint64, name string, entries []docEntry, baseDoc, span uint32, multi bool) error {
	bkd := docvalues.NewBKDWriter()
	if multi {
		w := docvalues.NewSortedNumericWriter(span, true)
		for _, e := range entries {
			i := uint32(e.docID) - baseDoc
			for _, v := range rawValues(e.doc, name) {
				fv, err := schema.ToFloat64(v)
				if err != nil {
					return err
				}
				w.AddFloat(i, fv)
				bkd.Add(i, docvalues.FloatToSortable(fv))
			}
		}
		if err := segment.WriteDocValues(c, segID, name, w.Bytes()); err != nil {
			return err
		}
	} else {
		w := docvalues.NewNumericWriter(span, true)
		for _, e := range entries {
			vals := rawValues(e.doc, name)
			if len(vals) == 0 {
				continue
			}
			i := uint32(e.docID) - baseDoc
			fv, err := schema.ToFloat64(vals[0])
			if err != nil {
				return err
			}
			w.SetFloat(i, fv)
			bkd.Add(i, docvalues.FloatToSortable(fv))
		}
		if err := segment.WriteDocValues(c, segID, name, w.Bytes()); err != nil {
			return err
		}
	}
	return segment.WritePoints(c, segID, name, bkd.Bytes())
}

func buildGeoColumn(c *catalog.Catalog, segID uint64, name string, entries []docEntry, baseDoc, span uint32) error {
	w := docvalues.NewGeoWriter(span)
	for _, e := range entries {
		lat, lon, ok := parseGeoPoint(e.doc[name])
		if !ok {
			continue
		}
		w.Set(uint32(e.docID)-baseDoc, lat, lon)
	}
	return segment.WriteDocValues(c, segID, name, w.Bytes())
}

// keywordString renders a doc-values keyword value as a string. A string value
// is used verbatim; any other scalar is formatted the same way the inverted
// index formats it so the doc-values key matches the indexed term.
func keywordString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// parseGeoPoint extracts a latitude and longitude from a geo_point value. It
// accepts a map with "lat"/"lon" keys or a two-element [lat, lon] array.
func parseGeoPoint(v any) (lat, lon float64, ok bool) {
	switch g := v.(type) {
	case map[string]any:
		la, okLat := toFloatLoose(g["lat"])
		lo, okLon := toFloatLoose(g["lon"])
		return la, lo, okLat && okLon
	case []any:
		if len(g) != 2 {
			return 0, 0, false
		}
		la, okLat := toFloatLoose(g[0])
		lo, okLon := toFloatLoose(g[1])
		return la, lo, okLat && okLon
	default:
		return 0, 0, false
	}
}

func toFloatLoose(v any) (float64, bool) {
	f, err := schema.ToFloat64(v)
	if err != nil {
		return 0, false
	}
	return f, true
}
