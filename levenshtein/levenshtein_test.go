package levenshtein

import "testing"

func TestDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"search", "searh", 1},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"café", "cafe", 1},
	}
	for _, c := range cases {
		if got := Distance(c.a, c.b); got != c.want {
			t.Errorf("Distance(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestAutomatonMatchesDistance(t *testing.T) {
	// The automaton's verdict must agree with the exact distance for every k.
	terms := []string{"search", "searh", "serch", "research", "surgery", "se", "searching"}
	for _, query := range []string{"search", "fox"} {
		for k := 0; k <= 2; k++ {
			a := New(query, k)
			for _, term := range terms {
				st := a.Start()
				for _, r := range term {
					st = a.Step(st, r)
				}
				got := a.IsMatch(st)
				want := Distance(query, term) <= k
				if got != want {
					t.Errorf("IsMatch(%q vs %q, k=%d) = %v, want %v", query, term, k, got, want)
				}
			}
		}
	}
}

func TestCanMatchPrunes(t *testing.T) {
	// After consuming a prefix that already diverges beyond k, CanMatch is false.
	a := New("abc", 1)
	st := a.Start()
	for _, r := range "xyz" {
		st = a.Step(st, r)
	}
	if a.CanMatch(st) {
		t.Fatal("xyz should not be extendable to within 1 edit of abc")
	}
}

func TestAutoEdits(t *testing.T) {
	cases := map[int]int{1: 0, 2: 0, 3: 1, 4: 1, 5: 2, 12: 2}
	for length, want := range cases {
		if got := AutoEdits(length); got != want {
			t.Errorf("AutoEdits(%d) = %d, want %d", length, got, want)
		}
	}
	if RuneLen("café") != 4 {
		t.Errorf("RuneLen(café) = %d, want 4", RuneLen("café"))
	}
}
