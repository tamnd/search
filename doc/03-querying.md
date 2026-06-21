# Querying

A query is a tree of typed nodes.
You build the tree directly in Go, or you parse it from one of two textual forms: the compact query string and the JSON query DSL.
This page covers the node types, the two textual forms, and the `sx query` command.

## The query model

Every node lives in package `github.com/tamnd/search/query` and carries a boost (default 1).
The core constructors:

```go
import "github.com/tamnd/search/query"

query.Term("status", "open")            // exact term, not analyzed
query.Match("title", "quick fox")       // analyzed text, OR-combined by default
query.Phrase("title", "quick fox")      // tokens in order, zero slop
query.Prefix("title", "qui")            // terms beginning with the prefix
query.Range("price", "10", "100", true, false) // [10, 100): include lower, exclude upper
query.MatchAll()                        // every document, constant score
query.MatchNone()                       // nothing
```

A few notes that matter in practice:

- `Term` is not analyzed.
The term must already be in indexed form, which is why it suits keyword fields and exact values.
For analyzed text use `Match`.
- `Match` combines its analyzed terms with OR by default.
Set `m.Operator = query.Must` for AND.
- `Phrase` defaults to zero slop (exact adjacency).
Set `ph.Slop` to allow gaps.
- `Range` bounds are textual and encoded to the field's term form by the planner, so the same node works for keyword, numeric, date, and boolean fields.
An empty bound is open.

### Boolean composition

`Bool` combines clauses by how they must occur.
A document matches when every Must and Filter clause matches, no MustNot clause matches, and at least `minimum_should_match` of the Should clauses match.

```go
q := query.Bool().
	MustClause(query.Match("title", "running shoes")).
	FilterClause(query.Term("category", "footwear")).
	ShouldClause(query.Match("brand", "acme")).
	MustNotClause(query.Term("discontinued", "true"))
```

`MustClause`, `ShouldClause`, `MustNotClause`, and `FilterClause` are convenience adders; `Add(occur, sub)` is the general form.
The difference between Must and Filter is scoring: a Must clause contributes to the BM25 score, a Filter clause must match but contributes nothing, which makes it the right choice for structured constraints.

The default `minimum_should_match` is 1 when a query has only Should clauses, otherwise 0.
Override it with `SetMinimumShouldMatch(n)`.

### Boosts

Every node has a boost that multiplies its score contribution.

```go
query.Match("title", "running").WithBoost(2.0)
```

`WithBoost` returns a copy, so it composes cleanly inside a bool tree.

### Running a query

```go
hits, err := db.Search(q, 10) // top 10 by score
```

`Search` resolves each hit's stored body.
For sort, facets, or collapse instead of pure score ranking, use the request API in [facets and sorting](04-facets-and-sorting.md).

## The query string form

`SearchString` parses the compact syntax and runs it.
Bare terms target the default field you pass.

```go
hits, err := db.SearchString(`+running -boots category:footwear`, "title", 10)
```

The grammar is small:

```
term                a bare term, OR-combined with its siblings
+term               a required term (must)
-term               a prohibited term (must_not)
"a b c"             a phrase
field:term          a term scoped to a field
field:"a b"         a phrase scoped to a field
field:val*          a prefix scoped to a field
field:[lo TO hi]    an inclusive range; {lo TO hi} is exclusive; brackets mix
AND OR NOT          uppercase boolean operators between terms
```

A range bound of `*` is open, so `price:[10 TO *]` means at least 10.
An empty or whitespace-only string parses to a match-none query.

```
sx query products.sx '+running -boots category:footwear'
sx query products.sx 'price:[10 TO 100}'
sx query products.sx '"red running shoes"'
```

## The JSON query form

`SearchJSON` parses the JSON DSL.
Each object has exactly one key naming the query type, plus an optional `boost`.

```json
{"match": {"field": "title", "query": "quick fox", "operator": "and"}}
```

```go
hits, err := db.SearchJSON([]byte(`{"match":{"field":"title","query":"quick fox"}}`), 10)
```

The supported shapes:

```json
{"term":         {"field": "status", "value": "open"}}
{"match":        {"field": "title", "query": "quick fox", "operator": "and"}}
{"match_phrase": {"field": "title", "query": "quick fox", "slop": 1}}
{"prefix":       {"field": "title", "value": "qui"}}
{"range":        {"field": "price", "gte": "10", "lt": "100"}}
{"bool":         {"must": [...], "should": [...], "must_not": [...],
                  "filter": [...], "minimum_should_match": 1}}
{"match_all":    {}}
{"match_none":   {}}
```

Range uses `gt`/`gte` for the lower bound and `lt`/`lte` for the upper.
A `boost` key on any object multiplies that node's score, either as a sibling (`{"term": {...}, "boost": 2}`) or wherever the body accepts it.

The CLI reads a JSON query from a file with `--json`.

```
sx query products.sx --json query.json --size 20
```

You must give exactly one of a query string or `--json`.

## Useful query flags

`sx query` (aliased as `sx search`) carries the common knobs:

```
--field f        default field for bare terms in the query string
--size n         number of hits to return (default 10)
--from n         offset for pagination
--fields a,b     stored fields to include in each hit
--format ...     table | json | jsonl
--explain        include a per-hit score explanation
```

Pagination is `--from` plus `--size`.
The table output shows score, id, and the first text field; JSON and JSONL always include `_id` and `_score` per hit.

For sort, facet, and collapse flags, see [facets and sorting](04-facets-and-sorting.md).
For vector and hybrid queries, see [vector search](05-vector-search.md) and [hybrid search](06-hybrid-search.md).
