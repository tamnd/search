package exec

import "github.com/tamnd/search/query"

// Scan runs q and calls visit for every matching, non-deleted document in
// ascending doc-id order together with its score. Unlike Search it performs no
// top-k pruning: the whole matching set is delivered, which is what a single-pass
// aggregation or a sort over a non-score key needs (spec 2063 doc 14 §6, §7.6).
// visit returning an error stops the scan and propagates the error.
func (se *Searcher) Scan(q query.Query, visit func(docID uint32, score float32) error) error {
	q = q.Rewrite()
	if err := q.Validate(schemaView{se.schema}); err != nil {
		return err
	}
	sc, err := se.compile(q)
	if err != nil {
		return err
	}
	sc = newLiveFilter(sc, se.dead)
	d, err := sc.next()
	if err != nil {
		return err
	}
	for d != noMore {
		if err := visit(d, sc.score()); err != nil {
			return err
		}
		d, err = sc.next()
		if err != nil {
			return err
		}
	}
	return nil
}
