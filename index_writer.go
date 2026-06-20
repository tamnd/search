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
	// A batch with no analyzed terms (every document carried only non-indexed or
	// absent fields) produces no postings, so no segment is written.
	if len(mt.FieldNames()) == 0 {
		return nil
	}

	segID, err := nextSegID(c)
	if err != nil {
		return err
	}
	_, err = segment.Flush(c, segID, mt, uint32(maxDoc)+1)
	return err
}

// indexFields analyzes every indexed text and keyword field of one document into
// the memtable under docID. Numeric, boolean, date, geo, and vector fields are
// not inverted here; they are served by the doc-values and vector structures of
// later milestones.
func indexFields(c *catalog.Catalog, s *schema.Schema, analyzers map[string]*analysis.Analyzer, mt *memtable.MemTable, docID uint32, doc map[string]any) error {
	for _, f := range s.Fields {
		if !f.Opts.Indexed || (f.Type != schema.TypeText && f.Type != schema.TypeKeyword) {
			continue
		}
		v, ok := doc[f.Name]
		if !ok || v == nil {
			continue
		}
		a, err := fieldAnalyzer(c, analyzers, f)
		if err != nil {
			return err
		}
		indexValue(mt, a, f, docID, v)
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
