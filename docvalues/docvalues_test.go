package docvalues

import (
	"math"
	"math/rand"
	"testing"
)

// roundtripNumeric encodes values into a NUMERIC column and reads each back.
func roundtripNumeric(t *testing.T, vals []int64) {
	t.Helper()
	w := NewNumericWriter(uint32(len(vals)), false)
	for i, v := range vals {
		w.Set(uint32(i), v)
	}
	col, err := OpenColumn(w.Bytes())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	nc := col.(NumericColumn)
	if nc.DocCount() != uint32(len(vals)) {
		t.Fatalf("doc count %d != %d", nc.DocCount(), len(vals))
	}
	for i, v := range vals {
		if got := nc.Int64(uint32(i)); got != v {
			t.Fatalf("doc %d: got %d want %d", i, got, v)
		}
		if !nc.HasValue(uint32(i)) {
			t.Fatalf("doc %d: HasValue false", i)
		}
	}
}

func TestDocValuesRoundtripInt64(t *testing.T) {
	cases := map[string][]int64{
		"constant":   {7, 7, 7, 7, 7},
		"small":      {999, 1499, 999, 2999, 999, 1499, 4999, 999},
		"gcd":        {0, 500, 1000, 1500, 2000},
		"monotone":   {100, 101, 103, 106, 110, 200, 5000},
		"negative":   {-5, -3, -1, 0, 2, 4},
		"wide":       {math.MinInt64, 0, math.MaxInt64, -1, 1},
		"single":     {42},
		"twovalues":  {1, 2, 1, 2, 1, 2},
		"largetable": rangeVals(300, 5),
	}
	for name, vals := range cases {
		t.Run(name, func(t *testing.T) { roundtripNumeric(t, vals) })
	}
}

// rangeVals builds n values cycling through distinct count d values.
func rangeVals(n, d int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64((i % d) * 17)
	}
	return out
}

func TestDocValuesRoundtripBlocks(t *testing.T) {
	// Span more than two blocks so block boundaries are exercised.
	n := BlockSize*2 + 123
	vals := make([]int64, n)
	rng := rand.New(rand.NewSource(1))
	for i := range vals {
		vals[i] = rng.Int63n(1_000_000) - 500_000
	}
	roundtripNumeric(t, vals)
}

func TestDocValuesRoundtripFloat64(t *testing.T) {
	vals := []float64{-1e9, -3.5, -0.25, 0.0, 0.25, 3.5, 42.0, 1e9, math.MaxFloat64, -math.MaxFloat64}
	w := NewNumericWriter(uint32(len(vals)), true)
	for i, v := range vals {
		w.SetFloat(uint32(i), v)
	}
	col, err := OpenColumn(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	nc := col.(NumericColumn)
	for i, v := range vals {
		if got := nc.Float64(uint32(i)); got != v {
			t.Fatalf("doc %d: got %v want %v", i, got, v)
		}
	}
	// The stored int order must match the float order (sortable transform).
	for i := 1; i < len(vals); i++ {
		a, b := nc.Int64(uint32(i-1)), nc.Int64(uint32(i))
		if (vals[i-1] < vals[i]) != (a < b) {
			t.Fatalf("order mismatch at %d: floats %v,%v ints %d,%d", i, vals[i-1], vals[i], a, b)
		}
	}
}

func TestDocValuesMissing(t *testing.T) {
	w := NewNumericWriter(6, false)
	w.Set(0, 10)
	w.Set(2, 30)
	w.Set(5, 60)
	col, err := OpenColumn(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	nc := col.(NumericColumn)
	present := map[uint32]int64{0: 10, 2: 30, 5: 60}
	for i := range uint32(6) {
		want, ok := present[i]
		if nc.HasValue(i) != ok {
			t.Fatalf("doc %d: HasValue %v want %v", i, nc.HasValue(i), ok)
		}
		if ok && nc.Int64(i) != want {
			t.Fatalf("doc %d: got %d want %d", i, nc.Int64(i), want)
		}
	}
}

func TestDocValuesRoundtripOrdinal(t *testing.T) {
	vals := []string{"Globotech", "Acme", "Globotech", "NanoWidget", "Acme", "QuantumCore"}
	w := NewSortedWriter(uint32(len(vals)))
	for i, v := range vals {
		w.Set(uint32(i), []byte(v))
	}
	col, err := OpenColumn(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	sc := col.(SortedColumn)
	// Ordinals follow lexicographic order: Acme=0 Globotech=1 NanoWidget=2 QuantumCore=3.
	wantOrd := map[string]int32{"Acme": 0, "Globotech": 1, "NanoWidget": 2, "QuantumCore": 3}
	if sc.OrdCount() != 4 {
		t.Fatalf("ord count %d want 4", sc.OrdCount())
	}
	for i, v := range vals {
		ord := sc.OrdAt(uint32(i))
		if ord != wantOrd[v] {
			t.Fatalf("doc %d %q: ord %d want %d", i, v, ord, wantOrd[v])
		}
		if string(sc.LookupOrd(uint32(ord))) != v {
			t.Fatalf("LookupOrd(%d) = %q want %q", ord, sc.LookupOrd(uint32(ord)), v)
		}
	}
}

func TestDocValuesOrdinalMissing(t *testing.T) {
	w := NewSortedWriter(4)
	w.Set(0, []byte("b"))
	w.Set(2, []byte("a"))
	col, _ := OpenColumn(w.Bytes())
	sc := col.(SortedColumn)
	if sc.OrdAt(0) != 1 || sc.OrdAt(2) != 0 {
		t.Fatalf("ords: %d %d want 1 0", sc.OrdAt(0), sc.OrdAt(2))
	}
	if sc.OrdAt(1) != -1 || sc.OrdAt(3) != -1 {
		t.Fatalf("missing ords should be -1, got %d %d", sc.OrdAt(1), sc.OrdAt(3))
	}
}

func TestDocValuesSortedSet(t *testing.T) {
	w := NewSortedSetWriter(3)
	w.Add(0, []byte("red"))
	w.Add(0, []byte("blue"))
	w.Add(0, []byte("red")) // duplicate collapses
	w.Add(2, []byte("green"))
	col, err := OpenColumn(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	ss := col.(SortedSetColumn)
	// Dict sorted: blue=0 green=1 red=2.
	got := ss.OrdinalsFor(0)
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("doc0 ords %v want [0 2]", got)
	}
	if ss.OrdinalsFor(1) != nil {
		t.Fatalf("doc1 should be empty")
	}
	g := ss.OrdinalsFor(2)
	if len(g) != 1 || string(ss.LookupOrd(g[0])) != "green" {
		t.Fatalf("doc2 ords %v", g)
	}
}

func TestGeoColumn(t *testing.T) {
	type pt struct{ lat, lon float64 }
	pts := []pt{{37.7749, -122.4194}, {40.7128, -74.0060}, {-33.8688, 151.2093}}
	w := NewGeoWriter(uint32(len(pts)))
	for i, p := range pts {
		w.Set(uint32(i), p.lat, p.lon)
	}
	col, err := OpenColumn(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	gc := col.(GeoColumn)
	for i, p := range pts {
		lat, lon := gc.LatLon(uint32(i))
		if math.Abs(lat-p.lat) > 1e-4 || math.Abs(lon-p.lon) > 1e-4 {
			t.Fatalf("pt %d: got %v,%v want %v,%v", i, lat, lon, p.lat, p.lon)
		}
	}
}

func TestBKDRange(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const n = 2000
	vals := make([]int64, n)
	w := NewBKDWriter()
	for i := range vals {
		v := rng.Int63n(1000) - 500
		vals[i] = v
		w.Add(uint32(i), v)
	}
	bkd, err := OpenBKD(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	check := func(lo, hi int64) {
		want := map[uint32]bool{}
		for i, v := range vals {
			if v >= lo && v <= hi {
				want[uint32(i)] = true
			}
		}
		got := bkd.RangeSearch(lo, hi)
		if len(got) != len(want) {
			t.Fatalf("range [%d,%d]: got %d docs want %d", lo, hi, len(got), len(want))
		}
		for j, d := range got {
			if !want[d] {
				t.Fatalf("range [%d,%d]: unexpected doc %d", lo, hi, d)
			}
			if j > 0 && got[j-1] > d {
				t.Fatalf("range result not sorted at %d", j)
			}
		}
	}
	for range 50 {
		a := rng.Int63n(1000) - 500
		b := rng.Int63n(1000) - 500
		if a > b {
			a, b = b, a
		}
		check(a, b)
	}
	check(math.MinInt64, math.MaxInt64) // all
	check(10000, 20000)                 // none
}

func TestBitpackRoundtrip(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for _, bw := range []uint{1, 2, 3, 5, 7, 8, 13, 17, 31, 64} {
		n := 100
		src := make([]uint64, n)
		mask := uint64(1)<<bw - 1
		if bw == 64 {
			mask = ^uint64(0)
		}
		for i := range src {
			src[i] = rng.Uint64() & mask
		}
		dst := make([]byte, packedLen(n, bw))
		packBits(dst, src, bw)
		for i := range src {
			if got := unpackSingle(dst, bw, uint32(i)); got != src[i] {
				t.Fatalf("bw=%d idx=%d got %d want %d", bw, i, got, src[i])
			}
		}
	}
}
