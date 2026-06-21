package search

import (
	"fmt"

	"github.com/tamnd/search/analysis"
	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/highlight"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
)

// applyHighlights fills Hit.Highlights for every hit, for each requested field
// (spec 2063 doc 11 §8). It re-analyzes the stored field value with the field's
// analyzer and wraps the tokens whose terms appear in the query. A field with no
// stored value or no matching term gets no entry.
func (db *DB) applyHighlights(c *catalog.Catalog, s *schema.Schema, q query.Query, hits []Hit, fields map[string]highlight.Options) error {
	if len(fields) == 0 || len(hits) == 0 {
		return nil
	}
	for field, opts := range fields {
		a, err := db.fieldAnalyzerFor(c, s, field)
		if err != nil {
			return err
		}
		terms := queryFieldTerms(q, field, a)
		if len(terms) == 0 {
			continue
		}
		h := highlight.New(a, opts)
		for i := range hits {
			text, ok := stringField(hits[i].Document, field)
			if !ok {
				continue
			}
			frags := h.Fragments(text, terms)
			if len(frags) == 0 {
				continue
			}
			if hits[i].Highlights == nil {
				hits[i].Highlights = make(map[string][]string)
			}
			hits[i].Highlights[field] = frags
		}
	}
	return nil
}

// fieldAnalyzerFor resolves the query-time analyzer for a field.
func (db *DB) fieldAnalyzerFor(c *catalog.Catalog, s *schema.Schema, field string) (*analysis.Analyzer, error) {
	name := "standard"
	if f, ok := s.Lookup(field); ok {
		name = fieldAnalyzerName(f)
	}
	return resolveAnalyzer(c, name)
}

// stringField returns a document field as a string when it is one.
func stringField(doc map[string]any, field string) (string, bool) {
	v, ok := doc[field]
	if !ok || v == nil {
		return "", false
	}
	if s, ok := v.(string); ok {
		return s, true
	}
	return fmt.Sprintf("%v", v), true
}

// queryFieldTerms collects the analyzed terms a query contributes for one field,
// the set a highlighter wraps. Match and phrase text is run through the analyzer so
// it matches the indexed tokens; term and span literals are taken as-is. Query
// types that expand against the dictionary (prefix, wildcard, fuzzy, regexp) are
// skipped, since their matched terms are not known without the index.
func queryFieldTerms(q query.Query, field string, a *analysis.Analyzer) map[string]struct{} {
	terms := make(map[string]struct{})
	collectFieldTerms(q, field, a, terms)
	return terms
}

func collectFieldTerms(q query.Query, field string, a *analysis.Analyzer, out map[string]struct{}) {
	switch n := q.(type) {
	case *query.TermQuery:
		if n.Field == field {
			out[n.Term] = struct{}{}
		}
	case *query.MatchQuery:
		if n.Field == field {
			addAnalyzed(a, n.Text, out)
		}
	case *query.MatchPhraseQuery:
		if n.Field == field {
			addAnalyzed(a, n.Text, out)
		}
	case *query.SpanNearQuery:
		if n.Field == field {
			for _, t := range n.Terms {
				out[t] = struct{}{}
			}
		}
	case *query.BoolQuery:
		for _, cl := range n.Clauses {
			if cl.Occur == query.MustNot {
				continue
			}
			collectFieldTerms(cl.Query, field, a, out)
		}
	case *query.FunctionScoreQuery:
		collectFieldTerms(n.Query, field, a, out)
	case *query.RescoreQuery:
		collectFieldTerms(n.Query, field, a, out)
		collectFieldTerms(n.Rescore, field, a, out)
	}
}

// addAnalyzed analyzes text and adds each resulting term to the set.
func addAnalyzed(a *analysis.Analyzer, text string, out map[string]struct{}) {
	if a == nil {
		out[text] = struct{}{}
		return
	}
	for _, tok := range a.Analyze(text) {
		out[tok.Term] = struct{}{}
	}
}
