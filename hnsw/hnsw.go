// Package hnsw implements the Hierarchical Navigable Small World graph the engine
// uses for approximate nearest-neighbor search (spec 2063 doc 15 §4-§5). A Graph
// is built by inserting vectors one at a time, then serialized to a self-
// describing blob; Load rebuilds a Graph from that blob for querying. Both the
// build and the query side live here; the distance math comes from package
// vector.
//
// Storage deviation from doc 15 §6: the spec lays the graph out across page
// extents (page types 0x0B-0x0E) read through mmap. This engine instead rides the
// catalog key/value seam like the FST, postings, and doc-values layers do, so the
// graph and its float32 vectors are one self-describing blob keyed by (segment
// id, field). The query side deserializes the blob into the same in-memory Graph
// the builder produced rather than walking mmap'd pages. The algorithm is
// identical; only the byte transport differs, the same documented choice the rest
// of the segment layer makes.
package hnsw

import (
	"math"

	"github.com/tamnd/search/vector"
)

// Metric selects the similarity measure. The graph orders nodes by a distance
// where smaller always means more similar, derived from the metric.
type Metric uint8

// The supported metrics.
const (
	Cosine Metric = iota // 1 - cosine similarity over normalized vectors
	Dot                  // negated dot product
	L2                   // squared Euclidean distance
)

// ParseMetric maps a field metric string to a Metric, defaulting to cosine.
func ParseMetric(s string) Metric {
	switch s {
	case "dot_product", "dot":
		return Dot
	case "l2", "l2_norm", "euclidean":
		return L2
	default:
		return Cosine
	}
}

// Params holds the HNSW build parameters (doc 15 §16.2). The zero value is not
// useful; build one with DefaultParams.
type Params struct {
	M              int     // max neighbors per node on upper layers
	Mmax0          int     // max neighbors at layer 0; default 2*M
	EfConstruction int     // candidate list size during build
	ML             float64 // level multiplier; default 1/ln(M)
	Seed           int64   // seed for level assignment
}

// DefaultParams returns build parameters for the given M and efConstruction,
// filling Mmax0 = 2*M and ML = 1/ln(M) per the HNSW paper.
func DefaultParams(m, efConstruction int) Params {
	if m <= 0 {
		m = 16
	}
	if efConstruction <= 0 {
		efConstruction = 100
	}
	return Params{
		M:              m,
		Mmax0:          2 * m,
		EfConstruction: efConstruction,
		ML:             1.0 / math.Log(float64(m)),
		Seed:           0,
	}
}

// Result is one neighbor returned by a search: its graph ordinal, the document
// id it carries, the raw distance (metric dependent), and the score normalized to
// [0,1] (doc 15 §9.4).
type Result struct {
	Ordinal  uint32
	DocID    uint32
	Distance float32
	Score    float32
}

// node is one graph vertex during build and after load.
type node struct {
	docID uint32
	level int
	links [][]uint32 // links[layer] = neighbor ordinals; len == level+1
}

// Graph is an in-memory HNSW graph. It is built with Add then queried with Search
// or ExactSearch, or serialized with Marshal and reloaded with Load. A loaded
// Graph is read-only and safe for concurrent Search calls (each search keeps its
// own scratch state).
type Graph struct {
	params   Params
	metric   Metric
	dims     int
	vectors  [][]float32
	nodes    []node
	entry    int // -1 when empty
	maxLevel int
	rng      splitMix
}

// New returns an empty graph for vectors of the given dimension.
func New(p Params, metric Metric, dims int) *Graph {
	return &Graph{
		params:   p,
		metric:   metric,
		dims:     dims,
		entry:    -1,
		maxLevel: 0,
		rng:      splitMix{state: uint64(p.Seed) ^ 0x100000001b3},
	}
}

// Len returns the number of nodes in the graph.
func (g *Graph) Len() int { return len(g.nodes) }

// Dims returns the vector dimension.
func (g *Graph) Dims() int { return g.dims }

// Metric returns the graph's metric.
func (g *Graph) Metric() Metric { return g.metric }

// prepared returns the form of v the graph stores and queries on: a normalized
// copy for cosine, an as-is copy otherwise.
func (g *Graph) prepared(v []float32) []float32 {
	if g.metric == Cosine {
		out, _ := vector.Normalize(v)
		return out
	}
	out := make([]float32, len(v))
	copy(out, v)
	return out
}

// dist returns the order-by distance between two prepared vectors, where smaller
// means more similar regardless of metric.
func (g *Graph) dist(a, b []float32) float32 {
	switch g.metric {
	case L2:
		return vector.L2Sq(a, b)
	case Dot:
		return -vector.Dot(a, b)
	default: // Cosine
		return 1 - vector.Dot(a, b)
	}
}

// distTo returns the distance from a prepared query to the stored node ordinal.
func (g *Graph) distTo(q []float32, ord uint32) float32 {
	return g.dist(q, g.vectors[ord])
}

// ScoreFromDist converts a raw order-by distance back to a [0,1] similarity score
// (doc 15 §9.4).
func ScoreFromDist(metric Metric, d float32) float32 {
	switch metric {
	case L2:
		return 1.0 / (1.0 + d)
	case Dot:
		return (-d + 1) / 2
	default: // Cosine
		return (1 - d + 1) / 2
	}
}

// assignLevel draws a node's top layer: floor(-ln(U) * ML) with U in (0,1].
func (g *Graph) assignLevel() int {
	u := g.rng.float64()
	if u <= 0 {
		u = math.SmallestNonzeroFloat64
	}
	return int(-math.Log(u) * g.params.ML)
}

// Add inserts a vector under docID and returns its assigned ordinal. The vector
// is stored in prepared form (normalized for cosine). The caller is responsible
// for validating dimension and rejecting zero/NaN vectors before calling.
func (g *Graph) Add(docID uint32, v []float32) uint32 {
	pv := g.prepared(v)
	ord := uint32(len(g.nodes))
	level := g.assignLevel()
	n := node{docID: docID, level: level, links: make([][]uint32, level+1)}
	g.nodes = append(g.nodes, n)
	g.vectors = append(g.vectors, pv)

	if g.entry == -1 {
		g.entry = int(ord)
		g.maxLevel = level
		return ord
	}

	cur := uint32(g.entry)
	// Phase 1: greedy descent from the top down to the layer just above level.
	for lc := g.maxLevel; lc > level; lc-- {
		cur = g.greedy(pv, cur, lc)
	}
	// Phase 2: from min(level, maxLevel) down to 0, connect with neighbor
	// selection and prune.
	start := min(level, g.maxLevel)
	entryPoints := []uint32{cur}
	for lc := start; lc >= 0; lc-- {
		cand := g.searchLayer(pv, entryPoints, g.params.EfConstruction, lc, nil)
		mmax := g.params.M
		if lc == 0 {
			mmax = g.params.Mmax0
		}
		neighbors := g.selectNeighbors(cand, mmax, true)
		g.nodes[ord].links[lc] = neighbors
		// Add the reverse edges and prune each neighbor back to mmax.
		for _, nb := range neighbors {
			g.nodes[nb].links[lc] = append(g.nodes[nb].links[lc], ord)
			if len(g.nodes[nb].links[lc]) > mmax {
				g.pruneNode(nb, lc, mmax)
			}
		}
		entryPoints = candOrdinals(cand)
		if len(entryPoints) == 0 {
			entryPoints = []uint32{cur}
		}
	}

	if level > g.maxLevel {
		g.maxLevel = level
		g.entry = int(ord)
	}
	return ord
}

// greedy walks one layer from cur toward q, hopping to the closest neighbor until
// no neighbor is closer, and returns the local closest ordinal.
func (g *Graph) greedy(q []float32, cur uint32, layer int) uint32 {
	curDist := g.distTo(q, cur)
	for {
		improved := false
		for _, nb := range g.nodes[cur].links[layer] {
			if d := g.distTo(q, nb); d < curDist {
				curDist, cur = d, nb
				improved = true
			}
		}
		if !improved {
			return cur
		}
	}
}

// pruneNode trims node nb's layer to its mmax closest neighbors using the same
// heuristic selection used at insert.
func (g *Graph) pruneNode(nb uint32, layer, mmax int) {
	links := g.nodes[nb].links[layer]
	cand := make([]candidate, len(links))
	base := g.vectors[nb]
	for i, l := range links {
		cand[i] = candidate{ord: l, dist: g.dist(base, g.vectors[l])}
	}
	g.nodes[nb].links[layer] = g.selectNeighbors(cand, mmax, false)
}
