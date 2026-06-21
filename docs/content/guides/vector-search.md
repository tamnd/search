---
title: "Vector search"
description: "Define a dense_vector field, index vectors, run kNN queries, tune the HNSW graph, quantize, and filter."
weight: 40
---

A `dense_vector` field holds a fixed-dimension float32 vector per document. You search it for the documents whose vector is nearest to a query vector, approximately (over an HNSW graph) or exactly. This guide covers defining the field, indexing vectors, running kNN queries, tuning the search, and filtering.

## Defining a dense_vector field

A dense_vector field needs at least a dimension. `NewField` applies the vector defaults: cosine metric, float32 elements, no quantization, an HNSW graph with M=16 and efConstruction=100, and a default efSearch (NumCandidates) of 100.

```go
import "github.com/tamnd/search/schema"

f := schema.NewField("embedding", schema.TypeDenseVector)
f.Opts.Dims = 768
f.Opts.Metric = schema.MetricCosine // or MetricDot, MetricL2
s.Add(f)
```

The metric constants are `MetricCosine` (`"cosine"`), `MetricDot` (`"dot_product"`), and `MetricL2` (`"l2"`). Dimension must be between 1 and 4096.

### Quantization

Quantization stores a compact sidecar alongside the float32 graph. The modes are `QuantNone`, `QuantInt8`, `QuantInt8Rerank`, `QuantPQ`, and `QuantPQRerank`.

```go
f.Opts.Quantization = schema.QuantInt8
```

The graph is built and searched in float32 for recall quality; the quantized codes are trained and persisted as the compact representation. A corpus too small to train a PQ codebook degrades gracefully to no quantization rather than failing the flush, so the float32 graph still serves queries.

### Index parameters

The HNSW build and query knobs live in the same options:

```go
f.Opts.Index = true            // build an HNSW graph; false means exact scan only
f.Opts.M = 16                  // HNSW M, in 4..64
f.Opts.EfConstruction = 200    // build-time candidate list, must be >= M
f.Opts.NumCandidates = 100     // default query-time efSearch
```

With `Index` false there is no graph and kNN falls back to an exact scan, which is fine for small corpora and gives exact results.

From the CLI, vector fields are configured in the schema JSON:

```json
{
  "fields": [
    {"name": "embedding", "type": "dense_vector", "dims": 768,
     "metric": "cosine", "quantization": "int8",
     "m": 16, "ef_construction": 200, "num_candidates": 100}
  ]
}
```

## Indexing vectors

A vector is just a field value in the document. It can be a `[]float32`, a `[]float64`, or a JSON array of numbers (the form JSONL decodes to).

```go
db.Index([]map[string]any{
	{"_id": "d1", "title": "running shoes", "embedding": []float32{0.12, 0.04, /* ... 768 values */}},
})
```

A document missing the field is skipped. A vector of the wrong dimension, or one holding NaN or Inf, is rejected so it cannot poison the graph.

From the CLI you index vectors the same way you index any document, as JSONL with the vector as a JSON array:

```
echo '{"_id":"d1","title":"running shoes","embedding":[0.12,0.04, ...]}' | sx index vecs.sx
```

## KNN queries

Build a `KNNQuery` with the field, the query vector, and how many neighbors you want.

```go
import "github.com/tamnd/search/query"

knn := query.KNN("embedding", queryVec, 10)
hits, err := db.Search(knn, 10)
```

The query vector must match the field's dimension, and under the cosine metric it must not be a zero vector. Results are merged across every live segment into one global top-k by score, with deleted documents excluded.

From the CLI:

```
sx knn vecs.sx --field embedding --vector '0.12,0.04, ...' --k 10
sx knn vecs.sx --field embedding --vector @query.json --k 10
```

The `--vector` flag takes comma or space separated floats, or `@path` to read them from a file (which may hold a JSON array).

## Tuning: efSearch and NumCandidates

`NumCandidates` is the per-segment efSearch, the size of the candidate list the graph explores during search. Higher means better recall and higher latency. Zero uses the field default.

```go
knn := query.KNN("embedding", queryVec, 10)
knn.NumCandidates = 200 // explore more, recall up, latency up
```

```
sx knn vecs.sx --field embedding --vector @q.json --k 10 --num-candidates 200
```

efSearch is clamped up to at least `k`, so you never explore fewer candidates than the neighbors you asked for.

## Filtered kNN

Set `Filter` to a query and only documents it matches are eligible neighbors (filtered ANN). The filter is collected as a full match set, then used to gate the graph traversal.

```go
knn := query.KNN("embedding", queryVec, 10)
knn.Filter = query.Term("category", "footwear")
hits, err := db.Search(knn, 10)
```

From the CLI, `--filter` takes a compact query string:

```
sx knn vecs.sx --field embedding --vector @q.json --k 10 --filter 'category:footwear'
```

## Combining text and vectors

To blend a text query with a kNN query rather than filter one by the other, use a hybrid query with RRF fusion. See [hybrid search](/guides/hybrid-search/).
