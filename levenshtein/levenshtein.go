// Package levenshtein builds a Levenshtein automaton for fuzzy term matching
// (spec 2063 doc 11 §3.9, doc 12 §9). The automaton drives a walk over the term
// dictionary's FST: at each candidate character the walk extends one row of the
// edit-distance dynamic-programming matrix, accepts a term when the final cell is
// within the edit distance, and prunes a branch as soon as every cell of the row
// exceeds the distance. Carrying the DP row instead of compiling a full DFA keeps
// the implementation small and exact while still pruning the FST walk.
package levenshtein

import "unicode/utf8"

// Automaton matches terms within a fixed edit distance of a query term. It is
// immutable after construction and safe for concurrent use; the mutable matching
// state lives in State values.
type Automaton struct {
	runes []rune
	max   int
}

// New builds an automaton that accepts any term within maxEdits edits (Levenshtein
// distance, substitutions, insertions, and deletions each cost one) of term.
func New(term string, maxEdits int) *Automaton {
	if maxEdits < 0 {
		maxEdits = 0
	}
	return &Automaton{runes: []rune(term), max: maxEdits}
}

// AutoEdits returns the conventional edit distance for a term of the given length
// in runes: 0 for very short terms, 1 for medium terms, 2 for long terms (doc 11
// §3.9). Callers may override it.
func AutoEdits(termLen int) int {
	switch {
	case termLen <= 2:
		return 0
	case termLen <= 4:
		return 1
	default:
		return 2
	}
}

// State is one row of the edit-distance matrix: cell j holds the distance between
// the candidate prefix consumed so far and the first j characters of the query
// term. The zero State is not valid; obtain the initial state from Start.
type State struct {
	row []int
}

// Start returns the matching state before any candidate character is consumed:
// the row [0, 1, 2, ..., len(term)] (the cost of deleting each query character).
func (a *Automaton) Start() State {
	row := make([]int, len(a.runes)+1)
	for i := range row {
		row[i] = i
	}
	return State{row: row}
}

// Step returns the state after consuming candidate rune r. It computes the next
// DP row from the current one.
func (a *Automaton) Step(s State, r rune) State {
	prev := s.row
	next := make([]int, len(prev))
	next[0] = prev[0] + 1
	for j := 1; j < len(prev); j++ {
		cost := 1
		if a.runes[j-1] == r {
			cost = 0
		}
		next[j] = min3(prev[j]+1, next[j-1]+1, prev[j-1]+cost)
	}
	return State{row: next}
}

// IsMatch reports whether the candidate consumed to reach s is within the edit
// distance of the query term (the final cell is at most max).
func (a *Automaton) IsMatch(s State) bool {
	return s.row[len(s.row)-1] <= a.max
}

// CanMatch reports whether any extension of the candidate consumed to reach s
// could still match: at least one cell of the row is within the edit distance. A
// false result lets the FST walk prune the whole subtree.
func (a *Automaton) CanMatch(s State) bool {
	for _, v := range s.row {
		if v <= a.max {
			return true
		}
	}
	return false
}

// Distance returns the exact Levenshtein distance between a and b. It is used by
// tests and as a direct fallback when an automaton is not warranted.
func Distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	cur := make([]int, len(rb)+1)
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// RuneLen returns the number of runes in s, the length AutoEdits expects.
func RuneLen(s string) int { return utf8.RuneCountInString(s) }

func min3(a, b, c int) int {
	return min(a, min(b, c))
}
