package sqlengine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrUnsupportedSQL is returned for a statement outside the supported subset.
var ErrUnsupportedSQL = errors.New("sqlengine: unsupported SQL construct")

// tokKind is a lexical token class.
type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tString
	tNumber
	tPunct   // ( ) , *
	tOp      // = != < <= > >=
	tParam   // ? or :name
	tKeyword // reserved word, normalized to upper case
)

type tok struct {
	kind tokKind
	text string
	// posIndex is assigned to positional ? parameters in source order.
	posIndex int
}

var keywords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "ORDER": true, "BY": true,
	"LIMIT": true, "OFFSET": true, "MATCH": true, "AND": true, "OR": true,
	"NOT": true, "BETWEEN": true, "IN": true, "LIKE": true, "IS": true,
	"NULL": true, "ASC": true, "DESC": true, "AS": true, "TRUE": true, "FALSE": true,
}

// lex tokenizes a SQL statement.
func lex(s string) ([]tok, error) {
	var toks []tok
	pos := 0
	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(' || c == ')' || c == ',' || c == '*':
			toks = append(toks, tok{kind: tPunct, text: string(c)})
			i++
		case c == '?':
			toks = append(toks, tok{kind: tParam, posIndex: pos})
			pos++
			i++
		case c == ':' && i+1 < n && isIdentStart(s[i+1]):
			j := i + 1
			for j < n && isIdentPart(s[j]) {
				j++
			}
			toks = append(toks, tok{kind: tParam, text: s[i+1 : j], posIndex: -1})
			i = j
		case c == '=' || c == '<' || c == '>' || c == '!':
			op, w := lexOp(s[i:])
			if w == 0 {
				return nil, fmt.Errorf("sqlengine: bad operator at %q", s[i:])
			}
			toks = append(toks, tok{kind: tOp, text: op})
			i += w
		case c == '\'':
			str, w, err := lexString(s[i:])
			if err != nil {
				return nil, err
			}
			toks = append(toks, tok{kind: tString, text: str})
			i += w
		case c >= '0' && c <= '9' || (c == '-' && i+1 < n && s[i+1] >= '0' && s[i+1] <= '9'):
			j := i + 1
			for j < n && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			toks = append(toks, tok{kind: tNumber, text: s[i:j]})
			i = j
		case isIdentStart(c):
			j := i
			for j < n && isIdentPart(s[j]) {
				j++
			}
			word := s[i:j]
			up := strings.ToUpper(word)
			if keywords[up] {
				toks = append(toks, tok{kind: tKeyword, text: up})
			} else {
				toks = append(toks, tok{kind: tIdent, text: word})
			}
			i = j
		default:
			return nil, fmt.Errorf("sqlengine: unexpected character %q", string(c))
		}
	}
	toks = append(toks, tok{kind: tEOF})
	return toks, nil
}

func lexOp(s string) (string, int) {
	switch {
	case strings.HasPrefix(s, "<="):
		return "<=", 2
	case strings.HasPrefix(s, ">="):
		return ">=", 2
	case strings.HasPrefix(s, "!="):
		return "!=", 2
	case strings.HasPrefix(s, "<>"):
		return "!=", 2
	case strings.HasPrefix(s, "="):
		return "=", 1
	case strings.HasPrefix(s, "<"):
		return "<", 1
	case strings.HasPrefix(s, ">"):
		return ">", 1
	}
	return "", 0
}

// lexString reads a single-quoted SQL string with ” as an escaped quote.
func lexString(s string) (string, int, error) {
	var b strings.Builder
	i := 1 // skip opening quote
	for i < len(s) {
		if s[i] == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')
				i += 2
				continue
			}
			return b.String(), i + 1, nil
		}
		b.WriteByte(s[i])
		i++
	}
	return "", 0, errors.New("sqlengine: unterminated string literal")
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '.'
}

// parser is a recursive descent parser over the token stream.
type parser struct {
	toks []tok
	i    int
}

func parse(sql string) (*selectStmt, error) {
	toks, err := lex(sql)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	st, err := p.parseSelect()
	if err != nil {
		return nil, err
	}
	if p.cur().kind != tEOF {
		return nil, fmt.Errorf("%w: trailing tokens near %q", ErrUnsupportedSQL, p.cur().text)
	}
	return st, nil
}

func (p *parser) cur() tok  { return p.toks[p.i] }
func (p *parser) next() tok { t := p.toks[p.i]; p.i++; return t }

func (p *parser) isKw(kw string) bool {
	return p.cur().kind == tKeyword && p.cur().text == kw
}

func (p *parser) expectKw(kw string) error {
	if !p.isKw(kw) {
		return fmt.Errorf("%w: expected %s, got %q", ErrUnsupportedSQL, kw, p.cur().text)
	}
	p.i++
	return nil
}

func (p *parser) parseSelect() (*selectStmt, error) {
	if err := p.expectKw("SELECT"); err != nil {
		return nil, err
	}
	st := &selectStmt{limit: -1}
	cols, err := p.parseColumns()
	if err != nil {
		return nil, err
	}
	st.columns = cols
	if err := p.expectKw("FROM"); err != nil {
		return nil, err
	}
	if p.cur().kind != tIdent {
		return nil, fmt.Errorf("%w: expected table name, got %q", ErrUnsupportedSQL, p.cur().text)
	}
	st.table = p.next().text

	if p.isKw("WHERE") {
		p.i++
		w, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		st.where = w
	}
	if p.isKw("ORDER") {
		p.i++
		if err := p.expectKw("BY"); err != nil {
			return nil, err
		}
		keys, err := p.parseOrderBy()
		if err != nil {
			return nil, err
		}
		st.orderBy = keys
	}
	if p.isKw("LIMIT") {
		p.i++
		v, err := p.parseIntLiteral()
		if err != nil {
			return nil, err
		}
		st.limit = v
	}
	if p.isKw("OFFSET") {
		p.i++
		v, err := p.parseIntLiteral()
		if err != nil {
			return nil, err
		}
		st.offset = v
	}
	return st, nil
}

func (p *parser) parseColumns() ([]selectCol, error) {
	var cols []selectCol
	for {
		t := p.cur()
		var col selectCol
		switch {
		case t.kind == tPunct && t.text == "*":
			p.i++
			col = selectCol{star: true}
		case t.kind == tIdent:
			p.i++
			col = selectCol{name: t.text}
		case t.kind == tKeyword && (t.text == "NULL" || t.text == "TRUE" || t.text == "FALSE"):
			return nil, fmt.Errorf("%w: literal in projection", ErrUnsupportedSQL)
		default:
			return nil, fmt.Errorf("%w: bad projection near %q", ErrUnsupportedSQL, t.text)
		}
		if p.isKw("AS") {
			p.i++
			if p.cur().kind != tIdent {
				return nil, fmt.Errorf("%w: expected alias", ErrUnsupportedSQL)
			}
			col.alias = p.next().text
		}
		cols = append(cols, col)
		if p.cur().kind == tPunct && p.cur().text == "," {
			p.i++
			continue
		}
		break
	}
	return cols, nil
}

func (p *parser) parseOrderBy() ([]orderKey, error) {
	var keys []orderKey
	for {
		t := p.cur()
		if t.kind != tIdent && t.kind != tKeyword {
			return nil, fmt.Errorf("%w: expected order column, got %q", ErrUnsupportedSQL, t.text)
		}
		name := t.text
		p.i++
		key := orderKey{col: name}
		if p.isKw("ASC") {
			p.i++
		} else if p.isKw("DESC") {
			p.i++
			key.desc = true
		}
		keys = append(keys, key)
		if p.cur().kind == tPunct && p.cur().text == "," {
			p.i++
			continue
		}
		break
	}
	return keys, nil
}

// parseExpr parses an OR-level expression.
func (p *parser) parseExpr() (expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.isKw("OR") {
		p.i++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &boolExpr{op: "OR", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (expr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.isKw("AND") {
		p.i++
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &boolExpr{op: "AND", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseNot() (expr, error) {
	if p.isKw("NOT") {
		p.i++
		sub, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &notExpr{sub: sub}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (expr, error) {
	if p.cur().kind == tPunct && p.cur().text == "(" {
		p.i++
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.cur().kind != tPunct || p.cur().text != ")" {
			return nil, fmt.Errorf("%w: missing close paren", ErrUnsupportedSQL)
		}
		p.i++
		return e, nil
	}
	// All remaining primaries start with an identifier (a field or table name).
	if p.cur().kind != tIdent {
		return nil, fmt.Errorf("%w: expected predicate, got %q", ErrUnsupportedSQL, p.cur().text)
	}
	name := p.next().text

	switch {
	case p.isKw("MATCH"):
		p.i++
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return &matchExpr{target: name, query: v}, nil
	case p.cur().kind == tOp:
		op := p.next().text
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return &cmpExpr{field: name, op: op, val: v}, nil
	case p.isKw("BETWEEN"):
		p.i++
		lo, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		if err := p.expectKw("AND"); err != nil {
			return nil, err
		}
		hi, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return &betweenExpr{field: name, lo: lo, hi: hi}, nil
	case p.isKw("IN"):
		p.i++
		if p.cur().kind != tPunct || p.cur().text != "(" {
			return nil, fmt.Errorf("%w: expected ( after IN", ErrUnsupportedSQL)
		}
		p.i++
		var vals []value
		for {
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			vals = append(vals, v)
			if p.cur().kind == tPunct && p.cur().text == "," {
				p.i++
				continue
			}
			break
		}
		if p.cur().kind != tPunct || p.cur().text != ")" {
			return nil, fmt.Errorf("%w: missing ) in IN list", ErrUnsupportedSQL)
		}
		p.i++
		if len(vals) > 1024 {
			return nil, fmt.Errorf("%w: IN list exceeds 1024 values", ErrUnsupportedSQL)
		}
		return &inExpr{field: name, vals: vals}, nil
	case p.isKw("LIKE"):
		p.i++
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return &likeExpr{field: name, pattern: v}, nil
	case p.isKw("IS"):
		p.i++
		not := false
		if p.isKw("NOT") {
			p.i++
			not = true
		}
		if err := p.expectKw("NULL"); err != nil {
			return nil, err
		}
		return &nullExpr{field: name, not: not}, nil
	}
	return nil, fmt.Errorf("%w: bad predicate on %q", ErrUnsupportedSQL, name)
}

func (p *parser) parseValue() (value, error) {
	t := p.cur()
	switch t.kind {
	case tString:
		p.i++
		return litValue(t.text), nil
	case tNumber:
		p.i++
		if strings.Contains(t.text, ".") {
			f, err := strconv.ParseFloat(t.text, 64)
			if err != nil {
				return value{}, err
			}
			return litValue(f), nil
		}
		n, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil {
			return value{}, err
		}
		return litValue(n), nil
	case tParam:
		p.i++
		if t.text != "" {
			return nameBind(t.text), nil
		}
		return posBind(t.posIndex), nil
	case tKeyword:
		switch t.text {
		case "NULL":
			p.i++
			return litValue(nil), nil
		case "TRUE":
			p.i++
			return litValue(true), nil
		case "FALSE":
			p.i++
			return litValue(false), nil
		}
	}
	return value{}, fmt.Errorf("%w: expected a value, got %q", ErrUnsupportedSQL, t.text)
}

func (p *parser) parseIntLiteral() (int, error) {
	t := p.cur()
	if t.kind != tNumber {
		return 0, fmt.Errorf("%w: expected an integer, got %q", ErrUnsupportedSQL, t.text)
	}
	p.i++
	n, err := strconv.Atoi(t.text)
	if err != nil {
		return 0, err
	}
	return n, nil
}
