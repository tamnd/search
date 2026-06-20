package analysis

import (
	"slices"
	"strings"
)

// porter2 implements the English Snowball ("Porter2") stemming algorithm. It is
// a faithful port of the reference algorithm at
// https://snowballstem.org/algorithms/english/stemmer.html (the english.sbl
// source), so its output matches the official test vocabulary and the stems used
// by Lucene and Elasticsearch. The result is a normalized stem that groups
// inflections of a word together; it is not always a real word ("lazy" -> "lazi").
//
// The input is expected to be lowercased by the lowercase filter, but the
// function lowercases defensively so it is correct in isolation.
func porter2(word string) string {
	word = strings.ToLower(word)
	// Words shorter than 3 letters are returned unchanged ("not hop 3").
	if len(word) < 3 {
		return word
	}
	// exception1: whole-word special cases checked before anything else.
	if r, ok := exception1[word]; ok {
		return r
	}

	w := []byte(word)
	w = prelude(w)

	p1, p2 := markRegions(w)

	w = step1a(w)
	w = step1b(w, p1)
	w = step1c(w)
	w = step2(w, p1)
	w = step3(w, p1, p2)
	w = step4(w, p2)
	w = step5(w, p1, p2)

	return restoreY(w)
}

// prelude removes a leading apostrophe and marks every consonant y as 'Y' (at the
// start of the word or following a vowel) so later steps treat it as a consonant.
func prelude(w []byte) []byte {
	if len(w) > 0 && w[0] == '\'' {
		w = w[1:]
	}
	if len(w) == 0 {
		return w
	}
	if w[0] == 'y' {
		w[0] = 'Y'
	}
	for i := 1; i < len(w); i++ {
		if w[i] == 'y' && isVowel(w[i-1]) {
			w[i] = 'Y'
		}
	}
	return w
}

// restoreY turns any remaining consonant marker 'Y' back into a lowercase y.
func restoreY(w []byte) string {
	for i := range w {
		if w[i] == 'Y' {
			w[i] = 'y'
		}
	}
	return string(w)
}

// isVowel reports whether c is a vowel. The marker 'Y' (consonant y) is not a
// vowel; only lowercase y is.
func isVowel(c byte) bool {
	switch c {
	case 'a', 'e', 'i', 'o', 'u', 'y':
		return true
	}
	return false
}

func isAEO(c byte) bool { return c == 'a' || c == 'e' || c == 'o' }

// regionPrefixes are the special prefixes that fix the R1 boundary directly,
// overriding the usual vowel/consonant scan (english.sbl mark_regions).
var regionPrefixes = []string{
	"gener", "commun", "arsen", "past", "univers", "later", "emerg", "organ", "inter",
}

// markRegions computes the R1 and R2 boundaries (p1 and p2). If the word starts
// with one of the special prefixes, R1 begins right after it; otherwise R1 is the
// region after the first non-vowel following a vowel. R2 applies that same scan
// starting from R1.
func markRegions(w []byte) (int, int) {
	s := string(w)
	p1 := -1
	for _, p := range regionPrefixes {
		if strings.HasPrefix(s, p) {
			p1 = len(p)
			break
		}
	}
	if p1 == -1 {
		p1 = regionAfter(w, 0)
	}
	p2 := regionAfter(w, p1)
	return p1, p2
}

// regionAfter returns the index after the first non-vowel that follows a vowel,
// scanning from start; it returns len(w) if there is no such position.
func regionAfter(w []byte, start int) int {
	i := start
	for i < len(w) && !isVowel(w[i]) {
		i++
	}
	for i < len(w) && isVowel(w[i]) {
		i++
	}
	if i < len(w) {
		return i + 1
	}
	return len(w)
}

// step1a handles possessive and plural s-suffixes.
func step1a(w []byte) []byte {
	// Strip a trailing possessive apostrophe form: 's', 's, or '.
	s := string(w)
	switch {
	case strings.HasSuffix(s, "'s'"):
		w = w[:len(w)-3]
	case strings.HasSuffix(s, "'s"):
		w = w[:len(w)-2]
	case strings.HasSuffix(s, "'"):
		w = w[:len(w)-1]
	}

	s = string(w)
	switch {
	case strings.HasSuffix(s, "sses"):
		return w[:len(w)-2] // sses -> ss
	case strings.HasSuffix(s, "ied"), strings.HasSuffix(s, "ies"):
		// replace by i if at least two letters precede the suffix, else by ie.
		if len(w)-3 >= 2 {
			return append(w[:len(w)-3], 'i')
		}
		return append(w[:len(w)-3], 'i', 'e')
	case strings.HasSuffix(s, "us"), strings.HasSuffix(s, "ss"):
		return w
	case strings.HasSuffix(s, "s"):
		// delete the s if a vowel precedes the character before it.
		for i := 0; i < len(w)-2; i++ {
			if isVowel(w[i]) {
				return w[:len(w)-1]
			}
		}
		return w
	}
	return w
}

// ingExceptions are the stems before "ing" that leave the whole word unchanged
// (inning, outing, canning, herring, earring, evening).
var ingExceptions = map[string]bool{
	"inn": true, "out": true, "cann": true, "herr": true, "earr": true, "even": true,
}

// step1b handles the eed/eedly and ed/edly/ing/ingly suffixes.
func step1b(w []byte, p1 int) []byte {
	s := string(w)

	// eed / eedly: replace with ee if in R1, except proceed/exceed/succeed.
	switch {
	case strings.HasSuffix(s, "eedly"):
		if len(w)-5 >= p1 {
			return w[:len(w)-3] // eedly -> ee
		}
		return w
	case strings.HasSuffix(s, "eed"):
		if len(w)-3 >= p1 {
			base := s[:len(w)-3]
			if base == "proc" || base == "exc" || base == "succ" {
				return w
			}
			return w[:len(w)-1] // eed -> ee
		}
		return w
	}

	// Exceptional -ing handling.
	if strings.HasSuffix(s, "ing") {
		base := s[:len(w)-3]
		if ingExceptions[base] {
			return w
		}
		// A single consonant + y + ing (whole word) becomes consonant + ie:
		// dying->die, lying->lie, tying->tie, vying->vie, hying->hie.
		if len(w) == 5 && (w[1] == 'y' || w[1] == 'Y') && !isVowel(w[0]) {
			return []byte{w[0], 'i', 'e'}
		}
	}

	// Generic ed/edly/ing/ingly: delete if the preceding part has a vowel.
	var suf string
	switch {
	case strings.HasSuffix(s, "ingly"):
		suf = "ingly"
	case strings.HasSuffix(s, "edly"):
		suf = "edly"
	case strings.HasSuffix(s, "ing"):
		suf = "ing"
	case strings.HasSuffix(s, "ed"):
		suf = "ed"
	}
	if suf == "" {
		return w
	}
	stem := w[:len(w)-len(suf)]
	if !containsVowel(stem) {
		return w
	}
	w = stem
	s = string(w)
	switch {
	case strings.HasSuffix(s, "at"), strings.HasSuffix(s, "bl"), strings.HasSuffix(s, "iz"):
		return append(w, 'e')
	case endsDouble(w):
		// Collapse the double unless the word is a vowel(a/e/o) then double at the
		// very start (add, ebb, egg, odd, off, err stay intact).
		if len(w) == 3 && isAEO(w[0]) {
			return w
		}
		return w[:len(w)-1]
	case isShort(w, p1):
		return append(w, 'e')
	}
	return w
}

// step1c turns a terminal y/Y into i when preceded by a non-vowel that is not the
// first letter of the word.
func step1c(w []byte) []byte {
	n := len(w)
	if n > 2 && (w[n-1] == 'y' || w[n-1] == 'Y') && !isVowel(w[n-2]) {
		w[n-1] = 'i'
	}
	return w
}

// step2 rewrites a set of derivational suffixes that lie in R1.
func step2(w []byte, p1 int) []byte {
	p, ok := longestPair(w, step2pairs)
	if !ok || len(w)-len(p.suf) < p1 {
		return w
	}
	switch p.suf {
	case "ogi":
		if len(w) >= 4 && w[len(w)-4] == 'l' {
			return replaceSuffix(w, p.suf, p.repl)
		}
		return w
	case "li":
		if len(w) >= 3 && isLiEnding(w[len(w)-3]) {
			return w[:len(w)-2]
		}
		return w
	default:
		return replaceSuffix(w, p.suf, p.repl)
	}
}

// step3 rewrites another set of suffixes that lie in R1 (ative only in R2).
func step3(w []byte, p1, p2 int) []byte {
	p, ok := longestPair(w, step3pairs)
	if !ok || len(w)-len(p.suf) < p1 {
		return w
	}
	if p.suf == "ative" {
		if len(w)-len(p.suf) >= p2 {
			return w[:len(w)-len(p.suf)]
		}
		return w
	}
	return replaceSuffix(w, p.suf, p.repl)
}

// step4 deletes derivational suffixes that lie in R2.
func step4(w []byte, p2 int) []byte {
	suf, ok := longestSuffix(w, step4suffixes)
	if !ok || len(w)-len(suf) < p2 {
		return w
	}
	if suf == "ion" {
		if len(w) >= 4 && (w[len(w)-4] == 's' || w[len(w)-4] == 't') {
			return w[:len(w)-len(suf)]
		}
		return w
	}
	return w[:len(w)-len(suf)]
}

// step5 removes a final e or l under the R1/R2 conditions.
func step5(w []byte, p1, p2 int) []byte {
	n := len(w)
	if n == 0 {
		return w
	}
	if w[n-1] == 'e' {
		if n-1 >= p2 {
			return w[:n-1]
		}
		if n-1 >= p1 && !endsShortSyllableAt(w, n-1) {
			return w[:n-1]
		}
		return w
	}
	if w[n-1] == 'l' && n-1 >= p2 && n >= 2 && w[n-2] == 'l' {
		return w[:n-1]
	}
	return w
}

// longestPair returns the longest suffix in pairs that w ends with. Porter2
// selects the single longest matching suffix, then applies its condition; it
// never falls back to a shorter suffix when the condition fails.
func longestPair(w []byte, pairs []sufPair) (sufPair, bool) {
	s := string(w)
	best := -1
	var bestPair sufPair
	for _, p := range pairs {
		if len(p.suf) > best && strings.HasSuffix(s, p.suf) {
			best = len(p.suf)
			bestPair = p
		}
	}
	return bestPair, best >= 0
}

// longestSuffix returns the longest entry in suffixes that w ends with.
func longestSuffix(w []byte, suffixes []string) (string, bool) {
	s := string(w)
	best := ""
	for _, suf := range suffixes {
		if len(suf) > len(best) && strings.HasSuffix(s, suf) {
			best = suf
		}
	}
	return best, best != ""
}

func replaceSuffix(w []byte, suf, repl string) []byte {
	base := w[:len(w)-len(suf)]
	return append(base, repl...)
}

func containsVowel(w []byte) bool {
	return slices.ContainsFunc(w, isVowel)
}

// endsDouble reports whether the word ends in one of the doubled consonants the
// algorithm collapses: bb dd ff gg mm nn pp rr tt.
func endsDouble(w []byte) bool {
	n := len(w)
	if n < 2 || w[n-1] != w[n-2] {
		return false
	}
	switch w[n-1] {
	case 'b', 'd', 'f', 'g', 'm', 'n', 'p', 'r', 't':
		return true
	}
	return false
}

// isShort reports whether the whole word is "short": R1 is empty (begins at or
// past the end of the word) and the word ends in a short syllable.
func isShort(w []byte, p1 int) bool {
	return p1 >= len(w) && endsShortSyllableAt(w, len(w))
}

// endsShortSyllableAt reports whether the word ends in a short syllable at the
// position end (exclusive): a non-vowel, then a vowel, then a non-vowel that is
// not w, x, or Y; or, at the start of the word, a vowel followed by a non-vowel.
// The whole word "past" also counts as ending in a short syllable.
func endsShortSyllableAt(w []byte, end int) bool {
	if end == 4 && string(w[:4]) == "past" {
		return true
	}
	if end == 2 {
		return isVowel(w[0]) && !isVowel(w[1])
	}
	if end < 3 {
		return false
	}
	a, b, c := w[end-3], w[end-2], w[end-1]
	if !isVowel(a) && isVowel(b) && !isVowel(c) {
		switch c {
		case 'w', 'x', 'Y':
			return false
		}
		return true
	}
	return false
}

func isLiEnding(c byte) bool {
	switch c {
	case 'c', 'd', 'e', 'g', 'h', 'k', 'm', 'n', 'r', 't':
		return true
	}
	return false
}

type sufPair struct{ suf, repl string }

// step2pairs map each step-2 suffix to its replacement (english.sbl Step_2).
var step2pairs = []sufPair{
	{"ization", "ize"},
	{"ational", "ate"},
	{"fulness", "ful"},
	{"ousness", "ous"},
	{"iveness", "ive"},
	{"tional", "tion"},
	{"biliti", "ble"},
	{"lessli", "less"},
	{"ousli", "ous"},
	{"entli", "ent"},
	{"ation", "ate"},
	{"alism", "al"},
	{"aliti", "al"},
	{"iviti", "ive"},
	{"fulli", "ful"},
	{"ogist", "og"},
	{"enci", "ence"},
	{"anci", "ance"},
	{"abli", "able"},
	{"izer", "ize"},
	{"ator", "ate"},
	{"alli", "al"},
	{"bli", "ble"},
	{"ogi", "og"},
	{"li", ""},
}

// step3pairs map each step-3 suffix to its replacement (english.sbl Step_3).
var step3pairs = []sufPair{
	{"ational", "ate"},
	{"tional", "tion"},
	{"alize", "al"},
	{"icate", "ic"},
	{"iciti", "ic"},
	{"ical", "ic"},
	{"ful", ""},
	{"ness", ""},
	{"ative", ""},
}

// step4suffixes are the step-4 suffixes deleted in R2 (english.sbl Step_4).
var step4suffixes = []string{
	"ement", "ance", "ence", "able", "ible", "ment",
	"ant", "ent", "ism", "ate", "iti", "ous", "ive", "ize", "ion",
	"al", "er", "ic",
}

// exception1 are whole-word special cases handled before any step (english.sbl
// exception1): special changes, special -ly cases, and invariant forms.
var exception1 = map[string]string{
	"skis":   "ski",
	"skies":  "sky",
	"idly":   "idl",
	"gently": "gentl",
	"ugly":   "ugli",
	"early":  "earli",
	"only":   "onli",
	"singly": "singl",
	"sky":    "sky",
	"news":   "news",
	"howe":   "howe",
	"atlas":  "atlas",
	"cosmos": "cosmos",
	"bias":   "bias",
	"andes":  "andes",
}
