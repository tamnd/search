---
title: "Guides"
linkTitle: "Guides"
description: "Task-oriented walkthroughs for the things people actually do with sx: building an index, searching it, faceting, vectors, hybrid, SQL, operations, and tuning."
weight: 20
featured: true
---

Each guide is built around a job rather than a flag: putting documents into an index, getting them back out by relevance, counting them into facets, searching by vector, and keeping a `.sx` file healthy in production. They assume you have worked through the [quick start](/getting-started/quick-start/).

- [Building an index](/guides/building-an-index/): the schema, field types, analyzers, batch indexing, updates and deletes, and reclaiming space.
- [Full-text search](/guides/full-text-search/): the query tree, the compact query string, the JSON DSL, boosts, and `sx query`.
- [Facets and sorting](/guides/facets-and-sorting/): doc-values, the request API, sort keys, aggregations, and collapse.
- [Vector search](/guides/vector-search/): the `dense_vector` field, HNSW knobs, kNN queries, quantization, and filtered kNN.
- [Hybrid search](/guides/hybrid-search/): fusing a text query and a kNN query with Reciprocal Rank Fusion.
- [The SQL interface](/guides/the-sql-interface/): the built-in SELECT surface, `MATCH`, bind parameters, and the Go API.
- [Operations](/guides/operations/): inspect, verify, backup, restore, repair, export, import, vacuum, exit codes, and locking.
- [Performance tuning](/guides/performance-tuning/): the latency budget, durability knobs, segment management, and the bench gate.
