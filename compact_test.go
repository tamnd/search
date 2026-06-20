package search

import (
	"fmt"
	"testing"

	"github.com/tamnd/search/query"
	"github.com/tamnd/search/segment"
)

// indexInBatches indexes n documents in batches of the given size so the index
// ends up with several segments to compact.
func indexInBatches(t testing.TB, db *DB, n, batch, vocab int) {
	docs := make([]map[string]any, 0, batch)
	flush := func() {
		if len(docs) == 0 {
			return
		}
		if _, err := db.Index(docs); err != nil {
			t.Fatal(err)
		}
		docs = docs[:0]
	}
	for i := 1; i <= n; i++ {
		body := ""
		for j := range vocab {
			if i%(j+1) == 0 {
				body += fmt.Sprintf("w%d ", j)
			}
		}
		docs = append(docs, map[string]any{"_id": fmt.Sprintf("d%d", i), "body": body})
		if len(docs) == batch {
			flush()
		}
	}
	flush()
}

// segmentCount returns how many live segments the index holds.
func segmentCount(t testing.TB, db *DB) int {
	var n int
	if err := db.View(func(tx *Txn) error {
		set, err := segment.LoadSet(tx.Catalog())
		if err != nil {
			return err
		}
		n = set.Len()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCompactionPreservesResults(t *testing.T) {
	db, err := Open("preserve.sx", testOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db)
	if err := db.PutSchema(scaleSchema(t)); err != nil {
		t.Fatal(err)
	}
	const n, vocab = 2000, 30
	indexInBatches(t, db, n, 250, vocab)
	if segmentCount(t, db) < 3 {
		t.Fatalf("expected several segments before compaction, got %d", segmentCount(t, db))
	}

	queries := []query.Query{
		query.Term("body", "w3"),
		query.Term("body", "w7"),
		query.Match("body", "w1 w2 w5"),
		query.Prefix("body", "w1"),
	}
	before := make([][]Hit, len(queries))
	for i, q := range queries {
		before[i], err = db.Search(q, 50)
		if err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.CompactAll(); err != nil {
		t.Fatal(err)
	}
	if got := segmentCount(t, db); got != 1 {
		t.Fatalf("after CompactAll: %d segments, want 1", got)
	}

	for i, q := range queries {
		after, err := db.Search(q, 50)
		if err != nil {
			t.Fatal(err)
		}
		assertSameHits(t, before[i], after)
	}
}

// assertSameHits checks two result lists hold the same documents and scores.
func assertSameHits(t testing.TB, want, got []Hit) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("hit count %d != %d", len(got), len(want))
	}
	for i := range want {
		if want[i].ExternalID != got[i].ExternalID {
			t.Fatalf("rank %d: %q != %q", i, got[i].ExternalID, want[i].ExternalID)
		}
		if d := want[i].Score - got[i].Score; d > 1e-4 || d < -1e-4 {
			t.Fatalf("rank %d %q: score %v != %v", i, want[i].ExternalID, got[i].Score, want[i].Score)
		}
	}
}

func TestCompactionReapsDeletes(t *testing.T) {
	db, err := Open("reap.sx", testOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db)
	if err := db.PutSchema(scaleSchema(t)); err != nil {
		t.Fatal(err)
	}
	const n, vocab = 1000, 20
	indexInBatches(t, db, n, 200, vocab)

	// Delete every third document.
	deleted := map[string]bool{}
	for i := 1; i <= n; i += 3 {
		id := fmt.Sprintf("d%d", i)
		ok, err := db.Delete(id)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("delete %s reported missing", id)
		}
		deleted[id] = true
	}

	if _, err := db.CompactAll(); err != nil {
		t.Fatal(err)
	}

	// No deleted document may appear in any result.
	hits, err := db.Search(query.Prefix("body", "w"), n+1)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if deleted[h.ExternalID] {
			t.Fatalf("deleted doc %s survived compaction", h.ExternalID)
		}
	}

	// The merged segment's document count must reflect the deletions.
	if err := db.View(func(tx *Txn) error {
		set, err := segment.LoadSet(tx.Catalog())
		if err != nil {
			return err
		}
		if set.Len() != 1 {
			t.Fatalf("want 1 segment, got %d", set.Len())
		}
		want := uint32(n - len(deleted))
		if got := set.Segments()[0].Meta().DocCount; got != want {
			t.Fatalf("segment DocCount = %d, want %d", got, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTieredCompactionReducesSegments(t *testing.T) {
	db, err := Open("tiered.sx", testOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db)
	if err := db.PutSchema(scaleSchema(t)); err != nil {
		t.Fatal(err)
	}
	// Ten equal-size batches land ten same-tier segments, over the default
	// threshold of four, so a compaction round must fire.
	for b := range 10 {
		docs := make([]map[string]any, 0, 100)
		for i := range 100 {
			id := b*100 + i + 1
			docs = append(docs, map[string]any{"_id": fmt.Sprintf("d%d", id), "body": "alpha beta gamma"})
		}
		if _, err := db.Index(docs); err != nil {
			t.Fatal(err)
		}
	}
	if got := segmentCount(t, db); got != 10 {
		t.Fatalf("before compaction: %d segments, want 10", got)
	}
	merged, err := db.Compact()
	if err != nil {
		t.Fatal(err)
	}
	if merged != 4 {
		t.Fatalf("compaction merged %d segments, want 4", merged)
	}
	if got := segmentCount(t, db); got != 7 {
		t.Fatalf("after one round: %d segments, want 7", got)
	}
}
