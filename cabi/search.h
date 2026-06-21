/* search.h - C API for github.com/tamnd/search
 *
 * The shared library is built with `go build -buildmode=c-shared`, which emits
 * libsearch.so (Linux), libsearch.dylib (macOS), or libsearch.dll (Windows)
 * plus a cgo-generated header. This file is the canonical, documented header:
 * it declares the same symbols with stable opaque handle typedefs and the error
 * code constants. All strings are UTF-8. All sizes are in bytes unless noted.
 * Result codes: SX_OK == 0; all errors are positive integers.
 *
 * Handles are opaque 64-bit values, not pointers. The high 8 bits encode the
 * object kind so a handle passed to the wrong function is rejected; never
 * dereference a handle and never reuse one after its close call.
 */

#ifndef SEARCH_H
#define SEARCH_H

#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* ---- ABI version ---- */
#define SX_ABI_VERSION 1

/* ---- Result codes ---- */
#define SX_OK         0
#define SX_NOTFOUND   1
#define SX_CORRUPT    2
#define SX_READONLY   3
#define SX_CLOSED     4
#define SX_CONFLICT   5
#define SX_TIMEOUT    6
#define SX_SCHEMA     7
#define SX_TOOBIG     8
#define SX_BUSY       9
#define SX_SNAPSHOTS  10
#define SX_CAPACITY   11
#define SX_VERSION    12
#define SX_ERROR      99

/* Returned by sx_step when a row is available. */
#define SX_ROW        100
/* Returned by sx_step when iteration is exhausted. */
#define SX_DONE       101

/* ---- Open flags ---- */
#define SX_OPEN_READWRITE      0x00000001
#define SX_OPEN_READONLY       0x00000002
#define SX_OPEN_CREATE         0x00000004
#define SX_OPEN_MEMORY         0x00000008

/* ---- Opaque handle types ---- */
typedef uint64_t sx_db;
typedef uint64_t sx_writer;
typedef uint64_t sx_snapshot;
typedef uint64_t sx_query;
typedef uint64_t sx_cursor;

/* ---- Library version ---- */

/* sx_libversion returns the library version string, e.g. "1.3.2". */
const char *sx_libversion(void);

/* sx_libversion_number returns MAJOR*1000000 + MINOR*1000 + PATCH. */
int sx_libversion_number(void);

/* sx_abi_version returns the C ABI contract version (SX_ABI_VERSION). */
int sx_abi_version(void);

/* ---- Open / Close ---- */

/* sx_open opens or creates the index at path. flags is a combination of the
 * SX_OPEN_* constants. On success *db receives the handle and SX_OK is returned;
 * on failure *db is set to 0. */
int sx_open(const char *path, int flags, sx_db *db);

/* sx_open_v2 is like sx_open but accepts a JSON options object (may be NULL). */
int sx_open_v2(const char *path, int flags, const char *options_json, sx_db *db);

/* sx_close closes db. Returns SX_SNAPSHOTS if open writers or snapshots remain.
 * After a successful close the handle is invalid. */
int sx_close(sx_db db);

/* sx_errmsg returns the most recent error string for db, "" if none. The
 * pointer is owned by the caller and must be released with sx_free. */
const char *sx_errmsg(sx_db db);
const char *sx_errmsg_writer(sx_writer w);
const char *sx_errmsg_snapshot(sx_snapshot snap);

/* ---- Schema ---- */

/* sx_set_mapping replaces the entire field mapping from a JSON document with a
 * "fields" array. Returns SX_SCHEMA on a malformed mapping. */
int sx_set_mapping(sx_db db, const char *mapping_json);

/* sx_define_field adds or updates one field from a JSON object with keys name,
 * type, stored, indexed, doc_values, positions, analyzer, vector_dim, etc. */
int sx_define_field(sx_db db, const char *field_json);

/* sx_get_mapping returns the current mapping as a JSON string in *out. The
 * pointer is caller-owned; release it with sx_free. */
int sx_get_mapping(sx_db db, const char **out);

/* ---- Write ---- */

/* sx_writer_open opens a batching writer for db. */
int sx_writer_open(sx_db db, sx_writer *w);

/* sx_index buffers a document (a JSON object including the id field) for the
 * next commit. An existing document with the same id is replaced. */
int sx_index(sx_writer w, const char *doc_json);

/* sx_index_buf is like sx_index but takes a byte buffer of length len. */
int sx_index_buf(sx_writer w, const char *buf, size_t len);

/* sx_delete buffers a delete of the document with the given external id. */
int sx_delete(sx_writer w, const char *id);

/* sx_commit applies all buffered deletes and documents atomically. After a
 * successful commit the writer handle is invalid. */
int sx_commit(sx_writer w);

/* sx_writer_close discards uncommitted changes and releases the writer. */
int sx_writer_close(sx_writer w);

/* ---- Snapshots ---- */

/* sx_snapshot_open pins the current committed state for reading. */
int sx_snapshot_open(sx_db db, sx_snapshot *snap);

/* sx_snapshot_close releases the snapshot. */
int sx_snapshot_close(sx_snapshot snap);

/* sx_snapshot_txid returns the transaction id of snap. */
uint64_t sx_snapshot_txid(sx_snapshot snap);

/* ---- Query / Prepare ---- */

/* sx_prepare compiles a JSON query against snap's schema into *q. */
int sx_prepare(sx_snapshot snap, const char *query_json, sx_query *q);

/* sx_query_run executes q and returns a cursor. options_json controls
 * pagination (from, size); pass NULL for from=0, size=10. */
int sx_query_run(sx_query q, const char *options_json, sx_cursor *cursor);

/* sx_query_close releases the prepared query. */
int sx_query_close(sx_query q);

/* ---- Cursor / Row iteration ---- */

/* sx_step advances the cursor. Returns SX_ROW, SX_DONE, or an error code. */
int sx_step(sx_cursor cursor);

/* sx_column_id returns the external id of the current row, caller-owned (free
 * with sx_free), or NULL if no row is current. */
const char *sx_column_id(sx_cursor cursor);

/* sx_column_score returns the relevance score of the current row. */
float sx_column_score(sx_cursor cursor);

/* sx_column_text returns a stored field of the current row, caller-owned (free
 * with sx_free), or NULL if absent. */
const char *sx_column_text(sx_cursor cursor, const char *field);

/* sx_column_text_dup is an alias of sx_column_text; the result is caller-owned
 * and must be released with sx_free. */
char *sx_column_text_dup(sx_cursor cursor, const char *field);

/* sx_column_int returns an integer field of the current row. */
int64_t sx_column_int(sx_cursor cursor, const char *field);

/* sx_column_float returns a float field of the current row. */
double sx_column_float(sx_cursor cursor, const char *field);

/* sx_column_json returns the current row as a JSON object (with _id and _score),
 * caller-owned; release it with sx_free. */
const char *sx_column_json(sx_cursor cursor);

/* sx_result_total returns the number of matching documents in the window. */
int64_t sx_result_total(sx_cursor cursor);

/* sx_cursor_close releases the cursor. */
int sx_cursor_close(sx_cursor cursor);

/* ---- Admin ---- */

/* sx_compact runs a synchronous full compaction. */
int sx_compact(sx_db db);

/* sx_stats_json returns engine statistics as JSON in *out, caller-owned. */
int sx_stats_json(sx_db db, const char **out);

/* sx_analyze runs a field's analyzer over text and returns a JSON token array
 * in *out_json, caller-owned. */
int sx_analyze(sx_snapshot snap, const char *field, const char *text,
               const char **out_json);

/* ---- Memory management ---- */

/* sx_free releases a string or buffer returned by this library. */
void sx_free(void *ptr);

#ifdef __cplusplus
}
#endif
#endif /* SEARCH_H */
