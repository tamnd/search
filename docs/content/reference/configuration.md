---
title: "Configuration"
description: "The Go Options that open an index, and the on-disk conventions of a .sx file."
weight: 20
---

The library is configured through the Go `Options` struct passed to
`search.Open`. The CLI exposes one piece of it, the `--unsafe-no-lock` flag
(see the [CLI reference](/reference/cli/)); everything else is a property of the
file, fixed at creation.

## Options

`search.Open(path string, opt search.Options) (*search.DB, error)`. The zero
value is the safe default: the OS filesystem, the default page size, and full
synchronous durability.

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `PageSize` | `uint32` | `16384` | Page size in bytes, used only when creating a new file. A power of two from 4096 to 65536. `0` means the default. |
| `Sync` | `wal.SyncLevel` | `SyncFull` | Durability discipline on commit (see below). |
| `ReadOnly` | `bool` | `false` | Open for queries only; no writer can be started. |
| `UnsafeNoLock` | `bool` | `false` | Skip the advisory multi-process file lock. Use only on a filesystem where locks are unsafe (NFS) and you can guarantee no other process opens the index concurrently. |
| `VFS` | `vfs.VFS` | OS filesystem | The virtual filesystem to open the file through; `nil` uses the real OS filesystem. |
| `Clock` | `determ.Clock` | OS clock | The time source; `nil` uses the OS clock. |
| `SaltSeed` | `uint64` | `0` | Makes WAL salt generation deterministic for tests; `0` is fine in production. |

## Sync levels

`Sync` selects how durable a commit is. The zero value, `SyncFull`, is the only
level that honors the crash-safety contract in full.

| Level | Meaning |
|-------|---------|
| `SyncFull` | Fsync on every commit (and the main file at checkpoint). After commit returns, the transaction is durable. This is the default. |
| `SyncNormal` | Fsync the WAL on commit but defer the main-file fsync to checkpoint. A crash can lose only un-checkpointed work, never corrupt the file. |
| `SyncOff` | No fsync. Fast and unsafe; a crash may lose recent commits. Intended for bulk-load-then-checkpoint workflows. |

## On-disk conventions

An index is one self-describing file. These conventions are fixed by the
format, not configurable.

- **Single file, always.** The term dictionaries, postings, stored fields,
  doc-values, and vector graphs all live in one file. Backup is `cp`.
- **The extension is `.sx`.** The binary is `sx` and the C library is
  `libsearch`.
- **Little-endian on disk, unconditionally,** on every platform.
- **The first 16 bytes are the magic** `tamndsearch fmt1`, so a file is
  identified by content, not by name. A file that does not start with the magic
  is rejected with `ErrNotSxFile`.
- **Page size is a power of two from 4096 to 65536, default 16384,** fixed at
  file creation and recorded in the header. The size cannot change after
  creation.

## Format version and the 1.0 freeze

The header carries an on-disk format version and a compatibility floor. At the
1.0 release the format is frozen for the 1.x line.

| Constant | Value | Meaning |
|----------|-------|---------|
| `FormatVersion` | `1` | The on-disk format version this build reads and writes. |
| engine version | `0x0100` | The build's engine version: high byte major, low byte minor (`0x0100` = 1.0). |
| `format_version_compat_min` | `0x0100` | The minimum engine version that can open a freshly created file, recorded in the header at offset 88. |

When a build opens a file whose `format_version_compat_min` exceeds its own
engine version, the open fails with `ErrTooNew` rather than a confusing feature
error: the file was written by a newer engine. A file written by an older
engine opens normally, so the 1.x line stays backward compatible.
