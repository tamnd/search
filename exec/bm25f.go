package exec

import (
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/score"
	"github.com/tamnd/search/segment"
)

// compileBM25F builds a multi-field BM25F scorer (doc 13 §4). It scores each
// segment with its own per-field norms and chains the segments into one stream.
// The terms are matched as indexed; they are not re-analyzed.
func (se *Searcher) compileBM25F(n *query.BM25FQuery) (scorer, error) {
	k1 := n.K1
	if k1 <= 0 {
		k1 = float32(se.k1)
	}
	// IDF per term, computed once from the global statistics. The document
	// frequency is the largest across the participating fields, a lower bound on
	// the cross-field union that keeps a common term from being over-weighted.
	idf := make([]float32, len(n.Terms))
	for i, t := range n.Terms {
		var df int64
		for _, f := range n.Fields {
			d, err := se.docFreqFor(f.Name, t)
			if err != nil {
				return nil, err
			}
			if d > df {
				df = d
			}
		}
		idf[i] = float32(score.IDF(df, se.n))
	}
	avgdl := make([]float32, len(n.Fields))
	for j, f := range n.Fields {
		a, err := se.avgdlFor(f.Name)
		if err != nil {
			return nil, err
		}
		avgdl[j] = float32(a)
	}

	var segScorers []scorer
	for _, seg := range se.segs {
		bs, err := se.bm25fSegment(seg, n, k1, idf, avgdl)
		if err != nil {
			return nil, err
		}
		if bs != nil {
			segScorers = append(segScorers, bs)
		}
	}
	switch len(segScorers) {
	case 0:
		return emptyScorer{}, nil
	case 1:
		return applyBoost(segScorers[0], n.Boost()), nil
	default:
		return applyBoost(newChainScorer(segScorers), n.Boost()), nil
	}
}

// bm25fSegment builds the BM25F scorer for one segment, or nil when no
// participating field holds any of the terms.
func (se *Searcher) bm25fSegment(seg *segment.Segment, n *query.BM25FQuery, k1 float32, idf, avgdl []float32) (scorer, error) {
	terms := make([]bm25fTerm, len(n.Terms))
	var leaves []scorer
	any := false
	for ti, t := range n.Terms {
		terms[ti] = bm25fTerm{idf: idf[ti], k1: k1}
		for fi, f := range n.Fields {
			fr, err := se.fieldReader(seg, f.Name)
			if err != nil {
				return nil, err
			}
			if fr == nil {
				continue
			}
			r, ok, err := fr.Postings(t)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			b := f.B
			if b < 0 {
				b = float32(se.b)
			}
			boost := f.Boost
			if boost == 0 {
				boost = 1
			}
			ts := newTermScorer(r, dummyWeight, fr, 0, 0)
			terms[ti].leaves = append(terms[ti].leaves, bm25fLeaf{ts: ts, boost: boost, b: b, avgdl: avgdl[fi]})
			leaves = append(leaves, ts)
			any = true
		}
	}
	if !any {
		return nil, nil
	}
	return &bm25fScorer{or: newOrScorer(leaves, 1), terms: terms}, nil
}

// bm25fLeaf is one (term, field) posting within a segment along with that field's
// BM25F parameters.
type bm25fLeaf struct {
	ts    *termScorer
	boost float32
	b     float32
	avgdl float32
}

// bm25fTerm groups all of a term's per-field leaves with the term's IDF.
type bm25fTerm struct {
	idf    float32
	k1     float32
	leaves []bm25fLeaf
}

// bm25fScorer scores one segment with BM25F. It is driven by a disjunction over
// every (term, field) leaf, so it visits exactly the documents that contain at
// least one term in one field. The per-doc score combines per-field evidence into
// a pseudo term frequency before the saturation, the defining property of BM25F.
type bm25fScorer struct {
	or    *orScorer
	terms []bm25fTerm
	cur   uint32
}

func (s *bm25fScorer) docID() uint32 { return s.cur }

func (s *bm25fScorer) next() (uint32, error) {
	d, err := s.or.next()
	s.cur = d
	return d, err
}

func (s *bm25fScorer) advance(t uint32) (uint32, error) {
	d, err := s.or.advance(t)
	s.cur = d
	return d, err
}

func (s *bm25fScorer) score() float32 {
	doc := s.cur
	var total float32
	for _, t := range s.terms {
		var pseudo float32
		for _, lf := range t.leaves {
			if lf.ts.docID() != doc {
				continue
			}
			freq := float32(lf.ts.curFreq)
			norm := float32(1)
			if lf.avgdl > 0 {
				dl := float32(score.NormLength(lf.ts.fr.Norm(doc)))
				norm = 1 - lf.b + lf.b*dl/lf.avgdl
			}
			if norm > 0 {
				pseudo += lf.boost * freq / norm
			}
		}
		if pseudo > 0 {
			total += t.idf * pseudo / (t.k1 + pseudo)
		}
	}
	return total
}

func (s *bm25fScorer) cost() int { return s.or.cost() }
