package docvalues

import (
	"encoding/binary"
	"testing"
)

// FuzzBKDRange builds a points index from random (docID, value) pairs, serializes
// and reopens it, then runs a random range query. Invariants: no panic, every
// returned doc id was inserted, every returned point's value lies in the query
// range, and the result count matches a brute-force scan.
func FuzzBKDRange(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 5}, int64(0), int64(10))
	f.Add([]byte{}, int64(-1), int64(1))
	f.Fuzz(func(t *testing.T, blob []byte, lo, hi int64) {
		// Decode the blob into points: 12 bytes each, u32 docID then i64 value.
		type pt struct {
			doc uint32
			val int64
		}
		var pts []pt
		w := NewBKDWriter()
		for off := 0; off+12 <= len(blob); off += 12 {
			doc := binary.BigEndian.Uint32(blob[off:])
			val := int64(binary.BigEndian.Uint64(blob[off+4:]))
			w.Add(doc, val)
			pts = append(pts, pt{doc, val})
		}

		tree, err := OpenBKD(w.Bytes())
		if err != nil {
			t.Fatalf("OpenBKD: %v", err)
		}
		if tree.Count() != len(pts) {
			t.Fatalf("Count = %d, want %d", tree.Count(), len(pts))
		}

		got := tree.RangeSearch(lo, hi)

		// Brute-force the expected count; the BKD answer must match it. An empty
		// range (lo > hi) yields nothing.
		want := 0
		if lo <= hi {
			for _, p := range pts {
				if p.val >= lo && p.val <= hi {
					want++
				}
			}
		}
		if len(got) != want {
			t.Fatalf("RangeSearch[%d,%d] returned %d docs, want %d", lo, hi, len(got), want)
		}

		// Every returned doc id must belong to a point whose value is in range.
		valid := map[uint32][]int64{}
		for _, p := range pts {
			valid[p.doc] = append(valid[p.doc], p.val)
		}
		for _, doc := range got {
			vals, ok := valid[doc]
			if !ok {
				t.Fatalf("RangeSearch returned unknown doc %d", doc)
			}
			inRange := false
			for _, v := range vals {
				if v >= lo && v <= hi {
					inRange = true
					break
				}
			}
			if !inRange {
				t.Fatalf("doc %d returned but no inserted value in [%d,%d]", doc, lo, hi)
			}
		}
	})
}
