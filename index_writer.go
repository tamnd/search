package search

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/tamnd/search/analysis"
	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/memtable"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/segment"
)

// segSeqKey is the catalog key under NSDocID holding the monotonic segment-id
// counter. Segment ids start at 1.
var segSeqKey = []byte("segseq")

// positionGap is the position increment inserted between the values of a
// multi-valued field, so a phrase query cannot match across two distinct values
// (the conventional position-increment gap).
const positionGap = 100

// flushBatch builds an inverted-index memtable over the batch and flushes it as
// one immutable segment. The segment's local doc-id space is the global internal
// doc-id space, so a posting's doc-id maps straight back to the document store;
// the doc-id offset across segments costs only one varint per postings block.
//
// Documents are added in ascending doc-id order so each posting list stays
// sorted by construction. A batch that indexes no text or keyword field produces
// no terms and is flushed only for its stored documents, so no segment is
// written.
func flushBatch(c *catalog.Catalog, s *schema.Schema, entries []docEntry, maxDoc uint64) error {
	sort.Slice(entries, func(i, j int) bool { return entries[i].docID < entries[j].docID })

	mt := memtable.New(0, 0)
	analyzers := make(map[string]*analysis.Analyzer)
	for _, e := range entries {
		if err := indexFields(c, s, analyzers, mt, uint32(e.docID), e.doc); err != nil {
			return err
		}
		mt.AddDoc()
	}
	// A batch with no analyzed terms produces no postings, but it may still carry
	// dense vectors or doc-values-only fields (a geo_point with no inverted terms).
	// Those still need a segment so their vectors and columns are discoverable, so
	// only a wholly empty batch is skipped.
	hasVectors := schemaHasVectorValues(s, entries)
	hasDocValues := schemaHasDocValues(s, entries)
	if len(mt.FieldNames()) == 0 && !hasVectors && !hasDocValues {
		return nil
	}

	segID, err := nextSegID(c)
	if err != nil {
		return err
	}
	// entries are sorted ascending by doc-id, so the first is the batch's base.
	baseDoc := uint32(entries[0].docID)
	if _, err = segment.Flush(c, segID, mt, baseDoc, uint32(maxDoc)+1); err != nil {
		return err
	}
	// Build the columnar doc-values and BKD points index for the same span from
	// the raw document bodies (doc 14). Sorting, faceting, and fast range scans
	// read these columns; the inverted index above serves matching.
	if err := flushDocValues(c, s, segID, entries, baseDoc, uint32(maxDoc)+1); err != nil {
		return err
	}
	// Build the HNSW graph and quantized sidecar for every dense_vector field
	// (doc 15). kNN and hybrid search read these per-field blobs.
	return flushVectors(c, s, segID, entries)
}

// indexFields analyzes every indexed text and keyword field of one document into
// the memtable under docID. Numeric, boolean, date, geo, and vector fields are
// not inverted here; they are served by the doc-values and vector structures of
// later milestones.
func indexFields(c *catalog.Catalog, s *schema.Schema, analyzers map[string]*analysis.Analyzer, mt *memtable.MemTable, docID uint32, doc map[string]any) error {
	for _, f := range s.Fields {
		if !f.Opts.Indexed {
			continue
		}
		v, ok := doc[f.Name]
		if !ok || v == nil {
			continue
		}
		switch f.Type {
		case schema.TypeText, schema.TypeKeyword:
			a, err := fieldAnalyzer(c, analyzers, f)
			if err != nil {
				return err
			}
			indexValue(mt, a, f, docID, v)
		case schema.TypeLong, schema.TypeDouble, schema.TypeDate, schema.TypeBoolean:
			if err := indexNumeric(mt, f, docID, v); err != nil {
				return err
			}
		default:
			// geo_point and dense_vector are served by later-milestone structures.
		}
	}
	return nil
}

// indexNumeric inverts a numeric, date, or boolean field as one order-preserving
// term per value so a RangeQuery can scan it as a term range. These fields keep
// no positions.
func indexNumeric(mt *memtable.MemTable, f schema.Field, docID uint32, v any) error {
	vals := []any{v}
	if arr, ok := v.([]any); ok {
		vals = arr
	}
	for _, sv := range vals {
		if sv == nil {
			continue
		}
		term, ok, err := schema.NumericTerm(f.Type, sv)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		mt.AddToken(f.Name, term, docID, 0, false)
	}
	return nil
}

// indexValue analyzes one field value (a scalar or an array of scalars) into the
// memtable, advancing the position across array elements with a gap so phrases
// cannot span two values.
func indexValue(mt *memtable.MemTable, a *analysis.Analyzer, f schema.Field, docID uint32, v any) {
	positional := f.Opts.Positions
	pos := -1
	for _, sv := range fieldValues(v) {
		for _, tok := range a.Analyze(sv) {
			pos += tok.PositionIncr
			mt.AddToken(f.Name, tok.Term, docID, uint32(pos), positional)
		}
		pos += positionGap
	}
}

// fieldValues returns the string forms of a field value: a single element for a
// scalar, or one per element for an array.
func fieldValues(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if e != nil {
				out = append(out, fmt.Sprintf("%v", e))
			}
		}
		return out
	default:
		return []string{fmt.Sprintf("%v", v)}
	}
}

// fieldAnalyzer resolves and caches the analyzer for a field within one batch.
func fieldAnalyzer(c *catalog.Catalog, cache map[string]*analysis.Analyzer, f schema.Field) (*analysis.Analyzer, error) {
	name := fieldAnalyzerName(f)
	if a, ok := cache[name]; ok {
		return a, nil
	}
	a, err := resolveAnalyzer(c, name)
	if err != nil {
		return nil, err
	}
	cache[name] = a
	return a, nil
}

// nextSegID increments and returns the monotonic segment-id counter.
func nextSegID(c *catalog.Catalog) (uint64, error) {
	var cur uint64
	if b, ok, err := c.Get(catalog.NSDocID, segSeqKey); err != nil {
		return 0, err
	} else if ok {
		cur = binary.BigEndian.Uint64(b)
	}
	cur++
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], cur)
	if err := c.Put(catalog.NSDocID, segSeqKey, b[:]); err != nil {
		return 0, err
	}
	return cur, nil
}
