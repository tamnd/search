package hnsw

import "sort"

// candidate is an ordinal paired with its distance to the working query.
type candidate struct {
	ord  uint32
	dist float32
}

// candOrdinals projects a candidate slice to its ordinals.
func candOrdinals(c []candidate) []uint32 {
	out := make([]uint32, len(c))
	for i := range c {
		out[i] = c[i].ord
	}
	return out
}

// visited is a versioned membership set: instead of clearing a map per search,
// it stamps each ordinal with a generation and treats a stale stamp as absent
// (doc 15 §5.3). One set is allocated per search call.
type visited struct {
	stamp []uint32
	gen   uint32
}

func newVisited(n int) *visited { return &visited{stamp: make([]uint32, n)} }

func (v *visited) reset() { v.gen++ }

func (v *visited) seen(ord uint32) bool { return v.stamp[ord] == v.gen }

func (v *visited) mark(ord uint32) { v.stamp[ord] = v.gen }

// searchLayer runs the best-first search of one layer from the given entry
// points, returning up to ef candidates ordered nearest first (doc 15 §4.3). When
// allow is non-nil only ordinals it accepts enter the result set, but every
// neighbor still enters the candidate frontier so the graph stays connected
// through filtered-out nodes (doc 15 §8.2).
func (g *Graph) searchLayer(q []float32, entryPoints []uint32, ef, layer int, allow func(uint32) bool) []candidate {
	vis := g.scratch(len(g.nodes))
	vis.reset()

	// cands is a min-heap by distance (nearest first); res is a max-heap by
	// distance (farthest first), capped at ef.
	cands := &minHeap{}
	res := &maxHeap{}
	for _, ep := range entryPoints {
		if vis.seen(ep) {
			continue
		}
		vis.mark(ep)
		d := g.distTo(q, ep)
		cands.push(candidate{ord: ep, dist: d})
		if allow == nil || allow(ep) {
			res.push(candidate{ord: ep, dist: d})
			if res.len() > ef {
				res.pop()
			}
		}
	}

	for cands.len() > 0 {
		c := cands.pop()
		// Stop only once the result set is full and the nearest remaining
		// candidate is farther than the worst kept result.
		if res.len() >= ef && c.dist > res.peek().dist {
			break
		}
		for _, nb := range g.nodes[c.ord].links[layer] {
			if vis.seen(nb) {
				continue
			}
			vis.mark(nb)
			d := g.distTo(q, nb)
			if res.len() < ef || d < res.peek().dist {
				cands.push(candidate{ord: nb, dist: d})
				if allow == nil || allow(nb) {
					res.push(candidate{ord: nb, dist: d})
					if res.len() > ef {
						res.pop()
					}
				}
			}
		}
	}

	out := res.drainSorted()
	return out
}

// selectNeighbors picks up to m diverse neighbors of base from cand using the
// HNSW heuristic (doc 15 §4.4): walking candidates nearest first, keep e when it
// is closer to base than to any already-kept neighbor. extend controls whether
// the leftover candidates backfill the result up to m, which the build pass wants
// for connectivity.
func (g *Graph) selectNeighbors(cand []candidate, m int, extend bool) []uint32 {
	sort.Slice(cand, func(i, j int) bool { return cand[i].dist < cand[j].dist })
	kept := make([]uint32, 0, m)
	var discarded []candidate
	for _, c := range cand {
		if len(kept) >= m {
			break
		}
		good := true
		for _, r := range kept {
			if g.dist(g.vectors[c.ord], g.vectors[r]) < c.dist {
				good = false
				break
			}
		}
		if good {
			kept = append(kept, c.ord)
		} else if extend {
			discarded = append(discarded, c)
		}
	}
	if extend {
		for _, c := range discarded {
			if len(kept) >= m {
				break
			}
			kept = append(kept, c.ord)
		}
	}
	return kept
}

// Search runs the two-phase kNN search for query q: a greedy descent of the upper
// layers, then a best-first search of layer 0 with candidate list size ef (doc 15
// §5.1). It returns up to k results nearest first. When allow is non-nil only
// documents it accepts are returned, but the walk still passes through filtered
// nodes for connectivity (filtered ANN, doc 15 §8).
func (g *Graph) Search(q []float32, k, ef int, allow func(docID uint32) bool) []Result {
	if g.entry == -1 || len(g.nodes) == 0 || k <= 0 {
		return nil
	}
	pq := g.prepared(q)
	if ef < k {
		ef = k
	}
	cur := uint32(g.entry)
	for lc := g.maxLevel; lc >= 1; lc-- {
		cur = g.greedy(pq, cur, lc)
	}
	var ordAllow func(uint32) bool
	if allow != nil {
		ordAllow = func(ord uint32) bool { return allow(g.nodes[ord].docID) }
	}
	cand := g.searchLayer(pq, []uint32{cur}, ef, 0, ordAllow)
	return g.toResults(cand, k)
}

// ExactSearch scans every node (optionally restricted to those allow accepts) and
// returns the exact top-k by distance. It is the brute-force fallback for small
// segments or very selective filters (doc 15 §5.4, §8.4).
func (g *Graph) ExactSearch(q []float32, k int, allow func(docID uint32) bool) []Result {
	if len(g.nodes) == 0 || k <= 0 {
		return nil
	}
	pq := g.prepared(q)
	res := &maxHeap{}
	for ord := range g.nodes {
		if allow != nil && !allow(g.nodes[ord].docID) {
			continue
		}
		d := g.dist(pq, g.vectors[ord])
		if res.len() < k || d < res.peek().dist {
			res.push(candidate{ord: uint32(ord), dist: d})
			if res.len() > k {
				res.pop()
			}
		}
	}
	return g.toResults(res.drainSorted(), k)
}

// toResults converts a nearest-first candidate slice to Results, trimming to k
// and attaching doc ids and scores.
func (g *Graph) toResults(cand []candidate, k int) []Result {
	if len(cand) > k {
		cand = cand[:k]
	}
	out := make([]Result, len(cand))
	for i, c := range cand {
		out[i] = Result{
			Ordinal:  c.ord,
			DocID:    g.nodes[c.ord].docID,
			Distance: c.dist,
			Score:    ScoreFromDist(g.metric, c.dist),
		}
	}
	return out
}

// scratch returns a fresh visited set sized to the graph. A pool is unnecessary
// here because each Search allocates one set; the versioned stamp avoids
// per-query clearing within a search.
func (g *Graph) scratch(n int) *visited { return newVisited(n) }
