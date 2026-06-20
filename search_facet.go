package search

import (
	"fmt"
	"sort"

	"github.com/tamnd/search/agg"
	"github.com/tamnd/search/analysis"
	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/collect"
	"github.com/tamnd/search/docstore"
	"github.com/tamnd/search/exec"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/segment"
)

// SortKey is one level of a sort specification (spec 2063 doc 14 §6). An empty
// Field (or "_score") sorts by relevance score. Desc reverses the order. Mode
// reduces a multi-valued numeric field to one value (min, max, avg, sum, median);
// the default is min ascending, max descending. MissingLast places documents
// without a value after those with one. Origin, when set on a geo_point field,
// sorts by great-circle distance to that point.
type SortKey struct {
	Field       string
	Desc        bool
	Mode        string
	MissingLast bool
	Origin      *GeoPoint
}

// GeoPoint is a latitude/longitude pair used as a geo-distance sort origin.
type GeoPoint struct {
	Lat float64
	Lon float64
}

// AggSpec describes one aggregation in a search request (doc 14 §7). Kind is one
// of terms, histogram, range, min, max, sum, avg, count, stats, cardinality, or
// percentiles. The remaining fields apply to the kinds that use them.
type AggSpec struct {
	Kind     string
	Field    string
	Size     int                // terms: max buckets
	ByKey    bool               // terms: order by key instead of count
	Interval float64            // histogram: bucket width
	Offset   float64            // histogram: bucket baseline
	Ranges   []agg.NumRange     // range
	Percents []float64          // percentiles
	Sub      map[string]AggSpec // terms: nested aggregations
}

// SearchRequest is a search with optional sort, aggregations, and field
// collapse. Query and K are required; the rest are optional.
type SearchRequest struct {
	Query    query.Query
	K        int
	Sort     []SortKey
	Aggs     map[string]AggSpec
	Collapse string // keyword field to collapse results on
}

// SearchResult carries the hits and the aggregation results of a SearchRequest.
type SearchResult struct {
	Hits []Hit
	Aggs map[string]agg.Result
}

// SearchRequestExec runs a full search request: matching, optional sort or
// collapse, and single-pass aggregations (doc 14). When the request asks for
// neither sort, aggregations, nor collapse, it falls back to the score-ranked
// top-k path.
func (db *DB) SearchRequestExec(req SearchRequest) (SearchResult, error) {
	var res SearchResult
	err := db.View(func(t *Txn) error {
		r, err := db.searchRequestTxn(t, req)
		if err != nil {
			return err
		}
		res = r
		return nil
	})
	return res, err
}

func (db *DB) searchRequestTxn(t *Txn, req SearchRequest) (SearchResult, error) {
	c := t.Catalog()
	s, ok, err := loadSchema(c)
	if err != nil {
		return SearchResult{}, err
	}
	if !ok {
		return SearchResult{}, ErrNoSchema
	}
	set, err := segment.LoadSet(c)
	if err != nil {
		return SearchResult{}, err
	}
	live, err := liveDocIDs(c)
	if err != nil {
		return SearchResult{}, err
	}
	dead, err := set.DeletedDocIDs(c)
	if err != nil {
		return SearchResult{}, err
	}
	analyzer := func(field string) (*analysis.Analyzer, error) {
		name := "standard"
		if f, ok := s.Lookup(field); ok {
			name = fieldAnalyzerName(f)
		}
		return resolveAnalyzer(c, name)
	}
	se := exec.New(c, set, s, analyzer, live, dead)
	store := docstore.New(c, catalog.NSDocStore)
	pk := s.PrimaryKey()

	// Fast path: no sort, aggs, or collapse means a plain score-ranked top-k.
	if len(req.Sort) == 0 && len(req.Aggs) == 0 && req.Collapse == "" {
		scored, err := se.Search(req.Query, req.K)
		if err != nil {
			return SearchResult{}, err
		}
		hits, err := resolveHits(store, pk, scored2hits(scored))
		if err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Hits: hits}, nil
	}

	// Build the aggregation accumulators.
	aggs := make(map[string]agg.Agg, len(req.Aggs))
	for name, spec := range req.Aggs {
		a, err := buildAgg(c, set, s, spec)
		if err != nil {
			return SearchResult{}, err
		}
		aggs[name] = a
	}

	// Build the sort-key extractors. With no explicit sort, rank by score
	// descending so aggregation-only and collapse-only requests still order hits.
	keys := req.Sort
	if len(keys) == 0 {
		keys = []SortKey{{Field: "_score", Desc: true}}
	}
	extractors, descs, missingLasts, err := buildSortExtractors(c, set, s, keys)
	if err != nil {
		return SearchResult{}, err
	}

	// Collapse field column, if requested.
	var collapseCols *fieldCols
	if req.Collapse != "" {
		collapseCols, err = openFieldCols(c, set, req.Collapse)
		if err != nil {
			return SearchResult{}, err
		}
	}

	cmp := comparator{descs: descs, missingLasts: missingLasts}
	var cands []candidate
	byGroup := make(map[string]int) // collapse key -> index into cands
	var soloSeq int

	visit := func(docID uint32, score float32) error {
		for _, a := range aggs {
			a.Collect(docID)
		}
		cand := candidate{docID: docID, score: score, keys: make([]sortVal, len(extractors))}
		for i, ex := range extractors {
			cand.keys[i] = ex(docID, score)
		}
		if collapseCols == nil {
			cands = append(cands, cand)
			return nil
		}
		gk, ok := collapseGroupKey(collapseCols, docID, &soloSeq)
		if !ok {
			cands = append(cands, cand)
			return nil
		}
		if idx, seen := byGroup[gk]; seen {
			if cmp.less(cand, cands[idx]) {
				cands[idx] = cand
			}
			return nil
		}
		byGroup[gk] = len(cands)
		cands = append(cands, cand)
		return nil
	}
	if err := se.Scan(req.Query, visit); err != nil {
		return SearchResult{}, err
	}

	sort.SliceStable(cands, func(i, j int) bool { return cmp.less(cands[i], cands[j]) })
	if req.K >= 0 && len(cands) > req.K {
		cands = cands[:req.K]
	}

	scored := make([]scoredDoc, len(cands))
	for i, cd := range cands {
		scored[i] = scoredDoc{docID: cd.docID, score: cd.score}
	}
	hits, err := resolveHits(store, pk, scored)
	if err != nil {
		return SearchResult{}, err
	}

	res := SearchResult{Hits: hits}
	if len(aggs) > 0 {
		res.Aggs = make(map[string]agg.Result, len(aggs))
		for name, a := range aggs {
			res.Aggs[name] = a.Result()
		}
	}
	return res, nil
}

// scoredDoc is a doc-id and its score, used to resolve stored bodies.
type scoredDoc struct {
	docID uint32
	score float32
}

func scored2hits(scored []collect.Hit) []scoredDoc {
	out := make([]scoredDoc, len(scored))
	for i, h := range scored {
		out[i] = scoredDoc{docID: h.DocID, score: h.Score}
	}
	return out
}

// resolveHits loads the stored body for each scored doc, skipping any whose body
// is gone (deleted between scan and fetch).
func resolveHits(store *docstore.Store, pk string, scored []scoredDoc) ([]Hit, error) {
	hits := make([]Hit, 0, len(scored))
	for _, h := range scored {
		doc, ok, err := store.Get(uint64(h.docID))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		hits = append(hits, Hit{
			DocID:      uint64(h.docID),
			ExternalID: externalID(doc, pk),
			Score:      h.score,
			Document:   doc,
		})
	}
	return hits, nil
}

// collapseGroupKey returns the collapse group key for a doc: the keyword value of
// the collapse field, or a unique synthetic key when the field has no value (so
// each value-less doc forms its own group, per doc 14 §10.2).
func collapseGroupKey(fc *fieldCols, docID uint32, soloSeq *int) (string, bool) {
	if kb, ok := fc.singleKey(docID); ok {
		return "v:" + string(kb), true
	}
	*soloSeq++
	return fmt.Sprintf("solo:%d", *soloSeq), true
}
