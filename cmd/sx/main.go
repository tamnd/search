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
var version = "1.0.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	args = stripGlobalFlags(args)
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
	case "update":
		return cmdUpdate(args[1:])
	case "delete":
		return cmdDelete(args[1:])
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
	case "sql":
		return cmdSQL(args[1:])
	case "knn":
		return cmdKNN(args[1:])
	case "hybrid":
		return cmdHybrid(args[1:])
	case "compact":
		return cmdCompact(args[1:])
	case "info":
		return cmdInfo(args[1:])
	case "stats":
		return cmdStats(args[1:])
	case "verify":
		return cmdVerify(args[1:])
	case "checkpoint":
		return cmdCheckpoint(args[1:])
	case "repair":
		return cmdRepair(args[1:])
	case "restore":
		return cmdRestore(args[1:])
	case "export":
		return cmdExport(args[1:])
	case "import":
		return cmdImport(args[1:])
	case "backup":
		return cmdBackup(args[1:])
	case "vacuum":
		return cmdVacuum(args[1:])
	case "bench":
		return cmdBench(args[1:])
	default:
		_, _ = fmt.Fprintf(os.Stderr, "sx: unknown subcommand %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

// unsafeNoLock is set by the global --unsafe-no-lock flag. It is read by
// openIndex when opening any index, so it applies uniformly to every command.
var unsafeNoLock bool

// stripGlobalFlags pulls flags that apply to every subcommand out of the
// argument list, wherever they appear, and returns the remaining arguments. It
// keeps the per-command flag sets free of having to declare these.
func stripGlobalFlags(args []string) []string {
	out := args[:0:0]
	for _, a := range args {
		switch a {
		case "--unsafe-no-lock":
			unsafeNoLock = true
		default:
			out = append(out, a)
		}
	}
	return out
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
  update     reindex documents, replacing ones with the same id
  delete     soft-delete documents by external id
  get        fetch a stored document by id
  analyze    run an analyzer over text
  schema     print the schema of a .sx file

  inspect    dump the segment structure of a .sx file
  query      run a query against a .sx file
  sql        run a SELECT statement through the built-in SQL surface
  knn        run a k-nearest-neighbor vector search
  hybrid     run a hybrid text + vector search fused with RRF
  compact    merge segments and reclaim deleted space

  info       print the file header and meta summary
  stats      print structural and runtime statistics
  verify     check the file for corruption (--deep scans all postings)
  checkpoint fold the WAL sidecar into the file (no-op: file is self-contained)
  repair     rebuild a damaged file into a new one (best-effort)
  export     write every live document as JSONL
  import     index documents from JSONL in streaming batches
  backup     copy a consistent snapshot to another path
  restore    copy a backup to a destination and verify it
  vacuum     force-merge all segments and reap deleted documents
  bench      run a load-test scenario and report latency percentiles

global flags:
  --unsafe-no-lock   open without the multi-process file lock (NFS escape hatch)
`)
}
