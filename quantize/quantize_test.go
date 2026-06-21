package quantize

import (
	"math"
	"testing"

	"github.com/tamnd/search/vector"
)

func corpus(n, d int, seed uint64) [][]float32 {
	r := newSplitMix(seed)
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

func TestInt8_RoundTripApprox(t *testing.T) {
	vecs := corpus(200, 32, 1)
	q := TrainInt8(vecs)
	var maxErr float64
	for _, v := range vecs {
		dec := q.Decode(q.Encode(v))
		for i := range v {
			e := math.Abs(float64(v[i] - dec[i]))
			if e > maxErr {
				maxErr = e
			}
		}
	}
	// One step of the 256-level grid over a range of about 2 is ~0.008; allow a
	// comfortable margin for rounding.
	if maxErr > 0.02 {
		t.Fatalf("int8 round-trip max error %.4f too high", maxErr)
	}
}

func TestInt8_Marshal(t *testing.T) {
	q := Int8Quantizer{Min: -1.5, Scale: 0.01}
	got, err := UnmarshalInt8(q.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got != q {
		t.Fatalf("int8 frame round trip: got %+v want %+v", got, q)
	}
}

func TestInt8_DotApprox(t *testing.T) {
	vecs := corpus(2, 64, 5)
	q := TrainInt8(vecs)
	a, b := q.Encode(vecs[0]), q.Encode(vecs[1])
	approx := q.Dot(a, b)
	exact := vector.Dot(vecs[0], vecs[1])
	if math.Abs(float64(approx-exact)) > 0.5 {
		t.Fatalf("int8 dot %.4f far from exact %.4f", approx, exact)
	}
}

func TestPQ_EncodeReconstructs(t *testing.T) {
	const d, m = 32, 8
	vecs := corpus(1000, d, 9)
	cb, err := TrainCodebook(vecs, PQConfig{M: m, Dims: d, Sample: 1000, Iters: 25, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The PQ approximate distance should rank a vector closest to itself.
	q := vecs[42]
	table := cb.L2Table(q)
	self := cb.Distance(table, cb.Encode(q))
	var sumOther float64
	for i := 0; i < 50; i++ {
		sumOther += float64(cb.Distance(table, cb.Encode(vecs[i*7])))
	}
	avgOther := sumOther / 50
	if float64(self) >= avgOther {
		t.Fatalf("PQ self distance %.4f not below average other %.4f", self, avgOther)
	}
}

func TestPQ_Marshal(t *testing.T) {
	const d, m = 16, 4
	vecs := corpus(500, d, 13)
	cb, err := TrainCodebook(vecs, PQConfig{M: m, Dims: d, Sample: 500, Iters: 10, Seed: 2})
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalCodebook(cb.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got.M != cb.M || got.Dims != cb.Dims || len(got.Centroids) != len(cb.Centroids) {
		t.Fatalf("codebook header mismatch after round trip")
	}
	for i := range cb.Centroids {
		if got.Centroids[i] != cb.Centroids[i] {
			t.Fatalf("centroid %d differs after round trip", i)
		}
	}
}

func TestPQ_RejectsBadDims(t *testing.T) {
	if _, err := TrainCodebook(corpus(10, 10, 1), PQConfig{M: 3, Dims: 10}); err == nil {
		t.Fatal("expected error when Dims not divisible by M")
	}
}
