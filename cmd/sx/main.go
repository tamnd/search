// Command sx is the CLI for the search engine (spec 2063 doc 21). At S0 it
// carries only version and help; the index, search, inspect, compact, backup,
// info, bench, and repl subcommands land as their engine support arrives.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/tamnd/search"
)

// version is the CLI version, overridable via -ldflags at release time.
var version = "0.1.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "version", "-v", "--version":
		printVersion(os.Stdout)
		return 0
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	case "create":
		return cmdCreate(args[1:])
	case "index":
		return cmdIndex(args[1:])
	case "get":
		return cmdGet(args[1:])
	case "analyze":
		return cmdAnalyze(args[1:])
	case "schema":
		return cmdSchema(args[1:])
	case "inspect":
		return cmdInspect(args[1:])
	case "query", "search":
		return cmdQuery(args[1:])
	default:
		_, _ = fmt.Fprintf(os.Stderr, "sx: unknown subcommand %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

func printVersion(w io.Writer) {
	_, _ = fmt.Fprintf(w, "sx %s\nformat version: %d\nbuild: %s\n",
		version, search.FormatVersion, buildCommit())
}

// buildCommit returns the VCS revision embedded by the Go toolchain, or
// "unknown" when the binary was built without VCS stamping.
func buildCommit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			if len(s.Value) >= 12 {
				return s.Value[:12]
			}
			return s.Value
		}
	}
	return "unknown"
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `sx - single-file full-text and vector search

usage:
  sx <command> [arguments]

commands:
  version    print the CLI, format, and build versions
  help       show this help

  create     create a .sx file and set its schema
  index      index documents from JSONL into a .sx file
  get        fetch a stored document by id
  analyze    run an analyzer over text
  schema     print the schema of a .sx file

  inspect    dump the segment structure of a .sx file
  query      run a query against a .sx file

  compact    compact and reclaim space               (not yet implemented)
  backup     copy a consistent snapshot              (not yet implemented)
  info       print file header and meta summary       (not yet implemented)
  bench      run the latency benchmark suite          (not yet implemented)
  repl       interactive query shell                  (not yet implemented)
`)
}
