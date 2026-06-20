package analysis

import (
	"strings"
	"unicode"
)

// TokenFilter transforms a token stream: it may rewrite terms, drop tokens, or
// inject tokens (synonyms). It receives and returns a materialized slice at S2
// (doc 07 §4). A filter must preserve offsets on the tokens it keeps and set
// PositionIncr to 0 on tokens that occupy the same position as the previous one.
type TokenFilter interface {
	Filter(toks []Token) []Token
}

// LowercaseFilter lowercases every term (doc 07 §4.1).
type LowercaseFilter struct{}

// Filter implements TokenFilter.
func (LowercaseFilter) Filter(toks []Token) []Token {
	for i := range toks {
		toks[i].Term = strings.ToLower(toks[i].Term)
	}
	return toks
}

// StopFilter removes tokens whose term is in the stop set (doc 07 §4.2). Removing
// a token carries its position increment onto the next surviving token so phrase
// positions stay faithful to the original text.
type StopFilter struct {
	set map[string]struct{}
}

// NewStopFilter returns a stop filter over the given words.
func NewStopFilter(words []string) StopFilter {
	return StopFilter{set: stopSet(words)}
}

// englishStopFilter is the predefined _english_ stop filter.
func englishStopFilter() StopFilter { return StopFilter{set: englishStopSet} }

// Filter implements TokenFilter.
func (f StopFilter) Filter(toks []Token) []Token {
	out := toks[:0]
	carry := 0
	for _, t := range toks {
		if _, stop := f.set[t.Term]; stop {
			carry += t.PositionIncr
			continue
		}
		t.PositionIncr += carry
		carry = 0
		out = append(out, t)
	}
	return out
}

// LengthFilter drops tokens whose rune length is outside [Min,Max] (doc 07 §4.6).
// A Max of 0 means no upper bound.
type LengthFilter struct {
	Min, Max int
}

// Filter implements TokenFilter.
func (f LengthFilter) Filter(toks []Token) []Token {
	out := toks[:0]
	carry := 0
	for _, t := range toks {
		n := len([]rune(t.Term))
		if n < f.Min || (f.Max > 0 && n > f.Max) {
			carry += t.PositionIncr
			continue
		}
		t.PositionIncr += carry
		carry = 0
		out = append(out, t)
	}
	return out
}

// UniqueFilter removes duplicate terms (doc 07 §4.7). It keeps the first
// occurrence; later duplicates are dropped and their position increments carried
// onto the next surviving token.
type UniqueFilter struct{}

// Filter implements TokenFilter.
func (UniqueFilter) Filter(toks []Token) []Token {
	seen := make(map[string]struct{}, len(toks))
	out := toks[:0]
	carry := 0
	for _, t := range toks {
		if _, dup := seen[t.Term]; dup {
			carry += t.PositionIncr
			continue
		}
		seen[t.Term] = struct{}{}
		t.PositionIncr += carry
		carry = 0
		out = append(out, t)
	}
	return out
}

// ASCIIFoldingFilter converts non-ASCII Latin characters to their nearest ASCII
// equivalents (doc 07 §4.8): café -> cafe, naïve -> naive. Characters with no
// mapping pass through unchanged.
type ASCIIFoldingFilter struct{}

// Filter implements TokenFilter.
func (ASCIIFoldingFilter) Filter(toks []Token) []Token {
	for i := range toks {
		toks[i].Term = foldASCII(toks[i].Term)
	}
	return toks
}

func foldASCII(s string) string {
	for _, r := range s {
		if r >= unicode.MaxASCII {
			return foldASCIISlow(s)
		}
	}
	return s
}

func foldASCIISlow(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if repl, ok := asciiFoldMap[r]; ok {
			b.WriteString(repl)
			continue
		}
		if r < unicode.MaxASCII {
			b.WriteRune(r)
			continue
		}
		// Decompose accented Latin letters by stripping combining marks via the
		// base-letter table; unknown runes are kept as-is.
		b.WriteRune(r)
	}
	return b.String()
}

// StemmerFilter applies the Porter2 English stemmer to every term that is not
// flagged as a keyword (doc 07 §4.3).
type StemmerFilter struct{}

// Filter implements TokenFilter.
func (StemmerFilter) Filter(toks []Token) []Token {
	for i := range toks {
		if toks[i].isKeyword() {
			continue
		}
		toks[i].Term = porter2(toks[i].Term)
	}
	return toks
}

// EnglishPossessiveFilter strips a trailing 's or ’s from each term before
// stemming (doc 07 §4.9): "company's" -> "company".
type EnglishPossessiveFilter struct{}

// Filter implements TokenFilter.
func (EnglishPossessiveFilter) Filter(toks []Token) []Token {
	for i := range toks {
		term := toks[i].Term
		low := strings.ToLower(term)
		if strings.HasSuffix(low, "'s") || strings.HasSuffix(low, "’s") {
			toks[i].Term = term[:len(term)-2]
		}
	}
	return toks
}

// SynonymFilter injects synonyms at the position of a matching term (doc 07
// §4.4). At S2 it supports the expand form: when a term matches, its configured
// synonyms are emitted at the same position (PositionIncr 0) right after it. The
// original term is retained.
type SynonymFilter struct {
	// syn maps a single-word term to the list of synonyms to add at its position.
	syn map[string][]string
}

// NewSynonymFilter builds a synonym filter from rules of the form
// "fast => quick" or "fast, quick, rapid" (a comma group expands every member to
// every other member). Each rule is one entry in rules.
func NewSynonymFilter(rules []string) SynonymFilter {
	syn := make(map[string][]string)
	add := func(from string, tos []string) {
		from = strings.TrimSpace(from)
		if from == "" {
			return
		}
		for _, to := range tos {
			to = strings.TrimSpace(to)
			if to == "" || to == from {
				continue
			}
			syn[from] = append(syn[from], to)
		}
	}
	for _, rule := range rules {
		if lhs, rhs, ok := strings.Cut(rule, "=>"); ok {
			tos := strings.Split(rhs, ",")
			for from := range strings.SplitSeq(lhs, ",") {
				add(from, tos)
			}
			continue
		}
		members := strings.Split(rule, ",")
		for _, m := range members {
			add(m, members)
		}
	}
	return SynonymFilter{syn: syn}
}

// Filter implements TokenFilter.
func (f SynonymFilter) Filter(toks []Token) []Token {
	if len(f.syn) == 0 {
		return toks
	}
	out := make([]Token, 0, len(toks))
	for _, t := range toks {
		out = append(out, t)
		for _, s := range f.syn[t.Term] {
			syn := t
			syn.Term = s
			syn.PositionIncr = 0
			syn.Type = "<SYNONYM>"
			out = append(out, syn)
		}
	}
	return out
}
