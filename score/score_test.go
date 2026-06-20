package score

import (
	"math"
	"testing"
)

func TestNormRoundTripSmall(t *testing.T) {
	// Lengths 0..15 are encoded exactly.
	for n := int32(0); n <= 15; n++ {
		if got := DecodeNorm(EncodeNorm(n)); got != uint32(n) {
			t.Fatalf("len %d round-trips to %d, want exact", n, got)
		}
	}
}

func TestNormMonotoneAndBounded(t *testing.T) {
	// Decoding is never larger than the encoded length (it floors to three
	// significant bits) and the relative error stays under one part in eight.
	for n := int32(1); n < 1<<24; n = n + 1 + n/64 {
		dec := DecodeNorm(EncodeNorm(n))
		if dec > uint32(n) {
			t.Fatalf("len %d decodes to %d, larger than input", n, dec)
		}
		if rel := float64(uint32(n)-dec) / float64(n); rel > 0.125 {
			t.Fatalf("len %d decodes to %d, relative error %.3f exceeds 1/8", n, dec, rel)
		}
	}
}

func TestNormTableMatchesDecode(t *testing.T) {
	for b := range 256 {
		if NormLength(byte(b)) != DecodeNorm(byte(b)) {
			t.Fatalf("table mismatch at byte %d", b)
		}
	}
}

func TestIDFNonNegative(t *testing.T) {
	// Even a term in every document yields a small positive IDF, never negative.
	if v := IDF(1000, 1000); v < 0 {
		t.Fatalf("IDF for an all-docs term = %v, want >= 0", v)
	}
}

// TestBM25Oracle reproduces the fully worked example in doc 13 §2.6.
func TestBM25Oracle(t *testing.T) {
	// N = 1,000,000 docs, term in n = 5,000, avgdl = 80, k1 = 1.2, b = 0.75.
	const (
		N     = 1_000_000
		n     = 5_000
		avgdl = 80.0
	)
	w := NewWeight(n, N, avgdl, 1.0, DefaultK1, DefaultB)

	// The spec computes IDF ~= 5.298.
	if idf := float64(w.IDFValue()); math.Abs(idf-5.298) > 1e-3 {
		t.Fatalf("IDF = %v, want ~5.298", idf)
	}

	// A document with freq = 5 and dl = 120 -> TF_sat = 5/(5+1.65) ~= 0.7519,
	// score ~= 5.298 * 0.7519 ~= 3.984. We encode dl = 120 as a norm byte; the
	// lossy norm makes dl slightly smaller, so we compare against the exact-dl
	// reference within a tolerance that covers the quantization.
	const freq = 5
	dl := DecodeNorm(EncodeNorm(120))
	normFactor := DefaultK1 * (1 - DefaultB + DefaultB*float64(dl)/avgdl)
	tf := freq / (freq + normFactor)
	want := float64(w.IDFValue()) * tf
	got := w.Score(freq, EncodeNorm(120))
	if math.Abs(float64(got)-want) > 1e-4 {
		t.Fatalf("score = %v, want %v (within 1e-4)", got, want)
	}

	// The second worked example: freq = 3, dl = 40 -> TF_sat = 0.8 exactly when
	// dl is exact. dl = 40 encodes exactly (it keeps three significant bits:
	// 40 = 101000b -> top three bits 101 with no remainder), so the norm is exact.
	if DecodeNorm(EncodeNorm(40)) != 40 {
		t.Fatalf("dl 40 should encode exactly, got %d", DecodeNorm(EncodeNorm(40)))
	}
	tf2 := 3.0 / (3.0 + DefaultK1*(1-DefaultB+DefaultB*40.0/avgdl))
	if math.Abs(tf2-0.8) > 1e-9 {
		t.Fatalf("TF_sat for freq 3 dl 40 = %v, want 0.8", tf2)
	}
}

func TestScoreZeroFreq(t *testing.T) {
	w := NewWeight(10, 1000, 50, 1, DefaultK1, DefaultB)
	if s := w.Score(0, EncodeNorm(50)); s != 0 {
		t.Fatalf("zero-freq score = %v, want 0", s)
	}
}

func TestScoreShorterScoresHigher(t *testing.T) {
	w := NewWeight(10, 1000, 50, 1, DefaultK1, DefaultB)
	short := w.Score(3, EncodeNorm(10))
	long := w.Score(3, EncodeNorm(200))
	if short <= long {
		t.Fatalf("short-field score %v should exceed long-field score %v", short, long)
	}
}

func TestBoostScalesScore(t *testing.T) {
	base := NewWeight(10, 1000, 50, 1, DefaultK1, DefaultB)
	boosted := NewWeight(10, 1000, 50, 2, DefaultK1, DefaultB)
	if math.Abs(float64(boosted.Score(3, EncodeNorm(50))-2*base.Score(3, EncodeNorm(50)))) > 1e-5 {
		t.Fatalf("boost of 2 should double the score")
	}
}
