# The C ABI

The engine ships a C ABI so non-Go programs can embed it.
You build a shared library, link or `dlopen` it, and drive it through the `sx_*` functions declared in `cabi/search.h`.
This page covers building the library, the handle model, the open/write/read lifecycle, and memory ownership.

## Building the shared library

Build the `cabi` package with `-buildmode=c-shared`.
That emits the shared object plus a cgo-generated header; `cabi/search.h` is the canonical, hand-documented header that declares the same symbols.

```
go build -buildmode=c-shared -o libsearch.dylib ./cabi   # macOS
go build -buildmode=c-shared -o libsearch.so   ./cabi   # Linux
go build -buildmode=c-shared -o libsearch.dll  ./cabi   # Windows
```

This is the one place the project uses cgo, so build with `CGO_ENABLED=1` and a working C toolchain.

## The handle model

Handles are opaque 64-bit integers, not pointers.
The high 8 bits encode the object kind, so a handle passed to the wrong function is rejected.
Never dereference a handle, and never reuse one after its close call.

There are five handle types:

```c
typedef uint64_t sx_db;        /* an open index */
typedef uint64_t sx_writer;    /* a batching writer */
typedef uint64_t sx_snapshot;  /* a pinned read snapshot */
typedef uint64_t sx_query;     /* a prepared query */
typedef uint64_t sx_cursor;    /* a row iterator */
```

All strings are UTF-8.
Result codes are `int`: `SX_OK` is 0, every error is a positive integer (`SX_NOTFOUND`, `SX_CORRUPT`, `SX_READONLY`, `SX_CLOSED`, `SX_SCHEMA`, and so on), and `sx_step` additionally returns `SX_ROW` (100) and `SX_DONE` (101).

## Open and close

`sx_open` opens or creates the index at `path`.
`flags` combines the `SX_OPEN_*` constants.
On success `*db` receives the handle and the call returns `SX_OK`; on failure `*db` is set to 0.

```c
#include "search.h"

sx_db db = 0;
int rc = sx_open("products.sx", SX_OPEN_READWRITE | SX_OPEN_CREATE, &db);
if (rc != SX_OK) { /* handle error */ }
```

`sx_open_v2` is the same but takes a JSON options object (may be NULL).
`sx_close` releases the handle and returns `SX_SNAPSHOTS` if open writers or snapshots remain, so close those first.

`sx_errmsg(db)` returns the most recent error string for a database handle (and there are writer and snapshot variants).
The returned string is caller-owned: release it with `sx_free`.

## Defining the schema

Define fields before indexing.
`sx_define_field` adds or updates one field from a JSON object; `sx_set_mapping` replaces the whole mapping from a JSON document with a `fields` array.

```c
sx_define_field(db, "{\"name\":\"title\",\"type\":\"text\",\"stored\":true,\"indexed\":true}");
sx_define_field(db, "{\"name\":\"category\",\"type\":\"keyword\",\"stored\":true,\"doc_values\":true}");
```

`sx_get_mapping(db, &out)` returns the current mapping as a JSON string in `*out`, caller-owned.

## Writing

Writes go through a batching writer.
Open one, buffer documents and deletes, then commit atomically.

```c
sx_writer w = 0;
sx_writer_open(db, &w);

sx_index(w, "{\"_id\":\"p1\",\"title\":\"Red running shoes\",\"category\":\"footwear\"}");
sx_index(w, "{\"_id\":\"p2\",\"title\":\"Blue hiking boots\",\"category\":\"footwear\"}");

sx_commit(w);   /* applies every buffered change atomically */
```

`sx_index` buffers a document (a JSON object including the id field); an existing document with the same id is replaced.
`sx_index_buf` is the same but takes a buffer and a length.
`sx_delete` buffers a delete by external id.
After a successful `sx_commit` the writer handle is invalid.
To throw away buffered changes instead, call `sx_writer_close`, which discards them and releases the writer.

## Reading

A snapshot pins the current committed state for reading.
You prepare a query against it, run the query to get a cursor, then step the cursor row by row.

```c
sx_snapshot snap = 0;
sx_snapshot_open(db, &snap);

sx_query q = 0;
sx_prepare(snap, "{\"match\":{\"field\":\"title\",\"query\":\"running\"}}", &q);

sx_cursor cur = 0;
sx_query_run(q, "{\"from\":0,\"size\":10}", &cur);   /* NULL options means from=0, size=10 */

while (sx_step(cur) == SX_ROW) {
    const char *id = sx_column_id(cur);
    float score = sx_column_score(cur);
    const char *json = sx_column_json(cur);   /* full row with _id and _score */
    /* use them ... */
    sx_free((void *)id);
    sx_free((void *)json);
}

sx_cursor_close(cur);
sx_query_close(q);
sx_snapshot_close(snap);
```

`sx_prepare` compiles a JSON query (the same DSL as [querying](03-querying.md)) against the snapshot's schema.
`sx_step` returns `SX_ROW` while rows remain, then `SX_DONE`, or an error code.

Per-column accessors for the current row:

- `sx_column_id` returns the external id (caller-owned).
- `sx_column_score` returns the relevance score.
- `sx_column_text` / `sx_column_text_dup` return a stored field (caller-owned).
- `sx_column_int` and `sx_column_float` return typed scalar fields.
- `sx_column_json` returns the whole row as a JSON object including `_id` and `_score` (caller-owned).
- `sx_result_total` returns the number of matching documents in the window.

Each of `prepare`, `run`, and `cursor` has its own close call, and they should be closed in the reverse of the order they were opened.

## Memory ownership

Any string or buffer the library returns is caller-owned and must be released with `sx_free`.

```c
const char *json = sx_column_json(cur);
/* ... use json ... */
sx_free((void *)json);
```

That covers `sx_errmsg` and its variants, `sx_get_mapping`, the `sx_column_*` string accessors, `sx_stats_json`, and `sx_analyze`.
Scalar returns (the `int` result codes, `sx_column_score`, `sx_column_int`, `sx_column_float`, `sx_result_total`) own nothing.
Do not free a handle with `sx_free`; close handles with their dedicated close call.

## Admin from C

A few admin operations are exposed directly:

- `sx_compact(db)` runs a synchronous full compaction.
- `sx_stats_json(db, &out)` returns engine statistics as JSON (caller-owned).
- `sx_analyze(snap, field, text, &out_json)` runs a field's analyzer over text and returns a JSON token array (caller-owned).

## A full example

`examples/python/search_example.py` drives the library from Python through ctypes, and the `cabi` test `TestCABIPythonExample` builds the shared object and runs it.
It is the shortest end-to-end reference: open, define fields, open a writer, index three documents, commit, open a snapshot, prepare a match query, run it, step the cursor, and close everything in order.
The same call sequence works from any language with a C FFI.
