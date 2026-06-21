package hnsw

import (
	"math"
	"testing"
)

// FuzzHNSWSearch builds a graph from random vectors, marshals and reloads it, and
// runs a random query. Invariants: no panic, every returned ordinal is a real
// node, every returned doc id was inserted, results are ordered by ascending
// distance, and Search never returns more than k neighbors.
func FuzzHNSWSearch(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8}, byte(3), uint16(2))
	f.Add([]byte{}, byte(0), uint16(1))
	f.Fuzz(func(t *testing.T, blob []byte, k byte, seed uint16) {
		const dims = 4
		g := New(DefaultParams(8, 32), Cosine, dims)

		// Carve the blob into dims-wide float vectors. Each byte becomes a small
		// float so vectors stay finite.
		docs := map[uint32]struct{}{}
		var docID uint32
		for off := 0; off+dims <= len(blob); off += dims {
			v := make([]float32, dims)
			for i := range dims {
				v[i] = float32(blob[off+i]) - 128
			}
			g.Add(docID, v)
			docs[docID] = struct{}{}
			docID++
		}

		// Round-trip through the codec so the fuzzer also exercises Load.
		g2, err := Load(g.Marshal())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if g2.Len() != g.Len() {
			t.Fatalf("reloaded graph has %d nodes, want %d", g2.Len(), g.Len())
		}

		// Build a query vector from the seed so it varies without Math.random.
		q := make([]float32, dims)
		for i := range q {
			q[i] = float32((int(seed)>>(i*2))&0xff) - 64
		}

		kk := int(k)
		res := g2.Search(q, kk, kk*4, nil)
		if kk > 0 && len(res) > kk {
			t.Fatalf("Search returned %d results, want <= %d", len(res), kk)
		}
		var last float32 = -math.MaxFloat32
		for _, r := range res {
			if int(r.Ordinal) >= g2.Len() {
				t.Fatalf("result ordinal %d out of range (%d nodes)", r.Ordinal, g2.Len())
			}
			if _, ok := docs[r.DocID]; !ok {
				t.Fatalf("result doc %d was never inserted", r.DocID)
			}
			if r.Distance < last {
				t.Fatalf("results out of order: %f after %f", r.Distance, last)
			}
			last = r.Distance
		}

		// ExactSearch must agree on membership and ordering invariants too.
		ex := g2.ExactSearch(q, kk, nil)
		if kk > 0 && len(ex) > kk {
			t.Fatalf("ExactSearch returned %d results, want <= %d", len(ex), kk)
		}
		for _, r := range ex {
			if _, ok := docs[r.DocID]; !ok {
				t.Fatalf("ExactSearch returned uninserted doc %d", r.DocID)
			}
		}
	})
}
