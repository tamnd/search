# Facets and sorting

Plain search ranks by relevance and returns the top k.
When you need to sort by a field, count documents into buckets, or collapse duplicates, you use the request API, which reads the columnar doc-values built alongside the inverted index.

## Doc-values fields

Sorting and faceting do not read the inverted index; they read a per-field column called a doc-value.
A field carries doc-values when its `DocValues` option is set, which is the default for keyword, numeric, date, boolean, and geo fields.
Text fields do not carry doc-values (you sort and facet on a keyword, not on analyzed text).

So a schema that you intend to sort and facet on looks like this, and you get the columns for free from the defaults:

```go
s := schema.New()
s.Add(schema.NewField("title", schema.TypeText))     // searchable
s.Add(schema.NewField("brand", schema.TypeKeyword))  // facet + sort
s.Add(schema.NewField("price", schema.TypeDouble))   // sort + range facet
s.Add(schema.NewField("created", schema.TypeDate))   // sort + histogram
```

## The SearchRequest API

`SearchRequestExec` runs a full request: matching, optional sort or collapse, and single-pass aggregations.
`Query` and `K` are required; the rest are optional.

```go
req := search.SearchRequest{
	Query: query.Match("title", "running"),
	K:     10,
	Sort:  []search.SortKey{{Field: "price", Desc: false}},
	Aggs: map[string]search.AggSpec{
		"by_brand": {Kind: "terms", Field: "brand", Size: 5},
	},
}
res, err := db.SearchRequestExec(req)
```

`res.Hits` holds the ranked hits and `res.Aggs` holds the aggregation results keyed by the names you gave.
When a request asks for neither sort, aggregations, nor collapse, the engine falls back to the plain score-ranked top-k path, so the request API is never slower than `Search` for the simple case.

## Sorting

A `SortKey` is one level of a sort specification.

```go
type SortKey struct {
	Field       string     // "" or "_score" sorts by relevance
	Desc        bool       // reverse the order
	Mode        string     // multi-valued reduction: min, max, avg, sum, median
	MissingLast bool       // documents without a value go after those with one
	Origin      *GeoPoint  // sort a geo_point field by distance to this point
}
```

Pass several keys for tie-breaking; they apply in order.

```go
req.Sort = []search.SortKey{
	{Field: "brand"},                 // ascending by brand
	{Field: "price", Desc: true},     // then by price, high to low
	{Field: "_score", Desc: true},    // then by relevance
}
```

For a multi-valued numeric field, `Mode` reduces the values to one for comparison (default min ascending, max descending).
For a `geo_point` field, set `Origin` to sort by great-circle distance to that point.

From the CLI, `--sort` takes comma-separated keys, each `field[:asc|desc][:missing_last]`:

```
sx query products.sx running --sort 'price:desc,brand:asc'
sx query products.sx running --sort '_score:desc'
```

## Aggregations

An `AggSpec` describes one aggregation.
`Kind` is one of `terms`, `histogram`, `range`, `min`, `max`, `sum`, `avg`, `count`, `stats`, `cardinality`, or `percentiles`.
The other fields apply to the kinds that use them.

```go
req.Aggs = map[string]search.AggSpec{
	"by_brand":   {Kind: "terms", Field: "brand", Size: 10},
	"price_hist": {Kind: "histogram", Field: "price", Interval: 50},
	"price_pcts": {Kind: "percentiles", Field: "price", Percents: []float64{50, 95, 99}},
	"avg_price":  {Kind: "avg", Field: "price"},
}
```

Reading the results back:

```go
for _, b := range res.Aggs["by_brand"].Buckets {
	fmt.Printf("%v: %d\n", b.Key, b.Count)
}
fmt.Println(res.Aggs["avg_price"].Value)        // single-value metrics
fmt.Println(res.Aggs["price_pcts"].Values)      // multi-value metrics
```

A bucketed aggregation (terms, histogram, range) fills `Buckets`.
A single-value metric (min, max, sum, avg, count, cardinality) fills `Value`.
A multi-value metric (stats, percentiles) fills `Values`, a name-to-number map.

Terms aggregations can nest sub-aggregations through `Sub`, and you can order a terms agg by key instead of count with `ByKey`.

The CLI exposes a subset through `--facet`, semicolon-separated, each `name=kind:field[:opts]`:

```
sx query products.sx running --facet 'by_brand=terms:brand:5;price_hist=histogram:price:50'
sx query products.sx running --facet 'pcts=percentiles:price:50|95|99'
sx query products.sx running --facet 'avg=avg:price'
```

The CLI covers terms, histogram, the single-value metrics, cardinality, and percentiles.
Range facets and nested aggregations are library-only; build those with `SearchRequestExec`.

## Collapsing

Set `Collapse` to a keyword field to keep only the top hit per distinct value of that field, which dedupes results without a post-pass.

```go
req.Collapse = "brand" // one hit per brand, the best-scoring one
```

```
sx query products.sx running --collapse brand
```

Documents with no value for the collapse field each form their own group, so they are never merged together.

## Combining everything

Sort, facets, and collapse compose in one request, and the CLI mirrors that.

```
sx query products.sx running \
  --sort 'price:asc' \
  --facet 'by_brand=terms:brand:10' \
  --collapse brand \
  --size 20 --format json
```

See [querying](03-querying.md) for the query side, and [vector search](05-vector-search.md) for filtered kNN, which uses a query as a pre-filter.
