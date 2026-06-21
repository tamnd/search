package search

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/vfs"
)

// randomQueries builds a deterministic set of varied queries over the scale
// vocabulary: term, match (multi-term OR), prefix, and boolean combinations. The
// same seed always yields the same queries so a failure reproduces.
func randomQueries(rng *determ.SplitMix64, vocab, n int) []query.Query {
	qs := make([]query.Query, 0, n)
	for len(qs) < n {
		switch rng.Intn(4) {
		case 0:
			qs = append(qs, query.Term("body", fmt.Sprintf("w%d", rng.Intn(vocab))))
		case 1:
			a, b := rng.Intn(vocab), rng.Intn(vocab)
			qs = append(qs, query.Match("body", fmt.Sprintf("w%d w%d", a, b)))
		case 2:
			// Single-digit prefix so it actually matches a band of terms.
			qs = append(qs, query.Prefix("body", fmt.Sprintf("w%d", rng.Intn(10))))
		case 3:
			bq := query.Bool()
			bq.Add(query.Must, query.Term("body", fmt.Sprintf("w%d", rng.Intn(vocab))))
			bq.Add(query.Should, query.Term("body", fmt.Sprintf("w%d", rng.Intn(vocab))))
			qs = append(qs, bq)
		}
	}
	return qs
}

// TestCompactionPreservesAllResults is the strong form of the S5 invariant: a
// randomized corpus spread across many segments, queried with a large random
// query set, must return byte-for-byte identical ranked results before and after
// a full compaction. The corpus and queries are seeded so any failure replays.
func TestCompactionPreservesAllResults(t *testing.T) {
	// The corpus is large enough to span many segments but stays under the
	// single-segment region size that one page can hold once everything merges
	// into one segment; a term whose postings outgrow a page is an S5 chunking
	// concern outside this property's scope.
	n, segTarget, nq := 3000, 20, 400
	if testing.Short() {
		n, segTarget, nq = 1200, 8, 120
	}
	db, err := Open("compactprop.sx", testOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db)
	if err := db.PutSchema(scaleSchema(t)); err != nil {
		t.Fatal(err)
	}

	const vocab = 60
	// Index in randomly sized batches so the segment count varies but stays near
	// the target. Each doc's body is a deterministic bag of words.
	rng := determ.NewPRNG(0xC0FFEE)
	batch := n / segTarget
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
		nw := 3 + rng.Intn(6)
		for j := 0; j < nw; j++ {
			body += fmt.Sprintf("w%d ", rng.Intn(vocab))
		}
		docs = append(docs, map[string]any{"_id": fmt.Sprintf("d%d", i), "body": body})
		// Vary the batch boundary by +/- 25% so segments differ in size.
		if len(docs) >= batch-batch/4+rng.Intn(batch/2+1) {
			flush()
		}
	}
	flush()

	if got := segmentCount(t, db); got < 3 {
		t.Fatalf("expected several segments before compaction, got %d", got)
	}

	queries := randomQueries(rng, vocab, nq)
	before := make([][]Hit, len(queries))
	for i, q := range queries {
		before[i], err = db.Search(q, 50)
		if err != nil {
			t.Fatalf("query %d before: %v", i, err)
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
			t.Fatalf("query %d after: %v", i, err)
		}
		assertSameHits(t, before[i], after)
	}
}

// TestMVCCIsolation runs many concurrent readers against an index while several
// writers commit new documents. Each reader opens one snapshot, records the
// result of a query, then re-runs the same query against that same snapshot many
// times; the result must never change, no matter how many commits land meanwhile.
// This is the snapshot-isolation contract (doc 18 §3). Run with -race.
func TestMVCCIsolation(t *testing.T) {
	db, err := Open("mvccprop.sx", testOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db)
	if err := db.PutSchema(scaleSchema(t)); err != nil {
		t.Fatal(err)
	}

	// Seed a baseline so readers have something to observe immediately.
	const vocab = 20
	seed := make([]map[string]any, 0, 500)
	for i := 1; i <= 500; i++ {
		seed = append(seed, map[string]any{"_id": fmt.Sprintf("s%d", i), "body": fmt.Sprintf("w%d base", i%vocab)})
	}
	if _, err := db.Index(seed); err != nil {
		t.Fatal(err)
	}

	const readers, writers, writesEach = 10, 5, 20
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writers: each runs a sequence of independent commits. The single-writer
	// lock serializes them; the readers must be unaffected by every commit.
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range writesEach {
				doc := map[string]any{
					"_id":  fmt.Sprintf("w%d-%d", w, i),
					"body": fmt.Sprintf("w%d added", i%vocab),
				}
				if _, err := db.Index([]map[string]any{doc}); err != nil {
					t.Errorf("writer %d: %v", w, err)
					return
				}
			}
		}(w)
	}

	// Readers: open one snapshot, fix a query result, then keep re-checking that
	// the same snapshot returns exactly that result while writers commit.
	q := query.Term("body", "base")
	for r := range readers {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			tx, err := db.Begin(false)
			if err != nil {
				t.Errorf("reader %d begin: %v", r, err)
				return
			}
			defer func() { _ = tx.Rollback() }()

			want, err := db.searchTxn(tx, q, 1000)
			if err != nil {
				t.Errorf("reader %d first search: %v", r, err)
				return
			}
			wantIDs := hitIDs(want)
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := db.searchTxn(tx, q, 1000)
				if err != nil {
					t.Errorf("reader %d search: %v", r, err)
					return
				}
				if !equalIDs(wantIDs, hitIDs(got)) {
					t.Errorf("reader %d: snapshot changed under it (%d -> %d hits)", r, len(wantIDs), len(got))
					return
				}
			}
		}(r)
	}

	// Let writers finish, then signal readers to stop and wait.
	go func() {
		// Drain writers by waiting on a separate group is awkward here; instead we
		// rely on writesEach being small and close stop after a fixed settle.
	}()
	// Wait for writers by polling the committed count, then stop readers.
	wantTotal := 500 + writers*writesEach
	for {
		n := 0
		if err := db.View(func(tx *Txn) error {
			hits, err := db.searchTxn(tx, query.MatchAll(), wantTotal+10)
			n = len(hits)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if n >= wantTotal {
			break
		}
	}
	close(stop)
	wg.Wait()

	// After everything, a fresh snapshot sees every committed document.
	all, err := db.Search(query.MatchAll(), wantTotal+10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != wantTotal {
		t.Fatalf("final visible docs = %d, want %d", len(all), wantTotal)
	}
}

// TestRecoveryInvariant checks that reopening an index always lands on the last
// fully committed state and never on a partial one. It drives a random sequence
// of committed index batches interleaved with rolled-back transactions, reopening
// the file at every step, and asserts the visible document set equals exactly the
// committed documents. The durability substrate here is the double-buffered meta
// page; the WAL-crash variant of this invariant is exercised by package
// crashtest.
func TestRecoveryInvariant(t *testing.T) {
	steps := 40
	if testing.Short() {
		steps = 12
	}
	// One persistent in-memory VFS stands in for the disk: closing a DB and
	// reopening the same path reads the file back exactly as it was left. A fresh
	// testOpts() would mint a new empty VFS each call, so pin one here.
	opt := testOpts()
	opt.VFS = vfs.NewMem()

	db, err := Open("recover.sx", opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutSchema(scaleSchema(t)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	rng := determ.NewPRNG(0xBEEF1234)
	committed := map[string]bool{}
	next := 0

	for s := range steps {
		db, err := Open("recover.sx", opt)
		if err != nil {
			t.Fatalf("step %d reopen: %v", s, err)
		}

		// Before mutating, the visible set must equal the committed set: recovery
		// produced exactly the last committed state.
		assertVisibleEquals(t, db, committed, s)

		commit := rng.Intn(3) != 0 // about 2/3 commit, 1/3 roll back
		batch := 1 + rng.Intn(20)
		ids := make([]string, batch)
		docs := make([]map[string]any, batch)
		for i := range batch {
			id := fmt.Sprintf("d%d", next)
			next++
			ids[i] = id
			docs[i] = map[string]any{"_id": id, "body": fmt.Sprintf("w%d body", i%20)}
		}

		if commit {
			if _, err := db.Index(docs); err != nil {
				t.Fatalf("step %d index: %v", s, err)
			}
			for _, id := range ids {
				committed[id] = true
			}
		} else {
			// A write transaction that indexes then returns an error rolls back,
			// leaving nothing on disk.
			errCancel := errors.New("cancel")
			if err := db.Update(func(tx *Txn) error {
				if _, err := db.indexTxn(tx, docs); err != nil {
					return err
				}
				return errCancel
			}); err != nil && !errors.Is(err, errCancel) {
				t.Fatalf("step %d rollback update: %v", s, err)
			}
		}

		if err := db.Close(); err != nil {
			t.Fatalf("step %d close: %v", s, err)
		}
	}

	// One final reopen confirms the persisted state matches the committed set.
	db, err = Open("recover.sx", opt)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db)
	assertVisibleEquals(t, db, committed, steps)
}

// assertVisibleEquals checks the live document set equals want exactly.
func assertVisibleEquals(t *testing.T, db *DB, want map[string]bool, step int) {
	t.Helper()
	hits, err := db.Search(query.MatchAll(), len(want)+50)
	if err != nil {
		t.Fatalf("step %d match-all: %v", step, err)
	}
	if len(hits) != len(want) {
		t.Fatalf("step %d: %d visible docs, want %d", step, len(hits), len(want))
	}
	for _, h := range hits {
		if !want[h.ExternalID] {
			t.Fatalf("step %d: doc %q visible but not committed", step, h.ExternalID)
		}
	}
}

// hitIDs extracts the external ids from a hit list.
func hitIDs(hits []Hit) map[string]bool {
	m := make(map[string]bool, len(hits))
	for _, h := range hits {
		m[h.ExternalID] = true
	}
	return m
}

// equalIDs reports whether two id sets are identical.
func equalIDs(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
