package analysis

import (
	"unicode"
	"unicode/utf8"
)

// Tokenizer splits char-filtered text into a sequence of tokens. Offsets in the
// returned tokens are byte offsets into the text passed to Tokenize.
type Tokenizer interface {
	Tokenize(text string) []Token
}

// runeClass categorizes a rune for the standard tokenizer.
type runeClass int

const (
	classOther runeClass = iota
	classLetter
	classDigit
)

func classify(r rune) runeClass {
	switch {
	case unicode.IsLetter(r):
		return classLetter
	case unicode.IsDigit(r):
		return classDigit
	default:
		return classOther
	}
}

// StandardTokenizer is a Unicode word tokenizer in the spirit of UAX-29: it
// splits on whitespace and punctuation but keeps intra-word apostrophes (so
// "fox's" stays one token) and keeps runs that mix letters and digits or that
// look like numbers (so "192.168.1.1" tokenizes as a single <NUM>). It is the
// default tokenizer for the standard and english analyzers (doc 07 §3.1).
type StandardTokenizer struct{}

// Tokenize implements Tokenizer.
func (StandardTokenizer) Tokenize(text string) []Token {
	var toks []Token
	i := 0
	for i < len(text) {
		r, sz := utf8.DecodeRuneInString(text[i:])
		cls := classify(r)
		if cls == classOther {
			i += sz
			continue
		}
		start := i
		hasLetter := cls == classLetter
		hasDigit := cls == classDigit
		i += sz
		// Extend the token across letters, digits, and connective punctuation
		// that sits between two word characters.
		for i < len(text) {
			r, sz = utf8.DecodeRuneInString(text[i:])
			c := classify(r)
			if c == classLetter {
				hasLetter = true
				i += sz
				continue
			}
			if c == classDigit {
				hasDigit = true
				i += sz
				continue
			}
			// A connective character (apostrophe within a word, or . , - inside
			// a number) joins only if a word character follows it.
			if isConnector(r, hasDigit && !hasLetter) && nextIsWord(text[i+sz:], hasDigit && !hasLetter) {
				i += sz
				continue
			}
			break
		}
		typ := "<ALPHANUM>"
		switch {
		case hasDigit && !hasLetter:
			typ = "<NUM>"
		case hasLetter && !hasDigit:
			typ = "<ALPHA>"
		}
		toks = append(toks, newToken(text[start:i], start, i, typ))
	}
	return toks
}

// isConnector reports whether r may join two word characters inside one token.
// Inside a numeric run the separators . , and - are allowed (so IP addresses and
// decimals stay whole); otherwise only the apostrophe joins (so "fox's" holds).
func isConnector(r rune, numeric bool) bool {
	if r == '\'' || r == '’' {
		return true
	}
	if numeric {
		switch r {
		case '.', ',', '-':
			return true
		}
	}
	return false
}

// nextIsWord reports whether the next rune in s is a word character of the kind
// that can continue the current token.
func nextIsWord(s string, numeric bool) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	c := classify(r)
	if numeric {
		return c == classDigit
	}
	return c == classLetter || c == classDigit
}

// WhitespaceTokenizer splits only on Unicode whitespace and keeps everything
// else verbatim (doc 07 §3.2).
type WhitespaceTokenizer struct{}

// Tokenize implements Tokenizer.
func (WhitespaceTokenizer) Tokenize(text string) []Token {
	var toks []Token
	i := 0
	for i < len(text) {
		r, sz := utf8.DecodeRuneInString(text[i:])
		if unicode.IsSpace(r) {
			i += sz
			continue
		}
		start := i
		for i < len(text) {
			r, sz = utf8.DecodeRuneInString(text[i:])
			if unicode.IsSpace(r) {
				break
			}
			i += sz
		}
		toks = append(toks, newToken(text[start:i], start, i, "<WORD>"))
	}
	return toks
}

// KeywordTokenizer emits the entire input as a single token (doc 07 §3.3). It is
// the tokenizer of the keyword analyzer, used for exact-match fields.
type KeywordTokenizer struct{}

// Tokenize implements Tokenizer.
func (KeywordTokenizer) Tokenize(text string) []Token {
	if text == "" {
		return nil
	}
	return []Token{newToken(text, 0, len(text), "<KEYWORD>")}
}

// LetterTokenizer splits on any non-letter, keeping maximal runs of letters
// (doc 07 §3.6). It is the tokenizer of the simple analyzer.
type LetterTokenizer struct{}

// Tokenize implements Tokenizer.
func (LetterTokenizer) Tokenize(text string) []Token {
	var toks []Token
	i := 0
	for i < len(text) {
		r, sz := utf8.DecodeRuneInString(text[i:])
		if !unicode.IsLetter(r) {
			i += sz
			continue
		}
		start := i
		for i < len(text) {
			r, sz = utf8.DecodeRuneInString(text[i:])
			if !unicode.IsLetter(r) {
				break
			}
			i += sz
		}
		toks = append(toks, newToken(text[start:i], start, i, "<ALPHA>"))
	}
	return toks
}

// NGramTokenizer emits all character n-grams whose length is in [min,max], in
// per-start-position order, shortest length first at each position (doc 07 §3.4).
// For "hello" with min=2 max=3 it yields he, hel, el, ell, ll, llo, lo.
type NGramTokenizer struct {
	Min, Max int
}

// Tokenize implements Tokenizer. Offsets are byte offsets; n-gram lengths are
// counted in runes so multi-byte text grams correctly.
func (t NGramTokenizer) Tokenize(text string) []Token {
	mn, mx := t.Min, t.Max
	if mn < 1 {
		mn = 1
	}
	if mx < mn {
		mx = mn
	}
	// Index the byte offset of every rune plus the end sentinel.
	var starts []int
	for i := range text {
		starts = append(starts, i)
	}
	starts = append(starts, len(text))
	n := len(starts) - 1 // rune count

	var toks []Token
	for s := range n {
		for l := mn; l <= mx && s+l <= n; l++ {
			a, b := starts[s], starts[s+l]
			toks = append(toks, newToken(text[a:b], a, b, "<NGRAM>"))
		}
	}
	return toks
}

// EdgeNGramTokenizer emits n-grams anchored at the start of the input, of length
// min up to max (doc 07 §3.5). For "hello" with min=1 max=3 it yields h, he, hel.
type EdgeNGramTokenizer struct {
	Min, Max int
}

// Tokenize implements Tokenizer.
func (t EdgeNGramTokenizer) Tokenize(text string) []Token {
	mn, mx := t.Min, t.Max
	if mn < 1 {
		mn = 1
	}
	if mx < mn {
		mx = mn
	}
	var starts []int
	for i := range text {
		starts = append(starts, i)
	}
	starts = append(starts, len(text))
	n := len(starts) - 1

	var toks []Token
	for l := mn; l <= mx && l <= n; l++ {
		b := starts[l]
		toks = append(toks, newToken(text[:b], 0, b, "<EDGE_NGRAM>"))
	}
	return toks
}
