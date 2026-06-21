//go:build cgo

// This file is the cgo shim that exports the sx_* C symbols. It is compiled only
// when CGO_ENABLED=1; the pure-Go core in core.go carries all the engine logic
// and is what the parity tests exercise. Building this package with
// `go build -buildmode=c-shared` produces libsearch.{so,dylib,dll} plus a cgo
// generated header. The committed, documented header is cabi/search.h; the
// generate step diffs the two to catch ABI drift.
package main

/*
#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>
*/
import "C"

import (
	"strconv"
	"unsafe"
)

// handle is the C-visible opaque handle type. The exported functions take and
// return uintptr-sized handles cast to opaque pointers in search.h.

//export sx_libversion
func sx_libversion() *C.char {
	return C.CString(LibVersion)
}

//export sx_libversion_number
func sx_libversion_number() C.int {
	return C.int(LibVersionNumber)
}

//export sx_abi_version
func sx_abi_version() C.int {
	return C.int(ABIVersion)
}

//export sx_open
func sx_open(path *C.char, flags C.int, db *C.uint64_t) C.int {
	h, rc, _ := Open(C.GoString(path), int(flags))
	if db != nil {
		*db = C.uint64_t(h)
	}
	return C.int(rc)
}

//export sx_open_v2
func sx_open_v2(path *C.char, flags C.int, _ *C.char, db *C.uint64_t) C.int {
	// options_json is accepted for ABI compatibility; the open options it can
	// carry are applied through the Go API in future revisions.
	return sx_open(path, flags, db)
}

//export sx_close
func sx_close(db C.uint64_t) C.int {
	return C.int(Close(uint64(db)))
}

//export sx_errmsg
func sx_errmsg(db C.uint64_t) *C.char {
	return C.CString(ErrMsg(uint64(db)))
}

//export sx_errmsg_writer
func sx_errmsg_writer(w C.uint64_t) *C.char {
	return C.CString(ErrMsgWriter(uint64(w)))
}

//export sx_errmsg_snapshot
func sx_errmsg_snapshot(snap C.uint64_t) *C.char {
	return C.CString(ErrMsgSnapshot(uint64(snap)))
}

//export sx_set_mapping
func sx_set_mapping(db C.uint64_t, mappingJSON *C.char) C.int {
	return C.int(SetMapping(uint64(db), C.GoString(mappingJSON)))
}

//export sx_define_field
func sx_define_field(db C.uint64_t, fieldJSON *C.char) C.int {
	return C.int(DefineField(uint64(db), C.GoString(fieldJSON)))
}

//export sx_get_mapping
func sx_get_mapping(db C.uint64_t, out **C.char) C.int {
	s, rc := GetMapping(uint64(db))
	if out != nil {
		*out = C.CString(s)
	}
	return C.int(rc)
}

//export sx_writer_open
func sx_writer_open(db C.uint64_t, w *C.uint64_t) C.int {
	h, rc := WriterOpen(uint64(db))
	if w != nil {
		*w = C.uint64_t(h)
	}
	return C.int(rc)
}

//export sx_index
func sx_index(w C.uint64_t, docJSON *C.char) C.int {
	return C.int(Index(uint64(w), C.GoString(docJSON)))
}

//export sx_index_buf
func sx_index_buf(w C.uint64_t, buf *C.char, n C.size_t) C.int {
	return C.int(Index(uint64(w), C.GoStringN(buf, C.int(n))))
}

//export sx_delete
func sx_delete(w C.uint64_t, id *C.char) C.int {
	return C.int(DeleteDoc(uint64(w), C.GoString(id)))
}

//export sx_commit
func sx_commit(w C.uint64_t) C.int {
	return C.int(Commit(uint64(w)))
}

//export sx_writer_close
func sx_writer_close(w C.uint64_t) C.int {
	return C.int(WriterClose(uint64(w)))
}

//export sx_snapshot_open
func sx_snapshot_open(db C.uint64_t, snap *C.uint64_t) C.int {
	h, rc := SnapshotOpen(uint64(db))
	if snap != nil {
		*snap = C.uint64_t(h)
	}
	return C.int(rc)
}

//export sx_snapshot_close
func sx_snapshot_close(snap C.uint64_t) C.int {
	return C.int(SnapshotClose(uint64(snap)))
}

//export sx_snapshot_txid
func sx_snapshot_txid(snap C.uint64_t) C.uint64_t {
	return C.uint64_t(SnapshotTxid(uint64(snap)))
}

//export sx_prepare
func sx_prepare(snap C.uint64_t, queryJSON *C.char, q *C.uint64_t) C.int {
	h, rc := Prepare(uint64(snap), C.GoString(queryJSON))
	if q != nil {
		*q = C.uint64_t(h)
	}
	return C.int(rc)
}

//export sx_query_run
func sx_query_run(q C.uint64_t, optionsJSON *C.char, cursor *C.uint64_t) C.int {
	h, rc := QueryRun(uint64(q), C.GoString(optionsJSON))
	if cursor != nil {
		*cursor = C.uint64_t(h)
	}
	return C.int(rc)
}

//export sx_query_close
func sx_query_close(q C.uint64_t) C.int {
	return C.int(QueryClose(uint64(q)))
}

//export sx_step
func sx_step(cursor C.uint64_t) C.int {
	return C.int(Step(uint64(cursor)))
}

//export sx_column_id
func sx_column_id(cursor C.uint64_t) *C.char {
	id, ok := ColumnID(uint64(cursor))
	if !ok {
		return nil
	}
	return C.CString(id)
}

//export sx_column_score
func sx_column_score(cursor C.uint64_t) C.float {
	return C.float(ColumnScore(uint64(cursor)))
}

//export sx_column_text
func sx_column_text(cursor C.uint64_t, field *C.char) *C.char {
	s, ok := ColumnText(uint64(cursor), C.GoString(field))
	if !ok {
		return nil
	}
	return C.CString(s)
}

//export sx_column_text_dup
func sx_column_text_dup(cursor C.uint64_t, field *C.char) *C.char {
	s, ok := ColumnText(uint64(cursor), C.GoString(field))
	if !ok {
		return nil
	}
	return C.CString(s)
}

//export sx_column_int
func sx_column_int(cursor C.uint64_t, field *C.char) C.int64_t {
	s, ok := ColumnText(uint64(cursor), C.GoString(field))
	if !ok {
		return 0
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return C.int64_t(v)
}

//export sx_column_float
func sx_column_float(cursor C.uint64_t, field *C.char) C.double {
	s, ok := ColumnText(uint64(cursor), C.GoString(field))
	if !ok {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return C.double(v)
}

//export sx_column_json
func sx_column_json(cursor C.uint64_t) *C.char {
	s, ok := ColumnJSON(uint64(cursor))
	if !ok {
		return nil
	}
	return C.CString(s)
}

//export sx_result_total
func sx_result_total(cursor C.uint64_t) C.int64_t {
	return C.int64_t(ResultTotal(uint64(cursor)))
}

//export sx_cursor_close
func sx_cursor_close(cursor C.uint64_t) C.int {
	return C.int(CursorClose(uint64(cursor)))
}

//export sx_analyze
func sx_analyze(snap C.uint64_t, field, text *C.char, outJSON **C.char) C.int {
	s, rc := Analyze(uint64(snap), C.GoString(field), C.GoString(text))
	if outJSON != nil {
		*outJSON = C.CString(s)
	}
	return C.int(rc)
}

//export sx_compact
func sx_compact(db C.uint64_t) C.int {
	return C.int(Compact(uint64(db)))
}

//export sx_stats_json
func sx_stats_json(db C.uint64_t, out **C.char) C.int {
	s, rc := StatsJSON(uint64(db))
	if out != nil {
		*out = C.CString(s)
	}
	return C.int(rc)
}

//export sx_free
func sx_free(ptr unsafe.Pointer) {
	C.free(ptr)
}
