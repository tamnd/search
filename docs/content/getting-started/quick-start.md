---
title: "Quick start"
description: "From an empty terminal to a working full-text query, first as a Go library and then from the sx command line."
weight: 30
---

This walks the core loop: open a file, give it a schema, index a handful of documents, and run a search. We do it twice, once from Go and once from the `sx` binary, against the same kind of tiny book index.

## As a Go library

### 1. Open a file

`search.Open` creates the file if it does not exist and validates the header if it does. The zero `Options` value is the default: the OS filesystem, the default page size, full synchronous durability.

```go
package main

import (
	"fmt"
	"log"

	"github.com/tamnd/search"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
)

func main() {
	db, err := search.Open("books.sx", search.Options{})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
}
```

### 2. Give it a schema

Before you can index, the file needs a schema: an ordered list of typed fields plus a primary-key field, which defaults to `_id`. A `text` field is analyzed and full-text searchable; a `keyword` field is stored and matched whole, the right type for ids, tags, and facet values.

```go
	s := schema.New()
	if err := s.Add(schema.NewField("title", schema.TypeText)); err != nil {
		log.Fatal(err)
	}
	if err := s.Add(schema.NewField("author", schema.TypeKeyword)); err != nil {
		log.Fatal(err)
	}
	if err := db.PutSchema(s); err != nil {
		log.Fatal(err)
	}
```

### 3. Index a few documents

A document is a `map[string]any`. `Index` takes a batch, stores each document keyed by its primary-key value, and flushes one immutable segment over the batch. It returns how many documents were written.

```go
	n, err := db.Index([]map[string]any{
		{"_id": "1", "title": "the go programming language", "author": "donovan"},
		{"_id": "2", "title": "the rust programming language", "author": "klabnik"},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("indexed %d documents", n)
```

A document whose `_id` already exists is replaced: the old version is soft-deleted and the new one indexed.

### 4. Run a search

`query.Term` builds a single-term query against a field. `Search` runs it and returns the top `k` hits ranked by BM25.

```go
	hits, err := db.Search(query.Term("title", "go"), 10)
	if err != nil {
		log.Fatal(err)
	}
	for _, h := range hits {
		fmt.Printf("%s  %.3f  %v\n", h.ExternalID, h.Score, h.Document["title"])
	}
```

Each `Hit` carries the internal doc-id, the external id (`ExternalID`), the BM25 `Score`, and the stored `Document` body. The query for `go` matches document `1` but not document `2`.

## From the command line

The `sx` binary does the same four steps without any Go code.

### 1. Create a file with a schema

Describe the schema as JSON and hand it to `sx create`:

```sh
echo '{"id_field":"_id","fields":[
  {"name":"title","type":"text","analyzer":"english"},
  {"name":"author","type":"keyword"}]}' > schema.json

sx create books.sx --schema schema.json
```

### 2. Index documents

`sx index` reads JSON Lines, one JSON object per line, from `--file` or from stdin:

```sh
printf '%s\n' \
  '{"_id":"1","title":"the go programming language","author":"donovan"}' \
  '{"_id":"2","title":"the rust programming language","author":"klabnik"}' > books.jsonl

sx index books.sx --file books.jsonl
```

### 3. Query

`sx query` takes a query string. Use `--field` to set the field that bare terms target; without it, the first text field in the schema is the default.

```sh
sx query books.sx --field title go
```

```
QUERY: go   HITS: 1   TIME: 210µs

  SCORE    ID               TITLE
  0.288    1                the go programming language
```

Add `--format json` or `--format jsonl` for machine-readable output, `--fields title,author` to project specific stored fields, and `--size` and `--from` to page through results.

## Where to go next

- The [guides](/guides/) cover building an index, the full query model, facets and sorting, vector and hybrid search, the SQL surface, the C ABI, and operations.
- The [CLI reference](/reference/cli/) lists every command and flag.
