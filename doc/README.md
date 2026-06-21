# Tutorials

A guided tour of the engine, from opening your first `.sx` file to tuning a production workload.
Read them in order, or jump to what you need.

1. [Getting started](01-getting-started.md): install, open a file, index a few documents, run one search, from Go and the `sx` CLI.
2. [Building an index](02-building-an-index.md): schema and field types, analyzers, batch indexing, updates, deletes, compaction.
3. [Querying](03-querying.md): the query model and its two textual forms, the query string and the JSON DSL.
4. [Facets and sorting](04-facets-and-sorting.md): doc-values, aggregations, sorting, and the SearchRequest API.
5. [Vector search](05-vector-search.md): dense_vector fields, kNN queries, efSearch tuning, and filtered kNN.
6. [Hybrid search](06-hybrid-search.md): fusing a text query and a kNN query with RRF.
7. [The C ABI](07-c-abi.md): embedding libsearch from C and other languages.
8. [The SQL interface](08-sql-interface.md): the built-in SQL SELECT surface and `sx sql`.
9. [Operations](09-operations.md): info, stats, verify, backup, restore, repair, export, import, and exit codes.
10. [Performance tuning](10-performance-tuning.md): the latency budget, durability knobs, and keeping the index fast.
