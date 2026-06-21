// Package bench is the performance benchmark suite for the search engine
// (spec 2063 doc 19). It carries two execution modes that share one harness:
// the Go benchmark functions in bench_test.go, which the CI regression gate
// reads through benchstat, and the load generator in this file, which the
// `sx bench` command drives to record a latency histogram under concurrent
// load.
//
// The large real corpora the spec names (Wikipedia-10M, SIFT1M) are supplied by
// an external runner; the in-repo harness builds a small deterministic synthetic
// corpus so the suite runs anywhere with no data download. That synthetic path
// is what the CI gate and the `sx bench` smoke runs exercise. The percentiles it
// reports are real; only the corpus scale differs from the headline SLO numbers,
// which are measured on the runner against the full datasets.
package bench

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tamnd/search"
	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/vfs"
)

// RegressionThreshold is the relative latency increase, as a fraction, above
// which Compare flags a scenario as a regression (spec doc 19 §1.2: a PR that
// regresses any SLO metric by more than 5% must be fixed or ship a documented
// budget revision).
const RegressionThreshold = 0.05

// Latency holds the percentile summary of a run, in microseconds, matching the
// JSON shape the spec defines in §7.1.
type Latency struct {
	P50US  int64 `json:"p50_us"`
	P95US  int64 `json:"p95_us"`
	P99US  int64 `json:"p99_us"`
	P999US int64 `json:"p999_us"`
	MaxUS  int64 `json:"max_us"`
}

// Result is one scenario's measured outcome. Its JSON encoding matches the
// structured format in spec §7.1 so a result file round-trips through --output
// and --compare.
type Result struct {
	Scenario    string   `json:"scenario"`
	Corpus      string   `json:"corpus"`
	GoVersion   string   `json:"go_version"`
	Ops         uint64   `json:"ops"`
	DurationS   float64  `json:"duration_s"`
	QPS         float64  `json:"qps"`
	Concurrency int      `json:"concurrency"`
	Latency     Latency  `json:"latency"`
	AllocsPerOp float64  `json:"allocs_per_op"`
	BytesPerOp  float64  `json:"bytes_per_op"`
	RecallAt10  *float64 `json:"recall_at_10"`
}

// LoadOptions configures a load-generator run. The duration and warmup are
// fractional seconds so a smoke test can run sub-second while production runs
// follow the spec defaults of 300 s steady state after 60 s of warmup.
type LoadOptions struct {
	Concurrency int
	QPS         float64 // 0 means as fast as possible
	Duration    time.Duration
	Warmup      time.Duration
	EfSearch    int

	// Corpus knobs. Zero values fall back to the defaults a synthetic run uses.
	Docs     int
	Vocab    int
	Dims     int
	PageSize uint32
}

func (o LoadOptions) withDefaults() LoadOptions {
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	if o.Duration <= 0 {
		o.Duration = 300 * time.Second
	}
	if o.Warmup < 0 {
		o.Warmup = 0
	}
	if o.Docs <= 0 {
		o.Docs = 20000
	}
	if o.Vocab <= 0 {
		o.Vocab = 64
	}
	if o.Dims <= 0 {
		o.Dims = 64
	}
	if o.PageSize == 0 {
		o.PageSize = 65536
	}
	return o
}

// CorpusOptions describes the synthetic corpus to build.
type CorpusOptions struct {
	Docs     int
	Vocab    int
	Dims     int    // 0 builds a text-only corpus; >0 adds a dense_vector field
	Quant    string // dense_vector quantization when Dims > 0
	PageSize uint32
	VFS      vfs.VFS // nil builds in memory
	Path     string  // "" uses an in-memory file name
}

// BuildCorpus creates and fills a synthetic index for benchmarking and returns
// the open database. A text-only corpus carries a single analyzed "body" field
// whose terms follow a divisibility rule, so each term's document frequency is
// known by construction. A vector corpus adds a "title" text field, a "tag"
// keyword field, and an "embed" dense_vector field of the requested dimension.
func BuildCorpus(opt CorpusOptions) (*search.DB, error) {
	if opt.Docs <= 0 {
		opt.Docs = 20000
	}
	if opt.Vocab <= 0 {
		opt.Vocab = 64
	}
	if opt.PageSize == 0 {
		opt.PageSize = 65536
	}
	fsys := opt.VFS
	if fsys == nil {
		fsys = vfs.NewMem()
	}
	path := opt.Path
	if path == "" {
		path = "bench.sx"
	}
	db, err := search.Open(path, search.Options{
		VFS:      fsys,
		PageSize: opt.PageSize,
		Clock:    determ.NewFakeClock(0),
		SaltSeed: 1,
	})
	if err != nil {
		return nil, err
	}
	s := schema.New()
	if opt.Dims > 0 {
		if err := s.Add(schema.NewField("title", schema.TypeText)); err != nil {
			return nil, err
		}
		if err := s.Add(schema.NewField("tag", schema.TypeKeyword)); err != nil {
			return nil, err
		}
		vf := schema.NewField("embed", schema.TypeDenseVector)
		vf.Opts.Dims = opt.Dims
		vf.Opts.Metric = schema.MetricL2
		if opt.Quant != "" {
			vf.Opts.Quantization = opt.Quant
		}
		if err := s.Add(vf); err != nil {
			return nil, err
		}
	} else {
		if err := s.Add(schema.NewField("body", schema.TypeText)); err != nil {
			return nil, err
		}
	}
	if err := db.PutSchema(s); err != nil {
		_ = db.Close()
		return nil, err
	}

	const batch = 1000
	rng := &splitMix{state: 0x9e3779b97f4a7c15}
	docs := make([]map[string]any, 0, batch)
	flush := func() error {
		if len(docs) == 0 {
			return nil
		}
		if _, err := db.Index(docs); err != nil {
			return err
		}
		docs = docs[:0]
		return nil
	}
	for i := 1; i <= opt.Docs; i++ {
		doc := map[string]any{"_id": fmt.Sprintf("d%d", i)}
		if opt.Dims > 0 {
			doc["title"] = bodyFor(i, opt.Vocab)
			doc["tag"] = fmt.Sprintf("t%d", i%8)
			doc["embed"] = randVecAny(rng, opt.Dims)
		} else {
			doc["body"] = bodyFor(i, opt.Vocab)
		}
		docs = append(docs, doc)
		if len(docs) == batch {
			if err := flush(); err != nil {
				_ = db.Close()
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// bodyFor renders the analyzed text for document i: term wj is present whenever
// i is divisible by j+1, in ascending j order, so divisors that all divide i
// land adjacent and phrase queries over them match by construction.
func bodyFor(i, vocab int) string {
	body := make([]byte, 0, vocab*4)
	for j := range vocab {
		if i%(j+1) == 0 {
			if len(body) > 0 {
				body = append(body, ' ')
			}
			body = fmt.Appendf(body, "w%d", j)
		}
	}
	return string(body)
}

// Op is one unit of work the load generator times. It must be safe to call from
// multiple goroutines.
type Op func() error

// scenario binds a name to a corpus requirement and an op factory. The factory
// receives the open database, the resolved options, and a shared monotonic
// counter that write scenarios use to mint unique document ids and that query
// scenarios use to rotate through a query set.
type scenario struct {
	name    string
	dims    int    // 0 = text corpus, >0 = vector corpus of this dimension
	quant   string // vector quantization when dims > 0
	factory func(db *search.DB, opt LoadOptions, ctr *atomic.Int64) (Op, error)
}

var scenarios = []scenario{
	{name: "bm25-single-warm", factory: termOp(query.Term("body", "w7"))},
	{name: "bm25-and4", factory: termOp(query.Bool().
		MustClause(query.Term("body", "w1")).
		MustClause(query.Term("body", "w2")).
		MustClause(query.Term("body", "w3")).
		MustClause(query.Term("body", "w5")))},
	{name: "bm25-or4", factory: termOp(query.Bool().
		ShouldClause(query.Term("body", "w1")).
		ShouldClause(query.Term("body", "w2")).
		ShouldClause(query.Term("body", "w3")).
		ShouldClause(query.Term("body", "w5")))},
	{name: "phrase3", factory: termOp(query.Phrase("body", "w0 w1 w2"))},
	{name: "knn-f32", dims: 64, factory: knnOp},
	{name: "knn-int8", dims: 64, quant: schema.QuantInt8, factory: knnOp},
	{name: "hybrid", dims: 64, factory: hybridOp},
	{name: "index-single", factory: indexSingleOp},
	{name: "bulk-ingest", factory: bulkIngestOp},
}

// Scenarios returns the registered scenario names in run order.
func Scenarios() []string {
	out := make([]string, len(scenarios))
	for i, s := range scenarios {
		out[i] = s.name
	}
	return out
}

func lookup(name string) (scenario, bool) {
	for _, s := range scenarios {
		if s.name == name {
			return s, true
		}
	}
	return scenario{}, false
}

// CorpusFor returns the corpus options a scenario needs, so a caller can build
// one index and reuse it across scenarios that share a shape.
func CorpusFor(name string, opt LoadOptions) (CorpusOptions, bool) {
	s, ok := lookup(name)
	if !ok {
		return CorpusOptions{}, false
	}
	opt = opt.withDefaults()
	co := CorpusOptions{Docs: opt.Docs, Vocab: opt.Vocab, PageSize: opt.PageSize}
	if s.dims > 0 {
		co.Dims = opt.Dims
		co.Quant = s.quant
	}
	return co, true
}

// termOp builds a factory that runs a fixed text query for the top 10 hits.
func termOp(q query.Query) func(*search.DB, LoadOptions, *atomic.Int64) (Op, error) {
	return func(db *search.DB, _ LoadOptions, _ *atomic.Int64) (Op, error) {
		return func() error {
			_, err := db.Search(q, 10)
			return err
		}, nil
	}
}

// queryVectors precomputes a small rotating set of query vectors so the kNN and
// hybrid ops do not allocate a fresh vector per call.
func queryVectors(n, dims int) [][]float32 {
	rng := &splitMix{state: 0xdeadbeefcafef00d}
	out := make([][]float32, n)
	for i := range out {
		out[i] = randVec(rng, dims)
	}
	return out
}

func knnOp(db *search.DB, opt LoadOptions, ctr *atomic.Int64) (Op, error) {
	opt = opt.withDefaults()
	qs := queryVectors(64, opt.Dims)
	return func() error {
		q := query.KNN("embed", qs[int(ctr.Add(1))%len(qs)], 10)
		q.NumCandidates = opt.EfSearch
		_, err := db.Search(q, 10)
		return err
	}, nil
}

func hybridOp(db *search.DB, opt LoadOptions, ctr *atomic.Int64) (Op, error) {
	opt = opt.withDefaults()
	qs := queryVectors(64, opt.Dims)
	return func() error {
		v := qs[int(ctr.Add(1))%len(qs)]
		knn := query.KNN("embed", v, 10)
		knn.NumCandidates = opt.EfSearch
		_, err := db.Search(query.Hybrid(query.Term("title", "w7"), knn, 10), 10)
		return err
	}, nil
}

func indexSingleOp(db *search.DB, opt LoadOptions, ctr *atomic.Int64) (Op, error) {
	opt = opt.withDefaults()
	return func() error {
		id := ctr.Add(1)
		_, err := db.Index([]map[string]any{{
			"_id":  fmt.Sprintf("load-%d", id),
			"body": bodyFor(int(id), opt.Vocab),
		}})
		return err
	}, nil
}

func bulkIngestOp(db *search.DB, opt LoadOptions, ctr *atomic.Int64) (Op, error) {
	opt = opt.withDefaults()
	const batch = 1000
	return func() error {
		base := ctr.Add(batch)
		docs := make([]map[string]any, batch)
		for k := range batch {
			id := base - batch + int64(k) + 1
			docs[k] = map[string]any{
				"_id":  fmt.Sprintf("bulk-%d", id),
				"body": bodyFor(int(id), opt.Vocab),
			}
		}
		_, err := db.Index(docs)
		return err
	}, nil
}

// NewOp builds the op for a scenario against an already-open corpus. It returns
// an error if the scenario name is unknown.
func NewOp(name string, db *search.DB, opt LoadOptions) (Op, error) {
	s, ok := lookup(name)
	if !ok {
		return nil, fmt.Errorf("unknown scenario %q", name)
	}
	var ctr atomic.Int64
	return s.factory(db, opt, &ctr)
}

// RunLoad drives op at the requested concurrency and QPS for the warmup period
// (discarded) then the measurement period, and returns the latency summary. It
// does not build the corpus; the caller builds it once and passes the op so a
// single index serves repeated runs.
func RunLoad(name string, op Op, opt LoadOptions) (Result, error) {
	opt = opt.withDefaults()

	// Warmup: run flat out, discarding timings, so caches and the allocator
	// reach steady state before measurement.
	if opt.Warmup > 0 {
		if err := runFor(op, opt.Concurrency, 0, opt.Warmup, nil); err != nil {
			return Result{}, err
		}
	}

	hists := make([]*histogram, opt.Concurrency)
	for i := range hists {
		hists[i] = newHistogram()
	}
	start := time.Now()
	if err := runFor(op, opt.Concurrency, opt.QPS, opt.Duration, hists); err != nil {
		return Result{}, err
	}
	elapsed := time.Since(start)

	merged := newHistogram()
	for _, h := range hists {
		merged.mergeFrom(h)
	}
	res := Result{
		Scenario:    name,
		Corpus:      fmt.Sprintf("synthetic-%dd", opt.Docs),
		Concurrency: opt.Concurrency,
		Ops:         merged.n,
		DurationS:   elapsed.Seconds(),
		Latency: Latency{
			P50US:  merged.percentileUS(0.50),
			P95US:  merged.percentileUS(0.95),
			P99US:  merged.percentileUS(0.99),
			P999US: merged.percentileUS(0.999),
			MaxUS:  merged.maxUS,
		},
	}
	if elapsed > 0 {
		res.QPS = float64(merged.n) / elapsed.Seconds()
	}
	return res, nil
}

// runFor runs op across n goroutines until d elapses. When qps > 0 the load is
// paced so the aggregate rate approaches qps; when qps is 0 each goroutine runs
// flat out. If hists is non-nil each goroutine records into its own histogram,
// avoiding contention on a shared one.
func runFor(op Op, n int, qps float64, d time.Duration, hists []*histogram) error {
	deadline := time.Now().Add(d)
	var perOpGap time.Duration
	if qps > 0 {
		perOpGap = time.Duration(float64(time.Second) * float64(n) / qps)
	}

	var wg sync.WaitGroup
	var firstErr atomic.Value
	for g := range n {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			var h *histogram
			if hists != nil {
				h = hists[g]
			}
			next := time.Now()
			for time.Now().Before(deadline) {
				if perOpGap > 0 {
					if wait := time.Until(next); wait > 0 {
						time.Sleep(wait)
					}
					next = next.Add(perOpGap)
				}
				t0 := time.Now()
				err := op()
				if h != nil {
					h.add(time.Since(t0))
				}
				if err != nil {
					firstErr.CompareAndSwap(nil, errBox{err})
					return
				}
			}
		}(g)
	}
	wg.Wait()
	if v := firstErr.Load(); v != nil {
		return v.(errBox).err
	}
	return nil
}

type errBox struct{ err error }

// Change is one row of a --compare report: how a scenario's P50 moved between
// two result files.
type Change struct {
	Scenario   string
	OldP50US   int64
	NewP50US   int64
	Delta      float64 // relative change in P50, e.g. 0.051 for +5.1%
	Regression bool
}

// Compare pairs scenarios present in both result sets by name and reports the
// P50 movement, flagging any increase beyond RegressionThreshold. Scenarios
// missing from either side are skipped, since there is nothing to compare.
func Compare(old, new []Result) []Change {
	prev := make(map[string]Result, len(old))
	for _, r := range old {
		prev[r.Scenario] = r
	}
	var out []Change
	for _, r := range new {
		o, ok := prev[r.Scenario]
		if !ok {
			continue
		}
		c := Change{
			Scenario: r.Scenario,
			OldP50US: o.Latency.P50US,
			NewP50US: r.Latency.P50US,
		}
		if o.Latency.P50US > 0 {
			c.Delta = float64(r.Latency.P50US-o.Latency.P50US) / float64(o.Latency.P50US)
		}
		c.Regression = c.Delta > RegressionThreshold
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scenario < out[j].Scenario })
	return out
}

// HasRegression reports whether any change crossed the threshold.
func HasRegression(changes []Change) bool {
	for _, c := range changes {
		if c.Regression {
			return true
		}
	}
	return false
}

// histogram buckets latencies at 10 µs resolution up to a 1 s ceiling so a long
// run keeps bounded memory regardless of op count. Samples past the ceiling
// land in the final bucket but the true maximum is tracked separately.
type histogram struct {
	counts []uint64
	n      uint64
	maxUS  int64
}

const (
	histBucketUS = 10
	histCeilUS   = 1_000_000
	histBuckets  = histCeilUS/histBucketUS + 1
)

func newHistogram() *histogram { return &histogram{counts: make([]uint64, histBuckets)} }

func (h *histogram) add(d time.Duration) {
	us := max(d.Microseconds(), 0)
	h.n++
	h.maxUS = max(h.maxUS, us)
	b := us / histBucketUS
	if b >= int64(len(h.counts)) {
		b = int64(len(h.counts)) - 1
	}
	h.counts[b]++
}

func (h *histogram) mergeFrom(o *histogram) {
	for i, c := range o.counts {
		h.counts[i] += c
	}
	h.n += o.n
	if o.maxUS > h.maxUS {
		h.maxUS = o.maxUS
	}
}

// percentileUS returns the latency at percentile p (0..1), using the midpoint of
// the bucket the cumulative count first crosses.
func (h *histogram) percentileUS(p float64) int64 {
	if h.n == 0 {
		return 0
	}
	target := uint64(math.Ceil(float64(h.n) * p))
	target = max(target, 1)
	var cum uint64
	for i, c := range h.counts {
		cum += c
		if cum >= target {
			return int64(i)*histBucketUS + histBucketUS/2
		}
	}
	return h.maxUS
}

// splitMix is a small deterministic PRNG so the synthetic corpus and query
// vectors are reproducible without pulling in math/rand global state.
type splitMix struct{ state uint64 }

func (s *splitMix) next() uint64 {
	s.state += 0x9e3779b97f4a7c15
	z := s.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (s *splitMix) float64() float64 { return float64(s.next()>>11) / float64(1<<53) }

func randVec(s *splitMix, d int) []float32 {
	v := make([]float32, d)
	for i := range v {
		v[i] = float32(s.float64()*2 - 1)
	}
	return v
}

func randVecAny(s *splitMix, d int) []any {
	v := make([]any, d)
	for i := range v {
		v[i] = s.float64()*2 - 1
	}
	return v
}
