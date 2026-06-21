// Package exec plans and runs a query tree against the live segments and merges
// the per-segment results into a single top-k (spec 2063 doc 12). Because the
// engine uses one global doc-id space and assigns each batch a contiguous
// ascending range, a term's per-segment postings can be chained into one
// ascending stream, so the whole query is planned and executed once and the
// multi-segment merge needs no remapping. Scoring uses the index-wide collection
// statistics (total document count, per-field average length, and a term's
// document frequency summed across every segment) so a document's BM25 score does
// not depend on which segment it landed in.
package exec

import (
	"fmt"
	"sort"

	"github.com/tamnd/search/analysis"
	"github.com/tamnd/search/collect"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/score"
	"github.com/tamnd/search/segment"
)

// AnalyzerFunc resolves the query-time analyzer for a field.
type AnalyzerFunc func(field string) (*analysis.Analyzer, error)

// Searcher runs queries over a snapshot's segments.
type Searcher struct {
	kv       segment.KV
	segs     []*segment.Segment
	schema   *schema.Schema
	analyzer AnalyzerFunc
	live     []uint32 // sorted live global doc-ids, for match_all
	dead     []uint32 // sorted deleted global doc-ids, filtered out of every result

	n            int64 // total documents in the collection
	k1, b        float64
	frCache      map[uint64]map[string]*segment.FieldReader
	avgdlCache   map[string]float64
	docFreqCache map[string]int64
	warnings     []string
}

// New builds a searcher over the segment set. live is the sorted slice of live
// global doc-ids (used only by match_all). k1 and b are the BM25 parameters; pass
// score.DefaultK1 and score.DefaultB for the standard model.
func New(kv segment.KV, set *segment.SegmentSet, s *schema.Schema, analyzer AnalyzerFunc, live, dead []uint32) *Searcher {
	se := &Searcher{
		kv:           kv,
		segs:         set.Segments(),
		schema:       s,
		analyzer:     analyzer,
		live:         live,
		dead:         dead,
		k1:           score.DefaultK1,
		b:            score.DefaultB,
		frCache:      make(map[uint64]map[string]*segment.FieldReader),
		avgdlCache:   make(map[string]float64),
		docFreqCache: make(map[string]int64),
	}
	for _, seg := range se.segs {
		se.n += int64(seg.Meta().DocCount)
	}
	return se
}

// Search runs q and returns the k highest-scoring hits.
func (se *Searcher) Search(q query.Query, k int) ([]collect.Hit, error) {
	q = q.Rewrite()
	if err := q.Validate(schemaView{se.schema}); err != nil {
		return nil, err
	}
	if rq, ok := q.(*query.RescoreQuery); ok {
		return se.searchRescore(rq, k)
	}
	sc, err := se.compile(q)
	if err != nil {
		return nil, err
	}
	return se.collectTopK(sc, k)
}

// collectTopK drives a scorer to completion and returns the k highest-scoring
// documents, filtering deleted docs and feeding the running threshold back to a
// WAND-capable scorer so it can prune.
func (se *Searcher) collectTopK(sc scorer, k int) ([]collect.Hit, error) {
	sc = newLiveFilter(sc, se.dead)
	c := collect.NewTopK(k)
	prune, prunes := sc.(pruner)
	d, err := sc.next()
	if err != nil {
		return nil, err
	}
	for d != noMore {
		c.Collect(d, sc.score())
		if prunes {
			prune.setMinScore(c.Threshold())
		}
		d, err = sc.next()
		if err != nil {
			return nil, err
		}
	}
	return c.Results(), nil
}

// searchRescore runs the two-phase rescore (doc 13 §10): the base query supplies a
// candidate window, then the rescore query recomputes a score for each candidate
// it matches and the two scores blend. Candidates the rescore query does not match
// keep only their weighted base score.
func (se *Searcher) searchRescore(rq *query.RescoreQuery, k int) ([]collect.Hit, error) {
	window := max(rq.WindowSize, k)
	if window <= 0 {
		window = k
	}
	base, err := se.Search(rq.Query, window)
	if err != nil {
		return nil, err
	}
	rsc, err := se.compile(rq.Rescore)
	if err != nil {
		return nil, err
	}
	// Candidates come back score-sorted; advancing the rescore scorer needs them
	// in ascending doc-id order, so sort a working copy by doc-id first.
	cand := make([]collect.Hit, len(base))
	copy(cand, base)
	sort.Slice(cand, func(i, j int) bool { return cand[i].DocID < cand[j].DocID })
	if _, err := rsc.next(); err != nil {
		return nil, err
	}
	c := collect.NewTopK(k)
	for _, h := range cand {
		var rs float32
		d := rsc.docID()
		if d != noMore && d < h.DocID {
			d, err = rsc.advance(h.DocID)
			if err != nil {
				return nil, err
			}
		}
		if d == h.DocID {
			rs = rsc.score()
		}
		c.Collect(h.DocID, rq.QueryWeight*h.Score+rq.RescoreWeight*rs)
	}
	return c.Results(), nil
}

// Warnings returns any non-fatal planner warnings accumulated while running the
// last query, such as a regexp scan that visited an excessive number of terms.
func (se *Searcher) Warnings() []string { return se.warnings }

// warnf records a planner warning to surface to the caller.
func (se *Searcher) warnf(format string, args ...any) {
	se.warnings = append(se.warnings, fmt.Sprintf(format, args...))
}

// fieldReader returns and caches the field reader for a segment, or nil when the
// segment has no such field.
func (se *Searcher) fieldReader(seg *segment.Segment, field string) (*segment.FieldReader, error) {
	id := seg.ID()
	byField, ok := se.frCache[id]
	if !ok {
		byField = make(map[string]*segment.FieldReader)
		se.frCache[id] = byField
	}
	if fr, ok := byField[field]; ok {
		return fr, nil
	}
	fr, ok, err := seg.Field(se.kv, field)
	if err != nil {
		return nil, err
	}
	if !ok {
		byField[field] = nil
		return nil, nil
	}
	byField[field] = fr
	return fr, nil
}

// avgdlFor returns the index-wide average length of a field in tokens, or 0 when
// the field has no documents (which disables length normalization).
func (se *Searcher) avgdlFor(field string) (float64, error) {
	if v, ok := se.avgdlCache[field]; ok {
		return v, nil
	}
	st, err := segment.LoadFieldStats(se.kv, field)
	if err != nil {
		return 0, err
	}
	var avg float64
	if st.DocCount > 0 {
		avg = float64(st.SumTotalTermFreq) / float64(st.DocCount)
	}
	se.avgdlCache[field] = avg
	return avg, nil
}

// docFreqFor returns the number of documents containing term in field across
// every segment, the n used in IDF.
func (se *Searcher) docFreqFor(field, term string) (int64, error) {
	key := field + "\x00" + term
	if v, ok := se.docFreqCache[key]; ok {
		return v, nil
	}
	var df int64
	for _, seg := range se.segs {
		fr, err := se.fieldReader(seg, field)
		if err != nil {
			return 0, err
		}
		if fr == nil {
			continue
		}
		r, ok, err := fr.Postings(term)
		if err != nil {
			return 0, err
		}
		if ok {
			df += int64(r.Count())
		}
	}
	se.docFreqCache[key] = df
	return df, nil
}

// weightFor builds the BM25 weight for a scored term, using the global statistics.
func (se *Searcher) weightFor(field, term string, boost float32) (*score.Weight, error) {
	n, err := se.docFreqFor(field, term)
	if err != nil {
		return nil, err
	}
	avgdl, err := se.avgdlFor(field)
	if err != nil {
		return nil, err
	}
	return score.NewWeight(n, se.n, avgdl, float64(boost), se.k1, se.b), nil
}

// schemaView adapts the index schema to the query.Schema interface.
type schemaView struct{ s *schema.Schema }

func (v schemaView) FieldType(name string) (string, bool) {
	if v.s == nil {
		return "", true // no schema available: skip field-existence checks
	}
	f, ok := v.s.Lookup(name)
	if !ok {
		return "", false
	}
	return string(f.Type), true
}
