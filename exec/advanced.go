package exec

import (
	"regexp"
	"regexp/syntax"

	"github.com/tamnd/search/docvalues"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/score"
)

// regexpVisitCap bounds how many terms a single segment's regexp scan examines
// before the planner flags the query as expensive (doc 11 §3.11). It is a soft
// guard: the scan stops and the overflow is reported, it does not error.
const regexpVisitCap = 10000

// compileFuzzy expands the query term to every dictionary term within the edit
// distance and unions them with a constant score, like a prefix query.
func (se *Searcher) compileFuzzy(n *query.FuzzyQuery) (scorer, error) {
	edits := n.MaxEdits
	if n.AutoEdits {
		edits = autoEdits(n.Term)
	}
	expand := func(fr fieldReaderLike) ([]string, error) {
		return fr.FuzzyTerms(n.Term, edits)
	}
	return se.compileExpansion(n.Field, n.Boost(), expand)
}

// compileWildcard expands a glob pattern to matching dictionary terms.
func (se *Searcher) compileWildcard(n *query.WildcardQuery) (scorer, error) {
	expand := func(fr fieldReaderLike) ([]string, error) {
		return fr.WildcardTerms(n.Pattern)
	}
	return se.compileExpansion(n.Field, n.Boost(), expand)
}

// compileRegexp expands a regular expression to fully matching dictionary terms.
// The expression is anchored to the whole term and its literal prefix restricts
// the per-segment scan.
func (se *Searcher) compileRegexp(n *query.RegexpQuery) (scorer, error) {
	re, err := regexp.Compile("^(?:" + n.Pattern + ")$")
	if err != nil {
		return nil, &query.Error{Msg: "regexp query: " + err.Error()}
	}
	prefix := regexpLiteralPrefix(n.Pattern)
	expand := func(fr fieldReaderLike) ([]string, error) {
		terms, over, err := fr.RegexpTerms(re, prefix, regexpVisitCap)
		if err != nil {
			return nil, err
		}
		if over {
			se.warnf("regexp query on %q visited more than %d terms; consider a prefix", n.Field, regexpVisitCap)
		}
		return terms, nil
	}
	return se.compileExpansion(n.Field, n.Boost(), expand)
}

// fieldReaderLike is the subset of FieldReader the term-expansion closures use,
// declared so the closures stay readable.
type fieldReaderLike interface {
	FuzzyTerms(term string, maxEdits int) ([]string, error)
	WildcardTerms(pattern string) ([]string, error)
	RegexpTerms(re *regexp.Regexp, literalPrefix string, maxVisit int) ([]string, bool, error)
}

// compileExpansion runs an expansion function over every segment's field reader
// and unions the resulting term postings with a constant score.
func (se *Searcher) compileExpansion(field string, boost float32, expand func(fr fieldReaderLike) ([]string, error)) (scorer, error) {
	var children []scorer
	for _, seg := range se.segs {
		fr, err := se.fieldReader(seg, field)
		if err != nil {
			return nil, err
		}
		if fr == nil {
			continue
		}
		terms, err := expand(fr)
		if err != nil {
			return nil, err
		}
		for _, t := range terms {
			r, ok, err := fr.Postings(t)
			if err != nil {
				return nil, err
			}
			if ok {
				children = append(children, newTermScorer(r, dummyWeight, fr, 0, 0))
			}
		}
	}
	return constantUnion(children, boost), nil
}

// compileSpanNear matches the literal terms within slop positions, in order or
// unordered, per segment, chained across segments. The terms are not analyzed;
// they are used as indexed.
func (se *Searcher) compileSpanNear(n *query.SpanNearQuery) (scorer, error) {
	if len(n.Terms) == 1 {
		return se.compileTerm(n.Field, n.Terms[0], n.Boost())
	}
	weights := make([]*score.Weight, len(n.Terms))
	for i, t := range n.Terms {
		w, err := se.weightFor(n.Field, t, n.Boost())
		if err != nil {
			return nil, err
		}
		weights[i] = w
	}
	var segScorers []scorer
	for _, seg := range se.segs {
		fr, err := se.fieldReader(seg, n.Field)
		if err != nil {
			return nil, err
		}
		if fr == nil || !fr.Positional() {
			continue
		}
		tss := make([]*termScorer, len(n.Terms))
		ok := true
		for i, t := range n.Terms {
			r, found, err := fr.Postings(t)
			if err != nil {
				return nil, err
			}
			if !found {
				ok = false
				break
			}
			tss[i] = newTermScorer(r, weights[i], fr, 0, 0)
		}
		if !ok {
			continue
		}
		segScorers = append(segScorers, newSpanScorer(tss, n.Slop, n.InOrder))
	}
	switch len(segScorers) {
	case 0:
		return emptyScorer{}, nil
	case 1:
		return segScorers[0], nil
	default:
		return newChainScorer(segScorers), nil
	}
}

// compileGeoDistance matches every document whose geo_point value lies within the
// query radius, scoring each a constant. It reads the geographic doc-values column
// per segment and verifies the great-circle distance exactly.
func (se *Searcher) compileGeoDistance(n *query.GeoDistanceQuery) (scorer, error) {
	var matched []uint32
	for _, seg := range se.segs {
		blob, ok, err := seg.DocValues(se.kv, n.Field)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		col, err := docvalues.OpenColumn(blob)
		if err != nil {
			return nil, err
		}
		gc, ok := col.(docvalues.GeoColumn)
		if !ok {
			continue
		}
		base := seg.Meta().BaseDoc
		cnt := gc.DocCount()
		for i := range cnt {
			if !gc.HasValue(i) {
				continue
			}
			lat, lon := gc.LatLon(i)
			if docvalues.Haversine(n.Lat, n.Lon, lat, lon) <= n.Meters {
				matched = append(matched, base+i)
			}
		}
	}
	if len(matched) == 0 {
		return emptyScorer{}, nil
	}
	return &constantScorer{inner: newSliceScorer(matched, 1), value: n.Boost()}, nil
}

// spanNearScorer matches the constituent terms within slop positions of one
// another in one segment, either in left-to-right order or in any order. It
// conjoins the per-term positional scorers to find candidate documents, then
// verifies the positional constraint. Its score is the sum of the term scores,
// matching a phrase query.
type spanNearScorer struct {
	terms   []*termScorer
	and     *andScorer
	slop    int
	inOrder bool
	cur     uint32
}

func newSpanScorer(terms []*termScorer, slop int, inOrder bool) *spanNearScorer {
	subs := make([]scorer, len(terms))
	for i, t := range terms {
		subs[i] = t
	}
	return &spanNearScorer{terms: terms, and: newAndScorer(subs), slop: slop, inOrder: inOrder}
}

func (p *spanNearScorer) docID() uint32 { return p.cur }

func (p *spanNearScorer) next() (uint32, error) {
	d, err := p.and.next()
	return p.find(d, err)
}

func (p *spanNearScorer) advance(target uint32) (uint32, error) {
	d, err := p.and.advance(target)
	return p.find(d, err)
}

// find filters the conjunction's candidate stream down to the documents whose
// term positions satisfy the span. A rejected candidate is followed with the
// conjunction's next(), not a repeat advance(target): once the conjunction has
// passed the target, re-advancing returns the same document and would spin.
func (p *spanNearScorer) find(d uint32, err error) (uint32, error) {
	for {
		if err != nil {
			return 0, err
		}
		if d == noMore {
			p.cur = noMore
			return noMore, nil
		}
		ok, mErr := p.matches()
		if mErr != nil {
			return 0, mErr
		}
		if ok {
			p.cur = d
			return d, nil
		}
		d, err = p.and.next()
	}
}

func (p *spanNearScorer) matches() (bool, error) {
	posLists := make([][]uint32, len(p.terms))
	for i, t := range p.terms {
		pos, err := t.r.Positions()
		if err != nil {
			return false, err
		}
		posLists[i] = pos
	}
	if p.inOrder {
		return phraseInOrder(posLists, p.slop), nil
	}
	return spanUnordered(posLists, p.slop), nil
}

func (p *spanNearScorer) score() float32 {
	var s float32
	for _, t := range p.terms {
		s += t.score()
	}
	return s
}

func (p *spanNearScorer) cost() int { return p.and.cost() }

// spanUnordered reports whether positions admit an unordered near match: there is
// a choice of one position per term whose covering window, max - min, minus the
// term count is at most slop. It computes the smallest window covering one element
// from every list with a k-way merge over the ascending position lists.
func spanUnordered(posLists [][]uint32, slop int) bool {
	k := len(posLists)
	if k == 0 {
		return false
	}
	for _, pl := range posLists {
		if len(pl) == 0 {
			return false
		}
	}
	idx := make([]int, k)
	best := -1
	for {
		lo, hi, loList := ^uint32(0), uint32(0), -1
		for i := range k {
			v := posLists[i][idx[i]]
			if v < lo {
				lo, loList = v, i
			}
			if v > hi {
				hi = v
			}
		}
		window := int(hi) - int(lo)
		if best < 0 || window < best {
			best = window
		}
		idx[loList]++
		if idx[loList] >= len(posLists[loList]) {
			break
		}
	}
	return best-(k-1) <= slop
}

// autoEdits returns the conventional edit distance for a term of the given rune
// length: 0 for very short, 1 for medium, 2 for long terms (doc 11 §3.9).
func autoEdits(term string) int {
	switch n := len([]rune(term)); {
	case n <= 2:
		return 0
	case n <= 4:
		return 1
	default:
		return 2
	}
}

// regexpLiteralPrefix returns the literal prefix of a regular expression, the run
// of characters that every match must begin with, used to restrict the FST scan.
func regexpLiteralPrefix(pattern string) string {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return ""
	}
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		return ""
	}
	prefix, _ := prog.Prefix()
	return prefix
}
