---
title: "Introduction"
description: "Why search is SQLite for full-text and vector data: one file, lock-free reads, durable transactional writes, pure Go, sub-millisecond warm queries."
weight: 10
---

A search index usually arrives as infrastructure. You stand up a cluster, push a mapping over HTTP, and keep a process alive to answer queries. That is the right shape when the index is huge and shared, but most indexes are neither. They fit in memory, they belong to one application, and the cluster is overhead you pay for nothing. search is for that case.

The model is SQLite, applied to search. SQLite is not a database server; it is a library that reads and writes one file, linked straight into your process. search is the same thing for full-text and vector data. You link `github.com/tamnd/search` into your Go program (or use the `sx` binary), open a `.sx` file, and index and query documents in process. There is no server to run, no port to open, no daemon to babysit. Backup is `cp`.

## What a .sx file is

A `.sx` file is the whole index in one place. The term dictionary, the compressed postings, the stored document bodies, the columnar doc-values that drive facets and sorting, and the dense-vector graphs all live inside it. There is no sidecar directory, no separate write-ahead log file you have to keep next to it, no lockfile to clean up. Move the file and you move the index.

The format is little-endian on every platform, so a file written on one machine opens on any other. The page size is fixed when the file is created, a power of two from 4096 to 65536 (default 16384), and the format version is recorded in the header so an old engine refuses a file it cannot read rather than guessing.

## Reads are lock-free

When you open a file and run a query, the reader pins an immutable snapshot of the index and serves the query straight out of the page cache. There are no locks taken on the read path and no coordination with whatever is writing. Many goroutines can query the same open index at once, and a query never blocks on a commit happening underneath it. A snapshot is a fixed view of the data: a query sees the index exactly as it was when the query started, even if a writer commits new documents a moment later.

This is what makes warm queries sub-millisecond. Once the pages a query touches are resident, answering it is pointer-chasing and arithmetic, with no system calls and no lock contention in the way.

## Writes are durable and transactional

A write is a transaction. It either lands completely or not at all. When you index a batch and the commit returns, the new documents are on disk and survive a crash; if the process dies mid-commit, the file is left fully at the previous version, never half-written.

The durability comes from double-buffered meta pages, the same idea LMDB uses. The file keeps two copies of its root metadata. A commit writes its new pages, fsyncs them, then flips a single meta page to point at the new root and fsyncs again. The flip is atomic: a reader that opens the file sees either the old root or the new one, and crash recovery just trusts whichever meta page is valid and newest. There is no replay step to get wrong and no window where the file is inconsistent.

Writes are serialized (one writer at a time) while reads stay lock-free, so a steady stream of queries runs at full speed while a batch is being indexed.

## Pure Go, and one module

The core is pure Go with no cgo, so `CGO_ENABLED=0` builds work and cross-compilation is ordinary. There is one module, `github.com/tamnd/search`, with flat packages, no `internal/` directories, and no `/vN` suffix. When you do want the engine from another language, it builds with `-buildmode=c-shared` into `libsearch` and exposes a C ABI that C, Python, and anything with an FFI can call.

The names are consistent: the binary is `sx`, the library is `libsearch`, the file extension is `.sx`.

Next: [install search](/getting-started/installation/).
