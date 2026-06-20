package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/tamnd/search"
	"github.com/tamnd/search/analysis"
	"github.com/tamnd/search/schema"
)

// schemaFile is the JSON shape of a schema definition passed to `sx create`.
type schemaFile struct {
	IDField string            `json:"id_field"`
	Fields  []schemaFileField `json:"fields"`
}

// schemaFileField is one field entry in a schema definition file.
type schemaFileField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Analyzer string `json:"analyzer,omitempty"`
	Dims     int    `json:"dims,omitempty"`
	Metric   string `json:"metric,omitempty"`
}

// fail prints an error to stderr and returns the standard failure code.
func fail(format string, a ...any) int {
	_, _ = fmt.Fprintf(os.Stderr, "sx: "+format+"\n", a...)
	return 1
}

// openIndex opens (creating if needed) the index at path.
func openIndex(path string, readOnly bool) (*search.DB, error) {
	return search.Open(path, search.Options{ReadOnly: readOnly})
}

// cmdCreate creates a .sx file and optionally applies a schema from a JSON file.
func cmdCreate(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx create <file> [--schema schema.json]")
	}
	path := args[0]
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	schemaPath := fs.String("schema", "", "path to a JSON schema definition")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx create <file> [--schema schema.json]")
	}

	db, err := openIndex(path, false)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	if *schemaPath != "" {
		s, err := loadSchemaFile(*schemaPath)
		if err != nil {
			return fail("schema %s: %v", *schemaPath, err)
		}
		if err := db.PutSchema(s); err != nil {
			return fail("apply schema: %v", err)
		}
	}
	fmt.Printf("created %s\n", path)
	return 0
}

// loadSchemaFile reads a JSON schema definition and builds a schema.Schema.
func loadSchemaFile(path string) (*schema.Schema, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sf schemaFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return nil, err
	}
	s := schema.New()
	if sf.IDField != "" {
		s.IDField = sf.IDField
	}
	for _, ff := range sf.Fields {
		f := schema.NewField(ff.Name, schema.FieldType(ff.Type))
		if ff.Analyzer != "" {
			f.Opts.Analyzer = ff.Analyzer
		}
		if ff.Dims != 0 {
			f.Opts.Dims = ff.Dims
		}
		if ff.Metric != "" {
			f.Opts.Metric = ff.Metric
		}
		if err := s.Add(f); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// cmdIndex reads JSONL documents (from --file or stdin) and indexes them.
func cmdIndex(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx index <file> [--file docs.jsonl] [--id-field _id]")
	}
	path := args[0]
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	docFile := fs.String("file", "", "path to a JSONL document file (default stdin)")
	idField := fs.String("id-field", "", "primary-key field name (sets the schema id field on an empty index)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx index <file> [--file docs.jsonl] [--id-field _id]")
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
	docs, err := readJSONL(in)
	if err != nil {
		return fail("read documents: %v", err)
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

	n, err := db.Index(docs)
	if err != nil {
		return fail("index: %v", err)
	}
	fmt.Printf("indexed %d document(s)\n", n)
	return 0
}

// ensureIDField sets the primary-key field on an index that has no schema yet,
// creating a minimal schema so documents can be indexed without a create step.
func ensureIDField(db *search.DB, idField string) error {
	if _, err := db.Schema(); err == nil {
		return nil // a schema already exists; leave its primary key untouched.
	}
	s := schema.New()
	s.IDField = idField
	return db.PutSchema(s)
}

// readJSONL parses one JSON object per non-empty line.
func readJSONL(r io.Reader) ([]map[string]any, error) {
	var docs []map[string]any
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(text), &doc); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		docs = append(docs, doc)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}

// cmdGet fetches a stored document by internal doc-id (numeric positional) or by
// external id (--id).
func cmdGet(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx get <file> <doc-id> | sx get <file> --id <external-id>")
	}
	path := args[0]
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	extID := fs.String("id", "", "fetch by external id instead of internal doc-id")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	rest := fs.Args()

	db, err := openIndex(path, true)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	var doc map[string]any
	if *extID != "" {
		doc, err = db.GetByExternalID(*extID)
	} else {
		if len(rest) < 1 {
			return fail("usage: sx get <file> <doc-id>")
		}
		id, perr := strconv.ParseUint(rest[0], 10, 64)
		if perr != nil {
			return fail("invalid doc-id %q: %v", rest[0], perr)
		}
		doc, err = db.GetByDocID(id)
	}
	if err != nil {
		return fail("get: %v", err)
	}
	return printJSON(doc)
}

// cmdAnalyze runs an analyzer over text and prints the resulting tokens.
func cmdAnalyze(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx analyze <file> [--analyzer name | --field name] [--format table|json] <text>")
	}
	path := args[0]
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	analyzer := fs.String("analyzer", "standard", "analyzer name (built-in or stored)")
	field := fs.String("field", "", "analyze with the analyzer configured for this field")
	format := fs.String("format", "table", "output format: table|json")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	rest := fs.Args()
	text := strings.Join(rest, " ")
	if text == "" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fail("read stdin: %v", err)
		}
		text = string(b)
	}

	db, err := openIndex(path, true)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	var toks []analysis.Token
	if *field != "" {
		toks, err = db.AnalyzeField(*field, text)
	} else {
		toks, err = db.Analyze(*analyzer, text)
	}
	if err != nil {
		return fail("analyze: %v", err)
	}

	if *format == "json" {
		return printAnalyzeJSON(toks)
	}
	return printAnalyzeTable(toks)
}

// printAnalyzeTable prints one token per line with its offsets, position
// increment, and type.
func printAnalyzeTable(toks []analysis.Token) int {
	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()
	_, _ = fmt.Fprintf(w, "%-24s %-7s %-7s %-4s %s\n", "term", "start", "end", "pos+", "type")
	for _, t := range toks {
		_, _ = fmt.Fprintf(w, "%-24s %-7d %-7d %-4d %s\n",
			t.Term, t.StartOffset, t.EndOffset, t.PositionIncr, t.Type)
	}
	return 0
}

// tokenJSON is the JSON view of an analyzed token.
type tokenJSON struct {
	Term         string `json:"term"`
	Start        int    `json:"start"`
	End          int    `json:"end"`
	PositionIncr int    `json:"position_increment"`
	Type         string `json:"type"`
}

// printAnalyzeJSON prints the tokens as a JSON array.
func printAnalyzeJSON(toks []analysis.Token) int {
	out := make([]tokenJSON, len(toks))
	for i, t := range toks {
		out[i] = tokenJSON{
			Term: t.Term, Start: t.StartOffset, End: t.EndOffset,
			PositionIncr: t.PositionIncr, Type: t.Type,
		}
	}
	return printJSON(out)
}

// cmdSchema prints the schema of an index as JSON.
func cmdSchema(args []string) int {
	fs := flag.NewFlagSet("schema", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		return fail("usage: sx schema <file>")
	}
	path := fs.Arg(0)

	db, err := openIndex(path, true)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	s, err := db.Schema()
	if err != nil {
		return fail("schema: %v", err)
	}
	out := schemaFile{IDField: s.PrimaryKey()}
	fields := append([]schema.Field(nil), s.Fields...)
	sort.SliceStable(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	for _, f := range fields {
		out.Fields = append(out.Fields, schemaFileField{
			Name:     f.Name,
			Type:     string(f.Type),
			Analyzer: f.Opts.Analyzer,
			Dims:     f.Opts.Dims,
			Metric:   f.Opts.Metric,
		})
	}
	return printJSON(out)
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fail("encode json: %v", err)
	}
	return 0
}

// closeDB closes db, reporting any error to stderr.
func closeDB(db *search.DB) {
	if err := db.Close(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "sx: close: %v\n", err)
	}
}
