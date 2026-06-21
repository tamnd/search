// Package sqlengine is the built-in SQL surface over a search index (spec 2063
// doc 17). It is pure Go with no cgo dependency: a small lexer and recursive
// descent parser accept the supported SELECT subset, a planner translates the
// statement into the engine's query tree plus a sort and a result window, and
// the executor calls straight into search.DB. The Go call stack for a query is
// Query -> parse -> plan -> search.SearchRequestExec -> Rows, with no network
// and no separate process.
//
// Supported subset at S8: SELECT of field names, "*", and the score/_id/rowid
// pseudo-columns; FROM one table (the index); WHERE with a MATCH predicate,
// scalar comparisons (=, !=, <, <=, >, >=), IN, BETWEEN, LIKE, AND, OR, NOT and
// parentheses; ORDER BY with multiple keys; LIMIT and OFFSET. Bind parameters
// use ? (positional) or :name (named).
package sqlengine

// selectStmt is a parsed SELECT.
type selectStmt struct {
	columns []selectCol
	table   string
	where   expr // nil when there is no WHERE clause
	orderBy []orderKey
	limit   int // -1 means no LIMIT
	offset  int
}

// selectCol is one entry in the projection list. A star projects every stored
// field; otherwise name is a field or pseudo-column, optionally aliased.
type selectCol struct {
	star  bool
	name  string
	alias string
}

// outName returns the column name the row exposes for this projection entry.
func (c selectCol) outName() string {
	if c.alias != "" {
		return c.alias
	}
	return c.name
}

// orderKey is one ORDER BY level.
type orderKey struct {
	col  string
	desc bool
}

// expr is a WHERE-clause node.
type expr interface{ exprNode() }

// boolExpr is a binary AND/OR of two sub-expressions.
type boolExpr struct {
	op    string // "AND" or "OR"
	left  expr
	right expr
}

// notExpr negates a sub-expression.
type notExpr struct{ sub expr }

// matchExpr is a full-text MATCH predicate: target MATCH 'query'. target is the
// table name (table-level MATCH) or a field name (field-level MATCH).
type matchExpr struct {
	target string
	query  value
}

// cmpExpr is a scalar comparison: field <op> value.
type cmpExpr struct {
	field string
	op    string // = != < <= > >=
	val   value
}

// betweenExpr is field BETWEEN lo AND hi (inclusive).
type betweenExpr struct {
	field string
	lo    value
	hi    value
}

// inExpr is field IN (v1, v2, ...).
type inExpr struct {
	field string
	vals  []value
}

// likeExpr is field LIKE 'pattern' with SQL % and _ wildcards.
type likeExpr struct {
	field   string
	pattern value
}

// nullExpr is field IS NULL / field IS NOT NULL.
type nullExpr struct {
	field string
	not   bool
}

func (*boolExpr) exprNode()    {}
func (*notExpr) exprNode()     {}
func (*matchExpr) exprNode()   {}
func (*cmpExpr) exprNode()     {}
func (*betweenExpr) exprNode() {}
func (*inExpr) exprNode()      {}
func (*likeExpr) exprNode()    {}
func (*nullExpr) exprNode()    {}

// value is a literal or a bind placeholder. Exactly one of the fields applies:
// a placeholder (bindPos >= 0 or bindName != "") is resolved against the query
// arguments before planning; otherwise lit holds a concrete literal.
type value struct {
	lit      any    // string, int64, float64, bool, or nil
	bindPos  int    // 0-based positional placeholder index, or -1
	bindName string // named placeholder, or ""
}

func litValue(v any) value       { return value{lit: v, bindPos: -1} }
func posBind(i int) value        { return value{bindPos: i} }
func nameBind(name string) value { return value{bindPos: -1, bindName: name} }
func (v value) isBind() bool     { return v.bindPos >= 0 || v.bindName != "" }
