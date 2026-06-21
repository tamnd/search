package hnsw

import (
	"math"
	"testing"
)

// randCorpus builds a deterministic pseudo-random corpus of n vectors in d dims
// using a SplitMix source so tests are reproducible without math/rand.
func randCorpus(n, d int, seed uint64) [][]float32 {
	r := splitMix{state: seed}
	out := make([][]float32, n)
	for i := range out {
		v := make([]float32, d)
		for j := range v {
			v[j] = float32(r.float64()*2 - 1)
		}
		out[i] = v
	}
	return out
}

// bruteTopK returns the exact top-k doc ids for q under the metric, the ground
// truth recall is measured against.
func bruteTopK(corpus [][]float32, q []float32, k int, m Metric) []uint32 {
	type pair struct {
		id uint32
		d  float32
	}
	g := &Graph{metric: m}
	pq := g.prepared(q)
	prepped := make([][]float32, len(corpus))
	for i, v := range corpus {
		prepped[i] = g.prepared(v)
	}
	ps := make([]pair, len(corpus))
	for i := range corpus {
		ps[i] = pair{id: uint32(i), d: g.dist(pq, prepped[i])}
	}
	// simple selection of k smallest
	for i := 0; i < k && i < len(ps); i++ {
		best := i
		for j := i + 1; j < len(ps); j++ {
			if ps[j].d < ps[best].d {
				best = j
			}
		}
		ps[i], ps[best] = ps[best], ps[i]
	}
	out := make([]uint32, 0, k)
	for i := 0; i < k && i < len(ps); i++ {
		out = append(out, ps[i].id)
	}
	return out
}

func recallAt(got []Result, truth []uint32) float64 {
	set := make(map[uint32]bool, len(truth))
	for _, t := range truth {
		set[t] = true
	}
	hit := 0
	for _, r := range got {
		if set[r.DocID] {
			hit++
		}
	}
	return float64(hit) / float64(len(truth))
}

func buildGraph(t *testing.T, corpus [][]float32, m Metric) *Graph {
	t.Helper()
	g := New(DefaultParams(16, 200), m, len(corpus[0]))
	for i, v := range corpus {
		g.Add(uint32(i), v)
	}
	return g
}

func TestRecall_L2(t *testing.T) {
	const n, d, k = 2000, 32, 10
	corpus := randCorpus(n, d, 1)
	g := buildGraph(t, corpus, L2)

	queries := randCorpus(50, d, 99)
	var total float64
	for _, q := range queries {
		got := g.Search(q, k, 100, nil)
		truth := bruteTopK(corpus, q, k, L2)
		total += recallAt(got, truth)
	}
	avg := total / float64(len(queries))
	if avg < 0.95 {
		t.Fatalf("L2 recall@%d = %.3f, want >= 0.95", k, avg)
	}
}

func TestRecall_Cosine(t *testing.T) {
	const n, d, k = 2000, 48, 10
	corpus := randCorpus(n, d, 7)
	g := buildGraph(t, corpus, Cosine)

	queries := randCorpus(50, d, 123)
	var total float64
	for _, q := range queries {
		got := g.Search(q, k, 100, nil)
		truth := bruteTopK(corpus, q, k, Cosine)
		total += recallAt(got, truth)
	}
	avg := total / float64(len(queries))
	if avg < 0.95 {
		t.Fatalf("cosine recall@%d = %.3f, want >= 0.95", k, avg)
	}
}

func TestExactSearch_MatchesBrute(t *testing.T) {
	const n, d, k = 500, 16, 10
	corpus := randCorpus(n, d, 3)
	g := buildGraph(t, corpus, L2)
	q := randCorpus(1, d, 555)[0]
	got := g.ExactSearch(q, k, nil)
	truth := bruteTopK(corpus, q, k, L2)
	if recallAt(got, truth) != 1.0 {
		t.Fatalf("exact search must match brute force exactly")
	}
}

func TestFilteredSearch_OnlyMatches(t *testing.T) {
	const n, d, k = 2000, 32, 10
	corpus := randCorpus(n, d, 11)
	g := buildGraph(t, corpus, L2)
	allow := func(docID uint32) bool { return docID%5 == 0 } // 20% selectivity
	q := randCorpus(1, d, 77)[0]
	got := g.Search(q, k, 100, allow)
	if len(got) == 0 {
		t.Fatal("filtered search returned nothing")
	}
	for _, r := range got {
		if r.DocID%5 != 0 {
			t.Fatalf("filtered search returned doc %d not matching filter", r.DocID)
		}
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	const n, d, k = 800, 24, 10
	corpus := randCorpus(n, d, 21)
	g := buildGraph(t, corpus, Cosine)
	blob := g.Marshal()
	g2, err := Load(blob)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if g2.Len() != g.Len() || g2.dims != g.dims || g2.metric != g.metric {
		t.Fatalf("reloaded graph header mismatch")
	}
	q := randCorpus(1, d, 222)[0]
	a := g.Search(q, k, 100, nil)
	b := g2.Search(q, k, 100, nil)
	if len(a) != len(b) {
		t.Fatalf("result count differs after round trip: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].DocID != b[i].DocID {
			t.Fatalf("result %d differs after round trip: %d vs %d", i, a[i].DocID, b[i].DocID)
		}
		if math.Abs(float64(a[i].Score-b[i].Score)) > 1e-6 {
			t.Fatalf("score %d differs after round trip", i)
		}
	}
}

func TestScoreFromDist(t *testing.T) {
	// Cosine of identical unit vectors: dist 0 -> score 1.
	if s := ScoreFromDist(Cosine, 0); math.Abs(float64(s-1)) > 1e-6 {
		t.Fatalf("cosine score at dist 0 = %v, want 1", s)
	}
	// L2 dist 0 -> score 1.
	if s := ScoreFromDist(L2, 0); math.Abs(float64(s-1)) > 1e-6 {
		t.Fatalf("l2 score at dist 0 = %v, want 1", s)
	}
}
