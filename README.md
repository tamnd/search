# search

An embedded, single-file, full-text and vector search engine for Go.
It is to search what SQLite is to relational data: a library you link into your process, one ordinary file on disk, no server, no cluster, no daemon.
You open a `.sx` file, index documents, and run searches against a memory-resident index with sub-millisecond latency.

This repository is built milestone by milestone against spec 2063.
This is **S0**: the foundation pour.
At S0 the public surface is only the file lifecycle and the page substrate underneath it.
There is no search content yet; that arrives at S2.
The roadmap is S0 through S9, and every milestone is independently shippable and crash-safe.

## What S0 gives you

- A single `.sx` file with a fixed-size paged layout (default 16 KiB pages).
- A 128-byte file header (page 0) with magic `tamndsearch fmt1`, format version, and a CRC-32C checksum.
- Two double-buffered meta pages (pages 1 and 2) for LMDB-style atomic commit.
- A pager that creates, opens, allocates, reads, writes, and checksums pages.
- An in-memory, fault-injecting filesystem backend so the durability path can be crash-tested without touching real disks.
- The `sx` CLI with `version` and `help`.

## Design commitments

These three commitments shape every design decision, from the file format up.

- **Single file, always.** The index, term dictionaries, postings, stored fields, doc-values, vector graphs, and the write-ahead log all live in one file. Backup is `cp`.
- **Reads are lock-free.** A reader pins an immutable snapshot and serves queries straight out of the page cache with no locks and no coordination with the writer.
- **Writes are durable and transactional.** A commit is atomic and crash-safe: after `fsync` returns, the file is either fully at the new version or fully at the old one, never in between.

## Conventions

- Pure Go, no cgo in the core. The C ABI is produced later with `-buildmode=c-shared`.
- One Go module, `github.com/tamnd/search`, flat packages, no `internal/` directories, no `/vN` suffix.
- The binary is `sx`, the library is `libsearch`, the file extension is `.sx`.
- Little-endian on disk, unconditionally, on every platform.
- Page size is a power of two from 4096 to 65536, default 16384, fixed at file creation.

## Layout

| Package | Role |
|---------|------|
| `search` (root) | The `DB` lifecycle: open, create, close. |
| `vfs` | The virtual-filesystem seam; OS backend plus an in-memory fault-injecting backend. |
| `checksum` | CRC-32C (Castagnoli) over `hash/crc32`. |
| `page` | The on-disk format: file header, meta pages, common page header, page types, primitives. |
| `pager` | Fixed-size pages: allocate, read, write, checksum, sync, meta-page selection. |
| `wal` | The write-ahead log (stub at S0, built at S1). |
| `cmd/sx` | The CLI. |

## Status

S0 is the substrate. It stores no documents.
Run `go test ./...` to exercise the page store, and `go run ./cmd/sx version` to print build info.
