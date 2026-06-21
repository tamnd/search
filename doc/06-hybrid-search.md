# Hybrid search

A hybrid query runs a text query and a kNN query independently, then fuses their ranked results with Reciprocal Rank Fusion.
It is the right tool when keyword relevance and vector similarity each catch documents the other misses, and you want one ranked list that respects both.

This builds on [querying](03-querying.md) and [vector search](05-vector-search.md); read those first if the text and vector sides are new to you.

## Why RRF

The text side scores with BM25, the vector side scores with a distance metric.
Those scores are not comparable, so you cannot just add them.
Reciprocal Rank Fusion sidesteps the problem by ignoring the raw scores and using only the rank position in each list: each list contributes `1 / (rrfK + rank)` to a document's fused score, with rank counted from 1.
A document that ranks high in either list rises; a document that ranks high in both rises further.

## Building a hybrid query

`query.Hybrid` takes a text query, a kNN query, and the final number of hits.

```go
import "github.com/tamnd/search/query"

text := query.Match("title", "running shoes")
knn := query.KNN("embedding", queryVec, 50)

h := query.Hybrid(text, knn, 10)
hits, err := db.Search(h, 10)
```

The text side can be any text query tree, including a bool with filters.
The kNN side is a full `KNNQuery`, so its `NumCandidates` and `Filter` apply exactly as in a standalone kNN search.

### The fusion constant

`RRFK` is the fusion constant.
Zero uses the default of 60.
A larger constant flattens the contribution of rank, so top positions matter a little less; the default of 60 is the conventional starting point.

```go
h := query.Hybrid(text, knn, 10)
h.RRFK = 60
```

## How the two sides are sized

The engine ranks each side to a window before fusing.
The window is the hybrid `K` when set, otherwise the `k` you pass to `Search`.
Both sides are ranked to that window, fused, and the fused list is trimmed to the window.
So asking for a larger `K` gives the fusion more candidates to work with from each side, at some extra cost.

## From the CLI

`sx hybrid` takes the text query as a positional string and the vector side through flags.

```
sx hybrid vecs.sx --field embedding --vector @query.json 'running shoes'
```

The full flag set:

```
--text-field f       default field for bare terms in the text query
--field f            dense_vector field to search
--vector '...'       query vector: comma/space floats, or @file
--k n                number of hits to return (default 10)
--rrf-k n            RRF fusion constant (default 60)
--num-candidates n   per-segment efSearch for the vector side (0 uses field default)
--fields a,b         stored fields to include in each hit
--format ...         table | json | jsonl
```

A worked example:

```
sx hybrid vecs.sx \
  --field embedding --vector @query.json \
  --text-field title --k 20 --rrf-k 60 --num-candidates 200 \
  --format json \
  'lightweight running shoes'
```

The text query string uses the same compact syntax as `sx query`, so you can scope fields, require terms, and add ranges on the text side while the vector side handles semantic similarity.
