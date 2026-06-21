package sqlengine

import (
	"fmt"
	"strconv"
	"strings"

	search "github.com/tamnd/search"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
)

// plan is the executable form of a SELECT: the query tree, the result window,
// the sort keys, and the projection columns resolved against the schema.
type plan struct {
	query   query.Query
	sort    []search.SortKey
	limit   int // -1 means no limit
	offset  int
	columns []projectedColumn // output columns in order
}

// projectedColumn is one output column: name is what the row exposes (an alias
// when one was given), source is the field or pseudo-column the value comes from.
type projectedColumn struct {
	name   string
	source string
}

// planSelect resolves bind arguments and translates a parsed statement into a
// plan against the index schema.
func planSelect(st *selectStmt, s *schema.Schema, args []any) (*plan, error) {
	binder := &binder{args: args}

	var q query.Query
	hasMatch := false
	if st.where != nil {
		hasMatch = exprHasMatch(st.where)
		built, _, err := translate(st.where, st, s, binder)
		if err != nil {
			return nil, err
		}
		q = built
	} else {
		q = query.MatchAll()
	}
	// Without a MATCH predicate the result is a pure structured filter: wrap it so
	// it contributes no score, matching the doc 17 §4.2 rule that score is 0.0
	// when no MATCH is present.
	if st.where != nil && !hasMatch {
		q = query.Bool().FilterClause(q)
	}

	cols, err := projection(st.columns, s)
	if err != nil {
		return nil, err
	}

	sortKeys := make([]search.SortKey, 0, len(st.orderBy))
	for _, k := range st.orderBy {
		sortKeys = append(sortKeys, search.SortKey{Field: sortField(k.col), Desc: k.desc})
	}
	if len(sortKeys) == 0 {
		sortKeys = []search.SortKey{{Field: "_score", Desc: true}}
	}

	return &plan{query: q, sort: sortKeys, limit: st.limit, offset: st.offset, columns: cols}, nil
}

// sortField maps an ORDER BY column to a sort field name. The score and rank
// pseudo-columns sort by relevance.
func sortField(col string) string {
	switch strings.ToLower(col) {
	case "score", "rank", "_score":
		return "_score"
	default:
		return col
	}
}

// projection resolves the output column list. A star expands to the schema's
// stored fields in mapping order.
func projection(cols []selectCol, s *schema.Schema) ([]projectedColumn, error) {
	var out []projectedColumn
	for _, c := range cols {
		if c.star {
			out = append(out, projectedColumn{name: "_id", source: "_id"})
			for _, f := range s.Fields {
				if f.Opts.Stored {
					out = append(out, projectedColumn{name: f.Name, source: f.Name})
				}
			}
			continue
		}
		out = append(out, projectedColumn{name: c.outName(), source: c.name})
	}
	return out, nil
}

// binder resolves value placeholders against the query arguments. Positional
// placeholders are consumed in source order; named ones look up a sql.NamedArg.
type binder struct {
	args []any
}

func (b *binder) resolve(v value) (any, error) {
	if !v.isBind() {
		return v.lit, nil
	}
	if v.bindName != "" {
		for _, a := range b.args {
			if na, ok := a.(namedArg); ok && na.Name == v.bindName {
				return na.Value, nil
			}
		}
		return nil, fmt.Errorf("%w: no argument for :%s", ErrUnsupportedSQL, v.bindName)
	}
	if v.bindPos < 0 || v.bindPos >= len(b.args) {
		return nil, fmt.Errorf("%w: missing positional argument %d", ErrUnsupportedSQL, v.bindPos+1)
	}
	return b.args[v.bindPos], nil
}

// translate converts a WHERE expression to a query node. It returns the node and
// whether the node contributes to scoring (a MATCH or a boolean combination that
// contains one); structured leaves are non-scoring filters.
func translate(e expr, st *selectStmt, s *schema.Schema, b *binder) (query.Query, bool, error) {
	switch n := e.(type) {
	case *boolExpr:
		left, ls, err := translate(n.left, st, s, b)
		if err != nil {
			return nil, false, err
		}
		right, rs, err := translate(n.right, st, s, b)
		if err != nil {
			return nil, false, err
		}
		bq := query.Bool()
		if n.op == "AND" {
			addClause(bq, left, ls)
			addClause(bq, right, rs)
		} else { // OR
			bq.ShouldClause(left)
			bq.ShouldClause(right)
			bq.SetMinimumShouldMatch(1)
		}
		return bq, ls || rs, nil
	case *notExpr:
		sub, _, err := translate(n.sub, st, s, b)
		if err != nil {
			return nil, false, err
		}
		return query.Bool().MustNotClause(sub), false, nil
	case *matchExpr:
		raw, err := b.resolve(n.query)
		if err != nil {
			return nil, false, err
		}
		text, ok := raw.(string)
		if !ok {
			return nil, false, fmt.Errorf("%w: MATCH value must be a string", ErrUnsupportedSQL)
		}
		q, err := buildMatch(n.target, st.table, text, s)
		if err != nil {
			return nil, false, err
		}
		return q, true, nil
	case *cmpExpr:
		q, err := translateCmp(n, s, b)
		return q, false, err
	case *betweenExpr:
		lo, err := b.resolve(n.lo)
		if err != nil {
			return nil, false, err
		}
		hi, err := b.resolve(n.hi)
		if err != nil {
			return nil, false, err
		}
		return query.Range(n.field, valString(lo), valString(hi), true, true), false, nil
	case *inExpr:
		bq := query.Bool().SetMinimumShouldMatch(1)
		for _, v := range n.vals {
			raw, err := b.resolve(v)
			if err != nil {
				return nil, false, err
			}
			bq.ShouldClause(query.Term(n.field, valString(raw)))
		}
		return bq, false, nil
	case *likeExpr:
		raw, err := b.resolve(n.pattern)
		if err != nil {
			return nil, false, err
		}
		pat, ok := raw.(string)
		if !ok {
			return nil, false, fmt.Errorf("%w: LIKE pattern must be a string", ErrUnsupportedSQL)
		}
		return translateLike(n.field, pat), false, nil
	case *nullExpr:
		return nil, false, fmt.Errorf("%w: IS [NOT] NULL is not supported", ErrUnsupportedSQL)
	}
	return nil, false, fmt.Errorf("%w: unknown predicate", ErrUnsupportedSQL)
}

// buildMatch compiles a MATCH predicate. A field-level MATCH (target is a field
// name) scopes bare terms to that field. A table-level MATCH (target is the table
// name) searches every indexed text field, FTS5-style: the query string is parsed
// once per text field and the per-field parses are OR-combined so a document
// matches when any of its text fields satisfy the query.
func buildMatch(target, table, text string, s *schema.Schema) (query.Query, error) {
	if target != table {
		return query.ParseString(text, target)
	}
	fields := textFields(s)
	if len(fields) == 0 {
		return query.ParseString(text, "")
	}
	if len(fields) == 1 {
		return query.ParseString(text, fields[0])
	}
	bq := query.Bool().SetMinimumShouldMatch(1)
	for _, f := range fields {
		q, err := query.ParseString(text, f)
		if err != nil {
			return nil, err
		}
		bq.ShouldClause(q)
	}
	return bq, nil
}

// textFields returns the names of the indexed full-text fields in mapping order.
func textFields(s *schema.Schema) []string {
	var out []string
	for _, f := range s.Fields {
		if f.Type == schema.TypeText && f.Opts.Indexed {
			out = append(out, f.Name)
		}
	}
	return out
}

// addClause adds a translated child to a bool query as a scoring Must clause or a
// non-scoring Filter clause depending on whether it carries score.
func addClause(bq *query.BoolQuery, child query.Query, scoring bool) {
	if scoring {
		bq.MustClause(child)
	} else {
		bq.FilterClause(child)
	}
}

// translateCmp maps a scalar comparison to a term or range node.
func translateCmp(n *cmpExpr, s *schema.Schema, b *binder) (query.Query, error) {
	raw, err := b.resolve(n.val)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		// "field = NULL" never matches in SQL three-valued logic.
		return query.MatchNone(), nil
	}
	val := valString(raw)
	switch n.op {
	case "=":
		if isNumeric(n.field, s) {
			return query.Range(n.field, val, val, true, true), nil
		}
		return query.Term(n.field, val), nil
	case "!=":
		var leaf query.Query
		if isNumeric(n.field, s) {
			leaf = query.Range(n.field, val, val, true, true)
		} else {
			leaf = query.Term(n.field, val)
		}
		// A bare must_not matches nothing on its own; anchor it with match_all so
		// the predicate means "every document except those equal to val".
		return query.Bool().MustClause(query.MatchAll()).MustNotClause(leaf), nil
	case "<":
		return query.Range(n.field, "", val, false, false), nil
	case "<=":
		return query.Range(n.field, "", val, false, true), nil
	case ">":
		return query.Range(n.field, val, "", false, false), nil
	case ">=":
		return query.Range(n.field, val, "", true, false), nil
	}
	return nil, fmt.Errorf("%w: bad operator %q", ErrUnsupportedSQL, n.op)
}

// translateLike converts a SQL LIKE pattern to a prefix or wildcard query. A
// pattern with a single trailing % and no other wildcard is a prefix; otherwise
// % maps to * and _ maps to ?.
func translateLike(field, pat string) query.Query {
	if strings.HasSuffix(pat, "%") {
		body := pat[:len(pat)-1]
		if !strings.ContainsAny(body, "%_") {
			return query.Prefix(field, body)
		}
	}
	var b strings.Builder
	for i := 0; i < len(pat); i++ {
		switch pat[i] {
		case '%':
			b.WriteByte('*')
		case '_':
			b.WriteByte('?')
		default:
			b.WriteByte(pat[i])
		}
	}
	return query.Wildcard(field, b.String())
}

// isNumeric reports whether a field is a numeric, date, or boolean field, the
// types that take range-encoded comparisons rather than term lookups.
func isNumeric(field string, s *schema.Schema) bool {
	f, ok := s.Lookup(field)
	if !ok {
		return false
	}
	switch f.Type {
	case schema.TypeLong, schema.TypeDouble, schema.TypeDate, schema.TypeBoolean:
		return true
	}
	return false
}

// valString renders a resolved argument as the string a query node expects.
func valString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// exprHasMatch reports whether an expression tree contains a MATCH predicate.
func exprHasMatch(e expr) bool {
	switch n := e.(type) {
	case *boolExpr:
		return exprHasMatch(n.left) || exprHasMatch(n.right)
	case *notExpr:
		return exprHasMatch(n.sub)
	case *matchExpr:
		return true
	}
	return false
}
