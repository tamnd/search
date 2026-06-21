---
title: "CLI reference"
description: "Every sx command and flag."
weight: 10
---

```
sx <command> [arguments]
```

`sx` drives a single `.sx` index file: it creates one, indexes documents into
it, runs full-text, vector, hybrid, and SQL queries against it, and carries the
operational commands (verify, repair, backup, restore, vacuum). Run
`sx help` for the canonical, up-to-date command list.

## Global flags

These apply to every subcommand. They are stripped from the argument list
wherever they appear, so position does not matter.

| Flag | Default | Meaning |
|------|---------|---------|
| `--unsafe-no-lock` | `false` | Open without the multi-process advisory file lock. The escape hatch for filesystems where locks are unsafe (NFS, SMB); only use it when no other process opens the index concurrently. |

`sx version` (also `-v`, `--version`) prints the CLI, format, and build
versions. `sx help` (also `-h`, `--help`) prints the command list.

## sx create

```
sx create <file> [--schema schema.json]
```

Creates a `.sx` file and optionally applies a schema from a JSON definition.

| Flag | Default | Meaning |
|------|---------|---------|
| `--schema` | | Path to a JSON schema definition (`id_field` plus a `fields` array) |

## sx index

```
sx index <file> [--file docs.jsonl] [--id-field _id]
```

Reads JSON Lines documents (one JSON object per line) and indexes them. A
document whose external id already exists is replaced. The whole input is read
into memory first; for a large dump use `sx import`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--file` | stdin | Path to a JSONL document file |
| `--id-field` | | Primary-key field name; sets the schema id field on an empty index so documents index without a separate `create` step |

## sx update

```
sx update <file> [--file docs.jsonl]
```

Reindexes documents from JSONL, replacing any existing document with the same
external id. This is `index` with replace-oriented reporting.

| Flag | Default | Meaning |
|------|---------|---------|
| `--file` | stdin | Path to a JSONL document file |

## sx delete

```
sx delete <file> <external-id>...
```

Soft-deletes one or more documents by external id. A missing id is reported on
stderr but does not fail the batch. The postings stay in their segment until a
later `sx compact` reaps them.

## sx get

```
sx get <file> <doc-id>
sx get <file> --id <external-id>
```

Fetches a stored document and prints it as JSON. With a positional argument it
looks up by internal numeric doc-id; with `--id` it looks up by external id.

| Flag | Default | Meaning |
|------|---------|---------|
| `--id` | | Fetch by external id instead of internal doc-id |

## sx analyze

```
sx analyze <file> [--analyzer name | --field name] [--format table|json] <text>
```

Runs an analyzer over text and prints the resulting tokens. The text is the
trailing positional argument, or stdin when no text is given.

| Flag | Default | Meaning |
|------|---------|---------|
| `--analyzer` | `standard` | Analyzer name (built-in or stored) |
| `--field` | | Analyze with the analyzer configured for this field (overrides `--analyzer`) |
| `--format` | `table` | Output format: `table` or `json` |

## sx schema

```
sx schema <file>
```

Prints the schema of an index as JSON: the primary-key field and every field
with its type, analyzer, and vector options.

## sx inspect

```
sx inspect <file> [--format table|json]
```

Dumps the segment structure: each segment with its document count and per-field
term and posting statistics.

| Flag | Default | Meaning |
|------|---------|---------|
| `--format` | `table` | Output format: `table` or `json` |

## sx query

```
sx query <file> '<query string>' [flags]
sx search <file> '<query string>' [flags]
```

Runs a full-text search and prints the hits. The query is a positional string
in the compact query syntax, or a JSON query DSL object via `--json` (pass
exactly one). `search` is an alias for `query`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--field` | first text field | Default field for bare terms in the query string |
| `--json` | | Read a JSON query DSL object from this path instead of a query string |
| `--size` | `10` | Number of hits to return |
| `--from` | `0` | Offset into the result set for pagination |
| `--fields` | all stored | Comma-separated stored fields to include in each hit |
| `--format` | `table` | Output format: `table`, `json`, or `jsonl` |
| `--explain` | `false` | Include a per-hit score explanation |
| `--sort` | `_score` | Sort keys, comma-separated, each `field[:asc|desc][:missing_last]`; `_score` for relevance |
| `--facet` | | Aggregations, semicolon-separated, each `name=kind:field[:opts]` |
| `--collapse` | | Keyword field to collapse hits on, keeping the top hit per group |

The `--facet` kinds are `terms` (opt = size), `histogram` (opt = interval),
`percentiles` (opt = pipe-separated percents), and `min`, `max`, `sum`, `avg`,
`count`, `stats`, `cardinality`. Range and nested facets are library-only.

## sx sql

```
sx sql <file> '<SELECT ...>' [flags]
```

Runs a single `SELECT` through the built-in SQL surface. `MATCH` compiles to a
full-text query and the structured predicates become filters, all in-process
against the `.sx` file.

| Flag | Default | Meaning |
|------|---------|---------|
| `--format` | `table` | Output format: `table`, `json`, `jsonl`, or `csv` |
| `-v` | | Named bind, `name=value`; repeat for more, referenced as `:name` in the SQL |

## sx knn

```
sx knn <file> --field f --vector '0.1,0.2,...' [flags]
```

Runs a k-nearest-neighbor search over a `dense_vector` field. The query vector
is a comma or whitespace separated list of floats, or `@path` to read it from a
file (which may hold a JSON array).

| Flag | Default | Meaning |
|------|---------|---------|
| `--field` | (required) | The `dense_vector` field to search |
| `--vector` | (required) | Query vector: comma/space separated floats, or `@file` |
| `--k` | `10` | Number of neighbors to return |
| `--num-candidates` | field default | Per-segment efSearch (0 uses the field default) |
| `--filter` | | Compact query string to pre-filter candidates (filtered ANN) |
| `--fields` | all stored | Comma-separated stored fields to include in each hit |
| `--format` | `table` | Output format: `table`, `json`, or `jsonl` |

## sx hybrid

```
sx hybrid <file> --field f --vector '...' [flags] '<text query>'
```

Runs a hybrid search: a text query and a kNN query fused with Reciprocal Rank
Fusion. The text query is the positional compact query string; the vector side
is given with `--field` and `--vector`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--text-field` | first text field | Default field for bare terms in the text query |
| `--field` | (required) | The `dense_vector` field to search |
| `--vector` | (required) | Query vector: comma/space separated floats, or `@file` |
| `--k` | `10` | Number of hits to return |
| `--rrf-k` | `60` | RRF fusion constant |
| `--num-candidates` | field default | Per-segment efSearch (0 uses the field default) |
| `--fields` | all stored | Comma-separated stored fields to include in each hit |
| `--format` | `table` | Output format: `table`, `json`, or `jsonl` |

## sx compact

```
sx compact <file> [--all]
```

Merges segments and reclaims the space held by deleted documents. By default it
runs one tiered round; `--all` force-merges every segment into one and reaps all
tombstones at once.

| Flag | Default | Meaning |
|------|---------|---------|
| `--all` | `false` | Force-merge every segment into one |

## sx info

```
sx info <file> [--format table|json]
```

Prints the file header and meta summary: geometry, format and engine versions,
and document and segment counts. It reads no segment data, so it is instant on
an index of any size.

| Flag | Default | Meaning |
|------|---------|---------|
| `--format` | `table` | Output format: `table` or `json` |

## sx stats

```
sx stats <file> [--format table|json]
```

Prints structural and runtime statistics: document and segment counts, the
freelist and snapshot bookkeeping that signal whether the index needs
maintenance, and a per-segment breakdown. It reads only metadata.

| Flag | Default | Meaning |
|------|---------|---------|
| `--format` | `table` | Output format: `table` or `json` |

## sx verify

```
sx verify <file> [--deep] [--format table|json]
```

Checks the file for corruption. By default it validates the catalog tree, every
stored value, and each segment's term dictionary; `--deep` additionally reads
every postings list, turning it into a full index scan. It exits non-zero when
any fault is found, so it is usable in a health check.

| Flag | Default | Meaning |
|------|---------|---------|
| `--deep` | `false` | Also read every postings list (full index scan) |
| `--format` | `table` | Output format: `table` or `json` |

## sx checkpoint

```
sx checkpoint <file>
```

Folds the WAL sidecar into the file. Durability here rides on double-buffered
meta pages, not a separate log, so a `.sx` file is always self-contained at
rest and no sidecar is ever produced. The command validates the file, finds no
sidecar, and reports that the file is self-contained. It exists so scripts that
defensively checkpoint before copying a file keep working.

## sx repair

```
sx repair <file> [--out fixed.sx] [--force]
```

Rebuilds a possibly-damaged index into a new file, leaving the source
untouched. It never writes in place. It exits 0 on a clean rebuild, 8 when it
recovered the file but had to drop unreadable documents, and 4 when the source
cannot be opened at all.

| Flag | Default | Meaning |
|------|---------|---------|
| `--out` | `<file>.repaired` | Output file for the rebuilt index |
| `--force` | `false` | Overwrite the output file if it exists |

## sx restore

```
sx restore <dest> --from <backup> [--force] [--no-verify]
```

Copies a backup to a destination and verifies it. The inverse of `sx backup`:
it refuses to clobber an existing file without `--force` and confirms the
restored file passes an integrity check before reporting success.

| Flag | Default | Meaning |
|------|---------|---------|
| `--from` | (required) | Source backup file |
| `--force` | `false` | Overwrite the destination if it exists |
| `--no-verify` | `false` | Skip the integrity check on the restored file |

## sx export

```
sx export <file> [--out docs.jsonl]
```

Writes every live document as JSON Lines to `--out` or stdout. Each document's
external id is restored under the schema primary-key field, so the output
reindexes cleanly with `sx index`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--out` | stdout | Write to this file instead of stdout |

## sx import

```
sx import <file> [--file docs.jsonl] [--batch 1000] [--id-field _id]
```

Indexes documents from JSONL in fixed-size batches, reporting progress to
stderr. Unlike `sx index`, which loads the whole file first, import streams the
input so a multi-gigabyte dump indexes with bounded memory.

| Flag | Default | Meaning |
|------|---------|---------|
| `--file` | stdin | Path to a JSONL document file |
| `--batch` | `1000` | Documents per index batch |
| `--id-field` | | Primary-key field name; sets the schema id field on an empty index |

## sx backup

```
sx backup <file> <dest>
```

Copies a `.sx` file to a destination as a consistent snapshot. The source is
opened read-only so no writer can change it mid-copy, then streamed out. The
result is a standalone file that opens on its own.

## sx vacuum

```
sx vacuum <file>
```

Reclaims the space held by deleted documents by force-merging every segment into
one and reaping all tombstones. It is `sx compact --all` under an operational
name. The single-file layout reuses freed pages through the freelist rather than
truncating, so the file does not necessarily shrink on disk; what vacuum
guarantees is that deleted documents stop costing query time.

## sx bench

```
sx bench [options] <scenario|all>
sx bench --compare base.json <scenario>
sx bench --compare base.json cur.json
```

Runs the load generator against a synthetic corpus and reports the latency
percentiles the service-level objectives are stated against. With one scenario
name it runs that scenario; `all` runs every scenario. With `--compare` and two
result files it diffs them without running, and exits 1 when any scenario
regresses past the threshold, so it gates a CI pipeline.

| Flag | Default | Meaning |
|------|---------|---------|
| `--duration` | `300` | Measurement duration in seconds |
| `--warmup` | `60` | Warmup duration in seconds |
| `--concurrency` | `1` | Number of concurrent goroutines |
| `--qps` | `0` | Target aggregate QPS (0 = as fast as possible) |
| `--ef-search` | field default | efSearch for vector scenarios (0 = field default) |
| `--docs` | scenario default | Synthetic corpus document count |
| `--vocab` | scenario default | Synthetic corpus vocabulary size |
| `--dims` | scenario default | Synthetic vector dimension for vector scenarios |
| `--output` | | Write JSON results to this file |
| `--compare` | | Compare results against this baseline JSON file |
| `--corpus` | | Path to a corpus file or directory (external runner only; the in-repo runner ignores it) |
| `--index` | | Path to a `.sx` index to use (external runner only; the in-repo runner ignores it) |
