// Package collect gathers the top-scoring documents of a query (spec 2063 doc 12
// §4). A TopK collector keeps the k highest-scoring hits seen so far in a binary
// min-heap keyed by score, so the smallest kept score sits at the root and a new
// hit is admitted only when it beats that root. This bounds memory to k entries
// regardless of how many documents match, and exposes the current root as a
// threshold a scorer can use to skip documents that cannot enter the heap.
package collect

import (
	"container/heap"
	"sort"
)

// Hit is one scored document. DocID is the global internal doc-id.
type Hit struct {
	DocID uint32
	Score float32
}

// TopK keeps the k highest-scoring hits. The zero value is not usable; build one
// with NewTopK.
type TopK struct {
	k    int
	heap hitHeap
}

// NewTopK returns a collector that retains the k best hits. A k <= 0 keeps no
// hits (every Collect is a no-op) but still reports a threshold of zero.
func NewTopK(k int) *TopK {
	if k < 0 {
		k = 0
	}
	return &TopK{k: k}
}

// Threshold returns the minimum score a hit must exceed to enter the heap once it
// is full, or 0 while the heap has spare capacity. A scorer may use it to prune.
func (c *TopK) Threshold() float32 {
	if len(c.heap) < c.k {
		return 0
	}
	if len(c.heap) == 0 {
		return 0
	}
	return c.heap[0].Score
}

// Full reports whether the heap holds k hits.
func (c *TopK) Full() bool { return c.k > 0 && len(c.heap) >= c.k }

// Collect offers a scored document to the collector. It is kept only if the heap
// has room or the score beats the current smallest kept score.
func (c *TopK) Collect(docID uint32, sc float32) {
	if c.k == 0 {
		return
	}
	if len(c.heap) < c.k {
		heap.Push(&c.heap, Hit{DocID: docID, Score: sc})
		return
	}
	if sc > c.heap[0].Score {
		c.heap[0] = Hit{DocID: docID, Score: sc}
		heap.Fix(&c.heap, 0)
	}
}

// Results returns the kept hits sorted by descending score, breaking ties by
// ascending doc-id so the order is deterministic.
func (c *TopK) Results() []Hit {
	out := make([]Hit, len(c.heap))
	copy(out, c.heap)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].DocID < out[j].DocID
	})
	return out
}

// hitHeap is a min-heap of hits ordered by score (ties broken by larger doc-id at
// the root so the most replaceable hit is evicted first).
type hitHeap []Hit

func (h hitHeap) Len() int { return len(h) }
func (h hitHeap) Less(i, j int) bool {
	if h[i].Score != h[j].Score {
		return h[i].Score < h[j].Score
	}
	return h[i].DocID > h[j].DocID
}
func (h hitHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *hitHeap) Push(x any) { *h = append(*h, x.(Hit)) }
func (h *hitHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
