// Package vector holds the pure math for dense-vector search (spec 2063 doc 15
// §12.3): the distance kernels and the small helpers the hnsw and quantize
// packages build on. It has no I/O and no graph logic, so it is the one place a
// future assembly or SIMD stub could slot in without disturbing the layers above.
//
// The float32 kernels are written as plain range loops. The Go compiler turns
// these into vectorized code on amd64 and arm64, so there is no hand-written
// assembly here; keeping them in one package marks the seam where assembly would
// go if a profile ever called for it.
package vector

import "math"

// Dot returns the dot product of two equal-length float32 vectors.
func Dot(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

// L2Sq returns the squared Euclidean distance between two equal-length vectors.
// The square root is skipped because monotonic ordering by distance is all the
// search needs, and the squared form avoids a per-call sqrt.
func L2Sq(a, b []float32) float32 {
	var sum float32
	for i := range a {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

// Norm returns the L2 norm (magnitude) of a vector.
func Norm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

// Normalize returns a unit-length copy of v and the original norm. A zero vector
// (norm 0) is returned as a copy unchanged with norm 0; callers that require a
// direction (cosine) must reject the zero vector before calling.
func Normalize(v []float32) ([]float32, float64) {
	n := Norm(v)
	out := make([]float32, len(v))
	if n == 0 {
		copy(out, v)
		return out, 0
	}
	inv := float32(1.0 / n)
	for i, x := range v {
		out[i] = x * inv
	}
	return out, n
}

// NormalizeInto writes a unit-length copy of v into dst (which must be the same
// length) and returns the original norm. It avoids an allocation on the hot
// query path.
func NormalizeInto(dst, v []float32) float64 {
	n := Norm(v)
	if n == 0 {
		copy(dst, v)
		return 0
	}
	inv := float32(1.0 / n)
	for i, x := range v {
		dst[i] = x * inv
	}
	return n
}

// DotInt8 returns the integer dot product of two equal-length int8 vectors. It
// is the kernel a quantized distance path uses; the compiler vectorizes it with
// widening multiply-accumulate on both supported architectures.
func DotInt8(a, b []int8) int32 {
	var sum int32
	for i := range a {
		sum += int32(a[i]) * int32(b[i])
	}
	return sum
}

// DotPQ sums one product-quantization distance from the precomputed asymmetric
// table: m subspaces, each contributing table[s*256 + codes[s]] (spec 2063 doc
// 15 §12.3). It is m table lookups and m additions.
func DotPQ(codes []uint8, table []float32, m int) float32 {
	var sum float32
	for s := range m {
		sum += table[s*256+int(codes[s])]
	}
	return sum
}

// HasNaNOrInf reports whether v contains a NaN or an infinity. The ingest path
// rejects such vectors before they reach the index (doc 15 §22.1, ErrNaN).
func HasNaNOrInf(v []float32) (index int, value float32, bad bool) {
	for i, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return i, x, true
		}
	}
	return 0, 0, false
}
