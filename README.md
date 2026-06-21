# search

An embedded, single-file, full-text and vector search engine for Go.
It is to search what SQLite is to relational data: a library you link into your process, one ordinary file on disk, no server, no cluster, no daemon.
You open a `.sx` file, index documents, and run searches against a memory-resident index with sub-millisecond latency.

The engine is built milestone by milestone against spec 2063, S0 through S9, and every milestone is independently shippable and crash-safe.
S9 is the production-hardening milestone: the full-text and vector paths, the operational tooling, the C ABI, and the performance gate are all in place.

## What you get

- Full-text search with BM25 scoring, term, phrase, prefix, range, and boolean queries, over an inverted index with an FST term dictionary and PFOR-compressed postings.
- Doc-values columns for facet counts and numeric sorting without touching the inverted index.
- Dense-vector search: a `dense_vector` field with cosine, dot-product, or L2 distance, an HNSW graph, optional int8 and product quantization, and filtered kNN.
- Hybrid search that fuses a text query and a kNN query with Reciprocal Rank Fusion.
- A built-in SQL SELECT surface for the people who would rather write SQL than a query tree.
- A C ABI (`-buildmode=c-shared`) so the engine is usable from C, Python, and anything with an FFI.
- Operational tooling: `info`, `stats`, `verify`, `backup`, `restore`, `repair`, `vacuum`, `export`, `import`, and a `bench` load generator.

## Quick start

### As a Go library

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

	if _, err := db.Index([]map[string]any{
		{"_id": "1", "title": "the go programming language", "author": "donovan"},
		{"_id": "2", "title": "the rust programming language", "author": "klabnik"},
	}); err != nil {
		log.Fatal(err)
	}

	hits, err := db.Search(query.Term("title", "go"), 10)
	if err != nil {
		log.Fatal(err)
	}
	for _, h := range hits {
		fmt.Printf("%s  %.3f  %v\n", h.ExternalID, h.Score, h.Document["title"])
	}
}
```

### From the command line

```sh
go install github.com/tamnd/search/cmd/sx@latest

echo '{"id_field":"_id","fields":[
  {"name":"title","type":"text","analyzer":"english"},
  {"name":"author","type":"keyword"}]}' > schema.json
sx create books.sx --schema schema.json

printf '%s\n' \
  '{"_id":"1","title":"the go programming language","author":"donovan"}' \
  '{"_id":"2","title":"the rust programming language","author":"klabnik"}' > books.jsonl
sx index books.sx --file books.jsonl

sx query books.sx --field title go
```

## Documentation

Full docs and guides live at **[search.tamnd.com](https://search.tamnd.com)**.

- [Getting started](https://search.tamnd.com/getting-started/) walks from install to a first query.
- [Guides](https://search.tamnd.com/guides/) cover building an index, full-text search, facets and sorting, vector search, hybrid search, the SQL interface, operations, and performance tuning.
- [Reference](https://search.tamnd.com/reference/) is the CLI, configuration, C ABI, and release notes.

## Design commitments

These three commitments shape every design decision, from the file format up.

- **Single file, always.** The index, term dictionaries, postings, stored fields, doc-values, and vector graphs all live in one file. Backup is `cp`.
- **Reads are lock-free.** A reader pins an immutable snapshot and serves queries straight out of the page cache with no locks and no coordination with the writer.
- **Writes are durable and transactional.** A commit is atomic and crash-safe: after `fsync` returns, the file is either fully at the new version or fully at the old one, never in between. Durability rides on double-buffered meta pages, the same idea LMDB uses.

## Conventions

- Pure Go, no cgo in the core. The C ABI is produced with `-buildmode=c-shared`.
- One Go module, `github.com/tamnd/search`, flat packages, no `internal/` directories, no `/vN` suffix.
- The binary is `sx`, the library is `libsearch`, the file extension is `.sx`.
- Little-endian on disk, unconditionally, on every platform.
- Page size is a power of two from 4096 to 65536, default 16384, fixed at file creation.

## Layout

| Package | Role |
|---------|------|
| `search` (root) | The `DB` lifecycle and the index, search, vector, facet, compact, and ops surfaces. |
| `query` | The query tree: term, match, phrase, prefix, range, bool, kNN, and hybrid nodes plus the string and JSON parsers. |
| `schema` | Field mappings, field types, and dense-vector options. |
| `analysis` | Tokenizers, filters, and the analyzer registry. |
| `exec` | Query planning and the iterator execution layer, including WAND. |
| `score` | BM25 and the scoring statistics. |
| `segment` | Immutable segments, the segment set, and the tiered merge policy. |
| `fst`, `postings`, `term dictionary` | The inverted index substrate: FST term dictionary and PFOR postings. |
| `docvalues`, `agg`, `collect` | Columnar doc-values, aggregations, and result collectors. |
| `vector`, `hnsw`, `quantize` | Dense-vector storage, the HNSW graph, and quantization. |
| `sqlengine` | The SQL SELECT surface. |
| `cabi` | The C ABI and its header `cabi/search.h`. |
| `catalog`, `btree`, `docstore` | The catalog B+tree and the stored-document store. |
| `page`, `pager`, `wal` | The on-disk page format, the pager, and the write-ahead log primitives. |
| `vfs` | The virtual-filesystem seam: an OS backend and an in-memory fault-injecting backend. |
| `checksum` | CRC-32C (Castagnoli) over `hash/crc32`. |
| `bench` | The benchmark suite and load generator behind `sx bench` and the CI performance gate. |
| `cmd/sx` | The CLI. |

## Building and testing

```sh
go test ./...                                   # full suite, race-clean
go test -run '^$' -bench=. -benchtime=1x ./bench/...   # run the benchmarks once
go build -buildmode=c-shared -o libsearch.so ./cabi    # build the C shared library
```
