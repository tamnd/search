package fst

import (
	"reflect"
	"sort"
	"testing"
)

// buildFST builds an FST from a term->output map, adding terms in sorted order.
func buildFST(t *testing.T, m map[string]uint64) *FST {
	t.Helper()
	terms := make([]string, 0, len(m))
	for k := range m {
		terms = append(terms, k)
	}
	sort.Strings(terms)
	b := NewBuilder()
	for _, term := range terms {
		if err := b.Add([]byte(term), m[term]); err != nil {
			t.Fatalf("Add(%q): %v", term, err)
		}
	}
	f, err := b.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return f
}

func TestFSTBuilder_ExactLookup(t *testing.T) {
	m := map[string]uint64{
		"car": 0, "card": 20, "care": 40, "cat": 60, "dog": 80,
	}
	f := buildFST(t, m)
	for term, want := range m {
		got, ok, err := f.Lookup([]byte(term))
		if err != nil {
			t.Fatal(err)
		}
		if !ok || got != want {
			t.Fatalf("Lookup(%q) = %d,%v want %d,true", term, got, ok, want)
		}
	}
}

func TestFSTBuilder_MissingLookup(t *testing.T) {
	f := buildFST(t, map[string]uint64{"car": 1, "card": 2, "care": 3})
	for _, term := range []string{"ca", "care!", "d", "cars", "", "z"} {
		if _, ok, err := f.Lookup([]byte(term)); err != nil || ok {
			t.Fatalf("Lookup(%q) = ok=%v err=%v, want false", term, ok, err)
		}
	}
}

func TestFSTBuilder_EmptyTermAndDict(t *testing.T) {
	// Empty dictionary.
	b := NewBuilder()
	f, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.Lookup([]byte("x")); ok {
		t.Fatal("empty dict matched a term")
	}

	// Dictionary containing the empty term.
	b = NewBuilder()
	if err := b.Add([]byte(""), 7); err != nil {
		t.Fatal(err)
	}
	if err := b.Add([]byte("a"), 9); err != nil {
		t.Fatal(err)
	}
	f, err = b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := f.Lookup([]byte("")); !ok || got != 7 {
		t.Fatalf("Lookup(empty) = %d,%v want 7,true", got, ok)
	}
	if got, ok, _ := f.Lookup([]byte("a")); !ok || got != 9 {
		t.Fatalf("Lookup(a) = %d,%v want 9,true", got, ok)
	}
}

func TestFSTBuilder_PrefixScan(t *testing.T) {
	f := buildFST(t, map[string]uint64{
		"car": 0, "card": 20, "care": 40, "cat": 60, "dog": 80,
	})
	got, err := f.PrefixScan([]byte("car"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"car", "card", "care"}
	if terms := entryTerms(got); !reflect.DeepEqual(terms, want) {
		t.Fatalf("PrefixScan(car) = %v want %v", terms, want)
	}

	got, err = f.PrefixScan([]byte("ca"))
	if err != nil {
		t.Fatal(err)
	}
	if terms := entryTerms(got); !reflect.DeepEqual(terms, []string{"car", "card", "care", "cat"}) {
		t.Fatalf("PrefixScan(ca) = %v", terms)
	}

	if got, _ := f.PrefixScan([]byte("zz")); len(got) != 0 {
		t.Fatalf("PrefixScan(zz) = %v want empty", got)
	}
}

func TestFSTBuilder_RangeScan(t *testing.T) {
	f := buildFST(t, map[string]uint64{
		"car": 0, "card": 20, "care": 40, "cat": 60, "dog": 80,
	})
	got, err := f.RangeScan([]byte("card"), []byte("dog"))
	if err != nil {
		t.Fatal(err)
	}
	if terms := entryTerms(got); !reflect.DeepEqual(terms, []string{"card", "care", "cat"}) {
		t.Fatalf("RangeScan(card,dog) = %v", terms)
	}

	// Unbounded ends.
	got, _ = f.RangeScan(nil, nil)
	if terms := entryTerms(got); !reflect.DeepEqual(terms, []string{"car", "card", "care", "cat", "dog"}) {
		t.Fatalf("RangeScan(nil,nil) = %v", terms)
	}
}

func TestFSTBuilder_RejectsUnsorted(t *testing.T) {
	b := NewBuilder()
	if err := b.Add([]byte("b"), 1); err != nil {
		t.Fatal(err)
	}
	if err := b.Add([]byte("a"), 2); err != ErrUnsortedInput {
		t.Fatalf("Add unsorted = %v want ErrUnsortedInput", err)
	}
	if err := b.Add([]byte("b"), 3); err != ErrUnsortedInput {
		t.Fatalf("Add duplicate = %v want ErrUnsortedInput", err)
	}
}

func TestFSTRoundTripSerialize(t *testing.T) {
	m := map[string]uint64{"alpha": 1, "alpine": 2, "beta": 3, "betatron": 4, "zzz": 9999}
	f := buildFST(t, m)
	reopened, err := Open(f.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for term, want := range m {
		got, ok, err := reopened.Lookup([]byte(term))
		if err != nil || !ok || got != want {
			t.Fatalf("reopened Lookup(%q) = %d,%v,%v", term, got, ok, err)
		}
	}
}

// FuzzFSTLookup builds an FST from random terms and cross-checks against a map.
func FuzzFSTLookup(f *testing.F) {
	f.Add([]byte("a\x00b\x00c"), uint64(1))
	f.Add([]byte("the\x00quick\x00brown\x00fox"), uint64(7))
	f.Fuzz(func(t *testing.T, blob []byte, base uint64) {
		// Derive a set of terms by splitting on NUL; keep unique, drop empties at
		// will (the empty term is allowed but only once).
		seen := map[string]uint64{}
		i := 0
		for _, part := range splitNUL(blob) {
			if len(part) > MaxTermLen {
				part = part[:MaxTermLen]
			}
			if _, ok := seen[part]; ok {
				continue
			}
			seen[part] = base + uint64(i)*10
			i++
		}
		terms := make([]string, 0, len(seen))
		for k := range seen {
			terms = append(terms, k)
		}
		sort.Strings(terms)

		b := NewBuilder()
		for _, term := range terms {
			if err := b.Add([]byte(term), seen[term]); err != nil {
				t.Fatalf("Add(%q): %v", term, err)
			}
		}
		fst, err := b.Finish()
		if err != nil {
			t.Fatal(err)
		}
		for term, want := range seen {
			got, ok, err := fst.Lookup([]byte(term))
			if err != nil || !ok || got != want {
				t.Fatalf("Lookup(%q) = %d,%v,%v want %d", term, got, ok, err, want)
			}
		}
		// A term guaranteed absent.
		if _, ok, _ := fst.Lookup([]byte("\x01absent-sentinel")); ok {
			t.Fatal("sentinel matched")
		}
	})
}

func splitNUL(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == 0 {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	out = append(out, string(b[start:]))
	return out
}

func entryTerms(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = string(e.Term)
	}
	return out
}
