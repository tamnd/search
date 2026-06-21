---
title: "Release notes"
description: "What changed in each sx release."
weight: 40
---

The authoritative, commit-level history lives in [`CHANGELOG.md`](https://github.com/tamnd/search/blob/main/CHANGELOG.md) and on the [releases page](https://github.com/tamnd/search/releases). This page summarises each version.

## v1.0.0

The 1.0 release. The engine is feature-complete across S0 through S9, the file format is frozen for the 1.x line, and the C ABI is stable at version 1.

- **Full-text, vector, hybrid, and SQL, in one file.** A `.sx` file carries the inverted index, columnar doc-values, stored fields, and dense-vector graphs together. The query surface spans BM25 full-text search, kNN over `dense_vector` fields, hybrid search fused with Reciprocal Rank Fusion, and a built-in SQL SELECT surface.
- **The format is frozen at version 0x0100.** The header records `format_version_compat_min` set to 0x0100, so a file that needs a newer engine fails to open with `ErrTooNew` instead of a confusing feature error. The pre-1.0 "format may change" notice is gone.
- **The C ABI is stable at version 1.** `libsearch` (built with `-buildmode=c-shared`) reports library version 1.0.0 while the ABI contract stays at version 1. See the [C ABI reference](/reference/c-abi/).
- **Multi-process safety.** Whole-file advisory locking (open-file-description `fcntl` locks): shared for readers, exclusive for writers. Network filesystems are detected and rejected with `ErrUnsupportedFilesystem`; `--unsafe-no-lock` bypasses both.
- **The complete operations CLI.** `verify` (with `--deep`), `repair`, `backup`, `restore`, `vacuum`, `checkpoint`, `info`, `stats`, `inspect`, `export`, and `import`. See the [CLI reference](/reference/cli/).
- **A load generator and a CI performance gate.** The `bench` package and `sx bench` report real latency percentiles, with a 5% regression threshold and allocation ceilings.
- **Hardening.** A full fuzz suite (query parser, FST scan, WAL replay, BKD range, HNSW search, PFOR, segment flush), property tests for compaction equivalence, MVCC snapshot isolation, and recovery to the last committed state, and a 10,000-cycle crash-injection recovery campaign.
- **Documentation.** Ten prose tutorials under `doc/`, a rewritten README, and a godoc comment on every exported symbol.

## v0.9.0

S8: the public surface. The C ABI (`-buildmode=c-shared`, `libsearch`, `search.h`), the built-in SQL SELECT surface, advanced query types (fuzzy, wildcard, regexp, span, geo-distance), function-score and rescore scoring, BM25F, and highlighting.

## v0.7.0

S7: dense-vector search. The `dense_vector` field, an HNSW graph, int8 and product quantization, filtered kNN, and hybrid search fused with Reciprocal Rank Fusion.

## v0.6.0

S6: doc-values and aggregations. Columnar doc-values, a 1D BKD index, terms, range, histogram, and metric aggregations, sorting, and result collapse.

## v0.5.0

S5: segments at scale. Soft deletes, updates, tiered compaction with an N-way segment merge, and block-max WAND early termination.

## v0.4.0

S4: query and scoring. The query tree and parsers, the iterator execution layer, and BM25 scoring.

## v0.3.0

S3: the inverted index. The FST term dictionary, PFOR postings, the memtable, and the segment flush path.

## v0.2.0

S2: documents and schema. The MessagePack codec, the typed schema, the document store, and the analysis pipeline.

## v0.1.0

S0 and S1: the substrate and durability. The VFS seam, checksums, the page format, the pager, the COW B+tree catalog, the WAL primitives, MVCC, and crash-safe commit.
