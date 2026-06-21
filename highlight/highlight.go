// Package highlight produces term-in-context snippets for matched documents (spec
// 2063 doc 11 §8). It implements the plain strategy: re-analyze the stored field
// value with the same analyzer used at index time, locate the tokens whose terms
// are in the query's term set, group nearby matches into fragments, and wrap each
// matched token in the configured tags. Working from byte offsets keeps the
// surrounding text intact, including punctuation and casing the analyzer dropped.
package highlight

import (
	"sort"
	"strings"

	"github.com/tamnd/search/analysis"
)

// Options controls how a field is highlighted (doc 11 §8.3, §8.4). The zero value
// is completed by withDefaults to the documented defaults.
type Options struct {
	PreTag       string // wraps the start of a matched term; default "<em>"
	PostTag      string // wraps the end of a matched term; default "</em>"
	FragmentSize int    // target fragment length in characters; 0 means the whole field
	NumFragments int    // maximum fragments to return; default 5
	Order        string // "none" (document order) or "score" (best first)
}

func (o Options) withDefaults() Options {
	if o.PreTag == "" {
		o.PreTag = "<em>"
	}
	if o.PostTag == "" {
		o.PostTag = "</em>"
	}
	if o.NumFragments == 0 {
		o.NumFragments = 5
	}
	return o
}

// Highlighter wraps an analyzer and the highlight options for one field.
type Highlighter struct {
	analyzer *analysis.Analyzer
	opts     Options
}

// New returns a highlighter for a field using the given analyzer.
func New(a *analysis.Analyzer, opts Options) *Highlighter {
	return &Highlighter{analyzer: a, opts: opts.withDefaults()}
}

// match is a token in the source text whose term is in the query set.
type match struct {
	start int
	end   int
}

// Fragments returns the highlighted fragments for one field value. terms is the
// set of analyzed query terms to highlight; a token highlights when its analyzed
// term is present. An empty result means the field value held none of the terms.
func (h *Highlighter) Fragments(text string, terms map[string]struct{}) []string {
	if text == "" || len(terms) == 0 {
		return nil
	}
	tokens := h.analyzer.Analyze(text)
	var matches []match
	for _, t := range tokens {
		if _, ok := terms[t.Term]; ok && t.EndOffset > t.StartOffset && t.EndOffset <= len(text) {
			matches = append(matches, match{start: t.StartOffset, end: t.EndOffset})
		}
	}
	if len(matches) == 0 {
		return nil
	}
	// fragment_size 0 returns the entire field as a single fragment with every
	// match wrapped in place.
	if h.opts.FragmentSize <= 0 {
		return []string{h.wrap(text, matches)}
	}
	return h.fragments(text, matches)
}

// fragments groups matches into windows of roughly FragmentSize characters,
// scores each by how many matches it holds, and returns up to NumFragments of
// them, wrapped. Fragments are returned in document order unless Order is "score".
func (h *Highlighter) fragments(text string, matches []match) []string {
	size := h.opts.FragmentSize
	type frag struct {
		start, end int
		count      int
	}
	var frags []frag
	i := 0
	for i < len(matches) {
		// Center a window of size on the first unconsumed match, snapped to the
		// text bounds and to nearby word boundaries.
		half := size / 2
		start := max(matches[i].start-half, 0)
		end := start + size
		if end > len(text) {
			end = len(text)
			start = max(end-size, 0)
		}
		start = snapLeft(text, start)
		end = snapRight(text, end)
		// Consume every match that falls inside the window.
		count := 0
		for i < len(matches) && matches[i].start >= start && matches[i].end <= end {
			count++
			i++
		}
		if count == 0 {
			// A single match longer than the window: emit it on its own.
			count = 1
			i++
		}
		frags = append(frags, frag{start: start, end: end, count: count})
	}
	if h.opts.Order == "score" {
		sort.SliceStable(frags, func(a, b int) bool { return frags[a].count > frags[b].count })
	}
	if len(frags) > h.opts.NumFragments {
		frags = frags[:h.opts.NumFragments]
	}
	out := make([]string, 0, len(frags))
	for _, f := range frags {
		inside := matchesIn(matches, f.start, f.end)
		out = append(out, strings.TrimSpace(h.wrapRange(text, f.start, f.end, inside)))
	}
	return out
}

// matchesIn returns the matches that fall within [start,end).
func matchesIn(matches []match, start, end int) []match {
	var in []match
	for _, m := range matches {
		if m.start >= start && m.end <= end {
			in = append(in, m)
		}
	}
	return in
}

// wrap wraps every match in the full text.
func (h *Highlighter) wrap(text string, matches []match) string {
	return h.wrapRange(text, 0, len(text), matches)
}

// wrapRange renders text[start:end) with each match in the slice wrapped in the
// configured tags. Matches must be sorted ascending and lie within the range.
func (h *Highlighter) wrapRange(text string, start, end int, matches []match) string {
	var b strings.Builder
	pos := start
	for _, m := range matches {
		if m.start < pos {
			continue
		}
		b.WriteString(text[pos:m.start])
		b.WriteString(h.opts.PreTag)
		b.WriteString(text[m.start:m.end])
		b.WriteString(h.opts.PostTag)
		pos = m.end
	}
	if pos < end {
		b.WriteString(text[pos:end])
	}
	return b.String()
}

// snapLeft moves a start offset left to just after the nearest whitespace, so a
// fragment begins at a word boundary rather than mid-word.
func snapLeft(text string, pos int) int {
	if pos <= 0 {
		return 0
	}
	for pos > 0 && !isSpace(text[pos-1]) {
		pos--
	}
	return pos
}

// snapRight moves an end offset right to the nearest whitespace, so a fragment
// ends at a word boundary.
func snapRight(text string, pos int) int {
	if pos >= len(text) {
		return len(text)
	}
	for pos < len(text) && !isSpace(text[pos]) {
		pos++
	}
	return pos
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
