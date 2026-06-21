package fst

import (
	"reflect"
	"regexp"
	"testing"
)

func scanTerms(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = string(e.Term)
	}
	return out
}

func TestFuzzyScan(t *testing.T) {
	f := buildFST(t, map[string]uint64{
		"search": 1, "serch": 2, "research": 3, "surgery": 4, "see": 5, "fox": 6,
	})
	got, err := f.FuzzyScan("search", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"search", "serch"}
	if !reflect.DeepEqual(scanTerms(got), want) {
		t.Fatalf("FuzzyScan(search,1) = %v, want %v", scanTerms(got), want)
	}
}

func TestWildcardScan(t *testing.T) {
	f := buildFST(t, map[string]uint64{
		"prefix": 1, "preface": 2, "present": 3, "post": 4, "pre": 5,
	})
	got, err := f.WildcardScan("pre*")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pre", "preface", "prefix", "present"}
	if !reflect.DeepEqual(scanTerms(got), want) {
		t.Fatalf("WildcardScan(pre*) = %v, want %v", scanTerms(got), want)
	}

	got, err = f.WildcardScan("p?e*")
	if err != nil {
		t.Fatal(err)
	}
	if terms := scanTerms(got); len(terms) != 4 {
		t.Fatalf("WildcardScan(p?e*) = %v, want 4", terms)
	}
}

func TestRegexpScan(t *testing.T) {
	f := buildFST(t, map[string]uint64{
		"AB1234": 1, "XY42": 2, "ZZ9999": 3, "lower42": 4,
	})
	re := regexp.MustCompile("^[A-Z]{2}[0-9]{4}$")
	got, over, err := f.RegexpScan(re, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if over {
		t.Fatal("did not expect overflow")
	}
	want := []string{"AB1234", "ZZ9999"}
	if !reflect.DeepEqual(scanTerms(got), want) {
		t.Fatalf("RegexpScan = %v, want %v", scanTerms(got), want)
	}
}

func TestRegexpScanOverflow(t *testing.T) {
	f := buildFST(t, map[string]uint64{"a": 1, "b": 2, "c": 3, "d": 4})
	re := regexp.MustCompile("^z$")
	_, over, err := f.RegexpScan(re, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !over {
		t.Fatal("expected overflow with a visit cap of 2 over 4 terms")
	}
}
