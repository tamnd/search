package hnsw

import (
	"encoding/binary"
	"errors"
	"math"
)

// hnswMagic tags a serialized graph blob.
var hnswMagic = [4]byte{'H', 'N', 'S', 'W'}

const hnswVersion = 1

// noEntry marks an empty graph's entry point in the blob.
const noEntry = 0xFFFFFFFF

// Marshal serializes the graph to a self-describing blob: a header (metric,
// dims, params, entry point, max level, node count), then per node its doc id,
// level, and per-layer neighbor lists, then the stored float32 vectors. The
// format folds the spec's separate node-block and vector-data-block pages (doc 15
// §6) into one blob, the documented KV-seam deviation.
func (g *Graph) Marshal() []byte {
	out := make([]byte, 0, 64+len(g.nodes)*32+len(g.nodes)*g.dims*4)
	out = append(out, hnswMagic[:]...)
	out = append(out, hnswVersion, byte(g.metric))
	out = binary.LittleEndian.AppendUint32(out, uint32(g.dims))
	out = binary.LittleEndian.AppendUint32(out, uint32(g.params.M))
	out = binary.LittleEndian.AppendUint32(out, uint32(g.params.Mmax0))
	out = binary.LittleEndian.AppendUint32(out, uint32(g.params.EfConstruction))
	out = binary.LittleEndian.AppendUint64(out, math.Float64bits(g.params.ML))
	entry := uint32(noEntry)
	if g.entry >= 0 {
		entry = uint32(g.entry)
	}
	out = binary.LittleEndian.AppendUint32(out, entry)
	out = binary.LittleEndian.AppendUint32(out, uint32(g.maxLevel))
	out = binary.LittleEndian.AppendUint32(out, uint32(len(g.nodes)))

	for _, n := range g.nodes {
		out = binary.LittleEndian.AppendUint32(out, n.docID)
		out = binary.LittleEndian.AppendUint32(out, uint32(n.level))
		for lc := 0; lc <= n.level; lc++ {
			out = binary.LittleEndian.AppendUint32(out, uint32(len(n.links[lc])))
			for _, nb := range n.links[lc] {
				out = binary.LittleEndian.AppendUint32(out, nb)
			}
		}
	}
	for _, v := range g.vectors {
		for _, f := range v {
			out = binary.LittleEndian.AppendUint32(out, math.Float32bits(f))
		}
	}
	return out
}

// Load rebuilds a Graph from a blob produced by Marshal. The returned graph is
// ready for Search and ExactSearch.
func Load(b []byte) (*Graph, error) {
	r := &reader{b: b}
	var magic [4]byte
	copy(magic[:], r.bytes(4))
	if magic != hnswMagic {
		return nil, errors.New("hnsw: bad magic")
	}
	ver := r.u8()
	if ver != hnswVersion {
		return nil, errors.New("hnsw: unsupported version")
	}
	metric := Metric(r.u8())
	dims := int(r.u32())
	p := Params{
		M:              int(r.u32()),
		Mmax0:          int(r.u32()),
		EfConstruction: int(r.u32()),
		ML:             math.Float64frombits(r.u64()),
	}
	entry := r.u32()
	maxLevel := int(r.u32())
	count := int(r.u32())
	if r.err != nil {
		return nil, r.err
	}

	g := &Graph{params: p, metric: metric, dims: dims, entry: -1, maxLevel: maxLevel}
	if entry != noEntry {
		g.entry = int(entry)
	}
	g.nodes = make([]node, count)
	for i := 0; i < count; i++ {
		docID := r.u32()
		level := int(r.u32())
		n := node{docID: docID, level: level, links: make([][]uint32, level+1)}
		for lc := 0; lc <= level; lc++ {
			ln := int(r.u32())
			links := make([]uint32, ln)
			for j := 0; j < ln; j++ {
				links[j] = r.u32()
			}
			n.links[lc] = links
		}
		g.nodes[i] = n
	}
	g.vectors = make([][]float32, count)
	for i := 0; i < count; i++ {
		v := make([]float32, dims)
		for d := 0; d < dims; d++ {
			v[d] = math.Float32frombits(r.u32())
		}
		g.vectors[i] = v
	}
	if r.err != nil {
		return nil, r.err
	}
	return g, nil
}

// Vector returns the stored (prepared) vector for an ordinal. It backs the
// re-rank pass, which needs the float32 vectors held in the graph blob.
func (g *Graph) Vector(ord uint32) []float32 { return g.vectors[ord] }

// DocID returns the document id carried by an ordinal.
func (g *Graph) DocID(ord uint32) uint32 { return g.nodes[ord].docID }

// reader is a tiny little-endian cursor over a byte slice that records the first
// short read in err so callers check once at the end.
type reader struct {
	b   []byte
	p   int
	err error
}

func (r *reader) bytes(n int) []byte {
	if r.err != nil || r.p+n > len(r.b) {
		r.fail()
		return make([]byte, n)
	}
	out := r.b[r.p : r.p+n]
	r.p += n
	return out
}

func (r *reader) u8() byte {
	if r.err != nil || r.p+1 > len(r.b) {
		r.fail()
		return 0
	}
	v := r.b[r.p]
	r.p++
	return v
}

func (r *reader) u32() uint32 {
	if r.err != nil || r.p+4 > len(r.b) {
		r.fail()
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.p:])
	r.p += 4
	return v
}

func (r *reader) u64() uint64 {
	if r.err != nil || r.p+8 > len(r.b) {
		r.fail()
		return 0
	}
	v := binary.LittleEndian.Uint64(r.b[r.p:])
	r.p += 8
	return v
}

func (r *reader) fail() {
	if r.err == nil {
		r.err = errors.New("hnsw: truncated graph blob")
	}
}
