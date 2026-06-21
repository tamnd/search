---
title: "C ABI reference"
description: "The libsearch C ABI: building it, the handle model, status codes, and memory ownership."
weight: 30
---

The engine ships a C ABI so non-Go programs can embed it. You build a shared
library, link or `dlopen` it, and drive it through the `sx_*` functions declared
in `cabi/search.h`. That header is the canonical, hand-documented contract; this
page covers building the library, the handle model, the status codes, and memory
ownership.

## Building the shared library

Build the `cabi` package with `-buildmode=c-shared`. That emits the shared
object plus a cgo-generated header.

```
go build -buildmode=c-shared -o libsearch.dylib ./cabi   # macOS
go build -buildmode=c-shared -o libsearch.so   ./cabi   # Linux
go build -buildmode=c-shared -o libsearch.dll  ./cabi   # Windows
```

This is the one place the project uses cgo, so build with `CGO_ENABLED=1` and a
working C toolchain.

## ABI version

`SX_ABI_VERSION` is `1`. The functions below are the version 1 contract. Three
calls report versions at runtime:

| Function | Returns |
|----------|---------|
| `sx_libversion()` | The library version string, e.g. `"1.0.0"`. |
| `sx_libversion_number()` | `MAJOR*1000000 + MINOR*1000 + PATCH`. |
| `sx_abi_version()` | The ABI contract version (`SX_ABI_VERSION`). |

## The handle model

Handles are opaque 64-bit integers, not pointers. The high 8 bits encode the
object kind, so a handle passed to the wrong function is rejected. Never
dereference a handle, and never reuse one after its close call.

```c
typedef uint64_t sx_db;        /* an open index */
typedef uint64_t sx_writer;    /* a batching writer */
typedef uint64_t sx_snapshot;  /* a pinned read snapshot */
typedef uint64_t sx_query;     /* a prepared query */
typedef uint64_t sx_cursor;    /* a row iterator */
```

## Status codes

Every call that can fail returns an `int`. `SX_OK` is 0; every error is a
positive integer. `sx_step` additionally returns `SX_ROW` and `SX_DONE`.

| Code | Value | Meaning |
|------|-------|---------|
| `SX_OK` | 0 | Success. |
| `SX_NOTFOUND` | 1 | The requested object does not exist. |
| `SX_CORRUPT` | 2 | The file failed an integrity check. |
| `SX_READONLY` | 3 | A write was attempted on a read-only handle. |
| `SX_CLOSED` | 4 | The handle is closed or invalid. |
| `SX_CONFLICT` | 5 | A write conflict. |
| `SX_TIMEOUT` | 6 | An operation timed out. |
| `SX_SCHEMA` | 7 | A malformed schema or mapping. |
| `SX_TOOBIG` | 8 | A value exceeds a size limit. |
| `SX_BUSY` | 9 | The resource is busy. |
| `SX_SNAPSHOTS` | 10 | `sx_close` found open writers or snapshots still pinning the db. |
| `SX_CAPACITY` | 11 | A capacity limit was reached. |
| `SX_VERSION` | 12 | A version mismatch. |
| `SX_ERROR` | 99 | A generic error; read `sx_errmsg` for detail. |
| `SX_ROW` | 100 | `sx_step` advanced to a row. |
| `SX_DONE` | 101 | `sx_step` exhausted the cursor. |

Open flags combine with bitwise OR: `SX_OPEN_READWRITE`, `SX_OPEN_READONLY`,
`SX_OPEN_CREATE`, `SX_OPEN_MEMORY`.

## Memory ownership

Any `const char *` or buffer the library returns through an out-parameter or a
return value is owned by the caller and must be released with `sx_free`. This
covers `sx_errmsg`, `sx_get_mapping`, `sx_column_id`, `sx_column_text`,
`sx_column_text_dup`, `sx_column_json`, `sx_stats_json`, and `sx_analyze`. Free
each string exactly once, and never pass it to `free` from the C runtime: the
allocation lives in the Go runtime, so only `sx_free` can release it.

```c
const char *msg = sx_errmsg(db);
/* ... use msg ... */
sx_free((void *)msg);
```

Handles are released by their own close call, not by `sx_free`: `sx_close`,
`sx_writer_close`, `sx_snapshot_close`, `sx_query_close`, `sx_cursor_close`. A
successful `sx_commit` invalidates the writer handle, so it need not be closed
after a commit.

## Usage sketch

The lifecycle is open, write through a batching writer, then read through a
snapshot, a prepared query, and a cursor. From Python through ctypes:

```python
import ctypes

lib = ctypes.CDLL("./libsearch.so")
lib.sx_open.argtypes = [ctypes.c_char_p, ctypes.c_int, ctypes.POINTER(ctypes.c_uint64)]
lib.sx_column_json.restype = ctypes.c_char_p

SX_OPEN_READWRITE = 0x1
SX_OPEN_CREATE = 0x4
SX_ROW = 100

db = ctypes.c_uint64()
lib.sx_open(b"products.sx", SX_OPEN_READWRITE | SX_OPEN_CREATE, ctypes.byref(db))

w = ctypes.c_uint64()
lib.sx_writer_open(db, ctypes.byref(w))
lib.sx_index(w, b'{"id": "1", "title": "wireless mouse"}')
lib.sx_commit(w)  # commit invalidates the writer handle

snap = ctypes.c_uint64()
lib.sx_snapshot_open(db, ctypes.byref(snap))

q = ctypes.c_uint64()
lib.sx_prepare(snap, b'{"match": {"title": "mouse"}}', ctypes.byref(q))

cur = ctypes.c_uint64()
lib.sx_query_run(q, None, ctypes.byref(cur))
while lib.sx_step(cur) == SX_ROW:
    print(lib.sx_column_json(cur).decode())  # caller owns the string; free it with sx_free

lib.sx_cursor_close(cur)
lib.sx_query_close(q)
lib.sx_snapshot_close(snap)
lib.sx_close(db)
```

The repository ships `examples/python/search_example.py`, and the `cabi` test
`TestCABIPythonExample` builds the shared object and runs it.
