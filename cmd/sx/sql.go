package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tamnd/search/sqlengine"
)

// bindList collects repeated -v name=value flags into named bind arguments.
type bindList []any

// String implements flag.Value; the accumulated binds have no useful printable form.
func (b *bindList) String() string { return "" }

// Set implements flag.Value by parsing one name=value flag into a named bind argument.
func (b *bindList) Set(v string) error {
	name, val, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("bind %q must be name=value", v)
	}
	*b = append(*b, sqlengine.Named(strings.TrimSpace(name), val))
	return nil
}

// cmdSQL runs a SELECT through the built-in SQL surface (doc 17). The statement
// is a single SELECT; MATCH compiles to a full-text query and the structured
// predicates become filters, all in-process against the .sx file.
func cmdSQL(args []string) int {
	if len(args) < 1 {
		return fail("usage: sx sql <file> '<SELECT ...>' [flags]")
	}
	path := args[0]
	fs := flag.NewFlagSet("sql", flag.ContinueOnError)
	format := fs.String("format", "table", "output format: table|json|jsonl|csv")
	var binds bindList
	fs.Var(&binds, "v", "named bind, name=value; repeat for more (use :name in the SQL)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	sql := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if sql == "" {
		return fail("provide a SELECT statement")
	}

	db, err := openIndex(path, true)
	if err != nil {
		return fail("open %s: %v", path, err)
	}
	defer closeDB(db)

	sdb, err := sqlengine.Open(db)
	if err != nil {
		return fail("sql: %v", err)
	}
	defer func() { _ = sdb.Close() }()

	start := time.Now()
	rows, err := sdb.Query(context.Background(), sql, []any(binds)...)
	if err != nil {
		return fail("sql: %v", err)
	}
	defer func() { _ = rows.Close() }()

	cols := rows.Columns()
	var records []map[string]any
	for rows.Next() {
		records = append(records, rows.Row())
	}
	elapsed := time.Since(start)

	switch *format {
	case "json":
		return printSQLJSON(records)
	case "jsonl":
		return printSQLJSONL(records)
	case "csv":
		return printSQLCSV(cols, records)
	default:
		return printSQLTable(cols, records, sql, elapsed)
	}
}

func printSQLJSON(records []map[string]any) int {
	if records == nil {
		records = []map[string]any{}
	}
	return printJSON(map[string]any{"rows": records})
}

func printSQLJSONL(records []map[string]any) int {
	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()
	enc := json.NewEncoder(w)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return fail("encode json: %v", err)
		}
	}
	return 0
}

func printSQLCSV(cols []string, records []map[string]any) int {
	w := csv.NewWriter(os.Stdout)
	defer w.Flush()
	if err := w.Write(cols); err != nil {
		return fail("write csv: %v", err)
	}
	row := make([]string, len(cols))
	for _, rec := range records {
		for i, c := range cols {
			row[i] = cellString(rec[c])
		}
		if err := w.Write(row); err != nil {
			return fail("write csv: %v", err)
		}
	}
	return 0
}

func printSQLTable(cols []string, records []map[string]any, sql string, elapsed time.Duration) int {
	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()
	_, _ = fmt.Fprintf(w, "SQL: %s   ROWS: %d   TIME: %s\n\n", sql, len(records), elapsed.Round(time.Microsecond))
	if len(records) == 0 {
		_, _ = fmt.Fprintln(w, "no rows")
		return 0
	}

	// Size each column to the widest cell so the table lines up.
	width := make([]int, len(cols))
	for i, c := range cols {
		width[i] = len(c)
	}
	cells := make([][]string, len(records))
	for r, rec := range records {
		cells[r] = make([]string, len(cols))
		for i, c := range cols {
			s := cellString(rec[c])
			cells[r][i] = s
			if len(s) > width[i] {
				width[i] = len(s)
			}
		}
	}

	for i, c := range cols {
		if i > 0 {
			_, _ = fmt.Fprint(w, "  ")
		}
		_, _ = fmt.Fprintf(w, "%-*s", width[i], c)
	}
	_, _ = fmt.Fprintln(w)
	for i := range cols {
		if i > 0 {
			_, _ = fmt.Fprint(w, "  ")
		}
		_, _ = fmt.Fprint(w, strings.Repeat("-", width[i]))
	}
	_, _ = fmt.Fprintln(w)
	for _, row := range cells {
		for i := range cols {
			if i > 0 {
				_, _ = fmt.Fprint(w, "  ")
			}
			_, _ = fmt.Fprintf(w, "%-*s", width[i], row[i])
		}
		_, _ = fmt.Fprintln(w)
	}
	return 0
}

// cellString renders a column value for the table and CSV outputs.
func cellString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float32:
		return fmt.Sprintf("%.4f", x)
	case float64:
		return fmt.Sprintf("%g", x)
	default:
		return fmt.Sprintf("%v", v)
	}
}
