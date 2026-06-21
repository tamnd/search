package search

import (
	"fmt"
	"math"
	"testing"

	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
)

// vecSplitMix is a tiny deterministic PRNG so the vector tests need no math/rand.
type vecSplitMix struct{ state uint64 }

func (s *vecSplitMix) next() uint64 {
	s.state += 0x9E3779B97F4A7C15
	z := s.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (s *vecSplitMix) float64() float64 {
	return float64(s.next()>>11) / float64(1<<53)
}

// vecCorpus builds n deterministic vectors of d dims in [-1,1].
func vecCorpus(n, d int, seed uint64) [][]float32 {
	r := &vecSplitMix{state: seed}
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

// vecToAny renders a float32 vector as the []any a JSON document would carry.
func vecToAny(v []float32) []any {
	out := make([]any, len(v))
	for i, e := range v {
		out[i] = float64(e)
	}
	return out
}

// vectorSchema builds a schema with a text field, a keyword filter field, and a
// dense_vector field of the given dimension, metric, and quantization.
func vectorSchema(t *testing.T, dims int, metric, quant string) *schema.Schema {
	t.Helper()
	s := schema.New()
	if err := s.Add(schema.NewField("title", schema.TypeText)); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(schema.NewField("tag", schema.TypeKeyword)); err != nil {
		t.Fatal(err)
	}
	vf := schema.NewField("embed", schema.TypeDenseVector)
	vf.Opts.Dims = dims
	vf.Opts.Metric = metric
	vf.Opts.Quantization = quant
	if err := s.Add(vf); err != nil {
		t.Fatal(err)
	}
	return s
}

// bruteTopK returns the exact nearest k doc indices for q under L2.
func bruteTopKL2(corpus [][]float32, q []float32, k int) []int {
	type pair struct {
		id int
		d  float32
	}
	ps := make([]pair, len(corpus))
	for i, v := range corpus {
		var sum float32
		for j := range v {
			d := v[j] - q[j]
			sum += d * d
		}
		ps[i] = pair{id: i, d: sum}
	}
	for i := 0; i < k && i < len(ps); i++ {
		best := i
		for j := i + 1; j < len(ps); j++ {
			if ps[j].d < ps[best].d {
				best = j
			}
		}
		ps[i], ps[best] = ps[best], ps[i]
	}
	out := make([]int, 0, k)
	for i := 0; i < k && i < len(ps); i++ {
		out = append(out, ps[i].id)
	}
	return out
}

func TestKNN_EndToEndRecall(t *testing.T) {
	const n, d, k = 1000, 32, 10
	corpus := vecCorpus(n, d, 1)

	db := openDB(t)
	if err := db.PutSchema(vectorSchema(t, d, schema.MetricL2, schema.QuantNone)); err != nil {
		t.Fatal(err)
	}
	docs := make([]map[string]any, n)
	for i := range corpus {
		docs[i] = map[string]any{"_id": fmt.Sprintf("d%d", i), "embed": vecToAny(corpus[i])}
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}

	queries := vecCorpus(20, d, 99)
	var total float64
	for _, q := range queries {
		hits, err := db.Search(query.KNN("embed", q, k), k)
		if err != nil {
			t.Fatal(err)
		}
		truth := bruteTopKL2(corpus, q, k)
		truthSet := make(map[string]bool, k)
		for _, id := range truth {
			truthSet[fmt.Sprintf("d%d", id)] = true
		}
		hit := 0
		for _, h := range hits {
			if truthSet[h.ExternalID] {
				hit++
			}
		}
		total += float64(hit) / float64(k)
	}
	avg := total / float64(len(queries))
	if avg < 0.90 {
		t.Fatalf("end-to-end kNN recall@%d = %.3f, want >= 0.90", k, avg)
	}
}

func TestKNN_ScoresDescend(t *testing.T) {
	const d, k = 16, 5
	corpus := vecCorpus(200, d, 5)
	db := openDB(t)
	if err := db.PutSchema(vectorSchema(t, d, schema.MetricCosine, schema.QuantNone)); err != nil {
		t.Fatal(err)
	}
	docs := make([]map[string]any, len(corpus))
	for i := range corpus {
		docs[i] = map[string]any{"_id": fmt.Sprintf("d%d", i), "embed": vecToAny(corpus[i])}
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	hits, err := db.Search(query.KNN("embed", corpus[0], k), k)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != k {
		t.Fatalf("got %d hits, want %d", len(hits), k)
	}
	// The first hit is the query vector itself, score ~1 under cosine.
	if hits[0].ExternalID != "d0" {
		t.Fatalf("nearest to a vector should be itself, got %q", hits[0].ExternalID)
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score+1e-6 {
			t.Fatalf("scores not descending at %d: %v > %v", i, hits[i].Score, hits[i-1].Score)
		}
		if math.IsNaN(float64(hits[i].Score)) {
			t.Fatalf("hit %d has NaN score", i)
		}
	}
}

func TestKNN_FilteredOnlyMatches(t *testing.T) {
	const d, k = 24, 10
	corpus := vecCorpus(600, d, 7)
	db := openDB(t)
	if err := db.PutSchema(vectorSchema(t, d, schema.MetricL2, schema.QuantNone)); err != nil {
		t.Fatal(err)
	}
	docs := make([]map[string]any, len(corpus))
	for i := range corpus {
		tag := "odd"
		if i%2 == 0 {
			tag = "even"
		}
		docs[i] = map[string]any{"_id": fmt.Sprintf("d%d", i), "tag": tag, "embed": vecToAny(corpus[i])}
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	knn := query.KNN("embed", corpus[3], k)
	knn.Filter = query.Term("tag", "even")
	hits, err := db.Search(knn, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("filtered kNN returned nothing")
	}
	for _, h := range hits {
		doc, err := db.GetByExternalID(h.ExternalID)
		if err != nil {
			t.Fatal(err)
		}
		if doc["tag"] != "even" {
			t.Fatalf("filtered kNN returned %q with tag %v", h.ExternalID, doc["tag"])
		}
	}
}

func TestHybrid_RRFFusesBothSides(t *testing.T) {
	const d, k = 16, 5
	corpus := vecCorpus(50, d, 3)
	db := openDB(t)
	if err := db.PutSchema(vectorSchema(t, d, schema.MetricCosine, schema.QuantNone)); err != nil {
		t.Fatal(err)
	}
	docs := make([]map[string]any, len(corpus))
	for i := range corpus {
		title := "ordinary document"
		if i == 7 {
			title = "rare keyword unicorn"
		}
		docs[i] = map[string]any{"_id": fmt.Sprintf("d%d", i), "title": title, "embed": vecToAny(corpus[i])}
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	// The text side strongly favors d7; the vector side favors d0 (the query
	// vector itself). RRF should surface both near the top.
	h := query.Hybrid(query.Match("title", "unicorn"), query.KNN("embed", corpus[0], k), k)
	hits, err := db.Search(h, k)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(hits))
	for _, hit := range hits {
		got[hit.ExternalID] = true
	}
	if !got["d7"] {
		t.Fatalf("hybrid result should include the text match d7, got %v", extIDs(hits))
	}
	if !got["d0"] {
		t.Fatalf("hybrid result should include the vector match d0, got %v", extIDs(hits))
	}
}

func TestKNN_QuantizationRoundTrips(t *testing.T) {
	const d, k = 32, 10
	corpus := vecCorpus(400, d, 11)
	for _, quant := range []string{schema.QuantInt8, schema.QuantPQ} {
		t.Run(quant, func(t *testing.T) {
			db := openDB(t)
			if err := db.PutSchema(vectorSchema(t, d, schema.MetricL2, quant)); err != nil {
				t.Fatal(err)
			}
			docs := make([]map[string]any, len(corpus))
			for i := range corpus {
				docs[i] = map[string]any{"_id": fmt.Sprintf("d%d", i), "embed": vecToAny(corpus[i])}
			}
			if _, err := db.Index(docs); err != nil {
				t.Fatal(err)
			}
			// The float32 graph still serves queries; the quantized sidecar is
			// persisted alongside and must not break search.
			hits, err := db.Search(query.KNN("embed", corpus[0], k), k)
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) == 0 || hits[0].ExternalID != "d0" {
				t.Fatalf("quant %s: nearest to a vector should be itself, got %v", quant, extIDs(hits))
			}
		})
	}
}

func TestKNN_VectorOnlyBatch(t *testing.T) {
	const d, k = 16, 3
	corpus := vecCorpus(20, d, 13)
	db := openDB(t)
	if err := db.PutSchema(vectorSchema(t, d, schema.MetricL2, schema.QuantNone)); err != nil {
		t.Fatal(err)
	}
	// Documents with no text or keyword field, only a vector: the batch must
	// still write a segment so the vectors are searchable.
	docs := make([]map[string]any, len(corpus))
	for i := range corpus {
		docs[i] = map[string]any{"_id": fmt.Sprintf("d%d", i), "embed": vecToAny(corpus[i])}
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	hits, err := db.Search(query.KNN("embed", corpus[0], k), k)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].ExternalID != "d0" {
		t.Fatalf("vector-only batch: nearest to a vector should be itself, got %v", extIDs(hits))
	}
}

func TestKNN_DimMismatchRejected(t *testing.T) {
	const d = 16
	db := openDB(t)
	if err := db.PutSchema(vectorSchema(t, d, schema.MetricL2, schema.QuantNone)); err != nil {
		t.Fatal(err)
	}
	docs := []map[string]any{{"_id": "a", "embed": vecToAny(vecCorpus(1, d, 1)[0])}}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Search(query.KNN("embed", make([]float32, d+1), 5), 5); err == nil {
		t.Fatal("expected a dimension-mismatch error")
	}
}
