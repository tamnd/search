package search

import (
	"fmt"
	"testing"

	"github.com/tamnd/search/determ"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/vfs"
)

// testOpts is the in-memory, deterministic open configuration used by the scale
// tests and benchmarks. It uses the maximum page size so a batch's per-field
// postings region and term dictionary, each stored as one catalog value, fit a
// single page; chunking a region across pages is an S5 concern.
func testOpts() Options {
	return Options{VFS: vfs.NewMem(), PageSize: 65536, Clock: determ.NewFakeClock(0), SaltSeed: 1}
}

// scaleSchema is a single analyzed text field for the scale tests.
func scaleSchema(t testing.TB) *schema.Schema {
	s := schema.New()
	if err := s.Add(schema.NewField("body", schema.TypeText)); err != nil {
		t.Fatal(err)
	}
	return s
}

// buildScaleIndex indexes n documents whose body is a deterministic bag of words
// drawn from a fixed vocabulary, so a term's document set is known by
// construction. Word w_j appears in document i whenever i is divisible by (j+1),
// giving each term a predictable, distinct document frequency.
func buildScaleIndex(t testing.TB, n, vocab int) *DB {
	db, err := Open("scale.sx", testOpts())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutSchema(scaleSchema(t)); err != nil {
		t.Fatal(err)
	}
	const batch = 1000
	docs := make([]map[string]any, 0, batch)
	flush := func() {
		if len(docs) == 0 {
			return
		}
		if _, err := db.Index(docs); err != nil {
			t.Fatal(err)
		}
		docs = docs[:0]
	}
	for i := 1; i <= n; i++ {
		body := ""
		for j := range vocab {
			if i%(j+1) == 0 {
				body += fmt.Sprintf("w%d ", j)
			}
		}
		docs = append(docs, map[string]any{"_id": fmt.Sprintf("d%d", i), "body": body})
		if len(docs) == batch {
			flush()
		}
	}
	flush()
	return db
}

// docFreqByConstruction returns how many of the n documents contain w_j.
func docFreqByConstruction(n, j int) int { return n / (j + 1) }

func TestTermQuery_Scale(t *testing.T) {
	const n, vocab = 10000, 40
	db := buildScaleIndex(t, n, vocab)
	defer mustClose(t, db)

	// w3 appears in every 4th document.
	hits, err := db.Search(query.Term("body", "w3"), n+1)
	if err != nil {
		t.Fatal(err)
	}
	want := docFreqByConstruction(n, 3)
	if len(hits) != want {
		t.Fatalf("term w3 returned %d docs, want %d", len(hits), want)
	}
	// Every returned doc's external id must be divisible by 4.
	for _, h := range hits {
		var id int
		if _, err := fmt.Sscanf(h.ExternalID, "d%d", &id); err != nil {
			t.Fatal(err)
		}
		if id%4 != 0 {
			t.Fatalf("doc %q does not contain w3 by construction", h.ExternalID)
		}
	}
}

func TestPrefixQuery_Scale(t *testing.T) {
	// A vocabulary of 1000 words all sharing the prefix "w"; every document
	// contains w0, so the prefix matches the whole corpus, and the term dictionary
	// holds all 1000 prefixed terms.
	const n, vocab = 1000, 1000
	db := buildScaleIndex(t, n, vocab)
	defer mustClose(t, db)

	hits, err := db.Search(query.Prefix("body", "w"), n+1)
	if err != nil {
		t.Fatal(err)
	}
	// w0 is in every document (i % 1 == 0), so the prefix matches all n.
	if len(hits) != n {
		t.Fatalf("prefix w matched %d docs, want %d", len(hits), n)
	}
}

func TestTopK_Exhaustive(t *testing.T) {
	const n, vocab = 5000, 40
	db := buildScaleIndex(t, n, vocab)
	defer mustClose(t, db)

	q := query.Match("body", "w1 w2 w5 w7")
	full, err := db.Search(q, n+1)
	if err != nil {
		t.Fatal(err)
	}
	top10, err := db.Search(q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top10) != 10 {
		t.Fatalf("top-10 returned %d", len(top10))
	}
	// The 10th score is the cutoff; no excluded document may score above it.
	cutoff := top10[len(top10)-1].Score
	for i, h := range full {
		if i < 10 {
			if h.ExternalID != top10[i].ExternalID {
				t.Fatalf("rank %d: full %q vs top10 %q", i, h.ExternalID, top10[i].ExternalID)
			}
			continue
		}
		if h.Score > cutoff {
			t.Fatalf("excluded doc %q scores %v > cutoff %v", h.ExternalID, h.Score, cutoff)
		}
	}
}

func BenchmarkQuerySingleTerm(b *testing.B) {
	const n, vocab = 100000, 60
	db := buildScaleIndex(b, n, vocab)
	defer func() { _ = db.Close() }()

	q := query.Term("body", "w7")
	b.ResetTimer()
	for range b.N {
		if _, err := db.Search(q, 10); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQueryMultiTermAND(b *testing.B) {
	const n, vocab = 100000, 60
	db := buildScaleIndex(b, n, vocab)
	defer func() { _ = db.Close() }()

	q := query.Bool().
		MustClause(query.Term("body", "w1")).
		MustClause(query.Term("body", "w2")).
		MustClause(query.Term("body", "w3")).
		MustClause(query.Term("body", "w5"))
	b.ResetTimer()
	for range b.N {
		if _, err := db.Search(q, 10); err != nil {
			b.Fatal(err)
		}
	}
}
