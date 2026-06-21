#!/usr/bin/env python3
"""Minimal binding example for libsearch via ctypes (spec 2063 doc 16 §7.5).

Build the shared library first:

    go build -buildmode=c-shared -o examples/python/libsearch.dylib ./cabi

then run:

    python3 examples/python/search_example.py

The example opens a temp index, defines a schema, indexes three documents,
commits, runs a MATCH query, and prints the ranked hits. It mirrors the C usage
example in the spec, but through Python's ctypes so it doubles as the
TestCABIPythonExample fixture.
"""

import ctypes
import json
import os
import sys
import tempfile

SX_OK = 0
SX_ROW = 100
SX_DONE = 101
SX_OPEN_READWRITE = 0x01
SX_OPEN_CREATE = 0x04


def load_lib():
    here = os.path.dirname(os.path.abspath(__file__))
    for name in ("libsearch.dylib", "libsearch.so", "libsearch.dll"):
        path = os.path.join(here, name)
        if os.path.exists(path):
            return ctypes.CDLL(path)
    sys.exit("libsearch not found; build it with: go build -buildmode=c-shared "
             "-o examples/python/libsearch.dylib ./cabi")


def main():
    lib = load_lib()

    # Declare the signatures we use. Handles are uint64.
    lib.sx_open.argtypes = [ctypes.c_char_p, ctypes.c_int, ctypes.POINTER(ctypes.c_uint64)]
    lib.sx_define_field.argtypes = [ctypes.c_uint64, ctypes.c_char_p]
    lib.sx_writer_open.argtypes = [ctypes.c_uint64, ctypes.POINTER(ctypes.c_uint64)]
    lib.sx_index.argtypes = [ctypes.c_uint64, ctypes.c_char_p]
    lib.sx_commit.argtypes = [ctypes.c_uint64]
    lib.sx_snapshot_open.argtypes = [ctypes.c_uint64, ctypes.POINTER(ctypes.c_uint64)]
    lib.sx_snapshot_close.argtypes = [ctypes.c_uint64]
    lib.sx_prepare.argtypes = [ctypes.c_uint64, ctypes.c_char_p, ctypes.POINTER(ctypes.c_uint64)]
    lib.sx_query_run.argtypes = [ctypes.c_uint64, ctypes.c_char_p, ctypes.POINTER(ctypes.c_uint64)]
    lib.sx_query_close.argtypes = [ctypes.c_uint64]
    lib.sx_step.argtypes = [ctypes.c_uint64]
    lib.sx_column_json.argtypes = [ctypes.c_uint64]
    lib.sx_column_json.restype = ctypes.c_char_p
    lib.sx_cursor_close.argtypes = [ctypes.c_uint64]
    lib.sx_close.argtypes = [ctypes.c_uint64]

    path = os.path.join(tempfile.mkdtemp(), "products.sx")
    db = ctypes.c_uint64(0)
    rc = lib.sx_open(path.encode(), SX_OPEN_READWRITE | SX_OPEN_CREATE, ctypes.byref(db))
    assert rc == SX_OK, f"open rc={rc}"

    for field in (
        '{"name":"title","type":"text","stored":true,"indexed":true}',
        '{"name":"category","type":"keyword","stored":true,"doc_values":true}',
    ):
        assert lib.sx_define_field(db, field.encode()) == SX_OK

    w = ctypes.c_uint64(0)
    assert lib.sx_writer_open(db, ctypes.byref(w)) == SX_OK
    docs = [
        {"_id": "p1", "title": "Red running shoes", "category": "footwear"},
        {"_id": "p2", "title": "Blue hiking boots", "category": "footwear"},
        {"_id": "p3", "title": "Lightweight running jacket", "category": "apparel"},
    ]
    for d in docs:
        assert lib.sx_index(w, json.dumps(d).encode()) == SX_OK
    assert lib.sx_commit(w) == SX_OK

    snap = ctypes.c_uint64(0)
    assert lib.sx_snapshot_open(db, ctypes.byref(snap)) == SX_OK
    q = ctypes.c_uint64(0)
    query = '{"match":{"field":"title","query":"running"}}'
    assert lib.sx_prepare(snap, query.encode(), ctypes.byref(q)) == SX_OK
    cur = ctypes.c_uint64(0)
    assert lib.sx_query_run(q, b'{"from":0,"size":10}', ctypes.byref(cur)) == SX_OK

    print("hits for 'running':")
    while lib.sx_step(cur) == SX_ROW:
        row = json.loads(lib.sx_column_json(cur).decode())
        print(f"  {row['_id']}  score={row['_score']:.4f}  {row['title']}")

    lib.sx_cursor_close(cur)
    lib.sx_query_close(q)
    lib.sx_snapshot_close(snap)
    lib.sx_close(db)


if __name__ == "__main__":
    main()
