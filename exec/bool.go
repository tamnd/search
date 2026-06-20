package exec

// boolScorer evaluates a boolean query within the global doc-id stream (spec 2063
// doc 11 §3.17, doc 12 §3). It leads with the required conjunction when one
// exists, otherwise with the should disjunction. Every candidate is checked
// against the prohibited clauses, the should clauses are folded in for score and
// the minimum-should-match count, and the surviving document's score is the sum
// of the required and matching should contributions.
type boolScorer struct {
	required   scorer    // must + filter, conjoined; nil when there are none
	shoulds    *orScorer // should clauses; nil when there are none
	prohibited []scorer  // must_not clauses
	minShould  int       // required should matches when required != nil
	cur        uint32
	curScore   float32
}

func (b *boolScorer) docID() uint32 { return b.cur }

func (b *boolScorer) lead() scorer {
	if b.required != nil {
		return b.required
	}
	return b.shoulds
}

func (b *boolScorer) next() (uint32, error) {
	return b.find(func() (uint32, error) { return b.lead().next() })
}

func (b *boolScorer) advance(target uint32) (uint32, error) {
	return b.find(func() (uint32, error) { return b.lead().advance(target) })
}

func (b *boolScorer) find(step func() (uint32, error)) (uint32, error) {
	for {
		d, err := step()
		if err != nil {
			return 0, err
		}
		if d == noMore {
			b.cur = noMore
			return noMore, nil
		}
		ex, err := b.excluded(d)
		if err != nil {
			return 0, err
		}
		if ex {
			continue
		}
		if b.required != nil {
			var ss float32
			var sc int
			if b.shoulds != nil {
				ss, sc, err = b.shoulds.scoreAt(d)
				if err != nil {
					return 0, err
				}
			}
			if sc < b.minShould {
				continue
			}
			b.curScore = b.required.score() + ss
		} else {
			// The should disjunction is the lead and already enforced its own
			// minimum-should-match, so its score at the current doc is final.
			b.curScore = b.shoulds.score()
		}
		b.cur = d
		return d, nil
	}
}

// excluded reports whether any must_not clause matches doc. advance never
// rewinds, and candidates arrive in ascending order, so advancing each
// prohibited scorer to doc is monotonic and safe.
func (b *boolScorer) excluded(doc uint32) (bool, error) {
	for _, p := range b.prohibited {
		pd, err := p.advance(doc)
		if err != nil {
			return false, err
		}
		if pd == doc {
			return true, nil
		}
	}
	return false, nil
}

func (b *boolScorer) score() float32 { return b.curScore }
func (b *boolScorer) cost() int      { return b.lead().cost() }
