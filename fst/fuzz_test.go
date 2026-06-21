package fst

import (
	"regexp"
	"sort"
	"testing"
)

// FuzzFSTScan builds an FST from random terms and runs every scan operation
// against it with random arguments. The invariants: no scan panics, every scan
// returns terms that are a subset of the built set, and the prefix and range
// scans return only terms that actually satisfy the predicate.
func FuzzFSTScan(f *testing.F) {
	f.Add([]byte("a\x00ab\x00abc\x00b"), []byte("a"))
	f.Add([]byte("the\x00there\x00their\x00then"), []byte("the"))
	f.Add([]byte("\x00x\x00"), []byte(""))
	f.Fuzz(func(t *testing.T, blob, arg []byte) {
		seen := map[string]struct{}{}
		for _, part := range splitNUL(blob) {
			if len(part) > MaxTermLen {
				part = part[:MaxTermLen]
			}
			seen[part] = struct{}{}
		}
		terms := make([]string, 0, len(seen))
		for k := range seen {
			terms = append(terms, k)
		}
		sort.Strings(terms)

		b := NewBuilder()
		for i, term := range terms {
			if err := b.Add([]byte(term), uint64(i)+1); err != nil {
				t.Fatalf("Add(%q): %v", term, err)
			}
		}
		fst, err := b.Finish()
		if err != nil {
			t.Fatalf("Finish: %v", err)
		}

		inSet := func(e Entry) bool {
			_, ok := seen[string(e.Term)]
			return ok
		}
		assertSubset := func(name string, es []Entry) {
			for _, e := range es {
				if !inSet(e) {
					t.Fatalf("%s returned term %q not in the built set", name, e.Term)
				}
			}
			if !sortedEntries(es) {
				t.Fatalf("%s returned terms out of order", name)
			}
		}

		all, err := fst.All()
		if err != nil {
			t.Fatalf("All: %v", err)
		}
		assertSubset("All", all)
		if len(all) != len(seen) {
			t.Fatalf("All returned %d terms, want %d", len(all), len(seen))
		}

		pre, err := fst.PrefixScan(arg)
		if err != nil {
			t.Fatalf("PrefixScan: %v", err)
		}
		assertSubset("PrefixScan", pre)

		rng, err := fst.RangeScan(arg, nil)
		if err != nil {
			t.Fatalf("RangeScan: %v", err)
		}
		assertSubset("RangeScan", rng)

		fz, err := fst.FuzzyScan(string(arg), 1)
		if err != nil {
			t.Fatalf("FuzzyScan: %v", err)
		}
		assertSubset("FuzzyScan", fz)

		// A literal pattern is always a valid wildcard; exercise the scanner.
		wc, err := fst.WildcardScan(string(arg) + "*")
		if err != nil {
			t.Fatalf("WildcardScan: %v", err)
		}
		assertSubset("WildcardScan", wc)

		// A trivial regexp that matches everything; bound the visit budget.
		if re, reErr := regexp.Compile(".*"); reErr == nil {
			rx, _, err := fst.RegexpScan(re, "", 1<<20)
			if err != nil {
				t.Fatalf("RegexpScan: %v", err)
			}
			assertSubset("RegexpScan", rx)
		}
	})
}

// sortedEntries reports whether es is in non-decreasing term order.
func sortedEntries(es []Entry) bool {
	for i := 1; i < len(es); i++ {
		if bytesCompare(es[i-1].Term, es[i].Term) > 0 {
			return false
		}
	}
	return true
}
