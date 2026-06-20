package agg

import (
	"math"
	"testing"
)

// keysFromSlice returns a KeysReader over a per-doc keyword slice.
func keysFromSlice(vals []string) KeysReader {
	return func(d uint32) [][]byte {
		if int(d) >= len(vals) || vals[d] == "" {
			return nil
		}
		return [][]byte{[]byte(vals[d])}
	}
}

// numFromSlice returns a NumReader over a per-doc value slice; NaN means missing.
func numFromSlice(vals []float64) NumReader {
	return func(d uint32) (float64, bool) {
		if int(d) >= len(vals) || math.IsNaN(vals[d]) {
			return 0, false
		}
		return vals[d], true
	}
}

func TestTermsFacet(t *testing.T) {
	brands := []string{"acme", "globotech", "acme", "acme", "globotech", "nano"}
	a := NewTerms(keysFromSlice(brands), 10, false, nil)
	for d := range brands {
		a.Collect(uint32(d))
	}
	got := a.Result().Buckets
	want := map[string]uint64{"acme": 3, "globotech": 2, "nano": 1}
	if len(got) != 3 {
		t.Fatalf("bucket count %d want 3", len(got))
	}
	for _, b := range got {
		if want[b.Key] != b.Count {
			t.Fatalf("bucket %q count %d want %d", b.Key, b.Count, want[b.Key])
		}
	}
	// Count-descending order: acme first.
	if got[0].Key != "acme" {
		t.Fatalf("top bucket %q want acme", got[0].Key)
	}
}

func TestTermsFacetSize(t *testing.T) {
	brands := []string{"a", "a", "a", "b", "b", "c"}
	a := NewTerms(keysFromSlice(brands), 2, false, nil)
	for d := range brands {
		a.Collect(uint32(d))
	}
	got := a.Result().Buckets
	if len(got) != 2 || got[0].Key != "a" || got[1].Key != "b" {
		t.Fatalf("top-2 buckets wrong: %+v", got)
	}
}

func TestRangeFacet(t *testing.T) {
	prices := []float64{5, 15, 25, 35, 105, 0}
	ranges := []NumRange{
		{Key: "cheap", From: math.NaN(), To: 20},
		{Key: "mid", From: 20, To: 100},
		{Key: "lux", From: 100, To: math.NaN()},
	}
	a := NewRange(numFromSlice(prices), ranges)
	for d := range prices {
		a.Collect(uint32(d))
	}
	got := a.Result().Buckets
	want := []uint64{3, 2, 1} // {5,15,0}, {25,35}, {105}
	for i, b := range got {
		if b.Count != want[i] {
			t.Fatalf("range %q count %d want %d", b.Key, b.Count, want[i])
		}
	}
}

func TestDateHistogram(t *testing.T) {
	// Values are epoch-day-like buckets; interval 10.
	vals := []float64{0, 3, 11, 12, 25, 27}
	a := NewHistogram(numFromSlice(vals), 10, 0)
	for d := range vals {
		a.Collect(uint32(d))
	}
	got := a.Result().Buckets
	want := map[string]uint64{"0": 2, "10": 2, "20": 2}
	if len(got) != 3 {
		t.Fatalf("bucket count %d want 3", len(got))
	}
	for _, b := range got {
		if want[b.Key] != b.Count {
			t.Fatalf("bucket %q count %d want %d", b.Key, b.Count, want[b.Key])
		}
	}
	// Ascending key order.
	if got[0].Key != "0" || got[2].Key != "20" {
		t.Fatalf("bucket order wrong: %+v", got)
	}
}

func TestHistogramNegative(t *testing.T) {
	vals := []float64{-15, -5, 5, 15}
	a := NewHistogram(numFromSlice(vals), 10, 0)
	for d := range vals {
		a.Collect(uint32(d))
	}
	got := a.Result().Buckets
	want := map[string]uint64{"-20": 1, "-10": 1, "0": 1, "10": 1}
	for _, b := range got {
		if want[b.Key] != b.Count {
			t.Fatalf("bucket %q count %d want %d", b.Key, b.Count, want[b.Key])
		}
	}
}

func TestMetricAgg_MinMaxAvgSum(t *testing.T) {
	vals := []float64{10, 20, 30, 40}
	check := func(metric string, want float64) {
		a := NewStats(numFromSlice(vals), metric)
		for d := range vals {
			a.Collect(uint32(d))
		}
		if got := a.Result().Value; got != want {
			t.Fatalf("%s = %v want %v", metric, got, want)
		}
	}
	check("min", 10)
	check("max", 40)
	check("sum", 100)
	check("avg", 25)
	check("count", 4)

	a := NewStats(numFromSlice(vals), "stats")
	for d := range vals {
		a.Collect(uint32(d))
	}
	v := a.Result().Values
	if v["min"] != 10 || v["max"] != 40 || v["sum"] != 100 || v["avg"] != 25 || v["count"] != 4 {
		t.Fatalf("stats wrong: %+v", v)
	}
}

func TestNestedAgg(t *testing.T) {
	brands := []string{"acme", "acme", "globotech", "globotech"}
	prices := []float64{10, 30, 5, 25}
	a := NewTerms(keysFromSlice(brands), 10, false, func() map[string]Agg {
		return map[string]Agg{"avg_price": NewStats(numFromSlice(prices), "avg")}
	})
	for d := range brands {
		a.Collect(uint32(d))
	}
	got := a.Result().Buckets
	for _, b := range got {
		avg := b.Subs["avg_price"].Value
		switch b.Key {
		case "acme":
			if avg != 20 {
				t.Fatalf("acme avg %v want 20", avg)
			}
		case "globotech":
			if avg != 15 {
				t.Fatalf("globotech avg %v want 15", avg)
			}
		}
	}
}

func TestCardinality(t *testing.T) {
	// 1000 distinct keys: HLL should land within a few percent.
	keys := make([]string, 1000)
	for i := range keys {
		keys[i] = "k" + formatNum(float64(i))
	}
	a := NewCardinalityKeyword(keysFromSlice(keys))
	for d := range keys {
		a.Collect(uint32(d))
	}
	est := a.Result().Value
	if math.Abs(est-1000) > 50 {
		t.Fatalf("cardinality estimate %v not within 5%% of 1000", est)
	}
}

func TestPercentiles(t *testing.T) {
	vals := make([]float64, 1000)
	for i := range vals {
		vals[i] = float64(i) // 0..999 uniform
	}
	a := NewPercentiles(numFromSlice(vals), []float64{50, 95, 99})
	for d := range vals {
		a.Collect(uint32(d))
	}
	v := a.Result().Values
	if math.Abs(v["50"]-500) > 30 {
		t.Fatalf("p50 = %v want ~500", v["50"])
	}
	if math.Abs(v["95"]-950) > 30 {
		t.Fatalf("p95 = %v want ~950", v["95"])
	}
	if math.Abs(v["99"]-990) > 20 {
		t.Fatalf("p99 = %v want ~990", v["99"])
	}
}
