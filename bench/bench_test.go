package bench

import (
	"testing"

	"github.com/tamnd/search"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/schema"
)

// smallText builds a modest text corpus for the query benchmarks and gate tests.
// The size is kept small so the suite runs in CI in seconds; the headline SLO
// numbers come from the external runner against the full Wikipedia-10M dataset.
func smallText(tb testing.TB) *search.DB {
	tb.Helper()
	db, err := BuildCorpus(CorpusOptions{Docs: 5000, Vocab: 48})
	if err != nil {
		tb.Fatal(err)
	}
	return db
}

func smallVec(tb testing.TB, quant string) *search.DB {
	tb.Helper()
	db, err := BuildCorpus(CorpusOptions{Docs: 2000, Vocab: 32, Dims: 64, Quant: quant})
	if err != nil {
		tb.Fatal(err)
	}
	return db
}

func mustClose(tb testing.TB, db *search.DB) {
	tb.Helper()
	if err := db.Close(); err != nil {
		tb.Fatal(err)
	}
}

func BenchmarkTermQueryWarm(b *testing.B) {
	db := smallText(b)
	defer mustClose(b, db)
	q := query.Term("body", "w7")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := db.Search(q, 10); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkANDQuery4Terms(b *testing.B) {
	db := smallText(b)
	defer mustClose(b, db)
	q := query.Bool().
		MustClause(query.Term("body", "w1")).
		MustClause(query.Term("body", "w2")).
		MustClause(query.Term("body", "w3")).
		MustClause(query.Term("body", "w5"))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := db.Search(q, 10); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkORQuery4Terms(b *testing.B) {
	db := smallText(b)
	defer mustClose(b, db)
	q := query.Bool().
		ShouldClause(query.Term("body", "w1")).
		ShouldClause(query.Term("body", "w2")).
		ShouldClause(query.Term("body", "w3")).
		ShouldClause(query.Term("body", "w5"))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := db.Search(q, 10); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPhraseQuery(b *testing.B) {
	db := smallText(b)
	defer mustClose(b, db)
	q := query.Phrase("body", "w0 w1 w2")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := db.Search(q, 10); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkKNNFloat32(b *testing.B) {
	db := smallVec(b, schema.QuantNone)
	defer mustClose(b, db)
	qs := queryVectors(8, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if _, err := db.Search(query.KNN("embed", qs[i%len(qs)], 10), 10); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkKNNInt8(b *testing.B) {
	db := smallVec(b, schema.QuantInt8)
	defer mustClose(b, db)
	qs := queryVectors(8, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		if _, err := db.Search(query.KNN("embed", qs[i%len(qs)], 10), 10); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHybridBM25KNN(b *testing.B) {
	db := smallVec(b, schema.QuantNone)
	defer mustClose(b, db)
	qs := queryVectors(8, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		h := query.Hybrid(query.Term("title", "w7"), query.KNN("embed", qs[i%len(qs)], 10), 10)
		if _, err := db.Search(h, 10); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkIndexSingle(b *testing.B) {
	db := smallText(b)
	defer mustClose(b, db)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		_, err := db.Index([]map[string]any{{"_id": idFor(i), "body": "w0 w1 w2 w3"}})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkIndexBulk1K(b *testing.B) {
	db := smallText(b)
	defer mustClose(b, db)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		docs := make([]map[string]any, 1000)
		for k := range docs {
			docs[k] = map[string]any{"_id": idFor(i*1000 + k), "body": bodyFor(k+1, 48)}
		}
		if _, err := db.Index(docs); err != nil {
			b.Fatal(err)
		}
	}
}

func idFor(i int) string {
	return "x" + string(rune('a'+i%26)) + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	p := len(buf)
	for i > 0 {
		p--
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[p:])
}
