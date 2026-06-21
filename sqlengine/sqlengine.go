package sqlengine

import (
	"context"
	"fmt"
	"io"

	search "github.com/tamnd/search"
)

// DB wraps an open search.DB with a SQL query surface. It runs entirely
// in-process: no network, no SQL server, no separate driver. The underlying
// index schema is read on each query to validate and type column references.
type DB struct {
	idx *search.DB
}

// namedArg binds a value to a named placeholder (:name). It mirrors
// database/sql.NamedArg so callers can pass Named("k", v) as a query argument.
type namedArg struct {
	Name  string
	Value any
}

// Named returns a named bind argument for a :name placeholder.
func Named(name string, value any) any { return namedArg{Name: name, Value: value} }

// Open wraps an existing search.DB with the SQL engine. It does not take
// ownership of idx; Close leaves idx open.
func Open(idx *search.DB) (*DB, error) {
	if idx == nil {
		return nil, fmt.Errorf("sqlengine: nil index")
	}
	return &DB{idx: idx}, nil
}

// Close releases the SQL engine. It does not close the underlying search.DB.
func (db *DB) Close() error { return nil }

// Query parses, plans, and executes a SELECT statement and returns the rows.
// Unsupported constructs return ErrUnsupportedSQL. Bind parameters use ? for
// positional and :name (with Named) for named placeholders.
func (db *DB) Query(ctx context.Context, sql string, args ...any) (*Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	st, err := parse(sql)
	if err != nil {
		return nil, err
	}
	s, err := db.idx.Schema()
	if err != nil {
		return nil, err
	}
	pl, err := planSelect(st, s, args)
	if err != nil {
		return nil, err
	}

	k := -1
	if pl.limit >= 0 {
		k = pl.offset + pl.limit
	}
	res, err := db.idx.SearchRequestExec(search.SearchRequest{
		Query: pl.query,
		K:     k,
		Sort:  pl.sort,
	})
	if err != nil {
		return nil, err
	}
	hits := res.Hits
	if pl.offset > 0 {
		if pl.offset >= len(hits) {
			hits = nil
		} else {
			hits = hits[pl.offset:]
		}
	}
	return &Rows{cols: pl.columns, hits: hits, pos: -1}, nil
}

// QueryRow runs a SELECT expected to return at most one row.
func (db *DB) QueryRow(ctx context.Context, sql string, args ...any) *Row {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return &Row{err: err}
	}
	return &Row{rows: rows}
}

// Rows is a result cursor over the matched documents, modeled on
// database/sql.Rows.
type Rows struct {
	cols []projectedColumn
	hits []search.Hit
	pos  int
}

// Columns returns the output column names in projection order.
func (r *Rows) Columns() []string {
	names := make([]string, len(r.cols))
	for i, c := range r.cols {
		names[i] = c.name
	}
	return names
}

// Next advances to the next row, reporting whether one is available.
func (r *Rows) Next() bool {
	r.pos++
	return r.pos < len(r.hits)
}

// Scan copies the current row's columns into dest. Each dest element must be a
// pointer to any (*any); the engine returns typed values without coercion.
func (r *Rows) Scan(dest ...any) error {
	if r.pos < 0 || r.pos >= len(r.hits) {
		return io.EOF
	}
	if len(dest) != len(r.cols) {
		return fmt.Errorf("sqlengine: Scan expected %d destinations, got %d", len(r.cols), len(dest))
	}
	h := r.hits[r.pos]
	for i, col := range r.cols {
		p, ok := dest[i].(*any)
		if !ok {
			return fmt.Errorf("sqlengine: Scan destination %d must be *any", i)
		}
		*p = columnValue(h, col.source)
	}
	return nil
}

// Row exposes the current row's column values by name.
func (r *Rows) Row() map[string]any {
	if r.pos < 0 || r.pos >= len(r.hits) {
		return nil
	}
	h := r.hits[r.pos]
	out := make(map[string]any, len(r.cols))
	for _, col := range r.cols {
		out[col.name] = columnValue(h, col.source)
	}
	return out
}

// Close releases the cursor. It is a no-op kept for database/sql parity.
func (r *Rows) Close() error { return nil }

// Row is a single-row result from QueryRow.
type Row struct {
	rows *Rows
	err  error
}

// Scan reads the single row into dest, returning io.EOF if there were no rows.
func (r *Row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if !r.rows.Next() {
		return io.EOF
	}
	return r.rows.Scan(dest...)
}

// columnValue resolves one output column for a hit, handling the pseudo-columns.
func columnValue(h search.Hit, col string) any {
	switch col {
	case "_id":
		return h.ExternalID
	case "score", "rank", "_score":
		return h.Score
	case "rowid", "_docid":
		return h.DocID
	default:
		if v, ok := h.Document[col]; ok {
			return v
		}
		return nil
	}
}
