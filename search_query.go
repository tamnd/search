package search

import (
	"encoding/binary"
	"sort"

	"github.com/tamnd/search/analysis"
	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/docstore"
	"github.com/tamnd/search/exec"
	"github.com/tamnd/search/query"
	"github.com/tamnd/search/segment"
)

// Hit is one search result: the matched document's internal doc-id, its external
// id, the BM25 (or constant) score, and the stored document body.
type Hit struct {
	DocID      uint64
	ExternalID string
	Score      float32
	Document   map[string]any
}

// Search runs a query tree against the live segments and returns the k
// highest-scoring documents, each with its stored body resolved. Scores are
// computed from the index-wide statistics so a document's rank does not depend on
// which segment it landed in.
func (db *DB) Search(q query.Query, k int) ([]Hit, error) {
	var out []Hit
	err := db.View(func(t *Txn) error {
		hits, err := db.searchTxn(t, q, k)
		if err != nil {
			return err
		}
		out = hits
		return nil
	})
	return out, err
}

// SearchString parses a query string in the compact query syntax (doc 11 §2) and
// runs it. Bare terms target defaultField.
func (db *DB) SearchString(qs, defaultField string, k int) ([]Hit, error) {
	q, err := query.ParseString(qs, defaultField)
	if err != nil {
		return nil, err
	}
	return db.Search(q, k)
}

// SearchJSON parses a query in the JSON query DSL (doc 11 §3) and runs it.
func (db *DB) SearchJSON(data []byte, k int) ([]Hit, error) {
	q, err := query.ParseJSON(data)
	if err != nil {
		return nil, err
	}
	return db.Search(q, k)
}

// searchTxn executes a query within an open read transaction.
func (db *DB) searchTxn(t *Txn, q query.Query, k int) ([]Hit, error) {
	c := t.Catalog()
	s, ok, err := loadSchema(c)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoSchema
	}

	set, err := segment.LoadSet(c)
	if err != nil {
		return nil, err
	}

	live, err := liveDocIDs(c)
	if err != nil {
		return nil, err
	}

	analyzer := func(field string) (*analysis.Analyzer, error) {
		name := "standard"
		if f, ok := s.Lookup(field); ok {
			name = fieldAnalyzerName(f)
		}
		return resolveAnalyzer(c, name)
	}

	se := exec.New(c, set, s, analyzer, live)
	scored, err := se.Search(q, k)
	if err != nil {
		return nil, err
	}

	store := docstore.New(c, catalog.NSDocStore)
	pk := s.PrimaryKey()
	hits := make([]Hit, 0, len(scored))
	for _, h := range scored {
		doc, ok, err := store.Get(uint64(h.DocID))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		hits = append(hits, Hit{
			DocID:      uint64(h.DocID),
			ExternalID: externalID(doc, pk),
			Score:      h.Score,
			Document:   doc,
		})
	}
	return hits, nil
}

// liveDocIDs returns the sorted set of live internal doc-ids, read from the
// external-id mapping (one entry per live document). match_all iterates this set.
func liveDocIDs(c *catalog.Catalog) ([]uint32, error) {
	var ids []uint32
	err := c.Scan(catalog.NSExternalID, func(_, val []byte) bool {
		if len(val) == 8 {
			ids = append(ids, uint32(binary.BigEndian.Uint64(val)))
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}
