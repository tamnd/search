package segment

import (
	"reflect"
	"testing"
)

func TestDeleteBitmapAddContains(t *testing.T) {
	bm := NewDeleteBitmap(100, 50)
	if !bm.Add(105) {
		t.Fatal("Add(105) reported no change on first set")
	}
	if bm.Add(105) {
		t.Fatal("Add(105) reported a change on repeat")
	}
	if !bm.Contains(105) {
		t.Fatal("Contains(105) false after Add")
	}
	if bm.Contains(104) {
		t.Fatal("Contains(104) true without Add")
	}
	// Out-of-range doc-ids are ignored.
	if bm.Add(99) || bm.Add(200) {
		t.Fatal("Add accepted an out-of-range doc-id")
	}
	if bm.Count() != 1 {
		t.Fatalf("Count = %d, want 1", bm.Count())
	}
}

func TestDeleteBitmapRoundtrip(t *testing.T) {
	m := &Meta{ID: 7, BaseDoc: 1000, MaxDoc: 1064}
	bm := NewDeleteBitmap(m.BaseDoc, m.MaxDoc-m.BaseDoc)
	for _, d := range []uint32{1000, 1001, 1030, 1063} {
		bm.Add(d)
	}
	kv := newMemKV()
	if err := StoreDeletes(kv, m, bm); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDeletes(kv, m)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count() != 4 {
		t.Fatalf("Count = %d, want 4", got.Count())
	}
	want := []uint32{1000, 1001, 1030, 1063}
	if ids := got.AppendTo(nil); !reflect.DeepEqual(ids, want) {
		t.Fatalf("AppendTo = %v, want %v", ids, want)
	}
}

func TestLoadDeletesAbsent(t *testing.T) {
	kv := newMemKV()
	m := &Meta{ID: 1, BaseDoc: 0, MaxDoc: 10}
	bm, err := LoadDeletes(kv, m)
	if err != nil {
		t.Fatal(err)
	}
	if !bm.Empty() {
		t.Fatal("absent bitmap should be empty")
	}
}

func TestTieredPolicySelect(t *testing.T) {
	p := NewTieredPolicy()
	// Five same-size segments share a tier; default threshold is four, so the
	// policy returns the four smallest by id.
	set := &SegmentSet{}
	for id := uint64(1); id <= 5; id++ {
		set.segments = append(set.segments, &Segment{meta: &Meta{ID: id, DocCount: 100}})
	}
	group := p.Select(set)
	if len(group) != 4 {
		t.Fatalf("selected %d, want 4", len(group))
	}
	for i, s := range group {
		if s.meta.ID != uint64(i+1) {
			t.Fatalf("group[%d] id = %d, want %d", i, s.meta.ID, i+1)
		}
	}
}

func TestTieredPolicyNoSelection(t *testing.T) {
	p := NewTieredPolicy()
	set := &SegmentSet{}
	for id := uint64(1); id <= 4; id++ {
		set.segments = append(set.segments, &Segment{meta: &Meta{ID: id, DocCount: 100}})
	}
	if g := p.Select(set); g != nil {
		t.Fatalf("expected no selection at threshold, got %d", len(g))
	}
}
