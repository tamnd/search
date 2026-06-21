package bench

import (
	"testing"
	"time"

	"github.com/tamnd/search"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
)

// Per-query allocation ceilings on a fixed single-segment corpus. These are the
// current measured baselines (term ~2432, and4 ~2541, knn ~8098) plus headroom,
// not the spec's aspirational zero-allocation targets from doc 19 §9.3. Those
// targets need the postings-enum pool and stack-allocated top-k heap, which are
// a tracked future optimization outside S9 hardening. The gate's job here is to
// catch a regression: a stray per-document or per-call allocation pushes the
// count well past the ceiling and fails the build.
const (
	allocCeilTerm = 3000
	allocCeilAND4 = 3200
	allocCeilKNN  = 9600
)

// singleSegment builds a small corpus and compacts it to one segment so the
// allocation count is stable run to run, independent of how the indexer happened
// to flush memtables.
func singleSegment(tb testing.TB, dims int) *search.DB {
	tb.Helper()
	db, err := BuildCorpus(CorpusOptions{Docs: 1000, Vocab: 32, Dims: dims, Quant: schema.QuantNone})
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := db.CompactAll(); err != nil {
		tb.Fatal(err)
	}
	return db
}

func TestAllocCeilingTermQuery(t *testing.T) {
	db := singleSegment(t, 0)
	defer mustClose(t, db)
	q := query.Term("body", "w7")
	got := testing.AllocsPerRun(50, func() { _, _ = db.Search(q, 10) })
	if got > allocCeilTerm {
		t.Fatalf("term query allocs/op = %.0f, ceiling %d (a hot-path allocation regressed)", got, allocCeilTerm)
	}
}

func TestAllocCeilingANDQuery(t *testing.T) {
	db := singleSegment(t, 0)
	defer mustClose(t, db)
	q := query.Bool().
		MustClause(query.Term("body", "w1")).
		MustClause(query.Term("body", "w2")).
		MustClause(query.Term("body", "w3")).
		MustClause(query.Term("body", "w5"))
	got := testing.AllocsPerRun(50, func() { _, _ = db.Search(q, 10) })
	if got > allocCeilAND4 {
		t.Fatalf("AND4 query allocs/op = %.0f, ceiling %d", got, allocCeilAND4)
	}
}

func TestAllocCeilingKNN(t *testing.T) {
	db := singleSegment(t, 64)
	defer mustClose(t, db)
	qs := queryVectors(4, 64)
	got := testing.AllocsPerRun(50, func() { _, _ = db.Search(query.KNN("embed", qs[0], 10), 10) })
	if got > allocCeilKNN {
		t.Fatalf("kNN query allocs/op = %.0f, ceiling %d", got, allocCeilKNN)
	}
}

func TestCompareFlagsRegression(t *testing.T) {
	old := []Result{
		{Scenario: "bm25-single-warm", Latency: Latency{P50US: 780}},
		{Scenario: "bm25-and4", Latency: Latency{P50US: 1420}},
		{Scenario: "knn-int8", Latency: Latency{P50US: 4900}},
	}
	// single-warm up 5.1% (regression), and4 flat, knn-int8 down 2% (fine).
	cur := []Result{
		{Scenario: "bm25-single-warm", Latency: Latency{P50US: 820}},
		{Scenario: "bm25-and4", Latency: Latency{P50US: 1410}},
		{Scenario: "knn-int8", Latency: Latency{P50US: 4800}},
	}
	changes := Compare(old, cur)
	if len(changes) != 3 {
		t.Fatalf("expected 3 paired changes, got %d", len(changes))
	}
	if !HasRegression(changes) {
		t.Fatalf("expected a regression to be flagged")
	}
	byName := map[string]Change{}
	for _, c := range changes {
		byName[c.Scenario] = c
	}
	if !byName["bm25-single-warm"].Regression {
		t.Fatalf("bm25-single-warm should be a regression: %+v", byName["bm25-single-warm"])
	}
	if byName["bm25-and4"].Regression || byName["knn-int8"].Regression {
		t.Fatalf("only single-warm should regress: %+v", changes)
	}
	if d := byName["bm25-single-warm"].Delta; d < 0.05 || d > 0.06 {
		t.Fatalf("delta = %.4f, want ~0.051", d)
	}
}

func TestCompareSkipsUnpairedScenarios(t *testing.T) {
	old := []Result{{Scenario: "a", Latency: Latency{P50US: 100}}}
	cur := []Result{{Scenario: "b", Latency: Latency{P50US: 100}}}
	if changes := Compare(old, cur); len(changes) != 0 {
		t.Fatalf("unpaired scenarios should not compare: %+v", changes)
	}
}

func TestRunLoadSmoke(t *testing.T) {
	db := smallText(t)
	defer mustClose(t, db)
	opt := LoadOptions{Concurrency: 2, Duration: 150 * time.Millisecond, Warmup: 30 * time.Millisecond}
	op, err := NewOp("bm25-single-warm", db, opt)
	if err != nil {
		t.Fatal(err)
	}
	res, err := RunLoad("bm25-single-warm", op, opt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Ops == 0 {
		t.Fatalf("no ops recorded")
	}
	if res.Latency.P50US == 0 || res.Latency.P99US < res.Latency.P50US {
		t.Fatalf("percentiles look wrong: %+v", res.Latency)
	}
	if res.QPS <= 0 {
		t.Fatalf("qps not computed: %v", res.QPS)
	}
}

// TestEveryScenarioRuns builds the corpus each scenario needs and runs a handful
// of ops, so a broken scenario wiring is caught without a full timed load run.
func TestEveryScenarioRuns(t *testing.T) {
	for _, name := range Scenarios() {
		t.Run(name, func(t *testing.T) {
			opt := LoadOptions{Docs: 1000, Vocab: 24, Dims: 32}
			co, ok := CorpusFor(name, opt)
			if !ok {
				t.Fatalf("no corpus for scenario %q", name)
			}
			db, err := BuildCorpus(co)
			if err != nil {
				t.Fatal(err)
			}
			defer mustClose(t, db)
			op, err := NewOp(name, db, opt)
			if err != nil {
				t.Fatal(err)
			}
			for range 5 {
				if err := op(); err != nil {
					t.Fatalf("scenario %q op: %v", name, err)
				}
			}
		})
	}
}

func TestNewOpUnknownScenario(t *testing.T) {
	db := smallText(t)
	defer mustClose(t, db)
	if _, err := NewOp("does-not-exist", db, LoadOptions{}); err == nil {
		t.Fatalf("expected an error for an unknown scenario")
	}
}
