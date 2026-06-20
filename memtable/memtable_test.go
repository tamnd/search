package memtable

import (
	"reflect"
	"testing"
)

func TestAddTokenAggregates(t *testing.T) {
	m := New(0, 0)
	// Two occurrences of "fox" in doc 1, one in doc 2.
	m.AddToken("body", "fox", 1, 0, true)
	m.AddToken("body", "fox", 1, 5, true)
	m.AddToken("body", "fox", 2, 3, true)
	m.AddDoc()
	m.AddDoc()

	f := m.Field("body")
	if f == nil {
		t.Fatal("no body field")
	}
	pl := f.Terms["fox"]
	if pl == nil || len(pl.Postings) != 2 {
		t.Fatalf("fox postings = %+v", pl)
	}
	if pl.Postings[0].DocID != 1 || pl.Postings[0].Freq != 2 {
		t.Fatalf("doc1 posting = %+v", pl.Postings[0])
	}
	if !reflect.DeepEqual(pl.Postings[0].Positions, []uint32{0, 5}) {
		t.Fatalf("doc1 positions = %v", pl.Postings[0].Positions)
	}
	if pl.Postings[1].DocID != 2 || pl.Postings[1].Freq != 1 {
		t.Fatalf("doc2 posting = %+v", pl.Postings[1])
	}
}

func TestNonPositionalField(t *testing.T) {
	m := New(0, 0)
	m.AddToken("tag", "red", 1, 0, false)
	m.AddToken("tag", "red", 1, 0, false)
	pl := m.Field("tag").Terms["red"]
	if pl.Postings[0].Freq != 2 {
		t.Fatalf("freq = %d want 2", pl.Postings[0].Freq)
	}
	if pl.Postings[0].Positions != nil {
		t.Fatalf("positions kept for non-positional field: %v", pl.Postings[0].Positions)
	}
	if m.Field("tag").Positional() {
		t.Fatal("tag should be non-positional")
	}
}

func TestMemtableFlushThreshold(t *testing.T) {
	// Doc-count threshold.
	m := New(1<<40, 5)
	for range 4 {
		m.AddDoc()
	}
	if m.NeedsFlush() {
		t.Fatal("flush before reaching doc threshold")
	}
	m.AddDoc()
	if !m.NeedsFlush() {
		t.Fatal("no flush at doc threshold")
	}

	// Byte threshold.
	m2 := New(200, 1<<30)
	if m2.NeedsFlush() {
		t.Fatal("empty memtable wants flush")
	}
	for i := 0; !m2.NeedsFlush() && i < 1000; i++ {
		m2.AddToken("body", "term"+string(rune('a'+i%26))+string(rune('0'+i/26)), uint32(i), 0, true)
	}
	if !m2.NeedsFlush() {
		t.Fatal("byte threshold never tripped")
	}
	if m2.EstimatedBytes() < 200 {
		t.Fatalf("estimate %d below threshold but flagged", m2.EstimatedBytes())
	}
}

func TestEmpty(t *testing.T) {
	m := New(0, 0)
	if !m.Empty() {
		t.Fatal("new memtable not empty")
	}
	m.AddToken("f", "t", 1, 0, true)
	if m.Empty() {
		t.Fatal("memtable empty after add")
	}
}
