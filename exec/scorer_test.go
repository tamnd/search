package exec

import (
	"testing"

	"github.com/tamnd/search/collect"
)

// fakeTerm is a term scorer over an explicit sorted doc-id list with a constant
// per-document contribution. Because the contribution is constant, its maxScore
// equals what it actually adds to any document it holds, so WAND over a set of
// fake terms is lossless and must return exactly what a plain disjunction does.
// It counts the iterator moves it is asked to make so a test can show WAND moves
// less than a full scan.
type fakeTerm struct {
	docs   []uint32
	weight float32
	pos    int
	cur    uint32
	moves  int
}

func newFakeTerm(docs []uint32, weight float32) *fakeTerm {
	return &fakeTerm{docs: docs, weight: weight, pos: -1}
}

func (f *fakeTerm) docID() uint32 { return f.cur }

func (f *fakeTerm) next() (uint32, error) {
	f.moves++
	f.pos++
	if f.pos >= len(f.docs) {
		f.cur = noMore
		return noMore, nil
	}
	f.cur = f.docs[f.pos]
	return f.cur, nil
}

func (f *fakeTerm) advance(target uint32) (uint32, error) {
	f.moves++
	if f.pos < 0 {
		f.pos = 0
	}
	for f.pos < len(f.docs) && f.docs[f.pos] < target {
		f.pos++
	}
	if f.pos >= len(f.docs) {
		f.cur = noMore
		return noMore, nil
	}
	f.cur = f.docs[f.pos]
	return f.cur, nil
}

func (f *fakeTerm) score() float32 {
	if f.cur == noMore {
		return 0
	}
	return f.weight
}

func (f *fakeTerm) cost() int         { return len(f.docs) }
func (f *fakeTerm) maxScore() float32 { return f.weight }

// drain runs a scorer through a top-k collector, pushing the threshold back into
// the scorer after each admission when it can prune.
func drain(t *testing.T, sc scorer, k int) []collect.Hit {
	t.Helper()
	c := collect.NewTopK(k)
	prune, prunes := sc.(pruner)
	d, err := sc.next()
	if err != nil {
		t.Fatal(err)
	}
	for d != noMore {
		c.Collect(d, sc.score())
		if prunes {
			prune.setMinScore(c.Threshold())
		}
		var err error
		d, err = sc.next()
		if err != nil {
			t.Fatal(err)
		}
	}
	return c.Results()
}

// terms builds matched, equivalent term scorers for both scorers under test.
func terms() ([]scorer, []scorer) {
	specs := []struct {
		docs   []uint32
		weight float32
	}{
		{[]uint32{1, 4, 7, 10, 13, 16, 19, 22, 25, 28}, 1.0}, // common, low weight
		{[]uint32{2, 4, 9, 16, 25}, 3.0},                     // rarer, higher weight
		{[]uint32{4, 8, 16, 32}, 5.0},                        // rarest, highest weight
	}
	wand := make([]scorer, len(specs))
	or := make([]scorer, len(specs))
	for i, s := range specs {
		wand[i] = newFakeTerm(s.docs, s.weight)
		or[i] = newFakeTerm(s.docs, s.weight)
	}
	return wand, or
}

func TestWandEqualsDisjunction(t *testing.T) {
	for _, k := range []int{1, 2, 3, 5, 100} {
		wandSubs, orSubs := terms()
		gotWand := drain(t, newWandScorer(wandSubs), k)
		gotOr := drain(t, newOrScorer(orSubs, 1), k)
		if len(gotWand) != len(gotOr) {
			t.Fatalf("k=%d: WAND returned %d hits, disjunction %d", k, len(gotWand), len(gotOr))
		}
		for i := range gotWand {
			if gotWand[i].DocID != gotOr[i].DocID {
				t.Fatalf("k=%d rank %d: WAND doc %d, disjunction doc %d", k, i, gotWand[i].DocID, gotOr[i].DocID)
			}
			if d := gotWand[i].Score - gotOr[i].Score; d > 1e-6 || d < -1e-6 {
				t.Fatalf("k=%d rank %d doc %d: WAND score %v, disjunction %v", k, i, gotWand[i].DocID, gotWand[i].Score, gotOr[i].Score)
			}
		}
	}
}

func TestWandSkipsDocuments(t *testing.T) {
	// With a small k the heap fills early and WAND should stop visiting the low
	// weight common term once no future document can beat the threshold, so its
	// total iterator moves stay below the full posting count.
	wandSubs, _ := terms()
	_ = drain(t, newWandScorer(wandSubs), 2)

	var common *fakeTerm
	for _, s := range wandSubs {
		ft := s.(*fakeTerm)
		if len(ft.docs) == 10 {
			common = ft
		}
	}
	if common == nil {
		t.Fatal("could not find the common term")
	}
	// A plain disjunction visits every one of the ten postings plus the trailing
	// exhausted read: eleven moves. WAND must do fewer.
	if common.moves >= len(common.docs)+1 {
		t.Fatalf("WAND visited the common term %d times, no pruning happened", common.moves)
	}
}

// TestBoolAdvanceSkipsExcluded is a regression test for an infinite loop in the
// should-led boolScorer. When advance(target) landed on a document that a
// must_not clause excluded, find() used to re-issue the same advance(target),
// which returns the same already-excluded document forever. find() must instead
// step forward with next() after a rejection.
func TestBoolAdvanceSkipsExcluded(t *testing.T) {
	should := newOrScorer([]scorer{newFakeTerm([]uint32{2, 4, 6}, 1.0)}, 1)
	prohibited := newFakeTerm([]uint32{2, 4}, 1.0)
	b := &boolScorer{shoulds: should, prohibited: []scorer{prohibited}}

	// 2 and 4 are excluded, so advancing to 2 must surface 6.
	d, err := b.advance(2)
	if err != nil {
		t.Fatal(err)
	}
	if d != 6 {
		t.Fatalf("advance(2) = %d, want 6 (2 and 4 are excluded)", d)
	}
	d, err = b.next()
	if err != nil {
		t.Fatal(err)
	}
	if d != noMore {
		t.Fatalf("next() after last match = %d, want noMore", d)
	}
}
