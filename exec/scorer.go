package exec

import (
	"math"
	"sort"

	"github.com/tamnd/search/postings"
	"github.com/tamnd/search/score"
	"github.com/tamnd/search/segment"
)

// noMore is the exhausted-iterator sentinel, shared with the postings layer.
const noMore = postings.NoMore

// scorer iterates the documents that match one (sub)query within a single
// segment, in ascending doc-id order, and scores the current document (spec 2063
// doc 12). All doc-ids are the global internal doc-ids, so results from different
// segments share one coordinate space and merge without remapping.
type scorer interface {
	// docID returns the current doc-id, or noMore once exhausted. Before the
	// first next/advance it returns 0xFFFFFFFF-1 conceptually; callers always
	// call next or advance first.
	docID() uint32
	// next advances to the next matching doc-id and returns it (or noMore).
	next() (uint32, error)
	// advance moves to the first matching doc-id >= target and returns it.
	advance(target uint32) (uint32, error)
	// score returns the score of the current document.
	score() float32
	// cost is an estimate of how many documents the scorer will visit, used to
	// order conjunction leads cheapest-first.
	cost() int
}

// maxScorer is a scorer that can bound its own score from above, the prerequisite
// for WAND pruning. A term scorer and a chain of term scorers both qualify.
type maxScorer interface {
	scorer
	// maxScore is the largest score the scorer can ever produce for a document.
	maxScore() float32
}

// pruner is a scorer that can be told the smallest score still worth producing,
// so it may skip documents that cannot beat the current top-k threshold. WAND
// implements it; the wrappers forward the threshold so it survives a liveFilter
// or boost in front of the disjunction.
type pruner interface {
	setMinScore(min float32)
}

// termScorer scores one term's postings in one segment field with a BM25 weight.
// maxFreq and minNorm are the term's WAND bound in this segment (the largest term
// frequency and shortest length observed), so maxScore is the largest BM25 score
// the term can contribute to any document in the segment.
type termScorer struct {
	r       *postings.Reader
	w       *score.Weight
	fr      *segment.FieldReader
	maxFreq uint32
	minNorm byte
	cur     uint32
	curFreq uint32
}

func newTermScorer(r *postings.Reader, w *score.Weight, fr *segment.FieldReader, maxFreq uint32, minNorm byte) *termScorer {
	return &termScorer{r: r, w: w, fr: fr, maxFreq: maxFreq, minNorm: minNorm}
}

func (t *termScorer) docID() uint32 { return t.cur }

// maxScore is the term's upper-bound BM25 contribution in this segment, used by
// WAND to skip documents that cannot enter the top-k.
func (t *termScorer) maxScore() float32 { return t.w.MaxScore(t.maxFreq, t.minNorm) }

func (t *termScorer) next() (uint32, error) {
	doc, freq, ok, err := t.r.Next()
	if err != nil {
		return 0, err
	}
	if !ok {
		t.cur = noMore
		return noMore, nil
	}
	t.cur, t.curFreq = doc, freq
	return doc, nil
}

func (t *termScorer) advance(target uint32) (uint32, error) {
	doc, freq, ok, err := t.r.SkipTo(target)
	if err != nil {
		return 0, err
	}
	if !ok {
		t.cur = noMore
		return noMore, nil
	}
	t.cur, t.curFreq = doc, freq
	return doc, nil
}

func (t *termScorer) score() float32 { return t.w.Score(t.curFreq, t.fr.Norm(t.cur)) }
func (t *termScorer) cost() int      { return t.r.Count() }

// chainScorer concatenates several sub-scorers whose doc-id ranges are ascending
// and disjoint (one per segment, in segment-id order). It presents them as a
// single ascending stream so the whole query is planned and run once over the
// global doc-id space, and the multi-segment merge falls out for free. score
// delegates to the active sub so each segment's own norms are used.
type chainScorer struct {
	subs []scorer
	idx  int
	cur  uint32
}

func newChainScorer(subs []scorer) *chainScorer {
	return &chainScorer{subs: subs}
}

func (c *chainScorer) docID() uint32 { return c.cur }

func (c *chainScorer) next() (uint32, error) {
	for c.idx < len(c.subs) {
		d, err := c.subs[c.idx].next()
		if err != nil {
			return 0, err
		}
		if d != noMore {
			c.cur = d
			return d, nil
		}
		c.idx++
	}
	c.cur = noMore
	return noMore, nil
}

func (c *chainScorer) advance(target uint32) (uint32, error) {
	for c.idx < len(c.subs) {
		d, err := c.subs[c.idx].advance(target)
		if err != nil {
			return 0, err
		}
		if d != noMore {
			c.cur = d
			return d, nil
		}
		c.idx++
	}
	c.cur = noMore
	return noMore, nil
}

func (c *chainScorer) score() float32 {
	if c.idx < len(c.subs) {
		return c.subs[c.idx].score()
	}
	return 0
}

func (c *chainScorer) cost() int {
	total := 0
	for _, s := range c.subs {
		total += s.cost()
	}
	return total
}

// maxScore is the largest BM25 contribution this term can make to any document
// across its segments: the maximum of the per-segment upper bounds. A sub that
// cannot bound itself forces an unbounded result, which disables WAND pruning for
// the disjunction that holds it.
func (c *chainScorer) maxScore() float32 {
	var m float32
	for _, s := range c.subs {
		ms, ok := s.(maxScorer)
		if !ok {
			return float32(math.Inf(1))
		}
		if v := ms.maxScore(); v > m {
			m = v
		}
	}
	return m
}

// sliceScorer iterates an explicit sorted slice of doc-ids with a constant score,
// used for match_all over the set of live documents.
type sliceScorer struct {
	docs  []uint32
	value float32
	pos   int
	cur   uint32
}

func newSliceScorer(docs []uint32, value float32) *sliceScorer {
	return &sliceScorer{docs: docs, value: value, pos: -1}
}

func (s *sliceScorer) docID() uint32 { return s.cur }

func (s *sliceScorer) next() (uint32, error) {
	s.pos++
	if s.pos >= len(s.docs) {
		s.cur = noMore
		return noMore, nil
	}
	s.cur = s.docs[s.pos]
	return s.cur, nil
}

func (s *sliceScorer) advance(target uint32) (uint32, error) {
	if s.pos < 0 {
		s.pos = 0
	}
	for s.pos < len(s.docs) && s.docs[s.pos] < target {
		s.pos++
	}
	if s.pos >= len(s.docs) {
		s.cur = noMore
		return noMore, nil
	}
	s.cur = s.docs[s.pos]
	return s.cur, nil
}

func (s *sliceScorer) score() float32 { return s.value }
func (s *sliceScorer) cost() int      { return len(s.docs) }

// emptyScorer matches nothing.
type emptyScorer struct{}

func (emptyScorer) docID() uint32                  { return noMore }
func (emptyScorer) next() (uint32, error)          { return noMore, nil }
func (emptyScorer) advance(uint32) (uint32, error) { return noMore, nil }
func (emptyScorer) score() float32                 { return 0 }
func (emptyScorer) cost() int                      { return 0 }

// constantScorer wraps a matching iterator and assigns every matched document a
// fixed score, used for prefix, range, and filter clauses (doc 11 §3.13).
type constantScorer struct {
	inner scorer
	value float32
}

func (c *constantScorer) docID() uint32                    { return c.inner.docID() }
func (c *constantScorer) next() (uint32, error)            { return c.inner.next() }
func (c *constantScorer) advance(t uint32) (uint32, error) { return c.inner.advance(t) }
func (c *constantScorer) score() float32                   { return c.value }
func (c *constantScorer) cost() int                        { return c.inner.cost() }

// boostScorer multiplies the wrapped scorer's score by a constant factor.
type boostScorer struct {
	inner  scorer
	factor float32
}

func (b *boostScorer) docID() uint32                    { return b.inner.docID() }
func (b *boostScorer) next() (uint32, error)            { return b.inner.next() }
func (b *boostScorer) advance(t uint32) (uint32, error) { return b.inner.advance(t) }
func (b *boostScorer) score() float32                   { return b.factor * b.inner.score() }
func (b *boostScorer) cost() int                        { return b.inner.cost() }

// setMinScore forwards the threshold to the wrapped scorer in its own score space.
// The boost multiplies the inner score, so the inner threshold is the outer one
// divided by the factor.
func (b *boostScorer) setMinScore(min float32) {
	if p, ok := b.inner.(pruner); ok && b.factor > 0 {
		p.setMinScore(min / b.factor)
	}
}

// zeroScorer wraps a scorer and contributes no score (filter clauses).
type zeroScorer struct{ inner scorer }

func (z *zeroScorer) docID() uint32                    { return z.inner.docID() }
func (z *zeroScorer) next() (uint32, error)            { return z.inner.next() }
func (z *zeroScorer) advance(t uint32) (uint32, error) { return z.inner.advance(t) }
func (z *zeroScorer) score() float32                   { return 0 }
func (z *zeroScorer) cost() int                        { return z.inner.cost() }

// andScorer is a conjunction: a document matches only when every child matches.
// It leads with the cheapest child and gallops the rest to its doc-id.
type andScorer struct {
	children []scorer // sorted ascending by cost; children[0] is the lead
	cur      uint32
}

func newAndScorer(children []scorer) *andScorer {
	sort.SliceStable(children, func(i, j int) bool { return children[i].cost() < children[j].cost() })
	return &andScorer{children: children}
}

func (a *andScorer) docID() uint32 { return a.cur }

func (a *andScorer) next() (uint32, error) {
	d, err := a.children[0].next()
	if err != nil {
		return 0, err
	}
	return a.align(d)
}

func (a *andScorer) advance(target uint32) (uint32, error) {
	d, err := a.children[0].advance(target)
	if err != nil {
		return 0, err
	}
	return a.align(d)
}

// align drives every child to a common doc-id, starting from the lead's doc.
func (a *andScorer) align(lead uint32) (uint32, error) {
	for lead != noMore {
		agree := true
		for i := 1; i < len(a.children); i++ {
			d, err := a.children[i].advance(lead)
			if err != nil {
				return 0, err
			}
			if d > lead {
				// This child overshot; restart the round from the new candidate.
				nl, err := a.children[0].advance(d)
				if err != nil {
					return 0, err
				}
				lead = nl
				agree = false
				break
			}
		}
		if agree {
			a.cur = lead
			return lead, nil
		}
	}
	a.cur = noMore
	return noMore, nil
}

func (a *andScorer) score() float32 {
	var s float32
	for _, c := range a.children {
		s += c.score()
	}
	return s
}

func (a *andScorer) cost() int { return a.children[0].cost() }

// orScorer is a disjunction: a document matches when at least minShould children
// match it. Its score is the sum of the matching children's scores. It scans the
// children linearly, which is simple and adequate for S4.
type orScorer struct {
	children  []scorer
	minShould int
	cur       uint32
	started   bool
}

func newOrScorer(children []scorer, minShould int) *orScorer {
	if minShould < 1 {
		minShould = 1
	}
	return &orScorer{children: children, minShould: minShould}
}

func (o *orScorer) docID() uint32 { return o.cur }

func (o *orScorer) next() (uint32, error) {
	if !o.started {
		o.started = true
		for _, c := range o.children {
			if _, err := c.next(); err != nil {
				return 0, err
			}
		}
		return o.findFrom(0)
	}
	return o.findFrom(o.cur + 1)
}

func (o *orScorer) advance(target uint32) (uint32, error) {
	if !o.started {
		o.started = true
		for _, c := range o.children {
			if _, err := c.next(); err != nil {
				return 0, err
			}
		}
	}
	return o.findFrom(target)
}

// findFrom returns the first doc-id >= from at which minShould children match.
func (o *orScorer) findFrom(from uint32) (uint32, error) {
	for {
		// Find the smallest child doc-id that is >= from.
		cand := noMore
		for _, c := range o.children {
			d := c.docID()
			if d < from {
				var err error
				d, err = c.advance(from)
				if err != nil {
					return 0, err
				}
			}
			if d < cand {
				cand = d
			}
		}
		if cand == noMore {
			o.cur = noMore
			return noMore, nil
		}
		if o.countAt(cand) >= o.minShould {
			o.cur = cand
			return cand, nil
		}
		from = cand + 1
	}
}

// countAt counts how many children are currently positioned exactly at doc.
func (o *orScorer) countAt(doc uint32) int {
	n := 0
	for _, c := range o.children {
		if c.docID() == doc {
			n++
		}
	}
	return n
}

func (o *orScorer) score() float32 {
	var s float32
	for _, c := range o.children {
		if c.docID() == o.cur {
			s += c.score()
		}
	}
	return s
}

func (o *orScorer) cost() int {
	total := 0
	for _, c := range o.children {
		total += c.cost()
	}
	return total
}

// wandScorer is a disjunction that prunes documents which cannot enter the top-k,
// the WAND algorithm (doc 13 §1.6). Each child reports an upper bound on the score
// it can contribute (maxScore). The collector pushes the current k-th best score
// down through setMinScore; WAND sorts its children by doc-id, accumulates their
// upper bounds, and finds the pivot, the first doc-id whose running bound sum can
// beat the threshold. Doc-ids below the pivot cannot make the top-k, so the lagging
// children skip straight to the pivot rather than visiting every document between.
// With a zero threshold (the heap is not yet full) it degrades to a full
// disjunction, so early results are exact. minShould is fixed at one; WAND is used
// only where a single matching clause suffices.
type wandScorer struct {
	children []maxScorer
	cur      uint32
	minScore float32
	started  bool
}

// newWandScorer builds a WAND disjunction over children. Every child must be able
// to bound its score; if any cannot, it falls back to a plain disjunction so the
// result stays correct (a missing bound would over-prune).
func newWandScorer(children []scorer) scorer {
	ms := make([]maxScorer, 0, len(children))
	for _, c := range children {
		m, ok := c.(maxScorer)
		if !ok {
			return newOrScorer(children, 1)
		}
		if math.IsInf(float64(m.maxScore()), 1) {
			return newOrScorer(children, 1)
		}
		ms = append(ms, m)
	}
	return &wandScorer{children: ms}
}

func (w *wandScorer) docID() uint32 { return w.cur }

func (w *wandScorer) setMinScore(min float32) { w.minScore = min }

func (w *wandScorer) next() (uint32, error) {
	if !w.started {
		w.started = true
		for _, c := range w.children {
			if _, err := c.next(); err != nil {
				return 0, err
			}
		}
		return w.findCandidate()
	}
	for _, c := range w.children {
		if c.docID() == w.cur {
			if _, err := c.next(); err != nil {
				return 0, err
			}
		}
	}
	return w.findCandidate()
}

func (w *wandScorer) advance(target uint32) (uint32, error) {
	if !w.started {
		w.started = true
		for _, c := range w.children {
			if _, err := c.next(); err != nil {
				return 0, err
			}
		}
	}
	for _, c := range w.children {
		if c.docID() < target {
			if _, err := c.advance(target); err != nil {
				return 0, err
			}
		}
	}
	return w.findCandidate()
}

// findCandidate returns the next doc-id that could enter the top-k, advancing the
// children as WAND prescribes.
func (w *wandScorer) findCandidate() (uint32, error) {
	for {
		sort.SliceStable(w.children, func(i, j int) bool {
			return w.children[i].docID() < w.children[j].docID()
		})
		var sum float32
		pivot := -1
		for i, c := range w.children {
			if c.docID() == noMore {
				break
			}
			sum += c.maxScore()
			if sum > w.minScore {
				pivot = i
				break
			}
		}
		if pivot < 0 {
			w.cur = noMore
			return noMore, nil
		}
		pivotDoc := w.children[pivot].docID()
		if pivotDoc == noMore {
			w.cur = noMore
			return noMore, nil
		}
		if w.children[0].docID() == pivotDoc {
			w.cur = pivotDoc
			return pivotDoc, nil
		}
		// A child before the pivot lags behind; skip it up to the pivot doc-id so
		// the next round can either form a candidate there or move the pivot on.
		for i := 0; i < pivot; i++ {
			if w.children[i].docID() < pivotDoc {
				if _, err := w.children[i].advance(pivotDoc); err != nil {
					return 0, err
				}
				break
			}
		}
	}
}

func (w *wandScorer) score() float32 {
	var s float32
	for _, c := range w.children {
		if c.docID() == w.cur {
			s += c.score()
		}
	}
	return s
}

func (w *wandScorer) cost() int {
	total := 0
	for _, c := range w.children {
		total += c.cost()
	}
	return total
}

// liveFilter wraps a scorer and drops any matched doc-id that is present in a
// sorted set of deleted doc-ids. Deletes are soft: a deleted document keeps its
// postings in an immutable segment until compaction, so the matched stream is
// filtered here. The dead cursor only moves forward because the wrapped scorer
// yields ascending doc-ids.
type liveFilter struct {
	inner scorer
	dead  []uint32
	di    int
}

func newLiveFilter(inner scorer, dead []uint32) scorer {
	if len(dead) == 0 {
		return inner
	}
	return &liveFilter{inner: inner, dead: dead}
}

// skipDead advances the wrapped scorer past any deleted doc-id, returning the
// first live doc-id at or after d.
func (f *liveFilter) skipDead(d uint32) (uint32, error) {
	for d != noMore {
		for f.di < len(f.dead) && f.dead[f.di] < d {
			f.di++
		}
		if f.di < len(f.dead) && f.dead[f.di] == d {
			var err error
			d, err = f.inner.next()
			if err != nil {
				return 0, err
			}
			continue
		}
		return d, nil
	}
	return noMore, nil
}

func (f *liveFilter) docID() uint32 { return f.inner.docID() }

func (f *liveFilter) next() (uint32, error) {
	d, err := f.inner.next()
	if err != nil {
		return 0, err
	}
	return f.skipDead(d)
}

func (f *liveFilter) advance(target uint32) (uint32, error) {
	d, err := f.inner.advance(target)
	if err != nil {
		return 0, err
	}
	return f.skipDead(d)
}

func (f *liveFilter) score() float32 { return f.inner.score() }
func (f *liveFilter) cost() int      { return f.inner.cost() }

// setMinScore forwards the top-k threshold to the wrapped scorer so deletion
// filtering does not disable WAND pruning underneath it.
func (f *liveFilter) setMinScore(min float32) {
	if p, ok := f.inner.(pruner); ok {
		p.setMinScore(min)
	}
}

// scoreAt advances every child to >= target and returns the summed score and the
// number of children positioned exactly at target. It is used by a bool query to
// fold optional should clauses onto a required lead's documents.
func (o *orScorer) scoreAt(target uint32) (float32, int, error) {
	if !o.started {
		o.started = true
		for _, c := range o.children {
			if _, err := c.next(); err != nil {
				return 0, 0, err
			}
		}
	}
	var s float32
	n := 0
	for _, c := range o.children {
		d := c.docID()
		if d < target {
			var err error
			d, err = c.advance(target)
			if err != nil {
				return 0, 0, err
			}
		}
		if d == target {
			s += c.score()
			n++
		}
	}
	return s, n, nil
}
