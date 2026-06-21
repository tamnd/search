# Getting started

This is an embedded full-text and vector search engine.
It is to search what SQLite is to relational data: a library you link into your process, one ordinary file on disk, no server, no daemon.
You open a `.sx` file, index documents, and run searches straight out of a memory-resident index.

This page gets you from nothing to a working search, twice: once from Go and once from the `sx` command line.

## Install

The library is `github.com/tamnd/search`.
Add it to a Go module the usual way.

```
go get github.com/tamnd/search
```

The CLI is the `sx` binary under `cmd/sx`.
Build it or run it directly from a checkout.

```
go build -o sx ./cmd/sx
# or, without building a binary:
go run ./cmd/sx help
```

The engine is pure Go with no cgo in the core, so `CGO_ENABLED=0` builds work.
The file extension is `.sx` and the library, when built as a shared object, is `libsearch`.

## Open a file from Go

`search.Open` creates the file if it does not exist and validates the header if it does.
The zero `Options` value is the default: the OS filesystem, the default page size, full synchronous durability.

```go
package main

import (
	"log"

	"github.com/tamnd/search"
)

func main() {
	db, err := search.Open("products.sx", search.Options{})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
}
```

Before you can index, the file needs a schema.
A schema is an ordered list of typed fields plus a primary-key field (default `_id`).
See [building an index](02-building-an-index.md) for the full field set; here is the minimum.

```go
import "github.com/tamnd/search/schema"

s := schema.New()
if err := s.Add(schema.NewField("title", schema.TypeText)); err != nil {
	log.Fatal(err)
}
if err := s.Add(schema.NewField("category", schema.TypeKeyword)); err != nil {
	log.Fatal(err)
}
if err := db.PutSchema(s); err != nil {
	log.Fatal(err)
}
```

## Index a few documents

A document is a `map[string]any`.
`Index` takes a batch, persists each document, and flushes one immutable segment over the batch.
It returns how many documents were written.

```go
n, err := db.Index([]map[string]any{
	{"_id": "p1", "title": "Red running shoes", "category": "footwear"},
	{"_id": "p2", "title": "Blue hiking boots", "category": "footwear"},
	{"_id": "p3", "title": "Lightweight running jacket", "category": "apparel"},
})
if err != nil {
	log.Fatal(err)
}
log.Printf("indexed %d documents", n)
```

Each document is keyed by the value of its primary-key field.
A document whose `_id` already exists is replaced: the old version is soft-deleted and the new one is indexed.

## Run one search

`SearchString` parses the compact query string and runs it, returning the top `k` hits ranked by BM25.
Bare terms target the default field you pass.

```go
hits, err := db.SearchString("running", "title", 10)
if err != nil {
	log.Fatal(err)
}
for _, h := range hits {
	log.Printf("%s  score=%.3f  %v", h.ExternalID, h.Score, h.Document["title"])
}
```

Each `Hit` carries the internal doc-id, the external id, the score, and the stored document body.
The query for `running` matches `p1` and `p3` but not `p2`.
For everything you can put in a query, see [querying](03-querying.md).

## The same thing from the CLI

Create a file and set its schema from a JSON definition.

```
cat > schema.json <<'JSON'
{
  "id_field": "_id",
  "fields": [
    {"name": "title", "type": "text"},
    {"name": "category", "type": "keyword"}
  ]
}
JSON

sx create products.sx --schema schema.json
```

Index documents from JSON Lines (one JSON object per line), either from a file or stdin.

```
cat > docs.jsonl <<'JSON'
{"_id": "p1", "title": "Red running shoes", "category": "footwear"}
{"_id": "p2", "title": "Blue hiking boots", "category": "footwear"}
{"_id": "p3", "title": "Lightweight running jacket", "category": "apparel"}
JSON

sx index products.sx --file docs.jsonl
```

Run a query.
The default field is the first text field in the schema, so you can leave `--field` off here.

```
sx query products.sx running
```

```
QUERY: running   HITS: 2   TIME: 210µs

  SCORE    ID               TITLE
  0.288    p1               Red running shoes
  0.288    p3               Lightweight running jacket
```

Add `--format json` or `--format jsonl` for machine-readable output, and `--fields title,category` to project specific stored fields.

## Where to go next

- [Building an index](02-building-an-index.md): field types, analyzers, batch indexing, updates, deletes, compaction.
- [Querying](03-querying.md): the full query model and its two textual forms.
- [Facets and sorting](04-facets-and-sorting.md): aggregations and sort by doc-values.
- [Vector search](05-vector-search.md) and [hybrid search](06-hybrid-search.md): dense-vector kNN and RRF fusion.
- [Operations](09-operations.md): info, stats, verify, backup, restore, repair.
