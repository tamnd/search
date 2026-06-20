package search

import (
	"testing"

	"github.com/tamnd/search/segment"
)

// fieldPostings opens the postings reader for a term in a field across the index's
// single S3 segment, failing the test if the segment or term is absent.
func fieldPostings(t *testing.T, db *DB, field, term string) ([]uint32, []uint32) {
	t.Helper()
	var docs, freqs []uint32
	err := db.View(func(tx *Txn) error {
		c := tx.Catalog()
		set, err := segment.LoadSet(c)
		if err != nil {
			return err
		}
		if set.Len() != 1 {
			t.Fatalf("segment count = %d, want 1", set.Len())
		}
		fr, ok, err := set.Segments()[0].Field(c, field)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("field %q absent", field)
		}
		r, ok, err := fr.Postings(term)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("term %q absent in field %q", term, field)
		}
		for {
			doc, freq, ok, err := r.Next()
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			docs = append(docs, doc)
			freqs = append(freqs, freq)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return docs, freqs
}

func TestIndexBuildsSegment(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)

	if err := db.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	docs := []map[string]any{
		{"_id": "a", "title": "the quick brown fox", "tag": "animal"},
		{"_id": "b", "title": "the lazy dog", "tag": "animal"},
		{"_id": "c", "title": "quick quick fox", "tag": "speed"},
	}
	if _, err := db.Index(docs); err != nil {
		t.Fatal(err)
	}

	segs, err := db.Segments()
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 {
		t.Fatalf("segment count = %d, want 1", len(segs))
	}
	if segs[0].DocCount != 3 {
		t.Fatalf("segment doc count = %d, want 3", segs[0].DocCount)
	}

	// The analyzed text field "quick" appears in docs 1 and 3, twice in doc 3.
	docIDs, freqs := fieldPostings(t, db, "title", "quick")
	if len(docIDs) != 2 || docIDs[0] != 1 || docIDs[1] != 3 {
		t.Fatalf("quick docs = %v, want [1 3]", docIDs)
	}
	if freqs[1] != 2 {
		t.Fatalf("quick freq in doc 3 = %d, want 2", freqs[1])
	}

	// The keyword field "tag" is a single unanalyzed term per document.
	tagDocs, _ := fieldPostings(t, db, "tag", "animal")
	if len(tagDocs) != 2 || tagDocs[0] != 1 || tagDocs[1] != 2 {
		t.Fatalf("animal docs = %v, want [1 2]", tagDocs)
	}
}

func TestIndexPositions(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)

	if err := db.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Index([]map[string]any{
		{"_id": "a", "title": "the quick brown fox jumps"},
	}); err != nil {
		t.Fatal(err)
	}

	var positions []uint32
	err := db.View(func(tx *Txn) error {
		c := tx.Catalog()
		set, err := segment.LoadSet(c)
		if err != nil {
			return err
		}
		fr, ok, err := set.Segments()[0].Field(c, "title")
		if err != nil || !ok {
			t.Fatalf("title field ok=%v err=%v", ok, err)
		}
		r, ok, err := fr.Postings("fox")
		if err != nil || !ok {
			t.Fatalf("fox postings ok=%v err=%v", ok, err)
		}
		if _, _, ok, err := r.Next(); err != nil || !ok {
			t.Fatalf("fox next ok=%v err=%v", ok, err)
		}
		positions, err = r.Positions()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// "the quick brown fox" -> fox is the fourth token at position 3.
	if len(positions) != 1 || positions[0] != 3 {
		t.Fatalf("fox positions = %v, want [3]", positions)
	}
}

func TestIndexNoIndexedFieldsNoSegment(t *testing.T) {
	db := openDB(t)
	defer mustClose(t, db)

	if err := db.PutSchema(sampleSchema(t)); err != nil {
		t.Fatal(err)
	}
	// A document carrying only its id has no indexed text or keyword values.
	if _, err := db.Index([]map[string]any{{"_id": "only-id"}}); err != nil {
		t.Fatal(err)
	}
	segs, err := db.Segments()
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 0 {
		t.Fatalf("segment count = %d, want 0 (no indexed values)", len(segs))
	}
	// The document is still stored and retrievable.
	if _, err := db.GetByExternalID("only-id"); err != nil {
		t.Fatalf("stored doc missing: %v", err)
	}
}
