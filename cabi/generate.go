package main

// The c-shared build emits a cgo-generated header next to the library. The
// committed, documented header is search.h; regenerate the library with:
//
//	go build -buildmode=c-shared -o libsearch.dylib ./cabi
//
// and diff the emitted libsearch.h against search.h to catch ABI drift. The CI
// C-ABI step performs this build and runs the language-binding example against
// the produced library.
//
//go:generate go build -buildmode=c-shared -o libsearch.dylib .
