# Changelog

All notable changes to this project are recorded here.
The format follows Keep a Changelog, and the project uses semantic versioning.

## [1.0.0]

The 1.0 release. The file format is frozen for the 1.x line and the C ABI is stable at version 1.

### Added

- Format-version compatibility floor in the file header (`format_version_compat_min`, set to 0x0100). A file that needs a newer engine fails to open with `ErrTooNew` instead of a confusing feature error.
- Multi-process safety through whole-file advisory locking (open-file-description `fcntl` locks): shared for readers, exclusive for writers. Network filesystems (NFS, SMB) are detected and rejected with `ErrUnsupportedFilesystem`; `--unsafe-no-lock` bypasses both.
- The complete operations CLI: `verify` (with `--deep`), `repair`, `backup`, `restore`, `vacuum`, `checkpoint`, `info`, `stats`, `inspect`, `export`, and `import`.
- The `bench` package and the `sx bench` load generator, reporting real latency percentiles, plus a CI performance gate with a 5% regression threshold and allocation ceilings.
- A full fuzz suite: the query parser, FST scan, WAL replay, BKD range, and HNSW search, alongside the existing PFOR and segment-flush targets.
- Property tests for compaction result-equivalence, MVCC snapshot isolation, and recovery to the last committed state.
- Crash injection extended over the segment-flush and compaction write paths, driven by a 10,000-cycle recovery campaign.
- Ten prose tutorials under `doc/`, a rewritten README, and a godoc comment on every exported symbol.

### Changed

- The file format is declared frozen at version 0x0100. The pre-1.0 "format may change" notice is removed.
- The C ABI library version reports 1.0.0; the ABI contract stays at version 1.

## [0.9.0]

- S8: the public surface. The C ABI (`-buildmode=c-shared`, `libsearch`, `search.h`), the built-in SQL SELECT surface, advanced query types (fuzzy, wildcard, regexp, span, geo-distance), function-score and rescore scoring, BM25F, and highlighting.

## [0.7.0]

- S7: dense-vector search. The `dense_vector` field, an HNSW graph, int8 and product quantization, filtered kNN, and hybrid search fused with Reciprocal Rank Fusion.

## [0.6.0]

- S6: doc-values and aggregations. Columnar doc-values, a 1D BKD index, terms, range, histogram, and metric aggregations, sorting, and result collapse.

## [0.5.0]

- S5: segments at scale. Soft deletes, updates, tiered compaction with an N-way segment merge, and block-max WAND early termination.

## [0.4.0]

- S4: query and scoring. The query tree and parsers, the iterator execution layer, and BM25 scoring.

## [0.3.0]

- S3: the inverted index. The FST term dictionary, PFOR postings, the memtable, and the segment flush path.

## [0.2.0]

- S2: documents and schema. The MessagePack codec, the typed schema, the document store, and the analysis pipeline.

## [0.1.0]

- S0 and S1: the substrate and durability. The VFS seam, checksums, the page format, the pager, the COW B+tree catalog, the WAL primitives, MVCC, and crash-safe commit.
