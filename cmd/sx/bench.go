package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/tamnd/search/bench"
)

// cmdBench runs the load generator from the bench package against a synthetic
// corpus and reports the latency percentiles the service-level objectives are
// stated against (spec doc 19 §9.4). It supports three shapes:
//
//	sx bench <scenario|all>                 run and print a summary
//	sx bench --output res.json <scenario>   run and also write JSON results
//	sx bench --compare base.json <scenario> run, then diff against a baseline
//	sx bench --compare base.json cur.json   diff two result files, no run
//
// A run drives an in-memory synthetic index. The large real corpora the spec
// names are loaded by an external runner; the --corpus and --index flags are
// accepted for forward compatibility but the in-repo runner ignores them and
// builds the synthetic corpus, which is what the CI smoke path uses.
func cmdBench(args []string) int {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	corpus := fs.String("corpus", "", "path to a corpus file or directory (external runner only)")
	index := fs.String("index", "", "path to a .sx index to use (external runner only)")
	duration := fs.Float64("duration", 300, "measurement duration in seconds")
	warmup := fs.Float64("warmup", 60, "warmup duration in seconds")
	concurrency := fs.Int("concurrency", 1, "number of concurrent goroutines")
	qps := fs.Float64("qps", 0, "target aggregate QPS (0 = as fast as possible)")
	efSearch := fs.Int("ef-search", 0, "efSearch for vector scenarios (0 = field default)")
	docs := fs.Int("docs", 0, "synthetic corpus document count")
	vocab := fs.Int("vocab", 0, "synthetic corpus vocabulary size")
	dims := fs.Int("dims", 0, "synthetic vector dimension for vector scenarios")
	output := fs.String("output", "", "write JSON results to this file")
	compare := fs.String("compare", "", "compare results against this baseline JSON file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = corpus
	_ = index

	rest := fs.Args()

	// Pure compare mode: a baseline and a current results file, no run.
	if *compare != "" && len(rest) == 1 && !isScenario(rest[0]) && fileExists(rest[0]) {
		return benchCompareFiles(*compare, rest[0])
	}

	if len(rest) != 1 {
		return fail("usage: sx bench [options] <scenario|all>  (scenarios: %v, all)", bench.Scenarios())
	}
	target := rest[0]

	var names []string
	switch target {
	case "all":
		names = bench.Scenarios()
	default:
		if !isScenario(target) {
			return fail("unknown scenario %q (scenarios: %v, all)", target, bench.Scenarios())
		}
		names = []string{target}
	}

	opt := bench.LoadOptions{
		Concurrency: *concurrency,
		QPS:         *qps,
		Duration:    secs(*duration),
		Warmup:      secs(*warmup),
		EfSearch:    *efSearch,
		Docs:        *docs,
		Vocab:       *vocab,
		Dims:        *dims,
	}

	results, code := runScenarios(names, opt)
	if code != 0 {
		return code
	}

	printResults(results)

	if *output != "" {
		if err := writeResults(*output, results); err != nil {
			return fail("write %s: %v", *output, err)
		}
		fmt.Printf("wrote %d result(s) to %s\n", len(results), *output)
	}

	if *compare != "" {
		base, err := readResults(*compare)
		if err != nil {
			return fail("read baseline %s: %v", *compare, err)
		}
		return reportCompare(base, results)
	}
	return 0
}

// runScenarios builds the corpus each scenario needs, runs the load generator,
// and returns the results. It rebuilds the corpus per scenario so a vector
// scenario does not pay for a corpus a text scenario would not use.
func runScenarios(names []string, opt bench.LoadOptions) ([]bench.Result, int) {
	results := make([]bench.Result, 0, len(names))
	for _, name := range names {
		co, ok := bench.CorpusFor(name, opt)
		if !ok {
			return nil, fail("unknown scenario %q", name)
		}
		_, _ = fmt.Fprintf(os.Stderr, "building corpus for %s...\n", name)
		db, err := bench.BuildCorpus(co)
		if err != nil {
			return nil, fail("build corpus for %s: %v", name, err)
		}
		op, err := bench.NewOp(name, db, opt)
		if err != nil {
			_ = db.Close()
			return nil, fail("scenario %s: %v", name, err)
		}
		_, _ = fmt.Fprintf(os.Stderr, "running %s...\n", name)
		res, err := bench.RunLoad(name, op, opt)
		_ = db.Close()
		if err != nil {
			return nil, fail("run %s: %v", name, err)
		}
		res.GoVersion = runtime.Version()
		results = append(results, res)
	}
	return results, 0
}

// printResults writes the percentile summary as an aligned table.
func printResults(results []bench.Result) {
	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()
	_, _ = fmt.Fprintf(w, "%-18s %10s %8s %8s %8s %8s\n", "scenario", "ops", "p50_us", "p95_us", "p99_us", "qps")
	for _, r := range results {
		_, _ = fmt.Fprintf(w, "%-18s %10d %8d %8d %8d %8.0f\n",
			r.Scenario, r.Ops, r.Latency.P50US, r.Latency.P95US, r.Latency.P99US, r.QPS)
	}
}

// benchCompareFiles reads two result files and prints the regression table.
func benchCompareFiles(basePath, curPath string) int {
	base, err := readResults(basePath)
	if err != nil {
		return fail("read %s: %v", basePath, err)
	}
	cur, err := readResults(curPath)
	if err != nil {
		return fail("read %s: %v", curPath, err)
	}
	return reportCompare(base, cur)
}

// reportCompare prints the P50 movement of each paired scenario and exits 1 when
// any scenario regresses past the threshold, so it gates a CI pipeline.
func reportCompare(base, cur []bench.Result) int {
	changes := bench.Compare(base, cur)
	if len(changes) == 0 {
		fmt.Println("no comparable scenarios")
		return 0
	}
	w := bufio.NewWriter(os.Stdout)
	_, _ = fmt.Fprintf(w, "%-18s %10s %10s %9s\n", "scenario", "old p50", "new p50", "change")
	for _, c := range changes {
		mark := "OK"
		if c.Regression {
			mark = "REGRESSION"
		}
		_, _ = fmt.Fprintf(w, "%-18s %8d us %8d us %+8.1f%% %s\n",
			c.Scenario, c.OldP50US, c.NewP50US, c.Delta*100, mark)
	}
	_ = w.Flush()
	if bench.HasRegression(changes) {
		return 1
	}
	return 0
}

func isScenario(name string) bool {
	return slices.Contains(bench.Scenarios(), name)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

func readResults(path string) ([]bench.Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs []bench.Result
	if err := json.Unmarshal(b, &rs); err != nil {
		return nil, err
	}
	return rs, nil
}

func writeResults(path string, rs []bench.Result) error {
	b, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
