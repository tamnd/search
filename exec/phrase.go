package exec

// segPhraseScorer matches a phrase within one segment: the analyzed terms must
// occur in order, within slop positions of one another (spec 2063 doc 11 §3.6).
// It conjoins the per-term positional scorers to find candidate documents, then
// verifies the positional constraint before emitting. The score at a matching
// document is the sum of the constituent term scores.
//
// Slop semantics: with slop 0 the terms must be exactly adjacent and in order.
// With a positive slop a document matches when there is an in-order choice of one
// position per term, q0 < q1 < ... < qk, whose total span minus the term count,
// (qk - q0) - k, is at most slop. This is exact for adjacency and a faithful
// in-order reading of slop; transposition scoring is left to a later milestone.
type segPhraseScorer struct {
	terms []*termScorer
	and   *andScorer
	slop  int
	cur   uint32
}

func newSegPhraseScorer(terms []*termScorer, slop int) *segPhraseScorer {
	subs := make([]scorer, len(terms))
	for i, t := range terms {
		subs[i] = t
	}
	return &segPhraseScorer{terms: terms, and: newAndScorer(subs), slop: slop}
}

func (p *segPhraseScorer) docID() uint32 { return p.cur }

func (p *segPhraseScorer) next() (uint32, error) {
	d, err := p.and.next()
	return p.find(d, err)
}

func (p *segPhraseScorer) advance(target uint32) (uint32, error) {
	d, err := p.and.advance(target)
	return p.find(d, err)
}

// find filters the conjunction's candidates down to documents whose positions
// satisfy the phrase. A rejected candidate is followed with the conjunction's
// next(), not a repeat advance(target): once the conjunction has passed the
// target, re-advancing returns the same document and would spin.
func (p *segPhraseScorer) find(d uint32, err error) (uint32, error) {
	for {
		if err != nil {
			return 0, err
		}
		if d == noMore {
			p.cur = noMore
			return noMore, nil
		}
		ok, mErr := p.matches()
		if mErr != nil {
			return 0, mErr
		}
		if ok {
			p.cur = d
			return d, nil
		}
		d, err = p.and.next()
	}
}

// matches checks the positional constraint at the conjunction's current doc.
func (p *segPhraseScorer) matches() (bool, error) {
	posLists := make([][]uint32, len(p.terms))
	for i, t := range p.terms {
		pos, err := t.r.Positions()
		if err != nil {
			return false, err
		}
		posLists[i] = pos
	}
	return phraseInOrder(posLists, p.slop), nil
}

func (p *segPhraseScorer) score() float32 {
	var s float32
	for _, t := range p.terms {
		s += t.score()
	}
	return s
}

func (p *segPhraseScorer) cost() int { return p.and.cost() }

// phraseInOrder reports whether positions admit an in-order phrase match within
// slop. posLists[i] holds the (ascending) positions of the i-th query term.
func phraseInOrder(posLists [][]uint32, slop int) bool {
	if len(posLists) == 0 {
		return false
	}
	if len(posLists) == 1 {
		return len(posLists[0]) > 0
	}
	for _, pl := range posLists {
		if len(pl) == 0 {
			return false
		}
	}
	k := len(posLists) - 1
	// For each starting position of the first term, greedily pick the smallest
	// strictly increasing position for each later term; that minimizes the span.
	for _, q0 := range posLists[0] {
		prev := q0
		idx := make([]int, len(posLists))
		ok := true
		for i := 1; i < len(posLists); i++ {
			j := idx[i]
			for j < len(posLists[i]) && posLists[i][j] <= prev {
				j++
			}
			if j >= len(posLists[i]) {
				ok = false
				break
			}
			idx[i] = j
			prev = posLists[i][j]
		}
		if !ok {
			continue
		}
		span := int(prev) - int(q0)
		if span-k <= slop {
			return true
		}
	}
	return false
}
