package collect

import (
	"math/rand/v2"
	"sort"
	"testing"
)

func TestTopKKeepsBest(t *testing.T) {
	c := NewTopK(3)
	in := []Hit{{1, 0.5}, {2, 0.9}, {3, 0.1}, {4, 0.7}, {5, 0.3}}
	for _, h := range in {
		c.Collect(h.DocID, h.Score)
	}
	got := c.Results()
	if len(got) != 3 {
		t.Fatalf("kept %d, want 3", len(got))
	}
	want := []uint32{2, 4, 1}
	for i, h := range got {
		if h.DocID != want[i] {
			t.Fatalf("result %d = doc %d, want %d (results %v)", i, h.DocID, want[i], got)
		}
	}
}

func TestTopKThreshold(t *testing.T) {
	c := NewTopK(2)
	if c.Threshold() != 0 {
		t.Fatalf("empty threshold = %v, want 0", c.Threshold())
	}
	c.Collect(1, 0.5)
	if c.Threshold() != 0 {
		t.Fatalf("partial threshold = %v, want 0", c.Threshold())
	}
	c.Collect(2, 0.9)
	if c.Threshold() != 0.5 {
		t.Fatalf("full threshold = %v, want 0.5", c.Threshold())
	}
	c.Collect(3, 0.7)
	if c.Threshold() != 0.7 {
		t.Fatalf("threshold after replace = %v, want 0.7", c.Threshold())
	}
}

func TestTopKTieBreak(t *testing.T) {
	c := NewTopK(2)
	c.Collect(5, 1.0)
	c.Collect(2, 1.0)
	got := c.Results()
	if got[0].DocID != 2 || got[1].DocID != 5 {
		t.Fatalf("tie order = %v, want doc 2 then 5", got)
	}
}

// TestTopKMatchesFullSort feeds random hits and checks the heap result equals a
// full sort truncated to k.
func TestTopKMatchesFullSort(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	const k = 10
	c := NewTopK(k)
	var all []Hit
	for i := range 1000 {
		s := rng.Float32()
		c.Collect(uint32(i), s)
		all = append(all, Hit{uint32(i), s})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		return all[i].DocID < all[j].DocID
	})
	got := c.Results()
	for i := range k {
		if got[i] != all[i] {
			t.Fatalf("rank %d: heap %v, full-sort %v", i, got[i], all[i])
		}
	}
}

func TestTopKZero(t *testing.T) {
	c := NewTopK(0)
	c.Collect(1, 1.0)
	if len(c.Results()) != 0 {
		t.Fatalf("k=0 should keep nothing")
	}
}
