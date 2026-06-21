package main

// main exists so the package builds as a command and as a c-shared library.
// When built with `go build -buildmode=c-shared` the Go runtime ignores main and
// exposes only the exported sx_* symbols; a plain `go build` produces a no-op
// binary. All of the surface lives in core.go and cabi.go.
func main() {}
