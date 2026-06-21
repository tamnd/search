package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCABIPythonExample builds the c-shared library and runs the ctypes example
// under python3, checking that the printed hits match what the query should
// return. It skips when python3 or a working cgo toolchain is unavailable so the
// pure-Go CI lane stays green; the C ABI CI lane has both and exercises it.
func TestCABIPythonExample(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found")
	}

	ext := "so"
	switch runtime.GOOS {
	case "darwin":
		ext = "dylib"
	case "windows":
		ext = "dll"
	}

	tmp := t.TempDir()
	lib := filepath.Join(tmp, "libsearch."+ext)

	build := exec.Command("go", "build", "-buildmode=c-shared", "-o", lib, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("c-shared build unavailable (no C compiler?): %v\n%s", err, out)
	}

	// The script loads the library from its own directory, so place a copy of the
	// example next to the freshly built lib.
	src, err := os.ReadFile(filepath.Join("..", "examples", "python", "search_example.py"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(tmp, "search_example.py")
	if err := os.WriteFile(script, src, 0o644); err != nil {
		t.Fatal(err)
	}

	run := exec.Command(py, script)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("python example failed: %v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{"p1", "p3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected hit %q in output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "p2") {
		t.Fatalf("p2 (no 'running' term) should not be a hit:\n%s", got)
	}
}
