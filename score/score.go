// Package score implements relevance scoring (spec 2063 doc 13). The default and
// only model in S4 is Okapi BM25 with Lucene-style length normalization:
//
//	BM25(Q, D) = sum_t [ IDF(t) * TF_sat(t, D) ]
//	IDF(t)     = log( 1 + (N - n + 0.5) / (n + 0.5) )
//	TF_sat     = freq / ( freq + k1 * (1 - b + b * dl/avgdl) )
//
// where N is the collection document count, n the term's document frequency, dl
// the field length of D in tokens, and avgdl the average field length. The IDF
// uses the +1 Lucene variant so the contribution is always non-negative.
//
// Per query term the costly parts (IDF, and the length-independent pieces of the
// denominator) are precomputed once into a Weight; the per-document hot loop in
// Score then does a norm-table lookup plus a few float operations.
package score

import "math"

// BM25 default parameters (doc 13 §2.2).
const (
	DefaultK1 = 1.2
	DefaultB  = 0.75
)

// IDF returns the Lucene BM25 inverse document frequency for a term that appears
// in n of N documents: log(1 + (N - n + 0.5)/(n + 0.5)). It is non-negative for
// all 0 <= n <= N.
func IDF(n, N int64) float64 {
	return math.Log(1 + (float64(N)-float64(n)+0.5)/(float64(n)+0.5))
}

// Weight holds the per-term, per-collection constants needed to score a term's
// postings. It is created once for a query term and reused across every segment
// and document. The zero value is not usable; build one with NewWeight.
type Weight struct {
	idf         float32 // log(1 + (N-n+0.5)/(n+0.5)), times the query boost
	k1          float32
	k1OneMinusB float32 // k1 * (1 - b)
	k1B         float32 // k1 * b
	avgdl       float32 // average field length in tokens; 0 means norms absent
}

// NewWeight builds a scoring weight for a term with document frequency n in a
// collection of N documents whose field has the given average length avgdl. The
// boost multiplies the term's IDF (a boost of 1 is the neutral default). k1 and b
// are the BM25 parameters; pass DefaultK1 and DefaultB for the standard model.
// An avgdl <= 0 disables length normalization (every document scores as if its
// length equalled avgdl), which is the correct behavior when a field omits norms.
func NewWeight(n, N int64, avgdl, boost, k1, b float64) *Weight {
	return &Weight{
		idf:         float32(IDF(n, N) * boost),
		k1:          float32(k1),
		k1OneMinusB: float32(k1 * (1 - b)),
		k1B:         float32(k1 * b),
		avgdl:       float32(avgdl),
	}
}

// IDFValue returns the (boosted) IDF component of the weight, exposed for
// explanations and tests.
func (w *Weight) IDFValue() float32 { return w.idf }

// Score returns the BM25 contribution of a document with term frequency freq and
// the given length norm byte. When the weight has no average length (avgdl <= 0)
// the document is scored with a normalized length of 1.
func (w *Weight) Score(freq uint32, norm byte) float32 {
	if freq == 0 {
		return 0
	}
	f := float32(freq)
	var normalizedDL float32 = 1
	if w.avgdl > 0 {
		normalizedDL = float32(NormLength(norm)) / w.avgdl
	}
	denom := f + w.k1OneMinusB + w.k1B*normalizedDL
	return w.idf * f / denom
}

// MaxScore returns the largest score the weight can produce given the maximum
// term frequency and minimum field length observed over a set of documents (a
// smaller field length yields a larger score). It is the exact block-max upper
// bound used by WAND-style pruning (doc 13 §1.6).
func (w *Weight) MaxScore(maxFreq uint32, minNorm byte) float32 {
	return w.Score(maxFreq, minNorm)
}
