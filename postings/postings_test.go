package postings

import (
	"reflect"
	"testing"
)

func TestPForEncoder_Roundtrip(t *testing.T) {
	cases := [][]uint32{
		{},
		{0},
		{1, 2, 3, 4, 5},
		{0, 0, 0, 0},
		{1000000, 1, 2, 3},
		seq(BlockSize),
	}
	for ci, vals := range cases {
		enc := pforEncode(vals)
		got, err := pforDecode(enc, len(vals))
		if err != nil {
			t.Fatalf("case %d decode: %v", ci, err)
		}
		if len(vals) == 0 {
			if len(got) != 0 {
				t.Fatalf("case %d = %v want empty", ci, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, vals) {
			t.Fatalf("case %d = %v want %v", ci, got, vals)
		}
	}
}

func TestPForEncoder_ExceptionHandling(t *testing.T) {
	// Mostly small with a few large outliers: PFOR should pick a narrow width and
	// patch the outliers, and still round-trip exactly.
	vals := make([]uint32, BlockSize)
	for i := range vals {
		vals[i] = uint32(i % 16)
	}
	vals[5] = 1 << 20
	vals[100] = 1 << 25
	vals[120] = 999999
	enc := pforEncode(vals)
	if enc[0] != modePFOR {
		t.Fatalf("expected PFOR mode, got %d", enc[0])
	}
	got, err := pforDecode(enc, len(vals))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, vals) {
		t.Fatalf("exception roundtrip mismatch")
	}
}

func TestPForEncoder_VarintFallback(t *testing.T) {
	// All-large, all-distinct values drive the exception rate over threshold, so
	// the codec falls back to a varint stream.
	vals := make([]uint32, BlockSize)
	for i := range vals {
		vals[i] = uint32(1_000_000 + i*7919)
	}
	enc := pforEncode(vals)
	got, err := pforDecode(enc, len(vals))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, vals) {
		t.Fatalf("varint fallback roundtrip mismatch")
	}
}

func TestPostingsRoundtrip(t *testing.T) {
	docs := []uint32{1, 5, 9, 100, 250, 9000}
	freqs := []uint32{1, 2, 1, 3, 1, 4}
	positions := [][]uint32{
		{0},
		{3, 7},
		{1},
		{0, 4, 9},
		{2},
		{1, 2, 3, 10},
	}
	docBlob, posBlob, err := Encode(docs, freqs, positions)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(docBlob, posBlob)
	if err != nil {
		t.Fatal(err)
	}
	if r.Count() != len(docs) {
		t.Fatalf("count = %d want %d", r.Count(), len(docs))
	}
	for i := range docs {
		d, f, ok, err := r.Next()
		if err != nil || !ok {
			t.Fatalf("Next %d = %v %v", i, ok, err)
		}
		if d != docs[i] || f != freqs[i] {
			t.Fatalf("doc %d = (%d,%d) want (%d,%d)", i, d, f, docs[i], freqs[i])
		}
		pos, err := r.Positions()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pos, positions[i]) {
			t.Fatalf("positions doc %d = %v want %v", i, pos, positions[i])
		}
	}
	if _, _, ok, _ := r.Next(); ok {
		t.Fatal("Next past end returned ok")
	}
}

func TestPostingsDecoder_SkipTo(t *testing.T) {
	// Many docs spanning multiple blocks so SkipTo must jump blocks.
	n := BlockSize*3 + 17
	docs := make([]uint32, n)
	freqs := make([]uint32, n)
	for i := range docs {
		docs[i] = uint32(i*10 + 1)
		freqs[i] = uint32(i%4 + 1)
	}
	docBlob, posBlob, err := Encode(docs, freqs, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(docBlob, posBlob)
	if err != nil {
		t.Fatal(err)
	}

	// Skip into the third block.
	target := docs[BlockSize*2+5]
	d, f, ok, err := r.SkipTo(target)
	if err != nil || !ok {
		t.Fatalf("SkipTo = %v %v", ok, err)
	}
	if d != target || f != freqs[BlockSize*2+5] {
		t.Fatalf("SkipTo landed (%d,%d) want (%d,%d)", d, f, target, freqs[BlockSize*2+5])
	}

	// Skip to a value between two docs lands on the next existing doc.
	mid := docs[BlockSize*2+10] - 3
	d, _, ok, err = r.SkipTo(mid)
	if err != nil || !ok {
		t.Fatalf("SkipTo mid = %v %v", ok, err)
	}
	if d != docs[BlockSize*2+10] {
		t.Fatalf("SkipTo mid landed %d want %d", d, docs[BlockSize*2+10])
	}

	// Skip past the end.
	if _, _, ok, _ := r.SkipTo(docs[n-1] + 1); ok {
		t.Fatal("SkipTo past end returned ok")
	}
}

func FuzzPForRoundtrip(f *testing.F) {
	f.Add([]byte{1, 2, 3})
	f.Add([]byte{255, 0, 128, 64})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) == 0 || len(raw) > BlockSize {
			return
		}
		vals := make([]uint32, len(raw))
		for i, b := range raw {
			// Mix in some large values so exceptions and fallback are exercised.
			vals[i] = uint32(b)
			if b%5 == 0 {
				vals[i] = uint32(b) << 22
			}
		}
		enc := pforEncode(vals)
		got, err := pforDecode(enc, len(vals))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, vals) {
			t.Fatalf("roundtrip mismatch: %v -> %v", vals, got)
		}
	})
}

func seq(n int) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = uint32(i * 3)
	}
	return out
}
