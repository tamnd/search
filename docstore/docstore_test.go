package docstore

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/tamnd/search/msgpack"
)

// memKV is an in-memory namespaced key/value store for exercising the docstore
// without a file.
type memKV struct {
	m map[string][]byte
}

func newMemKV() *memKV { return &memKV{m: make(map[string][]byte)} }

func (k *memKV) key(ns byte, key []byte) string { return string(append([]byte{ns}, key...)) }

func (k *memKV) Get(ns byte, key []byte) ([]byte, bool, error) {
	v, ok := k.m[k.key(ns, key)]
	return v, ok, nil
}

func (k *memKV) Put(ns byte, key, val []byte) error {
	cp := make([]byte, len(val))
	copy(cp, val)
	k.m[k.key(ns, key)] = cp
	return nil
}

func TestDocStoreRoundtrip(t *testing.T) {
	s := New(newMemKV(), 0x0C)
	const n = 10000
	mk := func(i int) map[string]any {
		return map[string]any{
			"title": fmt.Sprintf("document number %d about search", i),
			"count": int64(i),
			"price": float64(i) + 0.5,
			"live":  i%2 == 0,
			"tags":  []any{"alpha", fmt.Sprintf("t%d", i%7)},
		}
	}
	for i := range n {
		if err := s.Put(uint64(i+1), mk(i)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	for i := range n {
		got, ok, err := s.Get(uint64(i + 1))
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("doc %d missing", i+1)
		}
		if !reflect.DeepEqual(got, mk(i)) {
			t.Fatalf("doc %d mismatch:\n got %#v\nwant %#v", i+1, got, mk(i))
		}
	}
	if _, ok, _ := s.Get(uint64(n + 1)); ok {
		t.Fatal("expected miss for absent doc")
	}
}

func TestDocStoreCompression(t *testing.T) {
	// A full block of repetitive English text exceeds the compression threshold
	// and must shrink under Zstd.
	const docs = 512
	sentence := "the quick brown fox jumps over the lazy dog while the sun rises slowly over the quiet valley"
	encoded := make([][]byte, docs)
	rawTotal := 0
	for i := range docs {
		b, err := msgpack.Marshal(map[string]any{
			"id":   int64(i),
			"body": fmt.Sprintf("%s (paragraph %d of the collection)", sentence, i),
		})
		if err != nil {
			t.Fatal(err)
		}
		encoded[i] = b
		rawTotal += len(b)
	}
	if rawTotal < CompressThreshold {
		t.Fatalf("test block raw size %d below threshold %d", rawTotal, CompressThreshold)
	}
	blob, err := EncodeBlock(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if blob[0] != codecZstd {
		t.Fatalf("block was not Zstd-compressed (codec=%d)", blob[0])
	}
	if len(blob) >= rawTotal {
		t.Fatalf("compressed block %d not smaller than raw %d", len(blob), rawTotal)
	}
	t.Logf("compression: raw=%d compressed=%d ratio=%.2fx", rawTotal, len(blob), float64(rawTotal)/float64(len(blob)))

	// The block must still decode back to the exact documents.
	out, err := DecodeBlock(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != docs {
		t.Fatalf("decoded %d docs, want %d", len(out), docs)
	}
	for i := range docs {
		if !reflect.DeepEqual(out[i], encoded[i]) {
			t.Fatalf("doc %d round-trip mismatch", i)
		}
	}
}

func TestSmallBlockStoredRaw(t *testing.T) {
	blob, err := EncodeBlock([][]byte{[]byte("tiny")})
	if err != nil {
		t.Fatal(err)
	}
	if blob[0] != codecRaw {
		t.Fatalf("small block should be raw, codec=%d", blob[0])
	}
}
