package crashtest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tamnd/search"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/segment"
)

// liveSegments returns the number of live segments in the index, or -1 on error.
func liveSegments(db *search.DB) int {
	n := -1
	_ = db.View(func(tx *search.Txn) error {
		set, err := segment.LoadSet(tx.Catalog())
		if err != nil {
			return err
		}
		n = set.Len()
		return nil
	})
	return n
}

// engineSchema is a realistic schema that drives every segment write path at
// once: an analyzed text field (term dictionary + postings), a numeric field
// (the BKD doc-values column), and a dense_vector field (the HNSW graph extent).
// A crash campaign over an index of this schema therefore arms a fault at every
// boundary of every one of those writers, not just the catalog B+tree.
func engineSchema(dims int) (*schema.Schema, error) {
	s := schema.New()
	if err := s.Add(schema.NewField("body", schema.TypeText)); err != nil {
		return nil, err
	}
	if err := s.Add(schema.NewField("price", schema.TypeLong)); err != nil {
		return nil, err
	}
	vf := schema.NewField("embed", schema.TypeDenseVector)
	vf.Opts.Dims = dims
	vf.Opts.Metric = "l2"
	if err := s.Add(vf); err != nil {
		return nil, err
	}
	return s, nil
}

// engineDoc builds one document carrying all three field kinds. The vector is a
// cheap deterministic function of i so the corpus needs no PRNG state.
func engineDoc(id string, i, dims int) map[string]any {
	embed := make([]any, dims)
	for j := range embed {
		embed[j] = float64((i*7+j*13)%101) / 101.0
	}
	return map[string]any{
		"_id":   id,
		"body":  fmt.Sprintf("w%d w%d term%d", i%7, i%13, i%5),
		"price": int64(i % 1000),
		"embed": embed,
	}
}

// indexWorkload commits base documents fault-free, then runs one Index of n new
// documents under fault injection. Because Index is a single write transaction
// that flushes one segment, a crash anywhere must leave either the baseline alone
// or the whole new segment, never a half-written one. Every new document carries
// text, numeric, and vector fields, so the campaign covers the postings,
// doc-values, and HNSW write paths in addition to the catalog and docstore.
func indexWorkload(pageSize uint32, dims, base, n int) Workload {
	const dims0 = 8
	if dims == 0 {
		dims = dims0
	}
	sentinel := func() string { return fmt.Sprintf("new-%06d", 0) }

	return Workload{
		PageSize: pageSize,
		Seed: func(db *search.DB) error {
			s, err := engineSchema(dims)
			if err != nil {
				return err
			}
			if err := db.PutSchema(s); err != nil {
				return err
			}
			docs := make([]map[string]any, base)
			for i := range docs {
				docs[i] = engineDoc(fmt.Sprintf("base-%06d", i), i, dims)
			}
			_, err = db.Index(docs)
			return err
		},
		Mutate: func(db *search.DB) error {
			docs := make([]map[string]any, n)
			for i := range docs {
				docs[i] = engineDoc(fmt.Sprintf("new-%06d", i), base+i, dims)
			}
			_, err := db.Index(docs)
			return err
		},
		Post: func(db *search.DB) (bool, error) {
			_, err := db.GetByExternalID(sentinel())
			if errors.Is(err, search.ErrNoDoc) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			return true, nil
		},
		Verify: func(db *search.DB, post bool) error {
			// The whole baseline survives every recovered state.
			for i := range base {
				if _, err := db.GetByExternalID(fmt.Sprintf("base-%06d", i)); err != nil {
					return fmt.Errorf("baseline base-%06d lost: %w", i, err)
				}
			}
			// Every new document is present iff the workload took effect.
			for i := range n {
				_, err := db.GetByExternalID(fmt.Sprintf("new-%06d", i))
				switch {
				case post && err != nil:
					return fmt.Errorf("post state but new-%06d missing: %w", i, err)
				case !post && !errors.Is(err, search.ErrNoDoc):
					return fmt.Errorf("baseline state but new-%06d present (err=%v)", i, err)
				}
			}
			// A query must run cleanly against whichever segment set survived.
			_, err := db.Search(query.Term("body", "w1"), 10)
			return err
		},
	}
}

// compactWorkload seeds several segments fault-free, then runs CompactAll under
// fault injection. CompactAll is a single transaction that merges the inputs into
// one new segment, removes the originals, and recomputes stats, so a crash must
// leave either the original segment set or the single merged one. Either way the
// full document set and a query's results must be unchanged: this is the
// compaction-atomicity invariant exercised against the page-copy and catalog-swap
// write paths.
func compactWorkload(pageSize uint32, dims, segs, perSeg int) Workload {
	if dims == 0 {
		dims = 8
	}
	total := segs * perSeg
	wantHits := func(db *search.DB) (int, error) {
		hits, err := db.Search(query.Term("body", "w1"), total+10)
		return len(hits), err
	}
	var baselineHits int

	return Workload{
		PageSize: pageSize,
		Seed: func(db *search.DB) error {
			s, err := engineSchema(dims)
			if err != nil {
				return err
			}
			if err := db.PutSchema(s); err != nil {
				return err
			}
			for g := range segs {
				docs := make([]map[string]any, perSeg)
				for i := range docs {
					id := g*perSeg + i
					docs[i] = engineDoc(fmt.Sprintf("d-%06d", id), id, dims)
				}
				if _, err := db.Index(docs); err != nil {
					return err
				}
			}
			baselineHits, err = wantHits(db)
			return err
		},
		Mutate: func(db *search.DB) error {
			_, err := db.CompactAll()
			return err
		},
		Post: func(db *search.DB) (bool, error) {
			// post == the merge committed, observed as a single live segment.
			n := liveSegments(db)
			if n < 0 {
				return false, fmt.Errorf("could not read segment count")
			}
			return n == 1, nil
		},
		Verify: func(db *search.DB, post bool) error {
			// Every original document is present whether or not the merge landed.
			for id := range total {
				if _, err := db.GetByExternalID(fmt.Sprintf("d-%06d", id)); err != nil {
					return fmt.Errorf("doc d-%06d lost (post=%v): %w", id, post, err)
				}
			}
			// The query result count is identical before and after compaction.
			got, err := wantHits(db)
			if err != nil {
				return err
			}
			if got != baselineHits {
				return fmt.Errorf("hit count changed: %d != baseline %d (post=%v)", got, baselineHits, post)
			}
			return nil
		},
	}
}

func TestCrashRecovery_IndexSegmentFlush(t *testing.T) {
	// A 4 KiB page makes the flushed segment span many pages, so the campaign
	// arms a fault at every extent-write and sync boundary the segment writer
	// (term dictionary, postings, doc-values, HNSW graph, docstore) reaches.
	rep, err := Run(indexWorkload(4096, 8, 40, 120))
	if err != nil {
		t.Fatal(err)
	}
	if rep.FaultPoints < 20 {
		t.Fatalf("only %d fault points, want a real segment-flush campaign", rep.FaultPoints)
	}
	reportFailures(t, rep)
	t.Logf("index segment-flush campaign: %d fault points, %d cycles", rep.FaultPoints, rep.Cycles)
}

func TestCrashRecovery_Compaction(t *testing.T) {
	// Compaction merges every input into one segment, so each merged region must
	// fit a page: the large page size is required, which keeps the boundary count
	// modest but still covers the page-copy and catalog-swap write paths.
	rep, err := Run(compactWorkload(65536, 8, 6, 30))
	if err != nil {
		t.Fatal(err)
	}
	if rep.FaultPoints < 5 {
		t.Fatalf("only %d fault points, want a real compaction campaign", rep.FaultPoints)
	}
	reportFailures(t, rep)
	t.Logf("compaction campaign: %d fault points, %d cycles", rep.FaultPoints, rep.Cycles)
}

// TestCrashRecovery_Campaign10k is the headline durability gate: at least 10 000
// crash-and-recover cycles across diverse write paths with zero atomicity
// violations. Each campaign arms a crash, a tear, and an fsync failure at every
// write boundary its workload reaches; the loop keeps launching fresh campaigns,
// varying the workload each round so the corpus and its fault distribution differ,
// until the cumulative cycle count clears 10 000.
func TestCrashRecovery_Campaign10k(t *testing.T) {
	if testing.Short() {
		t.Skip("10k-cycle campaign is skipped under -short")
	}
	if raceEnabled {
		t.Skip("10k-cycle campaign is reserved for the non-race extended profile")
	}
	const target = 10000
	total, round := 0, 0
	for total < target {
		// Three interleaved workload families, each parameterized by the round so
		// successive campaigns exercise a different corpus and write pattern. Every
		// size is bounded so a merged region never outgrows its page and the
		// per-cycle replay stays cheap; the variety, not the size, is what widens
		// coverage across rounds.
		family := round % 3
		var name string
		var wl Workload
		switch family {
		case 0:
			name = "catalog"
			wl = catalogWorkload(4096, 50+round%60, 100+round%160)
		case 1:
			name = "index"
			wl = indexWorkload(4096, 8, 24+round%24, 100+round%40)
		default:
			name = "compaction"
			wl = compactWorkload(65536, 8, 4+round%3, 24+round%6)
		}
		rep, err := Run(wl)
		if err != nil {
			t.Fatalf("round %d (%s): %v", round, name, err)
		}
		reportFailures(t, rep)
		total += rep.Cycles
		round++
	}
	t.Logf("combined durability campaign: %d crash-and-recover cycles over %d campaigns, 0 violations", total, round)
}
