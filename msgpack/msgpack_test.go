package msgpack

import (
	"bytes"
	"reflect"
	"testing"
)

func TestRoundTripScalars(t *testing.T) {
	cases := []struct {
		in   any
		want any
	}{
		{nil, nil},
		{true, true},
		{false, false},
		{int(0), int64(0)},
		{int(127), int64(127)},
		{int(-1), int64(-1)},
		{int(-32), int64(-32)},
		{int(-33), int64(-33)},
		{int(255), int64(255)},
		{int(256), int64(256)},
		{int(-129), int64(-129)},
		{int64(1 << 40), int64(1 << 40)},
		{int64(-(1 << 40)), int64(-(1 << 40))},
		{uint64(1 << 63), uint64(1 << 63)},
		{float64(3.14159), float64(3.14159)},
		{"", ""},
		{"hello world", "hello world"},
		{[]byte{1, 2, 3}, []byte{1, 2, 3}},
	}
	for _, c := range cases {
		b, err := Marshal(c.in)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", c.in, err)
		}
		got, n, err := Unmarshal(b)
		if err != nil {
			t.Fatalf("Unmarshal(%v): %v", c.in, err)
		}
		if n != len(b) {
			t.Errorf("consumed %d of %d bytes for %v", n, len(b), c.in)
		}
		if bs, ok := c.want.([]byte); ok {
			if !bytes.Equal(got.([]byte), bs) {
				t.Errorf("bytes %v -> %v", c.in, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v (%T) -> %v (%T), want %v", c.in, c.in, got, got, c.want)
		}
	}
}

func TestRoundTripMap(t *testing.T) {
	in := map[string]any{
		"title": "the quick brown fox",
		"count": int64(42),
		"price": float64(9.99),
		"tags":  []any{"a", "b", "c"},
		"live":  true,
		"meta":  map[string]any{"k": "v", "n": int64(7)},
	}
	b, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", got, in)
	}
}

func TestDeterministicMapEncoding(t *testing.T) {
	m := map[string]any{"z": int64(1), "a": int64(2), "m": int64(3)}
	first, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatal("map encoding is not deterministic across calls")
		}
	}
}

func TestUnmarshalTruncated(t *testing.T) {
	b, err := Marshal("hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Unmarshal(b[:len(b)-2]); err == nil {
		t.Fatal("expected error on truncated input")
	}
}
