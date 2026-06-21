# Performance tuning

The engine shares a process with your application.
It cannot add nodes to scale; the only levers are algorithmic efficiency, data layout, and the few knobs the API exposes.
This page covers the mindset, the knobs that exist, and how the project keeps performance from regressing.

## Latency is a budget, not a hope

Treat query latency as a fixed budget with line items, not an aspiration.
A warm top-10 BM25 query over a large corpus is meant to return in well under a millisecond; a phrase or multi-term query a little more; a kNN query a few milliseconds depending on dimension and quantization.
When a query is slower than that, the first question is which line item blew the budget: analysis, term lookup, postings traversal, scoring, doc-values reads, or stored-body fetch.

The headline commitments the engine measures itself against, on a large corpus:

- Top-10 single-term BM25, warm: under ~1 ms median.
- Multi-term AND or OR, and phrase queries: a few ms.
- Facet plus top-10, and sort by a numeric doc-value: a few ms.
- kNN top-10 float32: ~2 ms; int8 at high dimension: a few ms.
- Hybrid (BM25 + kNN, RRF): under ~6 ms median.
- Single-field fetch with no analysis: tens of microseconds.

These are read targets.
You will not hit them with a cold cache (see below), so warm the index before you measure.

## Warm versus cold

A reader serves queries straight out of the page cache with no locks.
The first query after open is cold: it faults the pages it touches in from disk.
Once the working set is resident, subsequent queries are warm and hit the sub-millisecond targets.

Two practical consequences:

- Open the index once and keep it open for the life of the process.
Opening per query pays the cold cost every time.
- For latency-sensitive services, run a few representative queries at startup to warm the term dictionary and the hot postings before you take traffic.

Cold-open itself is cheap (the header and meta validation only); it is the first query's page faults that cost, so warm by querying, not by reopening.

## PageSize

Page size is fixed at file creation and set through `Options.PageSize`.
It must be a power of two from 4096 to 65536; the default is 16384.

```go
db, err := search.Open("products.sx", search.Options{PageSize: 32768})
```

A larger page reads more data per I/O and lowers tree depth, which helps sequential scans and large postings, at the cost of more wasted space in partially-filled pages and a larger minimum read.
The 16 KiB default is a good balance for mixed read and write.
You only get to choose this once, when the file is first created; reopening an existing file uses its stored page size.

## Durability knobs

Write latency is dominated by fsync.
`Options.Sync` selects the durability discipline, trading safety for speed.

```go
db, err := search.Open("products.sx", search.Options{Sync: wal.SyncFull})
```

- `SyncFull` (the default, the zero value) fsyncs on every commit.
After commit returns, the transaction is durable.
This is the only level that honors the crash-safety contract in full.
- `SyncNormal` fsyncs on commit but defers the main-file fsync.
A crash can lose only the most recent un-checkpointed work, never corrupt the file.
- `SyncOff` performs no fsync.
Fast and unsafe; a crash may lose recent commits.
It is meant for bulk-load-then-verify workflows where you can rebuild on failure.

The practical pattern for a large initial load is to index with a relaxed sync level, then switch back to `SyncFull` for steady-state writes.
For reads, also batch your indexing: each `Index` call is one commit and one segment, so fewer, larger batches mean fewer fsyncs and fewer segments to merge later.

`Options.ReadOnly` opens the file for queries only, which is the right choice for a query-serving replica that never writes.
`Options.UnsafeNoLock` skips the multi-process advisory lock; use it only on a filesystem where locks are unsafe (NFS) and you can guarantee no other process opens the index concurrently.

## Keep the segment count and tombstones down

Every query fans out across live segments and merges their results, and it filters deleted doc-ids out of every result.
So two things quietly raise latency over time: a growing segment count from many small batches, and accumulating tombstones from deletes and replaces.

Compaction fixes both.
Run `db.Compact()` (or `sx compact`) periodically for a tiered round, or `db.CompactAll()` (`sx vacuum`) to collapse to one segment and reap every tombstone.
Watch `sx stats` for the segment count and the deleted count to decide when.
See [building an index](02-building-an-index.md#compaction-and-vacuum).

## Allocation discipline

The hot read path is written to avoid per-query allocation: a reader pins an immutable snapshot and reads through the page cache, scoring against statistics computed once at index time rather than recomputed per query.
When you profile a query that is slower than its budget, look for allocations in your own surrounding code first (decoding documents, copying maps), since the engine's own read path is allocation-light by design.

Two habits that keep your side cheap:

- Project only the fields you need with `--fields` (CLI) or by reading specific keys from `Hit.Document`, rather than copying the whole stored body.
- Reuse the open `DB` and avoid re-parsing the same query string in a tight loop; build the `query.Query` tree once and pass it to `Search`.

## Measuring with the bench suite

The `bench` package and the `sx bench` subcommand are the project's own measurement tools, and you can point them at your own workload.

Two surfaces share one harness:

- Go benchmarks under `bench/` run in CI and feed `benchstat`. Run them once with `go test -run '^$' -bench=. -benchtime=1x ./bench/...`, or longer for stable numbers.
- The `sx bench` load generator runs a named scenario under a concurrency and QPS target and reports real latency percentiles (P50/P95/P99/P999/max).

```sh
sx bench bm25-single-warm --duration 30 --concurrency 8 --qps 2000
sx bench knn-f32 --ef-search 64 --output run.json
sx bench bm25-and4 --compare baseline.json   # exits non-zero on a regression
```

The scenarios cover the headline query shapes: single-term and boolean BM25, phrase, float32 and int8 kNN, hybrid, and the ingest paths.
The `--output` flag writes the structured JSON result, and `--compare` checks a fresh run against a saved baseline and fails when any metric regresses past the threshold.

## Guarding against regressions

The project's discipline is that a performance regression is a bug, not a tuning issue.
The service-level objectives above are treated as binding: a change that regresses a headline metric by more than 5% relative to the baseline has to be fixed or ship with a deliberate, documented budget revision.
That gate runs in CI: the `bench` job exercises the benchmark scenarios and the allocation ceilings, so a slow or allocation-heavy change is caught before it lands rather than discovered in production.
The full timed SLO run against the large reference corpora (Wikipedia-scale text, SIFT1M vectors) happens on a dedicated quiet runner, since shared CI hardware is too noisy for sub-millisecond percentiles; the in-repo path uses a smaller synthetic corpus to catch gross regressions.

When you tune your own workload, hold the same line: pick the metrics that matter for your service (your real query mix, your corpus size), measure them warm, and treat a regression past a few percent as something to investigate rather than absorb.
