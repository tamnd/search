package exec

import (
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/score"
)

// dummyWeight scores nothing; it stands in for the constituent term scorers of a
// constant-scored clause (prefix, range, filter), whose own scores are discarded.
var dummyWeight = score.NewWeight(0, 0, 0, 1, score.DefaultK1, score.DefaultB)

// compile turns a query node into a single global scorer over the segment chain.
func (se *Searcher) compile(q query.Query) (scorer, error) {
	switch n := q.(type) {
	case *query.MatchAllQuery:
		return newSliceScorer(se.live, n.Boost()), nil
	case *query.MatchNoneQuery:
		return emptyScorer{}, nil
	case *query.TermQuery:
		return se.compileTerm(n.Field, n.Term, n.Boost())
	case *query.MatchQuery:
		return se.compileMatch(n)
	case *query.MatchPhraseQuery:
		return se.compilePhrase(n)
	case *query.PrefixQuery:
		return se.compilePrefix(n.Field, n.Prefix, n.Boost())
	case *query.RangeQuery:
		return se.compileRange(n)
	case *query.BoolQuery:
		return se.compileBool(n)
	default:
		return emptyScorer{}, nil
	}
}

// compileTerm chains a term's per-segment postings into one scored stream.
func (se *Searcher) compileTerm(field, term string, boost float32) (scorer, error) {
	w, err := se.weightFor(field, term, boost)
	if err != nil {
		return nil, err
	}
	subs, err := se.termSubs(field, term, w)
	if err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return emptyScorer{}, nil
	}
	return newChainScorer(subs), nil
}

// termSubs builds one per-segment term scorer for every segment that holds the
// term, in segment order (ascending doc-ids).
func (se *Searcher) termSubs(field, term string, w *score.Weight) ([]scorer, error) {
	var subs []scorer
	for _, seg := range se.segs {
		fr, err := se.fieldReader(seg, field)
		if err != nil {
			return nil, err
		}
		if fr == nil {
			continue
		}
		r, maxFreq, minNorm, ok, err := fr.PostingsWithStats(term)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		subs = append(subs, newTermScorer(r, w, fr, maxFreq, minNorm))
	}
	return subs, nil
}

// compileMatch analyzes the query text and combines the resulting terms with the
// query's operator (OR by default).
func (se *Searcher) compileMatch(n *query.MatchQuery) (scorer, error) {
	terms, err := se.analyzeTerms(n.Field, n.Text)
	if err != nil {
		return nil, err
	}
	switch len(terms) {
	case 0:
		return emptyScorer{}, nil
	case 1:
		return se.compileTerm(n.Field, terms[0], n.Boost())
	}
	subs := make([]scorer, 0, len(terms))
	for _, t := range terms {
		ts, err := se.compileTerm(n.Field, t, n.Boost())
		if err != nil {
			return nil, err
		}
		subs = append(subs, ts)
	}
	if n.Operator == query.Must {
		return newAndScorer(subs), nil
	}
	return newWandScorer(subs), nil
}

// compilePhrase analyzes the text and requires the terms to appear in order
// within slop, per segment, chained across segments.
func (se *Searcher) compilePhrase(n *query.MatchPhraseQuery) (scorer, error) {
	terms, err := se.analyzeTerms(n.Field, n.Text)
	if err != nil {
		return nil, err
	}
	switch len(terms) {
	case 0:
		return emptyScorer{}, nil
	case 1:
		return se.compileTerm(n.Field, terms[0], n.Boost())
	}
	weights := make([]*score.Weight, len(terms))
	for i, t := range terms {
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
		tss := make([]*termScorer, len(terms))
		ok := true
		for i, t := range terms {
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
		segScorers = append(segScorers, newSegPhraseScorer(tss, n.Slop))
	}
	if len(segScorers) == 0 {
		return emptyScorer{}, nil
	}
	if len(segScorers) == 1 {
		return segScorers[0], nil
	}
	return newChainScorer(segScorers), nil
}

// compilePrefix matches any document whose field has a term beginning with prefix,
// scoring each match a constant equal to the boost.
func (se *Searcher) compilePrefix(field, prefix string, boost float32) (scorer, error) {
	var children []scorer
	for _, seg := range se.segs {
		fr, err := se.fieldReader(seg, field)
		if err != nil {
			return nil, err
		}
		if fr == nil {
			continue
		}
		terms, err := fr.PrefixTerms(prefix)
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

// compileRange matches any document whose field value falls in the range, scoring
// each match a constant equal to the boost. Keyword and text fields range
// lexicographically; numeric, date, and boolean fields use the order-preserving
// term encoding.
func (se *Searcher) compileRange(n *query.RangeQuery) (scorer, error) {
	lo, hi, err := se.rangeBounds(n)
	if err != nil {
		return nil, err
	}
	var children []scorer
	for _, seg := range se.segs {
		fr, err := se.fieldReader(seg, n.Field)
		if err != nil {
			return nil, err
		}
		if fr == nil {
			continue
		}
		terms, err := fr.RangeTerms(lo, hi)
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
	return constantUnion(children, n.Boost()), nil
}

// rangeBounds converts a range query's textual bounds into the half-open
// [lo, hi) byte bounds RangeScan expects, encoding numeric fields and adjusting
// for inclusivity by appending a zero byte (the next term) where needed.
func (se *Searcher) rangeBounds(n *query.RangeQuery) (lo, hi []byte, err error) {
	ft := schema.TypeKeyword
	if se.schema != nil {
		if f, ok := se.schema.Lookup(n.Field); ok {
			ft = f.Type
		}
	}
	encode := func(s string) (string, bool, error) {
		switch ft {
		case schema.TypeText, schema.TypeKeyword, schema.TypeStored:
			if s == "" {
				return "", false, nil
			}
			return s, true, nil
		default:
			return schema.ParseNumericBound(ft, s)
		}
	}
	loTerm, hasLo, err := encode(n.Lower)
	if err != nil {
		return nil, nil, err
	}
	hiTerm, hasHi, err := encode(n.Upper)
	if err != nil {
		return nil, nil, err
	}
	if hasLo {
		lo = []byte(loTerm)
		if !n.IncludeLower {
			lo = append(lo, 0) // exclude lo: start strictly after it
		}
	}
	if hasHi {
		hi = []byte(hiTerm)
		if n.IncludeUpper {
			hi = append(hi, 0) // include hi: stop just past it
		}
	}
	return lo, hi, nil
}

// compileBool assembles required, should, and prohibited scorers into a bool
// scorer.
func (se *Searcher) compileBool(n *query.BoolQuery) (scorer, error) {
	var required []scorer
	var shoulds []scorer
	var prohibited []scorer
	for _, cl := range n.Clauses {
		sub, err := se.compile(cl.Query)
		if err != nil {
			return nil, err
		}
		switch cl.Occur {
		case query.Must:
			required = append(required, sub)
		case query.Filter:
			required = append(required, &zeroScorer{inner: sub})
		case query.Should:
			shoulds = append(shoulds, sub)
		case query.MustNot:
			prohibited = append(prohibited, sub)
		}
	}

	b := &boolScorer{prohibited: prohibited, minShould: n.EffectiveMinShould()}
	if len(required) > 0 {
		b.required = newAndScorer(required)
	}
	if len(shoulds) > 0 {
		min := 1
		if len(required) > 0 {
			min = 1 // scoreAt path: orScorer min is unused, bool enforces minShould
		}
		b.shoulds = newOrScorer(shoulds, min)
	}
	// A bool with no positive clauses (only must_not, or nothing) matches no
	// document: there is no candidate stream to drive.
	if b.required == nil && b.shoulds == nil {
		return emptyScorer{}, nil
	}
	return applyBoost(b, n.Boost()), nil
}

// constantUnion wraps a union of matchers so each matching document scores a
// constant value, regardless of how many terms it matched.
func constantUnion(children []scorer, value float32) scorer {
	if len(children) == 0 {
		return emptyScorer{}
	}
	return &constantScorer{inner: newOrScorer(children, 1), value: value}
}

// applyBoost multiplies a scorer's score by a constant when the boost differs
// from 1.
func applyBoost(s scorer, boost float32) scorer {
	if boost == 1 {
		return s
	}
	return &boostScorer{inner: s, factor: boost}
}

// analyzeTerms runs the field's query analyzer over text and returns its terms.
func (se *Searcher) analyzeTerms(field, text string) ([]string, error) {
	if se.analyzer == nil {
		return []string{text}, nil
	}
	a, err := se.analyzer(field)
	if err != nil {
		return nil, err
	}
	toks := a.Analyze(text)
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		out = append(out, t.Term)
	}
	return out, nil
}
