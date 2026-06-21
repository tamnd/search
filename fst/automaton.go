package fst

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tamnd/search/levenshtein"
)

// FuzzyScan returns every term within maxEdits Levenshtein edits of term, in
// lexicographic order (spec 2063 doc 12 §9). The walk carries one row of the
// edit-distance matrix and prunes a subtree the moment no extension of the
// consumed prefix can still match, so it visits far fewer nodes than a full
// enumeration. Characters are matched at rune boundaries so multi-byte terms
// compare by rune, not by byte.
func (f *FST) FuzzyScan(term string, maxEdits int) ([]Entry, error) {
	aut := levenshtein.New(term, maxEdits)
	var out []Entry
	var walk func(off uint64, termBytes, pending []byte, st levenshtein.State, acc uint64) error
	walk = func(off uint64, termBytes, pending []byte, st levenshtein.State, acc uint64) error {
		n, err := f.readNode(off)
		if err != nil {
			return err
		}
		atBoundary := len(pending) == 0
		if atBoundary {
			if n.isFinal && aut.IsMatch(st) {
				out = append(out, Entry{Term: append([]byte(nil), termBytes...), Output: acc + n.finalOutput})
			}
			if !aut.CanMatch(st) {
				return nil
			}
		}
		for _, arc := range n.arcs {
			nbTerm := append(append([]byte(nil), termBytes...), arc.label)
			nbPending := append(append([]byte(nil), pending...), arc.label)
			if utf8.FullRune(nbPending) {
				r, _ := utf8.DecodeRune(nbPending)
				nst := aut.Step(st, r)
				if err := walk(arc.target, nbTerm, nil, nst, acc+arc.output); err != nil {
					return err
				}
			} else {
				if err := walk(arc.target, nbTerm, nbPending, st, acc+arc.output); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(f.root, nil, nil, aut.Start(), 0); err != nil {
		return nil, err
	}
	return out, nil
}

// WildcardScan returns every term matching a glob pattern, where * matches any
// run of characters and ? matches a single character (doc 11 §3.10). The literal
// prefix before the first wildcard restricts the FST walk; the remainder is
// matched with an anchored regular expression.
func (f *FST) WildcardScan(pattern string) ([]Entry, error) {
	prefix := wildcardLiteralPrefix(pattern)
	re, err := regexp.Compile("^" + wildcardToRegexp(pattern) + "$")
	if err != nil {
		return nil, err
	}
	return f.filterScan(prefix, re, 0)
}

// RegexpScan returns every term fully matching re, in lexicographic order (doc 11
// §3.11). re must be anchored by the caller's intent; this method matches the
// whole term. literalPrefix, when non-empty, restricts the FST walk to that
// prefix. maxVisit caps how many terms are examined; when the cap is exceeded the
// returned bool is true, signalling the planner to warn. A maxVisit of 0 means no
// cap.
func (f *FST) RegexpScan(re *regexp.Regexp, literalPrefix string, maxVisit int) ([]Entry, bool, error) {
	entries, visited, err := f.filterScanCounted(literalPrefix, re, maxVisit)
	return entries, visited, err
}

// filterScan walks the terms under prefix and keeps those matching re.
func (f *FST) filterScan(prefix string, re *regexp.Regexp, maxVisit int) ([]Entry, error) {
	entries, _, err := f.filterScanCounted(prefix, re, maxVisit)
	return entries, err
}

// filterScanCounted is the shared body of WildcardScan and RegexpScan. It returns
// the matching entries and whether the visit cap was exceeded.
func (f *FST) filterScanCounted(prefix string, re *regexp.Regexp, maxVisit int) ([]Entry, bool, error) {
	cands, err := f.PrefixScan([]byte(prefix))
	if err != nil {
		return nil, false, err
	}
	var out []Entry
	overflow := false
	for i, e := range cands {
		if maxVisit > 0 && i >= maxVisit {
			overflow = true
			break
		}
		if re.Match(e.Term) {
			out = append(out, e)
		}
	}
	return out, overflow, nil
}

// wildcardLiteralPrefix returns the leading run of pattern up to the first
// unescaped wildcard metacharacter.
func wildcardLiteralPrefix(pattern string) string {
	var b strings.Builder
	for _, r := range pattern {
		if r == '*' || r == '?' {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// wildcardToRegexp translates a glob pattern into a regular-expression body. Glob
// metacharacters * and ? become .* and .; every other character is quoted.
func wildcardToRegexp(pattern string) string {
	var b strings.Builder
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}
