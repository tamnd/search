package search

import (
	"testing"

	"github.com/tamnd/search/query"
)

func TestSoftDeleteImmediate(t *testing.T) {
	db := buildScaleIndex(t, 200, 20)
	defer mustClose(t, db)

	// Add a document with a term that appears nowhere else, confirm it matches,
	// delete it, and confirm the term stops matching right away.
	if _, err := db.Index([]map[string]any{{"_id": "marked", "body": "zorblax"}}); err != nil {
		t.Fatal(err)
	}
	hits, err := db.Search(query.Term("body", "zorblax"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("before delete: %d hits for unique term, want 1", len(hits))
	}

	existed, err := db.Delete("marked")
	if err != nil {
		t.Fatal(err)
	}
	if !existed {
		t.Fatal("Delete reported the document did not exist")
	}

	hits, err = db.Search(query.Term("body", "zorblax"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("after delete: %d hits, want 0", len(hits))
	}
	if _, err := db.GetByExternalID("marked"); err != ErrNoDoc {
		t.Fatalf("GetByExternalID after delete = %v, want ErrNoDoc", err)
	}
}

func TestDeleteMissing(t *testing.T) {
	db := buildScaleIndex(t, 10, 5)
	defer mustClose(t, db)
	existed, err := db.Delete("nope")
	if err != nil {
		t.Fatal(err)
	}
	if existed {
		t.Fatal("Delete of a missing id reported it existed")
	}
}

func TestUpdateIdempotent(t *testing.T) {
	db, err := Open("update.sx", testOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db)
	if err := db.PutSchema(scaleSchema(t)); err != nil {
		t.Fatal(err)
	}

	// Re-index the same logical document ten times with the same content. Only
	// one version must remain retrievable, and the document must match its term
	// exactly once.
	for range 10 {
		if _, err := db.Index([]map[string]any{{"_id": "doc", "body": "alpha beta"}}); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := db.Search(query.Term("body", "alpha"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("after 10 updates: %d hits, want 1", len(hits))
	}
	if hits[0].ExternalID != "doc" {
		t.Fatalf("hit external id = %q, want doc", hits[0].ExternalID)
	}
	got, err := db.GetByExternalID("doc")
	if err != nil {
		t.Fatal(err)
	}
	if got["body"] != "alpha beta" {
		t.Fatalf("body = %v, want alpha beta", got["body"])
	}
}

func TestUpdateChangesMatches(t *testing.T) {
	db, err := Open("change.sx", testOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db)
	if err := db.PutSchema(scaleSchema(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Index([]map[string]any{{"_id": "x", "body": "old"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Index([]map[string]any{{"_id": "x", "body": "new"}}); err != nil {
		t.Fatal(err)
	}

	// The replaced term must no longer match; the new term must.
	for term, want := range map[string]int{"old": 0, "new": 1} {
		hits, err := db.Search(query.Term("body", term), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != want {
			t.Fatalf("term %q: %d hits, want %d", term, len(hits), want)
		}
	}
}

func TestBatchDuplicateExternalIDLastWins(t *testing.T) {
	db, err := Open("dup.sx", testOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db)
	if err := db.PutSchema(scaleSchema(t)); err != nil {
		t.Fatal(err)
	}
	n, err := db.Index([]map[string]any{
		{"_id": "a", "body": "first"},
		{"_id": "a", "body": "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("indexed %d docs, want 1 after batch dedup", n)
	}
	for term, want := range map[string]int{"first": 0, "second": 1} {
		hits, err := db.Search(query.Term("body", term), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != want {
			t.Fatalf("term %q: %d hits, want %d", term, len(hits), want)
		}
	}
}
