package segment

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/memtable"
)

// memKV is an in-memory catalog KV for tests.
type memKV struct {
	m map[byte]map[string][]byte
}

func newMemKV() *memKV { return &memKV{m: make(map[byte]map[string][]byte)} }

func (k *memKV) Get(ns byte, key []byte) ([]byte, bool, error) {
	if sub, ok := k.m[ns]; ok {
		if v, ok := sub[string(key)]; ok {
			return v, true, nil
		}
	}
	return nil, false, nil
}

func (k *memKV) Put(ns byte, key, val []byte) error {
	if k.m[ns] == nil {
		k.m[ns] = make(map[string][]byte)
	}
	cp := make([]byte, len(val))
	copy(cp, val)
	k.m[ns][string(key)] = cp
	return nil
}

func (k *memKV) Scan(ns byte, fn func(key, val []byte) bool) error {
	sub := k.m[ns]
	keys := make([]string, 0, len(sub))
	for key := range sub {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !fn([]byte(key), sub[key]) {
			break
		}
	}
	return nil
}

// sampleMemtable builds a small positional memtable for the body field.
func sampleMemtable() *memtable.MemTable {
	mt := memtable.New(0, 0)
	docs := []struct {
		id   uint32
		toks []string
	}{
		{1, []string{"the", "quick", "brown", "fox"}},
		{2, []string{"the", "lazy", "dog"}},
		{3, []string{"quick", "quick", "fox", "jumps"}},
	}
	for _, d := range docs {
		for pos, tok := range d.toks {
			mt.AddToken("body", tok, d.id, uint32(pos), true)
		}
		mt.AddDoc()
	}
	return mt
}

func TestSegmentFlush_TermCount(t *testing.T) {
	kv := newMemKV()
	mt := sampleMemtable()
	meta, err := Flush(kv, 1, mt, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Fields) != 1 || meta.Fields[0].Name != "body" {
		t.Fatalf("fields = %+v", meta.Fields)
	}
	fm := meta.Fields[0]
	// distinct terms: the, quick, brown, fox, lazy, dog, jumps = 7
	if fm.TermCount != 7 {
		t.Fatalf("term count = %d want 7", fm.TermCount)
	}
	if fm.DocCount != 3 {
		t.Fatalf("field doc count = %d want 3", fm.DocCount)
	}
	// total term freq: the(2)+quick(3)+brown(1)+fox(2)+lazy(1)+dog(1)+jumps(1)=11
	if fm.SumTotalTermFreq != 11 {
		t.Fatalf("sum ttf = %d want 11", fm.SumTotalTermFreq)
	}
	// sum doc freq: the(2)+quick(2)+brown(1)+fox(2)+lazy(1)+dog(1)+jumps(1)=10
	if fm.SumDocFreq != 10 {
		t.Fatalf("sum df = %d want 10", fm.SumDocFreq)
	}
}

func TestSegmentFlush_PostingRoundtrip(t *testing.T) {
	kv := newMemKV()
	mt := sampleMemtable()
	if _, err := Flush(kv, 1, mt, 0, 4); err != nil {
		t.Fatal(err)
	}
	seg, err := Open(kv, 1)
	if err != nil {
		t.Fatal(err)
	}
	fr, ok, err := seg.Field(kv, "body")
	if err != nil || !ok {
		t.Fatalf("Field body = %v %v", ok, err)
	}

	// "quick" appears in docs 1 and 3, freq 1 and 2.
	r, ok, err := fr.Postings("quick")
	if err != nil || !ok {
		t.Fatalf("Postings(quick) = %v %v", ok, err)
	}
	var gotDocs, gotFreqs []uint32
	for {
		d, f, ok, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		gotDocs = append(gotDocs, d)
		gotFreqs = append(gotFreqs, f)
	}
	if !reflect.DeepEqual(gotDocs, []uint32{1, 3}) || !reflect.DeepEqual(gotFreqs, []uint32{1, 2}) {
		t.Fatalf("quick postings = %v %v", gotDocs, gotFreqs)
	}

	// A missing term.
	if _, ok, _ := fr.Postings("nonexistent"); ok {
		t.Fatal("missing term reported present")
	}

	// Term inventory.
	terms, err := fr.Terms()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"brown", "dog", "fox", "jumps", "lazy", "quick", "the"}
	if !reflect.DeepEqual(terms, want) {
		t.Fatalf("terms = %v", terms)
	}
}

func TestSegmentFlush_PositionRoundtrip(t *testing.T) {
	kv := newMemKV()
	mt := sampleMemtable()
	if _, err := Flush(kv, 1, mt, 0, 4); err != nil {
		t.Fatal(err)
	}
	seg, _ := Open(kv, 1)
	fr, _, err := seg.Field(kv, "body")
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := fr.Postings("quick")
	if err != nil {
		t.Fatal(err)
	}
	// doc 1: "quick" at position 1.
	if _, _, ok, _ := r.Next(); !ok {
		t.Fatal("no doc 1")
	}
	pos, err := r.Positions()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pos, []uint32{1}) {
		t.Fatalf("doc1 quick positions = %v want [1]", pos)
	}
	// doc 3: "quick" at positions 0 and 1.
	if _, _, ok, _ := r.Next(); !ok {
		t.Fatal("no doc 3")
	}
	pos, err = r.Positions()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pos, []uint32{0, 1}) {
		t.Fatalf("doc3 quick positions = %v want [0 1]", pos)
	}
}

func TestSegmentSetAndStats(t *testing.T) {
	kv := newMemKV()
	if _, err := Flush(kv, 1, sampleMemtable(), 0, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := Flush(kv, 2, sampleMemtable(), 0, 4); err != nil {
		t.Fatal(err)
	}
	set, err := LoadSet(kv)
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() != 2 {
		t.Fatalf("set len = %d want 2", set.Len())
	}
	if set.Segments()[0].ID() != 1 || set.Segments()[1].ID() != 2 {
		t.Fatalf("segment order = %d %d", set.Segments()[0].ID(), set.Segments()[1].ID())
	}
	// Stats accumulate across both segments.
	st, err := LoadFieldStats(kv, "body")
	if err != nil {
		t.Fatal(err)
	}
	if st.SumTotalTermFreq != 22 || st.DocCount != 6 {
		t.Fatalf("stats = %+v", st)
	}
	_ = catalog.NSStats
}

// TestGoldenSegment flushes a fixed synthetic corpus and asserts the serialized
// FST and postings are byte-stable. This replaces the roadmap's Wikipedia-lead
// fixture with a deterministic generated corpus (documented deviation): the
// engine has no bundled text corpus, and a generated one pins the encoders just
// as well.
func TestGoldenSegment(t *testing.T) {
	kv := newMemKV()
	mt := goldenCorpus()
	meta, err := Flush(kv, 1, mt, 0, uint32(mt.DocCount())+1)
	if err != nil {
		t.Fatal(err)
	}

	h := sha256.New()
	fb, _, _ := kv.Get(catalog.NSSegFST, segKey(1, "body"))
	pb, _, _ := kv.Get(catalog.NSSegPostings, segKey(1, "body"))
	h.Write(fb)
	h.Write(pb)
	got := hex.EncodeToString(h.Sum(nil))

	if meta.Fields[0].TermCount != 16 {
		t.Fatalf("golden term count = %d want 16", meta.Fields[0].TermCount)
	}
	if got != golden {
		t.Fatalf("golden digest changed (encoders are not byte-stable):\n got %s\nwant %s\n(fst=%dB postings=%dB)",
			got, golden, len(fb), len(pb))
	}
}

// golden pins the digest of the FST and postings for the synthetic corpus. A
// change here means an encoder's byte output changed and the on-disk format is
// no longer compatible.
const golden = "8611b609f3e87d9f8ca87cff70201f53114a50ed2efc1071ea2eae5998f4647d"

// goldenCorpus builds a deterministic 500-document corpus from a fixed word list.
func goldenCorpus() *memtable.MemTable {
	words := []string{
		"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta",
		"iota", "kappa", "lambda", "mu", "nu", "xi", "omicron", "pi",
	}
	mt := memtable.New(1<<40, 1<<30)
	for d := 1; d <= 500; d++ {
		// Each document gets a deterministic pseudo-random selection of words.
		n := 5 + d%12
		for pos := range n {
			w := words[(d*7+pos*13)%len(words)]
			mt.AddToken("body", w, uint32(d), uint32(pos), true)
		}
		mt.AddDoc()
	}
	return mt
}

func FuzzSegmentFlush(f *testing.F) {
	f.Add([]byte("the quick brown fox"), uint32(3))
	f.Add([]byte("a a a b c"), uint32(2))
	f.Fuzz(func(t *testing.T, text []byte, ndocs uint32) {
		if ndocs == 0 || ndocs > 50 || len(text) == 0 || len(text) > 4096 {
			return
		}
		toks := tokens(text)
		if len(toks) == 0 {
			return
		}
		mt := memtable.New(0, 0)
		// reference: term -> docID -> (freq, positions)
		ref := map[string]map[uint32][]uint32{}
		for d := uint32(1); d <= ndocs; d++ {
			for pos, tok := range toks {
				mt.AddToken("body", tok, d, uint32(pos), true)
				if ref[tok] == nil {
					ref[tok] = map[uint32][]uint32{}
				}
				ref[tok][d] = append(ref[tok][d], uint32(pos))
			}
			mt.AddDoc()
		}
		kv := newMemKV()
		if _, err := Flush(kv, 1, mt, 0, ndocs+1); err != nil {
			t.Fatal(err)
		}
		seg, err := Open(kv, 1)
		if err != nil {
			t.Fatal(err)
		}
		fr, ok, err := seg.Field(kv, "body")
		if err != nil || !ok {
			t.Fatalf("field: %v %v", ok, err)
		}
		for term, byDoc := range ref {
			r, ok, err := fr.Postings(term)
			if err != nil || !ok {
				t.Fatalf("postings(%q): %v %v", term, ok, err)
			}
			for {
				d, freq, ok, err := r.Next()
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				wantPos := byDoc[d]
				if int(freq) != len(wantPos) {
					t.Fatalf("term %q doc %d freq %d want %d", term, d, freq, len(wantPos))
				}
				pos, err := r.Positions()
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(pos, wantPos) {
					t.Fatalf("term %q doc %d pos %v want %v", term, d, pos, wantPos)
				}
			}
		}
	})
}

// tokens splits text on spaces into non-empty tokens.
func tokens(b []byte) []string {
	var out []string
	start := -1
	for i, c := range b {
		if c == ' ' || c == '\n' || c == '\t' {
			if start >= 0 {
				out = append(out, string(b[start:i]))
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, string(b[start:]))
	}
	// dedup adjacent identical handling not needed; positions are per occurrence.
	return out
}

func BenchmarkSegmentFlush(b *testing.B) {
	mt := benchMemtable(100_000)
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		kv := newMemKV()
		if _, err := Flush(kv, uint64(i+1), mt, 0, 100_001); err != nil {
			b.Fatal(err)
		}
	}
}

func benchMemtable(n int) *memtable.MemTable {
	mt := memtable.New(1<<40, 1<<30)
	for d := 1; d <= n; d++ {
		for pos := range 10 {
			w := fmt.Sprintf("term%d", (d*7+pos*13)%2000)
			mt.AddToken("body", w, uint32(d), uint32(pos), true)
		}
		mt.AddDoc()
	}
	return mt
}
