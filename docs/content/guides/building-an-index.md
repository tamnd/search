---
title: "Building an index"
description: "Define a schema, pick field types and analyzers, index in batches, replace and delete documents, and reclaim space."
weight: 10
---

An index is a schema plus the documents you put through it. This guide covers defining the schema, picking field types and analyzers, indexing in batches, replacing and deleting documents, and reclaiming space.

## The schema

A schema is an ordered list of typed fields and one primary-key field. It is stored inside the file, so reopening an index reconstructs the exact field types and per-field options it was written with. Build one with `schema.New`, add fields, then store it with `PutSchema`.

```go
import "github.com/tamnd/search/schema"

s := schema.New()                 // primary key defaults to "_id"
s.IDField = "sku"                 // or set your own
s.Add(schema.NewField("title", schema.TypeText))
s.Add(schema.NewField("brand", schema.TypeKeyword))
s.Add(schema.NewField("price", schema.TypeDouble))
db.PutSchema(s)
```

`NewField` gives a field the default options for its type. `Add` rejects an empty or duplicate name, a name over 512 bytes, and an unknown type.

A schema is frozen once any document is indexed. After that you may add new fields (older documents simply lack a value for them), but you may not change an existing field's type, remove a field, or change the primary key. Trying to do so returns `schema.ErrSchemaFrozen`.

## Field types

| Type | Constant | Indexed for | Notes |
|---|---|---|---|
| text | `TypeText` | full-text match, phrase, prefix | analyzed; keeps positions |
| keyword | `TypeKeyword` | exact term, prefix, range | not analyzed; doc-values on |
| long | `TypeLong` | range, sort, facets | 64-bit signed integer |
| double | `TypeDouble` | range, sort, facets | 64-bit float |
| boolean | `TypeBoolean` | term, range | true / false |
| date | `TypeDate` | range, sort, facets | RFC3339 in, stored as unix nanos |
| geo_point | `TypeGeoPoint` | geo distance, geo sort | WGS-84 lat/lon |
| dense_vector | `TypeDenseVector` | kNN | see [vector search](/guides/vector-search/) |
| stored | `TypeStored` | nothing | retrieval-only blob |

The per-field knobs live in `FieldOptions`: `Indexed`, `Stored`, `DocValues`, `Positions`, `TermVectors`, and `Analyzer`. `DefaultOptions(t)` applies the type-appropriate defaults, which is what `NewField` uses. Text fields keep positions (so phrases work); keyword, numeric, date, boolean, and geo fields carry doc-values (so [facets and sorting](/guides/facets-and-sorting/) work). A `stored` field is never indexed; it just rides along for retrieval.

## Analyzers

A text field is analyzed into terms at index time and query time. With no explicit analyzer a text field uses the standard analyzer; a non-text field is not analyzed and yields a single keyword token.

Set an analyzer per field through its options.

```go
f := schema.NewField("body", schema.TypeText)
f.Opts.Analyzer = "english"
s.Add(f)
```

To use a custom pipeline, register it once by name and reference it from a field.

```go
import "github.com/tamnd/search/analysis"

db.PutAnalyzer(analysis.AnalyzerConfig{
	Name:      "my_text",
	// tokenizer, filters, and so on per analysis.AnalyzerConfig
})
```

`PutAnalyzer` validates the configuration by building it once before storing it, so a bad config fails fast. You can preview what an analyzer produces without indexing anything.

```go
toks, _ := db.Analyze("standard", "Quick Brown Foxes")
toks, _ := db.AnalyzeField("body", "Quick Brown Foxes") // uses the field's analyzer
```

From the CLI:

```
sx analyze products.sx --analyzer standard "Quick Brown Foxes"
sx analyze products.sx --field title "Quick Brown Foxes" --format json
```

## Batch indexing

`Index` takes a slice of documents and writes them in one transaction, flushing a single immutable segment over the batch. Index in batches, not one document at a time: each call is a commit, and one segment per batch keeps the segment count down.

```go
n, err := db.Index([]map[string]any{
	{"sku": "p1", "title": "Red running shoes", "brand": "acme", "price": 79.0},
	{"sku": "p2", "title": "Blue hiking boots", "brand": "acme", "price": 119.0},
})
```

If a batch repeats an external id, the last occurrence wins and the earlier ones are not indexed. A document with no primary-key value is assigned a fresh external id equal to its internal doc-id.

From the CLI, `sx index` loads the whole JSONL file into memory first, while `sx import` streams it in fixed-size batches so a multi-gigabyte dump indexes with bounded memory.

```
sx index products.sx --file docs.jsonl
sx import products.sx --file big.jsonl --batch 5000
```

Both accept `--id-field` to set the primary key on an index that has no schema yet, so you can index without a separate create step.

```
sx import products.sx --file big.jsonl --id-field sku
```

## Updates

There is no separate update call. Re-indexing a document whose external id already exists is a replace: the old version is soft-deleted (its stored body dropped) and the new version is indexed under a fresh internal doc-id.

```go
db.Index([]map[string]any{
	{"sku": "p1", "title": "Red running shoes v2", "price": 69.0},
})
```

The CLI exposes this as `sx update` for clarity, but it is `Index` underneath with replace-oriented reporting.

```
sx update products.sx --file changed.jsonl
```

## Deletes

`Delete` removes a document by external id and reports whether it existed. The delete is soft: the postings stay in their immutable segment until compaction reaps them, but the document is no longer returned by queries or by `GetByExternalID`, and its stored body is dropped.

```go
existed, err := db.Delete("p2")
```

```
sx delete products.sx p2 p7 p9
```

A missing id is reported but does not fail the batch.

## Reading documents back

Fetch a stored document by external id or by internal doc-id.

```go
doc, err := db.GetByExternalID("p1")
doc, err := db.GetByDocID(42)
```

```
sx get products.sx --id p1
sx get products.sx 42
```

## Compaction and vacuum

Deletes and replaces leave tombstones behind. Compaction merges segments into one new segment that omits every deleted document, then recomputes the index-wide statistics. The whole round runs in a single transaction, so readers see either all the old segments or the one merged segment, never a half state.

```go
merged, err := db.Compact()    // one tiered round; 0 when no tier is over threshold
merged, err := db.CompactAll() // force-merge every segment into one
```

```
sx compact products.sx          # one tiered round
sx compact products.sx --all    # force-merge everything
sx vacuum products.sx           # alias for compact --all under an operational name
```

The single-file layout reuses freed pages through an internal freelist rather than truncating, so the file does not necessarily shrink on disk. What vacuum guarantees is that deleted documents stop costing query time and their pages return to the freelist for reuse. See [operations](/guides/operations/) for monitoring when compaction is due.

## Inspecting the result

`sx inspect` dumps the segment structure: each segment with its document count and per-field term and posting statistics.

```
sx inspect products.sx
sx schema products.sx           # print the stored schema as JSON
```

The Go equivalents are `db.Segments()` and `db.Schema()`.
