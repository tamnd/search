package agg

import (
	"math"
	"math/bits"
)

// hll is a HyperLogLog cardinality sketch with 2^p registers (doc 14 §7.4). The
// engine uses p=14 (16 384 registers), giving roughly 0.8% relative error at
// 12 KiB per sketch. This is the plain HLL estimator with the standard small-
// and large-range corrections, which is accurate enough for facet distinct
// counts; the HLL++ bias table is an additional refinement not implemented here.
type hll struct {
	p   uint
	reg []uint8
}

func newHLL() *hll {
	const p = 14
	return &hll{p: p, reg: make([]uint8, 1<<p)}
}

// add folds a 64-bit hash into the sketch.
func (h *hll) add(x uint64) {
	idx := x >> (64 - h.p)
	// The remaining bits determine the register's rank: 1 + the number of
	// leading zeros in the bits below the index, counted within the 64-p window.
	w := x << h.p
	rank := uint8(bits.LeadingZeros64(w)) + 1
	if r := uint8(64 - h.p); rank > r+1 {
		rank = r + 1
	}
	if rank > h.reg[idx] {
		h.reg[idx] = rank
	}
}

// estimate returns the approximate distinct count.
func (h *hll) estimate() float64 {
	m := float64(len(h.reg))
	alpha := alphaFor(len(h.reg))
	var sum float64
	var zeros int
	for _, r := range h.reg {
		sum += 1.0 / float64(uint64(1)<<r)
		if r == 0 {
			zeros++
		}
	}
	est := alpha * m * m / sum
	switch {
	case est <= 2.5*m && zeros > 0:
		// Small-range correction: linear counting.
		est = m * math.Log(m/float64(zeros))
	case est > (1.0/30.0)*math.Exp2(32):
		// Large-range correction for 32-bit hash spaces; harmless for 64-bit.
		est = -math.Exp2(32) * math.Log(1-est/math.Exp2(32))
	}
	return est
}

// alphaFor returns the HLL bias constant for m registers.
func alphaFor(m int) float64 {
	switch m {
	case 16:
		return 0.673
	case 32:
		return 0.697
	case 64:
		return 0.709
	default:
		return 0.7213 / (1 + 1.079/float64(m))
	}
}

// hashU64 mixes a 64-bit value with the splitmix64 finalizer, a fast hash with
// good avalanche behavior.
func hashU64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// hashBytes hashes a byte slice with FNV-1a then runs the splitmix64 finalizer
// so the result avalanches well across the high bits HLL indexes on.
func hashBytes(b []byte) uint64 {
	const (
		offset = 1469598103934665603
		prime  = 1099511628211
	)
	h := uint64(offset)
	for _, c := range b {
		h ^= uint64(c)
		h *= prime
	}
	return hashU64(h)
}
