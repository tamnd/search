package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/search"
	"github.com/tamnd/search/agg"
	"github.com/tamnd/search/analysis"
	"github.com/tamnd/search/query"
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

// cmdDelete soft-deletes documents by external id. One or more ids are given as
// positional arguments; each is resolved to its current doc-id, marked deleted,
// and dropped from the external-id map. A missing id is reported but does not fail
// the batch. The postings stay in their immutable segment until a compaction reaps
// them, so deletes are cheap and a later `sx compact` reclaims the space.
func cmdDelete(args []string) int {
	if len(args) < 2 {
		return fail("usage: sx delete <file> <external-id>...")
	}
	path := args[0]
	ids := args[1:]

	db, err := openIndex(path, false)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	deleted, missing := 0, 0
	for _, id := range ids {
		ok, err := db.Delete(id)
		if err != nil {
			return fail("delete %s: %v", id, err)
		}
		if ok {
			deleted++
		} else {
			missing++
			_, _ = fmt.Fprintf(os.Stderr, "sx: id %q not found\n", id)
		}
	}
	fmt.Printf("deleted %d document(s), %d not found\n", deleted, missing)
	return 0
}

// cmdUpdate reindexes documents from JSONL, replacing any existing document with
// the same external id. An update is a delete of the old document followed by a
// fresh index of the new one, which is exactly what Index does when it sees a
// repeated external id, so this is Index with replace-oriented reporting.
func cmdUpdate(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx update <file> [--file docs.jsonl]")
	}
	path := args[0]
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	docFile := fs.String("file", "", "path to a JSONL document file (default stdin)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx update <file> [--file docs.jsonl]")
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

	n, err := db.Index(docs)
	if err != nil {
		return fail("update: %v", err)
	}
	fmt.Printf("updated %d document(s)\n", n)
	return 0
}

// cmdCompact runs compaction to merge segments and reclaim the space held by
// deleted documents. By default it runs one tiered round; --all force-merges every
// segment into one and reaps all tombstones at once.
func cmdCompact(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx compact <file> [--all]")
	}
	path := args[0]
	fs := flag.NewFlagSet("compact", flag.ContinueOnError)
	all := fs.Bool("all", false, "force-merge every segment into one")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx compact <file> [--all]")
	}

	db, err := openIndex(path, false)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	var merged int
	if *all {
		merged, err = db.CompactAll()
	} else {
		merged, err = db.Compact()
	}
	if err != nil {
		return fail("compact: %v", err)
	}
	fmt.Printf("merged %d segment(s)\n", merged)
	return 0
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

// segmentJSON is the JSON view of one flushed segment.
type segmentJSON struct {
	ID       uint64      `json:"id"`
	DocCount uint32      `json:"doc_count"`
	MaxDoc   uint32      `json:"max_doc"`
	Fields   []fieldJSON `json:"fields"`
}

// fieldJSON is the JSON view of one field within a segment.
type fieldJSON struct {
	Name             string `json:"name"`
	TermCount        uint64 `json:"term_count"`
	DocCount         uint32 `json:"doc_count"`
	SumDocFreq       uint64 `json:"sum_doc_freq"`
	SumTotalTermFreq uint64 `json:"sum_total_term_freq"`
	Positional       bool   `json:"positional"`
}

// cmdInspect prints the segment structure of an index: each segment with its
// document count and per-field term and posting statistics.
func cmdInspect(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx inspect <file> [--format table|json]")
	}
	path := args[0]
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	format := fs.String("format", "table", "output format: table|json")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return fail("usage: sx inspect <file> [--format table|json]")
	}

	db, err := openIndex(path, true)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	segs, err := db.Segments()
	if err != nil {
		return fail("inspect: %v", err)
	}

	if *format == "json" {
		out := make([]segmentJSON, 0, len(segs))
		for _, s := range segs {
			sj := segmentJSON{ID: s.ID, DocCount: s.DocCount, MaxDoc: s.MaxDoc}
			for _, f := range s.Fields {
				sj.Fields = append(sj.Fields, fieldJSON{
					Name: f.Name, TermCount: f.TermCount, DocCount: f.DocCount,
					SumDocFreq: f.SumDocFreq, SumTotalTermFreq: f.SumTotalTermFreq,
					Positional: f.Positional,
				})
			}
			out = append(out, sj)
		}
		return printJSON(out)
	}
	return printInspectTable(segs)
}

// printInspectTable prints a human-readable segment and field summary.
func printInspectTable(segs []search.SegmentInfo) int {
	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()
	if len(segs) == 0 {
		_, _ = fmt.Fprintln(w, "no segments")
		return 0
	}
	for _, s := range segs {
		_, _ = fmt.Fprintf(w, "segment %d: %d doc(s), max_doc %d\n", s.ID, s.DocCount, s.MaxDoc)
		_, _ = fmt.Fprintf(w, "  %-20s %-8s %-8s %-8s %-8s %s\n",
			"field", "terms", "docs", "df", "ttf", "positional")
		for _, f := range s.Fields {
			_, _ = fmt.Fprintf(w, "  %-20s %-8d %-8d %-8d %-8d %t\n",
				f.Name, f.TermCount, f.DocCount, f.SumDocFreq, f.SumTotalTermFreq, f.Positional)
		}
	}
	return 0
}

// cmdQuery runs a full-text search and prints the hits (spec 2063 doc 21 §3.3).
// The query is given as a positional string in the compact query syntax, or as a
// full JSON query DSL object via --json. Faceting, highlighting, and multi-field
// sort are later milestones; S4 carries field-scoped search, paging, projection,
// and table/json/jsonl output.
func cmdQuery(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx query <file> '<query string>' [flags]")
	}
	path := args[0]
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	field := fs.String("field", "", "default field for bare terms in the query string")
	jsonPath := fs.String("json", "", "read a JSON query DSL object from this path instead of a query string")
	size := fs.Int("size", 10, "number of hits to return")
	from := fs.Int("from", 0, "offset into the result set for pagination")
	fields := fs.String("fields", "", "comma-separated list of stored fields to include in each hit")
	format := fs.String("format", "table", "output format: table|json|jsonl")
	explain := fs.Bool("explain", false, "include a per-hit score explanation")
	sortSpec := fs.String("sort", "", "sort keys, comma-separated, each field[:asc|desc][:missing_last]; _score for relevance")
	facetSpec := fs.String("facet", "", "aggregations, semicolon-separated, each name=kind:field[:opts] (see docs)")
	collapse := fs.String("collapse", "", "keyword field to collapse hits on, keeping the top hit per group")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	qstr := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if (qstr == "") == (*jsonPath == "") {
		return fail("provide exactly one of a query string or --json")
	}
	if *size < 0 || *from < 0 {
		return fail("--size and --from must be non-negative")
	}

	db, err := openIndex(path, true)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	defField := *field
	if defField == "" {
		defField = defaultField(db)
	}

	proj := splitList(*fields)
	label := qstr
	if label == "" {
		label = "(json) " + *jsonPath
	}

	// The sort, facet, and collapse path goes through SearchRequestExec, which
	// reads the doc-values columns (doc 14). Without any of those flags the plain
	// score-ranked top-k path is used.
	if *sortSpec != "" || *facetSpec != "" || *collapse != "" {
		req, err := buildSearchRequest(qstr, *jsonPath, defField, *from+*size, *sortSpec, *facetSpec, *collapse)
		if err != nil {
			return fail("query: %v", err)
		}
		start := time.Now()
		res, err := db.SearchRequestExec(req)
		if err != nil {
			return fail("query: %v", err)
		}
		elapsed := time.Since(start)
		hits := page(res.Hits, *from, *size)
		switch *format {
		case "json":
			return printRequestJSON(hits, res.Aggs, proj, *explain)
		case "jsonl":
			return printQueryJSONL(hits, proj, *explain)
		default:
			return printRequestTable(db, hits, res.Aggs, proj, label, elapsed)
		}
	}

	start := time.Now()
	hits, err := runQuery(db, qstr, *jsonPath, defField, *from+*size)
	if err != nil {
		return fail("query: %v", err)
	}
	elapsed := time.Since(start)

	hits = page(hits, *from, *size)

	switch *format {
	case "json":
		return printQueryJSON(hits, proj, *explain)
	case "jsonl":
		return printQueryJSONL(hits, proj, *explain)
	default:
		return printQueryTable(db, hits, proj, label, elapsed)
	}
}

// buildSearchRequest assembles a SearchRequest from the query string or JSON DSL
// plus the parsed sort, facet, and collapse flags.
func buildSearchRequest(qstr, jsonPath, defField string, k int, sortSpec, facetSpec, collapse string) (search.SearchRequest, error) {
	if k < 1 {
		k = 1
	}
	var q query.Query
	var err error
	if jsonPath != "" {
		b, rerr := os.ReadFile(jsonPath)
		if rerr != nil {
			return search.SearchRequest{}, rerr
		}
		q, err = query.ParseJSON(b)
	} else {
		q, err = query.ParseString(qstr, defField)
	}
	if err != nil {
		return search.SearchRequest{}, err
	}
	req := search.SearchRequest{Query: q, K: k, Collapse: collapse}
	if sortSpec != "" {
		req.Sort, err = parseSortSpec(sortSpec)
		if err != nil {
			return search.SearchRequest{}, err
		}
	}
	if facetSpec != "" {
		req.Aggs, err = parseFacetSpec(facetSpec)
		if err != nil {
			return search.SearchRequest{}, err
		}
	}
	return req, nil
}

// parseSortSpec parses a comma-separated sort flag. Each key is
// field[:asc|desc][:missing_last]; the field _score sorts by relevance.
func parseSortSpec(spec string) ([]search.SortKey, error) {
	var keys []search.SortKey
	for part := range strings.SplitSeq(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bits := strings.Split(part, ":")
		k := search.SortKey{Field: strings.TrimSpace(bits[0])}
		for _, opt := range bits[1:] {
			switch strings.TrimSpace(opt) {
			case "asc":
				k.Desc = false
			case "desc":
				k.Desc = true
			case "missing_last":
				k.MissingLast = true
			default:
				return nil, fmt.Errorf("bad sort option %q in %q", opt, part)
			}
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// parseFacetSpec parses a semicolon-separated facet flag. Each facet is
// name=kind:field[:opts]. Supported kinds: terms (opt = size), histogram (opt =
// interval), min/max/sum/avg/count/stats, cardinality, and percentiles (opt =
// pipe-separated percents). Range and nested facets are library-only.
func parseFacetSpec(spec string) (map[string]search.AggSpec, error) {
	aggs := map[string]search.AggSpec{}
	for part := range strings.SplitSeq(spec, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, body, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("bad facet %q, want name=kind:field[:opts]", part)
		}
		name = strings.TrimSpace(name)
		bits := strings.Split(body, ":")
		if len(bits) < 2 {
			return nil, fmt.Errorf("bad facet %q, want name=kind:field[:opts]", part)
		}
		a := search.AggSpec{Kind: strings.TrimSpace(bits[0]), Field: strings.TrimSpace(bits[1])}
		opt := ""
		if len(bits) > 2 {
			opt = strings.TrimSpace(bits[2])
		}
		switch a.Kind {
		case "terms":
			a.Size = 10
			if opt != "" {
				n, perr := strconv.Atoi(opt)
				if perr != nil {
					return nil, fmt.Errorf("bad terms size %q: %v", opt, perr)
				}
				a.Size = n
			}
		case "histogram":
			if opt == "" {
				return nil, fmt.Errorf("histogram facet %q needs an interval", name)
			}
			iv, perr := strconv.ParseFloat(opt, 64)
			if perr != nil {
				return nil, fmt.Errorf("bad histogram interval %q: %v", opt, perr)
			}
			a.Interval = iv
		case "percentiles":
			a.Percents = []float64{50, 95, 99}
			if opt != "" {
				a.Percents = nil
				for ps := range strings.SplitSeq(opt, "|") {
					p, perr := strconv.ParseFloat(strings.TrimSpace(ps), 64)
					if perr != nil {
						return nil, fmt.Errorf("bad percentile %q: %v", ps, perr)
					}
					a.Percents = append(a.Percents, p)
				}
			}
		case "min", "max", "sum", "avg", "count", "stats", "cardinality":
			// no options
		default:
			return nil, fmt.Errorf("unsupported facet kind %q (use the library for range and nested facets)", a.Kind)
		}
		aggs[name] = a
	}
	return aggs, nil
}

// runQuery parses and runs the query, requesting want hits.
func runQuery(db *search.DB, qstr, jsonPath, defField string, want int) ([]search.Hit, error) {
	if want < 1 {
		want = 1
	}
	if jsonPath != "" {
		b, err := os.ReadFile(jsonPath)
		if err != nil {
			return nil, err
		}
		return db.SearchJSON(b, want)
	}
	return db.SearchString(qstr, defField, want)
}

// defaultField returns the first text field in the schema, used as the implicit
// search field when the user gives none. An index without a text field yields "".
func defaultField(db *search.DB) string {
	s, err := db.Schema()
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		if f.Type == schema.TypeText {
			names = append(names, f.Name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// page slices hits to the [from, from+size) window, clamping to the bounds.
func page(hits []search.Hit, from, size int) []search.Hit {
	if from >= len(hits) {
		return nil
	}
	end := min(from+size, len(hits))
	return hits[from:end]
}

// splitList splits a comma-separated flag value into a trimmed, non-empty list.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// hitDoc returns the hit's document projected to the requested fields, always
// including _id and _score.
func hitDoc(h search.Hit, proj []string, explain bool) map[string]any {
	out := map[string]any{}
	if len(proj) == 0 {
		maps.Copy(out, h.Document)
	} else {
		for _, f := range proj {
			if v, ok := h.Document[f]; ok {
				out[f] = v
			}
		}
	}
	out["_id"] = h.ExternalID
	out["_score"] = h.Score
	if explain {
		out["_explain"] = map[string]any{
			"score":       h.Score,
			"description": "BM25(k1=1.2, b=0.75) summed over matched clauses",
		}
	}
	return out
}

// printQueryJSON prints the hits as one indented JSON object with a hits array.
func printQueryJSON(hits []search.Hit, proj []string, explain bool) int {
	out := make([]map[string]any, len(hits))
	for i, h := range hits {
		out[i] = hitDoc(h, proj, explain)
	}
	return printJSON(map[string]any{"hits": out})
}

// printQueryJSONL prints one hit per line as a compact JSON object.
func printQueryJSONL(hits []search.Hit, proj []string, explain bool) int {
	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()
	enc := json.NewEncoder(w)
	for _, h := range hits {
		if err := enc.Encode(hitDoc(h, proj, explain)); err != nil {
			return fail("encode json: %v", err)
		}
	}
	return 0
}

// printQueryTable prints a human-readable result table: a header line with the
// query, the hit count, and the elapsed time, then a score/id/summary row per
// hit. The summary column shows the first text field's value.
func printQueryTable(db *search.DB, hits []search.Hit, proj []string, label string, elapsed time.Duration) int {
	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()
	_, _ = fmt.Fprintf(w, "QUERY: %s   HITS: %d   TIME: %s\n\n", label, len(hits), elapsed.Round(time.Microsecond))
	if len(hits) == 0 {
		_, _ = fmt.Fprintln(w, "no hits")
		return 0
	}
	summaryField := ""
	if len(proj) > 0 {
		summaryField = proj[0]
	} else {
		summaryField = defaultField(db)
	}
	_, _ = fmt.Fprintf(w, "  %-8s %-16s %s\n", "SCORE", "ID", strings.ToUpper(summaryField))
	for _, h := range hits {
		summary := ""
		if summaryField != "" {
			if v, ok := h.Document[summaryField]; ok {
				summary = fmt.Sprintf("%v", v)
			}
		}
		_, _ = fmt.Fprintf(w, "  %-8.3f %-16s %s\n", h.Score, h.ExternalID, summary)
	}
	return 0
}

// aggResultJSON turns an aggregation result into a plain JSON-friendly value: a
// bucket list for bucketed aggs, a single number for single-value metrics, or a
// name/number map for multi-value metrics.
func aggResultJSON(r agg.Result) any {
	if r.Buckets != nil {
		buckets := make([]map[string]any, len(r.Buckets))
		for i, b := range r.Buckets {
			m := map[string]any{"key": b.Key, "count": b.Count}
			if len(b.Subs) > 0 {
				subs := make(map[string]any, len(b.Subs))
				for name, sr := range b.Subs {
					subs[name] = aggResultJSON(sr)
				}
				m["aggs"] = subs
			}
			buckets[i] = m
		}
		return buckets
	}
	if r.Values != nil {
		return r.Values
	}
	return r.Value
}

// printRequestJSON prints hits and aggregation results as one indented JSON
// object.
func printRequestJSON(hits []search.Hit, aggs map[string]agg.Result, proj []string, explain bool) int {
	out := make([]map[string]any, len(hits))
	for i, h := range hits {
		out[i] = hitDoc(h, proj, explain)
	}
	doc := map[string]any{"hits": out}
	if len(aggs) > 0 {
		a := make(map[string]any, len(aggs))
		for name, r := range aggs {
			a[name] = aggResultJSON(r)
		}
		doc["aggs"] = a
	}
	return printJSON(doc)
}

// printRequestTable prints the hit table followed by a block per aggregation.
func printRequestTable(db *search.DB, hits []search.Hit, aggs map[string]agg.Result, proj []string, label string, elapsed time.Duration) int {
	if rc := printQueryTable(db, hits, proj, label, elapsed); rc != 0 {
		return rc
	}
	if len(aggs) == 0 {
		return 0
	}
	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()
	names := make([]string, 0, len(aggs))
	for name := range aggs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r := aggs[name]
		_, _ = fmt.Fprintf(w, "\nFACET %s\n", name)
		switch {
		case r.Buckets != nil:
			for _, b := range r.Buckets {
				_, _ = fmt.Fprintf(w, "  %-24s %d\n", b.Key, b.Count)
			}
		case r.Values != nil:
			keys := make([]string, 0, len(r.Values))
			for k := range r.Values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				_, _ = fmt.Fprintf(w, "  %-24s %g\n", k, r.Values[k])
			}
		default:
			_, _ = fmt.Fprintf(w, "  %g\n", r.Value)
		}
	}
	return 0
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
