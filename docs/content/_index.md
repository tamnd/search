---
title: "search"
description: "An embedded, single-file, full-text and vector search engine for Go. It is to search what SQLite is to relational data: a library you link into your process, one ordinary file on disk, no server, no daemon. The binary is sx, the file extension is .sx, the library is libsearch."
heroTitle: "Search that lives in one file"
heroLead: "Open a .sx file, index documents, and run full-text and vector queries against a memory-resident index with sub-millisecond latency. Reads are lock-free, writes are durable and transactional, and the whole index is one ordinary file you can copy. Pure Go, no server, no cluster, no daemon."
heroPrimaryURL: "/getting-started/quick-start/"
heroPrimaryText: "Get started"
---

Most search needs a service: a cluster to run, a schema to push over HTTP, a process to keep alive. That is a lot of moving parts for an index that often fits in memory. search takes the SQLite approach instead. It is a library you link into your Go process and one file on disk. You open a `.sx` file, give it a schema, index documents, and query them, all in the same process, with no network in the path.

The same engine ships as a command-line tool, `sx`, so you can build and search an index without writing any Go:

```sh
sx create books.sx --schema schema.json
sx index books.sx --file books.jsonl
sx query books.sx --field title go
```

## What you get

- **Full-text search with BM25.** Term, phrase, prefix, range, and boolean queries over an inverted index with an FST term dictionary and PFOR-compressed postings.
- **Facets and sorting.** Columnar doc-values drive facet counts and numeric sorting without touching the inverted index.
- **Dense-vector search.** A `dense_vector` field with cosine, dot-product, or L2 distance, an HNSW graph, optional int8 and product quantization, and filtered kNN.
- **Hybrid search.** Fuse a text query and a kNN query with Reciprocal Rank Fusion, so keyword and semantic matches rank together.
- **A SQL surface.** A built-in SQL SELECT for people who would rather write SQL than build a query tree.
- **A C ABI.** Built with `-buildmode=c-shared`, so the engine is usable from C, Python, and anything with an FFI.
- **Operational tooling.** `info`, `stats`, `verify`, `backup`, `restore`, `repair`, `vacuum`, `export`, `import`, and a `bench` load generator.

The same index, opened as a library, is a few lines of Go:

```go
db, err := search.Open("books.sx", search.Options{})
if err != nil {
	log.Fatal(err)
}
defer db.Close()

hits, err := db.Search(query.Term("title", "go"), 10)
if err != nil {
	log.Fatal(err)
}
for _, h := range hits {
	fmt.Printf("%s  %.3f  %v\n", h.ExternalID, h.Score, h.Document["title"])
}
```

## Where to go next

- New here? Start with the [introduction](/getting-started/introduction/), then the [quick start](/getting-started/quick-start/).
- Want to install it? See [installation](/getting-started/installation/).
- Looking for a specific task? The [guides](/guides/) cover building an index, querying, facets, vector and hybrid search, the SQL surface, the C ABI, and operations.
- Need every flag? The [CLI reference](/reference/cli/) is the full surface.
