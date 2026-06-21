package highlight

import (
	"strings"
	"testing"

	"github.com/tamnd/search/analysis"
)

func termSet(terms ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(terms))
	for _, t := range terms {
		m[t] = struct{}{}
	}
	return m
}

func standard(t *testing.T) *analysis.Analyzer {
	t.Helper()
	a, err := analysis.NewNamed("standard")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestHighlighting_FragmentContent(t *testing.T) {
	a := standard(t)
	h := New(a, Options{})
	text := "The quick brown fox jumps over the lazy dog"
	frags := h.Fragments(text, termSet("quick", "fox"))
	if len(frags) != 1 {
		t.Fatalf("expected one full-field fragment, got %d: %v", len(frags), frags)
	}
	got := frags[0]
	if !strings.Contains(got, "<em>quick</em>") {
		t.Errorf("quick should be wrapped: %q", got)
	}
	if !strings.Contains(got, "<em>fox</em>") {
		t.Errorf("fox should be wrapped: %q", got)
	}
	// Casing and surrounding words are preserved verbatim.
	if !strings.HasPrefix(got, "The ") {
		t.Errorf("surrounding text should be preserved: %q", got)
	}
	if strings.Contains(got, "<em>brown</em>") {
		t.Errorf("non-query term should not be wrapped: %q", got)
	}
}

func TestHighlighting_Fragments(t *testing.T) {
	a := standard(t)
	h := New(a, Options{FragmentSize: 30, NumFragments: 2, PreTag: "<b>", PostTag: "</b>"})
	text := "alpha beta gamma delta epsilon zeta eta theta iota kappa needle lambda mu needle nu"
	frags := h.Fragments(text, termSet("needle"))
	if len(frags) == 0 {
		t.Fatal("expected at least one fragment")
	}
	for _, f := range frags {
		if !strings.Contains(f, "<b>needle</b>") {
			t.Errorf("fragment should contain the wrapped term: %q", f)
		}
		if len(f) > 80 {
			t.Errorf("fragment unexpectedly long: %q", f)
		}
	}
}

func TestHighlighting_NoMatch(t *testing.T) {
	a := standard(t)
	h := New(a, Options{})
	if frags := h.Fragments("nothing to see", termSet("absent")); frags != nil {
		t.Fatalf("expected no fragments, got %v", frags)
	}
}
