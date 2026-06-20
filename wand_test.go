package search

import (
	"testing"

	"github.com/tamnd/search/query"
)

// TestWANDMatchesExhaustive checks that the WAND-pruned disjunction returns the
// same top-k as an unpruned scan. A query asked for more hits than it can match
// never fills the heap, so it prunes nothing and yields the exhaustive ranking;
// every smaller k must equal that ranking's prefix, doc-id and score alike.
func TestWANDMatchesExhaustive(t *testing.T) {
	const n, vocab = 4000, 40
	db := buildScaleIndex(t, n, vocab)
	defer mustClose(t, db)
	if segmentCount(t, db) < 2 {
		t.Fatalf("want several segments to exercise the chain, got %d", segmentCount(t, db))
	}

	queries := []query.Query{
		query.Match("body", "w1 w3 w7"),
		query.Match("body", "w2 w5 w11 w19"),
		query.Match("body", "w0 w1"),
	}
	for qi, q := range queries {
		full, err := db.Search(q, n+1)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []int{1, 2, 5, 25, 100} {
			if k > len(full) {
				continue
			}
			got, err := db.Search(q, k)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != k {
				t.Fatalf("query %d k=%d: returned %d hits", qi, k, len(got))
			}
			for i := range got {
				if got[i].ExternalID != full[i].ExternalID {
					t.Fatalf("query %d k=%d rank %d: %q != %q", qi, k, i, got[i].ExternalID, full[i].ExternalID)
				}
				if d := got[i].Score - full[i].Score; d > 1e-5 || d < -1e-5 {
					t.Fatalf("query %d k=%d rank %d %q: score %v != %v", qi, k, i, got[i].ExternalID, got[i].Score, full[i].Score)
				}
			}
		}
	}
}
