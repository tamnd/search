package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/tamnd/search"
)

// cmdInfo prints a header and meta summary of a .sx file: its geometry, format
// and engine versions, and document and segment counts. It reads no segment
// data, so it is instant on an index of any size.
func cmdInfo(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx info <file> [--format table|json]")
	}
	path := args[0]
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	format := fs.String("format", "table", "output format: table|json")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx info <file> [--format table|json]")
	}

	db, err := openIndex(path, true)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	fi, err := db.Info()
	if err != nil {
		return fail("info: %v", err)
	}
	if *format == "json" {
		return printJSON(fi)
	}
	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()
	_, _ = fmt.Fprintf(w, "file:           %s\n", fi.Path)
	_, _ = fmt.Fprintf(w, "page size:      %d\n", fi.PageSize)
	_, _ = fmt.Fprintf(w, "page count:     %d\n", fi.PageCount)
	_, _ = fmt.Fprintf(w, "file bytes:     %d\n", fi.FileBytes)
	_, _ = fmt.Fprintf(w, "format version: %d\n", fi.FormatVersion)
	_, _ = fmt.Fprintf(w, "engine min:     %#04x\n", fi.EngineVersionMin)
	_, _ = fmt.Fprintf(w, "creator:        %s\n", fi.Creator)
	_, _ = fmt.Fprintf(w, "created epoch:   %d\n", fi.CreatedEpoch)
	_, _ = fmt.Fprintf(w, "live txn:       %d\n", fi.TxnID)
	_, _ = fmt.Fprintf(w, "catalog root:   %d\n", fi.CatalogRoot)
	_, _ = fmt.Fprintf(w, "segments:       %d\n", fi.SegmentCount)
	_, _ = fmt.Fprintf(w, "documents:      %d\n", fi.DocCount)
	_, _ = fmt.Fprintf(w, "deleted:        %d\n", fi.DeletedDocCount)
	_, _ = fmt.Fprintf(w, "last doc id:    %d\n", fi.LastDocID)
	_, _ = fmt.Fprintf(w, "schema version: %d\n", fi.SchemaVersion)
	return 0
}

// cmdVerify runs an integrity check over a .sx file. By default it validates the
// catalog tree, every stored value, and each segment's term dictionary; --deep
// additionally reads every postings list, turning it into a full index scan. It
// exits non-zero when any fault is found so it is usable in a health check.
func cmdVerify(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx verify <file> [--deep] [--format table|json]")
	}
	path := args[0]
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	deep := fs.Bool("deep", false, "also read every postings list (full index scan)")
	format := fs.String("format", "table", "output format: table|json")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx verify <file> [--deep] [--format table|json]")
	}

	db, err := openIndex(path, true)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	rep, err := db.Verify(*deep)
	if err != nil {
		return fail("verify: %v", err)
	}

	if *format == "json" {
		rc := printJSON(rep)
		if !rep.OK() {
			return 1
		}
		return rc
	}

	w := bufio.NewWriter(os.Stdout)
	_, _ = fmt.Fprintf(w, "pages:        %d\n", rep.PageCount)
	_, _ = fmt.Fprintf(w, "catalog keys: %d\n", rep.CatalogKeys)
	_, _ = fmt.Fprintf(w, "live docs:    %d\n", rep.LiveDocs)
	_, _ = fmt.Fprintf(w, "segments:     %d\n", rep.Segments)
	_, _ = fmt.Fprintf(w, "fields:       %d\n", rep.Fields)
	_, _ = fmt.Fprintf(w, "terms:        %d\n", rep.Terms)
	if rep.Deep {
		_, _ = fmt.Fprintf(w, "postings:     %d\n", rep.PostingsRead)
	}
	if rep.OK() {
		_, _ = fmt.Fprintln(w, "result:       OK")
		_ = w.Flush()
		return 0
	}
	_, _ = fmt.Fprintf(w, "result:       %d problem(s)\n", len(rep.Errors))
	for _, e := range rep.Errors {
		_, _ = fmt.Fprintf(w, "  - %s\n", e)
	}
	_ = w.Flush()
	return 1
}

// cmdExport writes every live document of a .sx file as JSON Lines to --out or
// stdout. The stream restores each document's external id under the schema
// primary-key field, so the output reindexes cleanly with `sx index`.
func cmdExport(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx export <file> [--out docs.jsonl]")
	}
	path := args[0]
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	out := fs.String("out", "", "write to this file instead of stdout")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx export <file> [--out docs.jsonl]")
	}

	db, err := openIndex(path, true)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	var w io.Writer = os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return fail("create %s: %v", *out, err)
		}
		defer func() { _ = f.Close() }()
		w = f
	}

	n, err := db.Export(w)
	if err != nil {
		return fail("export: %v", err)
	}
	if *out != "" {
		fmt.Printf("exported %d document(s) to %s\n", n, *out)
	}
	return 0
}

// cmdImport indexes documents from a JSONL file in fixed-size batches, reporting
// progress to stderr as it goes. It differs from `sx index`, which loads the
// whole file into memory first: import streams the file so a multi-gigabyte dump
// indexes with bounded memory.
func cmdImport(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx import <file> [--file docs.jsonl] [--batch 1000] [--id-field _id]")
	}
	path := args[0]
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	docFile := fs.String("file", "", "path to a JSONL document file (default stdin)")
	batch := fs.Int("batch", 1000, "documents per index batch")
	idField := fs.String("id-field", "", "primary-key field name (sets the schema id field on an empty index)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx import <file> [--file docs.jsonl] [--batch 1000] [--id-field _id]")
	}
	if *batch < 1 {
		return fail("--batch must be positive")
	}

	var in io.Reader = os.Stdin
	if *docFile != "" {
		f, err := os.Open(*docFile)
		if err != nil {
			return fail("open %s: %v", *docFile, err)
		}
		defer func() { _ = f.Close() }()
		in = f
	}

	db, err := openIndex(path, false)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	if *idField != "" {
		if err := ensureIDField(db, *idField); err != nil {
			return fail("set id field: %v", err)
		}
	}

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	total, line := 0, 0
	buf := make([]map[string]any, 0, *batch)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		n, err := db.Index(buf)
		if err != nil {
			return err
		}
		total += n
		buf = buf[:0]
		_, _ = fmt.Fprintf(os.Stderr, "\rimported %d document(s)", total)
		return nil
	}
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(text), &doc); err != nil {
			return fail("line %d: %v", line, err)
		}
		buf = append(buf, doc)
		if len(buf) >= *batch {
			if err := flush(); err != nil {
				return fail("import: %v", err)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fail("read: %v", err)
	}
	if err := flush(); err != nil {
		return fail("import: %v", err)
	}
	_, _ = fmt.Fprintln(os.Stderr)
	fmt.Printf("imported %d document(s)\n", total)
	return 0
}

// cmdBackup copies a .sx file to a destination path as a consistent snapshot.
// The source is opened read-only so no writer can change it mid-copy, then the
// bytes are streamed out. The result is a standalone file that opens on its own.
func cmdBackup(args []string) int {
	if len(args) != 2 {
		return fail("usage: sx backup <file> <dest>")
	}
	src, dst := args[0], args[1]

	// Opening read-only validates the header and pins the file as a quiescent,
	// committed state for the duration of the copy.
	db, err := openIndex(src, true)
	if err != nil {
		return fail("open %s: %v", src, err)
	}

	in, err := os.Open(src)
	if err != nil {
		closeDB(db)
		return fail("open %s: %v", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		closeDB(db)
		return fail("create %s: %v", dst, err)
	}
	n, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	closeDB(db)
	if copyErr != nil {
		return fail("copy: %v", copyErr)
	}
	if syncErr != nil {
		return fail("sync %s: %v", dst, syncErr)
	}
	if closeErr != nil {
		return fail("close %s: %v", dst, closeErr)
	}
	fmt.Printf("backed up %d byte(s) to %s\n", n, dst)
	return 0
}

// cmdStats prints a structural and runtime summary of a .sx file: document and
// segment counts, the freelist and snapshot bookkeeping that signal whether the
// index needs maintenance, and a per-segment breakdown. It reads only metadata,
// so it is fast on an index of any size.
func cmdStats(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx stats <file> [--format table|json]")
	}
	path := args[0]
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	format := fs.String("format", "table", "output format: table|json")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx stats <file> [--format table|json]")
	}

	db, err := openIndex(path, true)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	st, err := db.Stats()
	if err != nil {
		return fail("stats: %v", err)
	}
	if *format == "json" {
		return printJSON(st)
	}

	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()
	_, _ = fmt.Fprintf(w, "file:            %s\n", st.Path)
	_, _ = fmt.Fprintf(w, "page size:       %d\n", st.PageSize)
	_, _ = fmt.Fprintf(w, "page count:      %d\n", st.PageCount)
	_, _ = fmt.Fprintf(w, "file bytes:      %d\n", st.FileBytes)
	_, _ = fmt.Fprintf(w, "free pages:      %d\n", st.FreePages)
	_, _ = fmt.Fprintf(w, "pending free:    %d\n", st.PendingFreePages)
	_, _ = fmt.Fprintf(w, "documents:       %d\n", st.DocCount)
	_, _ = fmt.Fprintf(w, "deleted:         %d\n", st.DeletedDocCount)
	_, _ = fmt.Fprintf(w, "segments:        %d\n", st.SegmentCount)
	_, _ = fmt.Fprintf(w, "terms (sum):     %d\n", st.TotalTerms)
	_, _ = fmt.Fprintf(w, "active readers:  %d\n", st.ActiveReaders)
	_, _ = fmt.Fprintf(w, "oldest reader:   %d\n", st.OldestReaderTxn)
	_, _ = fmt.Fprintf(w, "live txn:        %d\n", st.TxnID)
	for _, s := range st.Segments {
		var terms uint64
		for _, f := range s.Fields {
			terms += f.TermCount
		}
		_, _ = fmt.Fprintf(w, "  segment %d: docs=%d maxdoc=%d fields=%d terms=%d\n",
			s.ID, s.DocCount, s.MaxDoc, len(s.Fields), terms)
	}
	return 0
}

// cmdCheckpoint folds the WAL sidecar back into the main file and removes it.
//
// In this engine durability is provided by the double-buffered meta pages, not a
// separate write-ahead log wired into the pager, so a .sx file is always
// self-contained at rest and no `<file>-wal` sidecar is ever produced. The
// command therefore validates the file and, finding no sidecar, reports that the
// file is already self-contained and exits 0, which is exactly the no-op the
// operations spec prescribes when no sidecar is present. The command exists so
// scripts that defensively checkpoint before copying a file keep working.
func cmdCheckpoint(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx checkpoint <file>")
	}
	path := args[0]
	fs := flag.NewFlagSet("checkpoint", flag.ContinueOnError)
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx checkpoint <file>")
	}

	db, err := openIndex(path, true)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	closeDB(db)

	sidecar := path + "-wal"
	if _, err := os.Stat(sidecar); err == nil {
		// A sidecar should never exist for a file this engine wrote; if a future
		// format introduces one, surface it rather than silently ignore it.
		return fail("found %s but this build has no WAL to fold; leaving it in place", sidecar)
	}
	fmt.Printf("no WAL sidecar; %s is self-contained\n", path)
	return 0
}

// cmdRestore copies a backup file to a destination and verifies it. It is the
// inverse of `sx backup`: a smart copy that refuses to clobber an existing file
// without --force and confirms the restored file passes an integrity check
// before reporting success.
func cmdRestore(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx restore <dest> --from <backup> [--force] [--no-verify]")
	}
	dst := args[0]
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	from := fs.String("from", "", "source backup file (required)")
	force := fs.Bool("force", false, "overwrite the destination if it exists")
	noVerify := fs.Bool("no-verify", false, "skip the integrity check on the restored file")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *from == "" {
		return fail("usage: sx restore <dest> --from <backup> [--force] [--no-verify]")
	}

	if _, err := os.Stat(dst); err == nil && !*force {
		return fail("%s exists; pass --force to overwrite", dst)
	}

	in, err := os.Open(*from)
	if err != nil {
		return fail("open %s: %v", *from, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return fail("create %s: %v", dst, err)
	}
	n, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return fail("copy: %v", copyErr)
	}
	if syncErr != nil {
		return fail("sync %s: %v", dst, syncErr)
	}
	if closeErr != nil {
		return fail("close %s: %v", dst, closeErr)
	}

	if !*noVerify {
		db, err := openIndex(dst, true)
		if err != nil {
			return fail("open restored %s: %v", dst, err)
		}
		rep, err := db.Verify(false)
		closeDB(db)
		if err != nil {
			return fail("verify restored %s: %v", dst, err)
		}
		if !rep.OK() {
			_, _ = fmt.Fprintf(os.Stderr, "sx: restored file failed verification:\n")
			for _, e := range rep.Errors {
				_, _ = fmt.Fprintf(os.Stderr, "  - %s\n", e)
			}
			return 4
		}
	}
	fmt.Printf("restored %d byte(s) to %s\n", n, dst)
	return 0
}

// cmdRepair rebuilds a possibly-damaged index into a new file, leaving the
// source untouched. It exits 0 on a clean rebuild, 8 when it recovered the file
// but had to drop unreadable documents (best-effort partial success), and 4 when
// the source cannot be opened at all.
func cmdRepair(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx repair <file> [--out fixed.sx] [--force]")
	}
	src := args[0]
	fs := flag.NewFlagSet("repair", flag.ContinueOnError)
	out := fs.String("out", "", "output file for the rebuilt index (default <file>.repaired)")
	force := fs.Bool("force", false, "overwrite the output file if it exists")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx repair <file> [--out fixed.sx] [--force]")
	}
	dst := *out
	if dst == "" {
		dst = src + ".repaired"
	}
	if dst == src {
		return fail("repair never writes in place; choose a different --out")
	}
	if _, err := os.Stat(dst); err == nil {
		if !*force {
			return fail("%s exists; pass --force to overwrite", dst)
		}
		if err := os.Remove(dst); err != nil {
			return fail("remove %s: %v", dst, err)
		}
	}

	rep, err := search.Repair(src, dst, search.Options{UnsafeNoLock: unsafeNoLock})
	if err != nil {
		// The source could not be opened or the output could not be created: an
		// unrecoverable repair from the operator's point of view.
		_, _ = fmt.Fprintf(os.Stderr, "sx: repair: %v\n", err)
		return 4
	}

	fmt.Printf("repaired %s -> %s\n", src, dst)
	fmt.Printf("  recovered: %d document(s)\n", rep.Recovered)
	fmt.Printf("  dropped:   %d document(s)\n", rep.Dropped)
	if len(rep.Errors) > 0 {
		_, _ = fmt.Fprintf(os.Stderr, "sx: repair completed with %d warning(s):\n", len(rep.Errors))
		for _, e := range rep.Errors {
			_, _ = fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
	}
	if !rep.OK() {
		// Best-effort partial success: the file was rebuilt but is not a complete
		// copy of the original.
		return 8
	}
	return 0
}

// cmdVacuum reclaims the space held by deleted documents by force-merging every
// segment into one and reaping all tombstones. It is `sx compact --all` under an
// operational name. The single-file layout reuses freed pages through the
// freelist rather than truncating, so the file does not necessarily shrink on
// disk; what vacuum guarantees is that deleted documents stop costing query time
// and their pages return to the freelist for reuse.
func cmdVacuum(args []string) int {
	fs := flag.NewFlagSet("vacuum", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		return fail("usage: sx vacuum <file>")
	}
	path := fs.Arg(0)

	db, err := openIndex(path, false)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	start := time.Now()
	merged, err := db.CompactAll()
	if err != nil {
		return fail("vacuum: %v", err)
	}
	fmt.Printf("vacuumed in %s, merged %d segment(s)\n", time.Since(start).Round(time.Millisecond), merged)
	return 0
}
