package quantize

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/tamnd/search/vector"
)

// pqK is the number of centroids per subspace. Eight bits per code means 256
// centroids, the standard choice (doc 15 §17.4); a code is therefore one byte
// per subspace regardless of the subspace dimension.
const pqK = 256

// PQConfig configures product quantization. M is the number of subspaces; Dims
// must be a multiple of M so each subspace has Dims/M components. Sample caps the
// number of training vectors drawn for k-means; Iters is the k-means iteration
// count (doc 15 §17).
type PQConfig struct {
	M      int
	Dims   int
	Sample int
	Iters  int
	Seed   int64
}

// sub returns the per-subspace dimension.
func (c PQConfig) sub() int { return c.Dims / c.M }

// Codebook is a trained product-quantization codebook: M subspaces, each with
// 256 centroids of Dims/M float32 components. Centroids are laid out flat in
// subspace-major then centroid-major order, the same order the on-disk format
// uses (doc 15 §17.3).
type Codebook struct {
	M         int
	Dims      int
	Sub       int
	Centroids []float32 // M * 256 * Sub
}

// TrainCodebook trains a PQ codebook from vectors using k-means++ initialization
// followed by Lloyd iterations per subspace (doc 15 §17.1-§17.2). It returns an
// error when Dims is not a multiple of M or no vectors are given.
func TrainCodebook(vectors [][]float32, cfg PQConfig) (*Codebook, error) {
	if cfg.M <= 0 || cfg.Dims <= 0 {
		return nil, errors.New("quantize: pq needs positive M and Dims")
	}
	if cfg.Dims%cfg.M != 0 {
		return nil, errors.New("quantize: pq Dims must be a multiple of M")
	}
	if len(vectors) == 0 {
		return nil, errors.New("quantize: pq needs at least one training vector")
	}
	if cfg.Iters <= 0 {
		cfg.Iters = 25
	}
	sub := cfg.sub()
	rng := newSplitMix(uint64(cfg.Seed) ^ 0x9e3779b97f4a7c15)

	// Draw the training sample (uniform without replacement is overkill; a strided
	// pick gives a spread-out, deterministic sample).
	sample := vectors
	if cfg.Sample > 0 && len(vectors) > cfg.Sample {
		sample = make([][]float32, 0, cfg.Sample)
		step := len(vectors) / cfg.Sample
		for i := 0; i < len(vectors) && len(sample) < cfg.Sample; i += step {
			sample = append(sample, vectors[i])
		}
	}

	cb := &Codebook{M: cfg.M, Dims: cfg.Dims, Sub: sub, Centroids: make([]float32, cfg.M*pqK*sub)}
	// Per-subspace training buffers reused across subspaces.
	points := make([][]float32, len(sample))
	for s := 0; s < cfg.M; s++ {
		lo := s * sub
		for i, v := range sample {
			points[i] = v[lo : lo+sub]
		}
		cents := trainSubspace(points, sub, cfg.Iters, rng)
		copy(cb.Centroids[s*pqK*sub:(s+1)*pqK*sub], cents)
	}
	return cb, nil
}

// trainSubspace runs k-means++ then Lloyd iterations over one subspace's points,
// returning the 256 centroids flat (256 * dim).
func trainSubspace(points [][]float32, dim, iters int, rng *splitMix) []float32 {
	cents := make([]float32, pqK*dim)
	k := pqK
	if len(points) < k {
		// Fewer points than centroids: seed each centroid with a point (cycling)
		// so every code id maps to something sensible.
		for c := 0; c < k; c++ {
			src := points[c%len(points)]
			copy(cents[c*dim:(c+1)*dim], src)
		}
		return cents
	}

	// k-means++ seeding.
	first := int(rng.next() % uint64(len(points)))
	copy(cents[0:dim], points[first])
	dist := make([]float64, len(points))
	for i := range dist {
		dist[i] = math.MaxFloat64
	}
	for c := 1; c < k; c++ {
		prev := cents[(c-1)*dim : c*dim]
		var total float64
		for i, p := range points {
			d := float64(vector.L2Sq(p, prev))
			if d < dist[i] {
				dist[i] = d
			}
			total += dist[i]
		}
		target := rng.float64() * total
		pick := len(points) - 1
		var acc float64
		for i := range points {
			acc += dist[i]
			if acc >= target {
				pick = i
				break
			}
		}
		copy(cents[c*dim:(c+1)*dim], points[pick])
	}

	// Lloyd iterations.
	assign := make([]int, len(points))
	sums := make([]float64, k*dim)
	counts := make([]int, k)
	for it := 0; it < iters; it++ {
		changed := false
		for i, p := range points {
			best, bestD := 0, math.MaxFloat64
			for c := 0; c < k; c++ {
				d := float64(vector.L2Sq(p, cents[c*dim:(c+1)*dim]))
				if d < bestD {
					bestD, best = d, c
				}
			}
			if assign[i] != best {
				assign[i] = best
				changed = true
			}
		}
		if !changed && it > 0 {
			break
		}
		for i := range sums {
			sums[i] = 0
		}
		for i := range counts {
			counts[i] = 0
		}
		for i, p := range points {
			c := assign[i]
			counts[c]++
			base := c * dim
			for d := 0; d < dim; d++ {
				sums[base+d] += float64(p[d])
			}
		}
		for c := 0; c < k; c++ {
			if counts[c] == 0 {
				continue // keep the old centroid for an empty cluster
			}
			base := c * dim
			inv := 1.0 / float64(counts[c])
			for d := 0; d < dim; d++ {
				cents[base+d] = float32(sums[base+d] * inv)
			}
		}
	}
	return cents
}

// Encode encodes one vector to its M-byte PQ code.
func (cb *Codebook) Encode(v []float32) []uint8 {
	code := make([]uint8, cb.M)
	for s := 0; s < cb.M; s++ {
		lo := s * cb.Sub
		seg := v[lo : lo+cb.Sub]
		best, bestD := 0, float32(math.MaxFloat32)
		cbase := s * pqK * cb.Sub
		for c := 0; c < pqK; c++ {
			cent := cb.Centroids[cbase+c*cb.Sub : cbase+(c+1)*cb.Sub]
			if d := vector.L2Sq(seg, cent); d < bestD {
				bestD, best = d, c
			}
		}
		code[s] = uint8(best)
	}
	return code
}

// EncodeAll encodes a slice of vectors to a flat code buffer (M bytes each).
func (cb *Codebook) EncodeAll(vectors [][]float32) []uint8 {
	out := make([]uint8, len(vectors)*cb.M)
	for i, v := range vectors {
		copy(out[i*cb.M:(i+1)*cb.M], cb.Encode(v))
	}
	return out
}

// L2Table precomputes the asymmetric squared-L2 distance table for a query: for
// each subspace s and centroid c, the squared distance from the query's subspace
// to that centroid (doc 15 §17.3). A PQ code's distance is then the sum over
// subspaces of table[s*256 + code[s]], computed by vector.DotPQ.
func (cb *Codebook) L2Table(q []float32) []float32 {
	table := make([]float32, cb.M*pqK)
	for s := 0; s < cb.M; s++ {
		lo := s * cb.Sub
		seg := q[lo : lo+cb.Sub]
		cbase := s * pqK * cb.Sub
		tbase := s * pqK
		for c := 0; c < pqK; c++ {
			cent := cb.Centroids[cbase+c*cb.Sub : cbase+(c+1)*cb.Sub]
			table[tbase+c] = vector.L2Sq(seg, cent)
		}
	}
	return table
}

// Distance returns the approximate squared-L2 distance between a query's
// precomputed table and a PQ code.
func (cb *Codebook) Distance(table []float32, code []uint8) float32 {
	return vector.DotPQ(code, table, cb.M)
}

// Marshal serializes the codebook: M, Dims as little-endian uint32 headers, then
// the centroid floats.
func (cb *Codebook) Marshal() []byte {
	out := make([]byte, 8+len(cb.Centroids)*4)
	binary.LittleEndian.PutUint32(out[0:4], uint32(cb.M))
	binary.LittleEndian.PutUint32(out[4:8], uint32(cb.Dims))
	for i, f := range cb.Centroids {
		binary.LittleEndian.PutUint32(out[8+i*4:], math.Float32bits(f))
	}
	return out
}

// UnmarshalCodebook reads a codebook produced by Marshal.
func UnmarshalCodebook(b []byte) (*Codebook, error) {
	if len(b) < 8 {
		return nil, errors.New("quantize: short codebook")
	}
	m := int(binary.LittleEndian.Uint32(b[0:4]))
	dims := int(binary.LittleEndian.Uint32(b[4:8]))
	if m <= 0 || dims <= 0 || dims%m != 0 {
		return nil, errors.New("quantize: bad codebook header")
	}
	sub := dims / m
	want := m * pqK * sub
	body := b[8:]
	if len(body) != want*4 {
		return nil, errors.New("quantize: codebook length mismatch")
	}
	cents := make([]float32, want)
	for i := range cents {
		cents[i] = math.Float32frombits(binary.LittleEndian.Uint32(body[i*4:]))
	}
	return &Codebook{M: m, Dims: dims, Sub: sub, Centroids: cents}, nil
}

// splitMix is a tiny deterministic PRNG (SplitMix64) used for reproducible
// k-means seeding without pulling in math/rand global state.
type splitMix struct{ state uint64 }

func newSplitMix(seed uint64) *splitMix { return &splitMix{state: seed} }

func (s *splitMix) next() uint64 {
	s.state += 0x9e3779b97f4a7c15
	z := s.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// float64 returns a value in [0,1).
func (s *splitMix) float64() float64 {
	return float64(s.next()>>11) / float64(uint64(1)<<53)
}
