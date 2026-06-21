package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/search/bench"
)

// fastBench is the flag set that keeps a bench run sub-second for tests: a short
// measurement window, a touch of warmup, and a tiny synthetic corpus.
func fastBench(extra ...string) []string {
	base := []string{
		"--duration", "0.1",
		"--warmup", "0.02",
		"--docs", "500",
		"--vocab", "16",
		"--dims", "16",
	}
	return append(base, extra...)
}

func TestSXBenchRunsScenario(t *testing.T) {
	out := captureStdout(t, func() int {
		return cmdBench(fastBench("bm25-single-warm"))
	})
	if !strings.Contains(out, "bm25-single-warm") {
		t.Fatalf("bench summary missing scenario:\n%s", out)
	}
	if !strings.Contains(out, "p50_us") {
		t.Fatalf("bench summary missing header:\n%s", out)
	}
}

func TestSXBenchOutputJSON(t *testing.T) {
	dir := t.TempDir()
	res := filepath.Join(dir, "res.json")
	code := cmdBench(fastBench("--output", res, "knn-f32"))
	if code != 0 {
		t.Fatalf("bench exit %d", code)
	}
	b, err := os.ReadFile(res)
	if err != nil {
		t.Fatal(err)
	}
	var rs []bench.Result
	if err := json.Unmarshal(b, &rs); err != nil {
		t.Fatalf("result file not json: %v\n%s", err, b)
	}
	if len(rs) != 1 || rs[0].Scenario != "knn-f32" {
		t.Fatalf("unexpected results: %+v", rs)
	}
	if rs[0].Ops == 0 || rs[0].Latency.P50US == 0 {
		t.Fatalf("result not populated: %+v", rs[0])
	}
}

func TestSXBenchCompareFilesClean(t *testing.T) {
	dir := t.TempDir()
	base := writeResultsFile(t, dir, "base.json", []bench.Result{
		{Scenario: "bm25-single-warm", Latency: bench.Latency{P50US: 1000}},
	})
	cur := writeResultsFile(t, dir, "cur.json", []bench.Result{
		{Scenario: "bm25-single-warm", Latency: bench.Latency{P50US: 1010}}, // +1%, fine
	})
	out := captureStdout(t, func() int { return cmdBench([]string{"--compare", base, cur}) })
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK in compare table:\n%s", out)
	}
}

func TestSXBenchCompareFilesRegression(t *testing.T) {
	dir := t.TempDir()
	base := writeResultsFile(t, dir, "base.json", []bench.Result{
		{Scenario: "bm25-single-warm", Latency: bench.Latency{P50US: 1000}},
	})
	cur := writeResultsFile(t, dir, "cur.json", []bench.Result{
		{Scenario: "bm25-single-warm", Latency: bench.Latency{P50US: 1200}}, // +20%, regression
	})
	out, code := captureStdoutCode(t, func() int { return cmdBench([]string{"--compare", base, cur}) })
	if code != 1 {
		t.Fatalf("compare with regression should exit 1, got %d", code)
	}
	if !strings.Contains(out, "REGRESSION") {
		t.Fatalf("expected REGRESSION marker:\n%s", out)
	}
}

func TestSXBenchUnknownScenario(t *testing.T) {
	if code := cmdBench(fastBench("not-a-scenario")); code == 0 {
		t.Fatalf("unknown scenario should fail")
	}
}

// captureStdoutCode is like captureStdout but returns the exit code instead of
// failing the test when it is non-zero, so a gate command's failure path can be
// asserted.
func captureStdoutCode(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	code := fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = orig
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return out, code
}

func writeResultsFile(t *testing.T, dir, name string, rs []bench.Result) string {
	t.Helper()
	b, err := json.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
