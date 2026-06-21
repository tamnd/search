---
title: "Operations"
description: "Inspect, verify, back up, restore, repair, export, import, and vacuum a .sx file, with exit codes and locking notes."
weight: 70
---

The `sx` CLI is the operational front end for a `.sx` file: inspect it, check it, back it up, restore it, repair it, and move documents in and out. Every read-only command opens the file read-only, writes diagnostics to stderr, and supports `--format json` where it makes sense, so stdout pipes cleanly into scripts.

## info: the header summary

`sx info` prints the file geometry, format and engine versions, and document and segment counts. It reads no segment data, so it is instant on an index of any size.

```
sx info products.sx
```

```
file:           products.sx
page size:      16384
page count:     42
file bytes:     688128
format version: 1
segments:       3
documents:      1200
deleted:        15
last doc id:    1215
```

The Go equivalent is `db.Info()`, which returns a `FileInfo`.

## stats: structural and runtime detail

`sx stats` adds the freelist, snapshot, and term bookkeeping you watch to decide whether the index needs maintenance, plus a per-segment breakdown. It reads only metadata, so it stays cheap.

```
sx stats products.sx
```

Key fields to watch:

- `free pages` is the durable freelist count, pages reusable by the next write without growing the file.
- `pending free` is pages freed by a committed write but still pinned by a live read snapshot. A persistently high value means a long-lived reader is holding back reclamation.
- `deleted` and `segments` together tell you when to compact: many segments or many tombstones means a [compaction](/guides/building-an-index/) is due.

The Go equivalent is `db.Stats()`, which returns an `IndexStats` embedding `FileInfo`.

## verify: integrity check

`sx verify` walks the live structure and reports any corruption. By default it validates the catalog tree, every stored value, and each segment's term dictionary. `--deep` additionally reads every postings list, turning it into a full index scan.

```
sx verify products.sx
sx verify products.sx --deep --format json
```

It exits non-zero when any fault is found, so it works directly in a health check. The Go equivalent is `db.Verify(deep bool)`, which gathers every problem into a `VerifyReport` rather than stopping at the first.

## backup and restore

`sx backup` copies the file to a destination as a consistent snapshot. The source is opened read-only so no writer can change it mid-copy, then the bytes are streamed and fsynced. The result is a standalone file that opens on its own. Because the index is a single self-contained file, `cp` works too; `sx backup` adds the read-only pin and the fsync.

```
sx backup products.sx products.bak.sx
```

`sx restore` is the inverse: a smart copy that refuses to clobber an existing file without `--force` and verifies the restored file before reporting success. The destination comes first and the backup is named with `--from`.

```
sx restore products.sx --from products.bak.sx
sx restore products.sx --from products.bak.sx --force --no-verify
```

## repair: best-effort recovery

`sx repair` rebuilds a possibly-damaged file into a new one, leaving the source untouched. Recovery is logical: the source is opened read-only (which already exercises the meta-page recovery, since two meta pages are kept and the higher valid one wins), its schema and custom analyzers are copied, then every readable live document is reindexed one at a time. A document whose stored body cannot be read is recorded and skipped instead of aborting the whole rebuild.

```
sx repair products.sx --out fixed.sx
```

The rebuilt file is a fresh, fully-checksummed index, not a byte-for-byte copy of the original. Repair never writes in place; choose a different `--out` (the default is `<file>.repaired`). The Go equivalent is `search.Repair(srcPath, outPath, opt)`, which returns a `RepairReport` with `Recovered`, `Dropped`, and any per-document warnings.

## checkpoint: a deliberate no-op

`sx checkpoint` exists for scripts that defensively fold a write-ahead log before copying a file. In this engine durability comes from double-buffered meta pages, not a separate WAL sidecar wired into the pager, so a `.sx` file is always self-contained at rest and no `<file>-wal` sidecar is ever produced. The command validates the file, finds no sidecar, reports that the file is already self-contained, and exits 0.

```
sx checkpoint products.sx
```

```
no WAL sidecar; products.sx is self-contained
```

If a `<file>-wal` ever does exist (a future format that this build does not write), the command surfaces it rather than silently ignoring it.

## export and import

`sx export` writes every live document as JSON Lines, restoring each document's external id under the schema's primary-key field, so the output reindexes cleanly. Documents stream as they are read, so memory stays flat regardless of index size.

```
sx export products.sx --out dump.jsonl
sx export products.sx | gzip > dump.jsonl.gz
```

`sx import` is the streaming counterpart to `sx index`: it reads JSONL in fixed-size batches and reports progress, so a multi-gigabyte dump indexes with bounded memory.

```
sx import products.sx --file dump.jsonl --batch 5000
```

Export plus import is the format-migration path: export from an old file, create a new one, import. The Go equivalent of export is `db.Export(w io.Writer)`.

## vacuum

`sx vacuum` reclaims the space held by deleted documents by force-merging every segment into one and reaping all tombstones. It is `sx compact --all` under an operational name.

```
sx vacuum products.sx
```

The single-file layout reuses freed pages through the freelist rather than truncating, so the file does not necessarily shrink on disk. What vacuum guarantees is that deleted documents stop costing query time and their pages return to the freelist for reuse.

## Exit codes

Scripts should check the exit code, not parse stderr. The codes you will see in practice:

```
0   Success.
1   General error (wrong flags, invalid arguments, schema mismatch, open failure).
2   Bad flag parsing (a flag set rejected its arguments).
4   Integrity error: sx repair could not open the source, or sx restore's
    verification failed.
8   Partial success: sx repair rebuilt the file but had to drop unreadable
    documents. Details are in stderr.
```

`sx verify` exits 1 when it finds corruption, 0 when the file is clean. The wider stable code set (timeout, resource-limit, unsupported-version) is reserved for the operations that grow those modes. The full table lives in the [CLI reference](/reference/cli/).

## Locking and concurrent access

An index takes a multi-process advisory lock when it is opened for writing, so two writers cannot corrupt the same file. Readers are lock-free and never block. The lock is the safe default and you want it on local disks.

The one escape hatch is `--unsafe-no-lock`, a global flag that opens the file without the advisory lock. Use it only on a filesystem where the lock is itself unsafe (NFS, where advisory locks are unreliable), and only when you can guarantee no other process opens the index concurrently. The Go equivalent is `Options.UnsafeNoLock`.

```
sx query /mnt/nfs/products.sx running --unsafe-no-lock
```

For a query-serving replica that never writes, open the file read-only instead (`Options.ReadOnly`, or any read-only command), which sidesteps the writer lock without giving up safety.

## A maintenance routine

A reasonable periodic check, suitable for cron or CI:

```
sx verify products.sx --deep || echo "CORRUPT" >&2
sx stats products.sx --format json    # watch segments, deleted, pending free
sx vacuum products.sx                  # when deleted/segments are high
sx backup products.sx products.bak.sx  # consistent snapshot
```

See [performance tuning](/guides/performance-tuning/) for the durability knobs that shape write latency, and [building an index](/guides/building-an-index/) for when compaction is worth running.
