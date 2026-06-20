package query

import (
	"strings"
)

// ParseString parses the compact query-string syntax into a query tree (spec
// 2063 doc 11 §5). The grammar is deliberately small:
//
//	term                a bare term, OR-combined with its siblings by default
//	+term               a required term (must)
//	-term               a prohibited term (must_not)
//	"a b c"             a phrase
//	field:term          a term scoped to a field
//	field:"a b"         a phrase scoped to a field
//	field:val*          a prefix scoped to a field
//	field:[lo TO hi]    an inclusive range; {lo TO hi} is exclusive; mix with [ }
//	AND OR NOT          uppercase boolean operators between terms
//
// defaultField is the field used for tokens that carry no field: prefix; it may be
// empty, in which case the planner fills in the index default. An empty or
// whitespace-only string parses to MatchNoneQuery.
func ParseString(input, defaultField string) (Query, error) {
	toks, err := lex(input)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, defaultField: defaultField}
	q, err := p.parse()
	if err != nil {
		return nil, err
	}
	if !p.eof() {
		return nil, &Error{Msg: "unexpected trailing input near " + p.peek().text}
	}
	return q, nil
}

// token kinds produced by the lexer.
type tokKind int

const (
	tWord tokKind = iota
	tPhrase
	tAnd
	tOr
	tNot
	tLBrack // [ inclusive lower
	tRBrack // ] inclusive upper
	tLBrace // { exclusive lower
	tRBrace // } exclusive upper
)

type token struct {
	kind tokKind
	text string // for tWord: raw word (may carry +/- prefix and field: and trailing *); for tPhrase: phrase body
}

// lex splits the input into tokens, treating quoted spans as a single phrase
// token and the bracket characters as their own tokens.
func lex(input string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(input) {
		c := input[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			i++
		case '"':
			j := i + 1
			for j < len(input) && input[j] != '"' {
				j++
			}
			if j >= len(input) {
				return nil, &Error{Msg: "unterminated phrase"}
			}
			toks = append(toks, token{kind: tPhrase, text: input[i+1 : j]})
			i = j + 1
		case '[':
			toks = append(toks, token{kind: tLBrack})
			i++
		case ']':
			toks = append(toks, token{kind: tRBrack})
			i++
		case '{':
			toks = append(toks, token{kind: tLBrace})
			i++
		case '}':
			toks = append(toks, token{kind: tRBrace})
			i++
		default:
			j := i
			for j < len(input) && !isBreak(input[j]) {
				// A quote inside a word (field:"...") ends the bare word; the phrase
				// is lexed on the next pass.
				if input[j] == '"' {
					break
				}
				j++
			}
			word := input[i:j]
			switch word {
			case "AND":
				toks = append(toks, token{kind: tAnd})
			case "OR":
				toks = append(toks, token{kind: tOr})
			case "NOT":
				toks = append(toks, token{kind: tNot})
			default:
				toks = append(toks, token{kind: tWord, text: word})
			}
			i = j
		}
	}
	return toks, nil
}

func isBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '[', ']', '{', '}':
		return true
	}
	return false
}

type parser struct {
	toks         []token
	pos          int
	defaultField string
}

func (p *parser) eof() bool     { return p.pos >= len(p.toks) }
func (p *parser) peek() token   { return p.toks[p.pos] }
func (p *parser) next() token   { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser) hasNext() bool { return p.pos < len(p.toks) }

// parse builds a bool query from the token stream. Bare terms become should
// clauses, +term/AND-joined terms become must, -term/NOT become must_not.
func (p *parser) parse() (Query, error) {
	b := Bool()
	pendingMust := false // set by an AND or leading NOT-less operator
	for p.hasNext() {
		t := p.peek()
		switch t.kind {
		case tAnd:
			p.next()
			pendingMust = true
			continue
		case tOr:
			p.next()
			pendingMust = false
			continue
		case tNot:
			p.next()
			sub, err := p.parseAtom()
			if err != nil {
				return nil, err
			}
			b.MustNotClause(sub)
			continue
		}
		occur := Should
		if pendingMust {
			occur = Must
		}
		// A leading +/- on a word overrides the operator-derived occurrence.
		forcedOccur, stripped := occurPrefix(t)
		if stripped {
			t.text = trimOccur(t.text)
			p.toks[p.pos] = t
			occur = forcedOccur
		}
		sub, err := p.parseAtom()
		if err != nil {
			return nil, err
		}
		b.Add(occur, sub)
		pendingMust = false
	}
	if len(b.Clauses) == 0 {
		return MatchNone(), nil
	}
	return b.Rewrite(), nil
}

// occurPrefix reports the occurrence forced by a leading + or - on a word token.
func occurPrefix(t token) (Occur, bool) {
	if t.kind != tWord || t.text == "" {
		return Should, false
	}
	switch t.text[0] {
	case '+':
		return Must, true
	case '-':
		return MustNot, true
	}
	return Should, false
}

func trimOccur(s string) string {
	if s != "" && (s[0] == '+' || s[0] == '-') {
		return s[1:]
	}
	return s
}

// parseAtom parses a single term, phrase, prefix, or range, including an optional
// field: scope.
func (p *parser) parseAtom() (Query, error) {
	if p.eof() {
		return nil, &Error{Msg: "expected a term"}
	}
	t := p.next()
	switch t.kind {
	case tPhrase:
		return Phrase(p.defaultField, t.text), nil
	case tLBrack, tLBrace:
		return p.parseRange(p.defaultField, t.kind)
	case tWord:
		field, rest := splitField(t.text)
		if field == "" {
			field = p.defaultField
		}
		// field:"phrase"
		if p.hasNext() && rest == "" && p.peek().kind == tPhrase {
			return Phrase(field, p.next().text), nil
		}
		// field:[lo TO hi] or field:{lo TO hi}
		if rest == "" && p.hasNext() && (p.peek().kind == tLBrack || p.peek().kind == tLBrace) {
			open := p.next().kind
			return p.parseRange(field, open)
		}
		if strings.HasSuffix(rest, "*") && len(rest) > 1 {
			return Prefix(field, strings.TrimSuffix(rest, "*")), nil
		}
		if rest == "" {
			return nil, &Error{Msg: "empty term for field " + field}
		}
		return Match(field, rest), nil
	default:
		return nil, &Error{Msg: "unexpected token in term position"}
	}
}

// parseRange parses the remainder of a range after its opening bracket has been
// consumed: "lo TO hi" followed by ] or }.
func (p *parser) parseRange(field string, open tokKind) (Query, error) {
	// Expect: lower TO upper close. lower/upper may be the wildcard *.
	if p.eof() {
		return nil, &Error{Msg: "unterminated range"}
	}
	lower := p.next()
	if lower.kind != tWord {
		return nil, &Error{Msg: "range lower bound must be a value"}
	}
	to := p.next()
	if to.kind != tWord || to.text != "TO" {
		return nil, &Error{Msg: "range expects TO between bounds"}
	}
	upper := p.next()
	if upper.kind != tWord {
		return nil, &Error{Msg: "range upper bound must be a value"}
	}
	if p.eof() {
		return nil, &Error{Msg: "unterminated range"}
	}
	close := p.next()
	includeLower := open == tLBrack
	var includeUpper bool
	switch close.kind {
	case tRBrack:
		includeUpper = true
	case tRBrace:
		includeUpper = false
	default:
		return nil, &Error{Msg: "range must close with ] or }"}
	}
	lo := rangeBound(lower.text)
	hi := rangeBound(upper.text)
	return Range(field, lo, hi, includeLower, includeUpper), nil
}

// rangeBound maps the open-bound wildcard to an empty string.
func rangeBound(s string) string {
	if s == "*" {
		return ""
	}
	return s
}

// splitField splits a "field:value" word into its field and value. A word with no
// colon yields an empty field and the whole word as the value.
func splitField(word string) (field, value string) {
	if f, v, ok := strings.Cut(word, ":"); ok {
		return f, v
	}
	return "", word
}
