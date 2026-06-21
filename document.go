package search

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tamnd/search/analysis"
	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/docstore"
	"github.com/tamnd/search/schema"
	"github.com/tamnd/search/segment"
)

// Errors from the document layer.
var (
	// ErrNoSchema is returned when indexing or analyzing by field before a schema
	// has been set with PutSchema.
	ErrNoSchema = errors.New("search: no schema defined")
	// ErrNoDoc is returned when a requested document does not exist.
	ErrNoDoc = errors.New("search: document not found")
	// ErrUnknownAnalyzer is returned when an analyzer name is neither a built-in
	// nor a stored custom analyzer.
	ErrUnknownAnalyzer = errors.New("search: unknown analyzer")
)

// docSeqKey is the catalog key under NSDocID holding the monotonic doc-id
// counter. Internal doc-ids start at 1; 0 is reserved.
var docSeqKey = []byte("seq")

// PutSchema stores the field schema. Until any document is indexed the schema may
// be replaced freely; afterward the new schema must be compatible with the old
// one (fields may be added, but existing fields may not change type or be
// removed, and the primary key is fixed), or ErrSchemaFrozen is returned.
func (db *DB) PutSchema(s *schema.Schema) error {
	enc, err := s.Serialize()
	if err != nil {
		return err
	}
	return db.Update(func(t *Txn) error {
		c := t.Catalog()
		if cur, ok, err := loadSchema(c); err != nil {
			return err
		} else if ok {
			n, err := docCount(c)
			if err != nil {
				return err
			}
			if n > 0 {
				if err := cur.CheckCompatible(s); err != nil {
					return err
				}
			}
		}
		return c.Put(catalog.NSSchema, nil, enc)
	})
}

// Schema returns the current field schema, or ErrNoSchema if none is set.
func (db *DB) Schema() (*schema.Schema, error) {
	var out *schema.Schema
	err := db.View(func(t *Txn) error {
		s, ok, err := loadSchema(t.Catalog())
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoSchema
		}
		out = s
		return nil
	})
	return out, err
}

// loadSchema reads and decodes the stored schema, reporting whether one exists.
func loadSchema(c *catalog.Catalog) (*schema.Schema, bool, error) {
	b, ok, err := c.Get(catalog.NSSchema, nil)
	if err != nil || !ok {
		return nil, false, err
	}
	s, err := schema.Deserialize(b)
	if err != nil {
		return nil, false, err
	}
	return s, true, nil
}

// docCount returns the number of doc-ids allocated so far (the counter value).
func docCount(c *catalog.Catalog) (uint64, error) {
	b, ok, err := c.Get(catalog.NSDocID, docSeqKey)
	if err != nil || !ok {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

// Index stores the given documents and returns how many were written. Each
// document is keyed by its external id (the value of the schema primary-key
// field, default "_id"). A document without the primary-key field is assigned a
// fresh external id equal to its new doc-id.
//
// Each Index call persists the stored documents and the external-id mappings and
// then flushes one immutable inverted-index segment over the batch: every
// indexed text and keyword field is analyzed into terms and written to the
// segment's term dictionary and postings (doc 10 §4-5). Re-indexing a document
// whose external id already exists is a replace: the old version is soft-deleted
// (its bit set in its segment's delete bitmap, its stored body dropped) and the
// new version is indexed under a fresh internal doc-id (doc 10 §8). When a batch
// repeats an external id, the last occurrence wins and the earlier ones are not
// indexed.
func (db *DB) Index(docs []map[string]any) (int, error) {
	n := 0
	err := db.Update(func(t *Txn) error {
		var err error
		n, err = db.indexTxn(t, docs)
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// indexTxn indexes docs into the catalog bound to t without committing. It is the
// shared body of Index; the caller's transaction decides whether the work is
// committed or rolled back. It returns the number of documents indexed so far,
// which is meaningful even when it returns an error.
func (db *DB) indexTxn(t *Txn, docs []map[string]any) (int, error) {
	c := t.Catalog()
	s, ok, err := loadSchema(c)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNoSchema
	}
	store := docstore.New(c, catalog.NSDocStore)
	idField := s.PrimaryKey()
	docs = dedupByExternalID(docs, idField)

	set, err := segment.LoadSet(c)
	if err != nil {
		return 0, err
	}
	del := newDeleter(c, set, store)

	// Persist every document first, collecting the (doc-id, body) pairs so the
	// memtable can be built in ascending doc-id order (the posting-list
	// invariant), independent of the order replaces resolve to.
	n := 0
	entries := make([]docEntry, 0, len(docs))
	var maxDoc uint64
	for _, doc := range docs {
		docID, err := indexOne(c, store, del, idField, doc)
		if err != nil {
			return n, err
		}
		entries = append(entries, docEntry{docID: docID, doc: doc})
		maxDoc = max(maxDoc, docID)
		n++
	}
	if err := del.flush(); err != nil {
		return n, err
	}
	if err := flushBatch(c, s, entries, maxDoc); err != nil {
		return n, err
	}
	return n, nil
}

// dedupByExternalID collapses a batch so each external id appears once, keeping
// the last occurrence. Documents with no external id are all kept. This stops a
// batch from flushing two live versions of the same logical document into one
// segment, where the replace machinery (which works against already-committed
// segments) could not see the earlier version.
func dedupByExternalID(docs []map[string]any, idField string) []map[string]any {
	seen := make(map[string]int, len(docs))
	out := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		ext := externalID(doc, idField)
		if ext == "" {
			out = append(out, doc)
			continue
		}
		if i, ok := seen[ext]; ok {
			out[i] = doc
			continue
		}
		seen[ext] = len(out)
		out = append(out, doc)
	}
	return out
}

// docEntry pairs a resolved internal doc-id with its document body.
type docEntry struct {
	docID uint64
	doc   map[string]any
}

// indexOne writes a single document: when its external id already maps to a
// document, that older version is soft-deleted first; then a fresh internal
// doc-id is allocated, the external-id mapping is pointed at it, and the body is
// stored. It returns the internal doc-id the document was stored under.
func indexOne(c *catalog.Catalog, store *docstore.Store, del *deleter, idField string, doc map[string]any) (uint64, error) {
	extID := externalID(doc, idField)

	if extID != "" {
		if b, ok, err := c.Get(catalog.NSExternalID, []byte(extID)); err != nil {
			return 0, err
		} else if ok {
			if _, err := del.mark(binary.BigEndian.Uint64(b)); err != nil {
				return 0, err
			}
		}
	}

	docID, err := nextDocID(c)
	if err != nil {
		return 0, err
	}
	if extID == "" {
		extID = fmt.Sprintf("%d", docID)
		doc[idField] = extID
	}

	var idb [8]byte
	binary.BigEndian.PutUint64(idb[:], docID)
	if err := c.Put(catalog.NSExternalID, []byte(extID), idb[:]); err != nil {
		return 0, err
	}
	return docID, store.Put(docID, doc)
}

// externalID returns the external id of a document as a string: the value of the
// primary-key field, or "" when that field is absent.
func externalID(doc map[string]any, idField string) string {
	v, ok := doc[idField]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// nextDocID increments and returns the monotonic doc-id counter.
func nextDocID(c *catalog.Catalog) (uint64, error) {
	cur, err := docCount(c)
	if err != nil {
		return 0, err
	}
	cur++
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], cur)
	if err := c.Put(catalog.NSDocID, docSeqKey, b[:]); err != nil {
		return 0, err
	}
	return cur, nil
}

// GetByDocID returns the stored document with the given internal doc-id.
func (db *DB) GetByDocID(docID uint64) (map[string]any, error) {
	var out map[string]any
	err := db.View(func(t *Txn) error {
		store := docstore.New(t.Catalog(), catalog.NSDocStore)
		doc, ok, err := store.Get(docID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoDoc
		}
		out = doc
		return nil
	})
	return out, err
}

// GetByExternalID returns the stored document with the given external id.
func (db *DB) GetByExternalID(extID string) (map[string]any, error) {
	var out map[string]any
	err := db.View(func(t *Txn) error {
		c := t.Catalog()
		b, ok, err := c.Get(catalog.NSExternalID, []byte(extID))
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoDoc
		}
		docID := binary.BigEndian.Uint64(b)
		store := docstore.New(c, catalog.NSDocStore)
		doc, ok, err := store.Get(docID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoDoc
		}
		out = doc
		return nil
	})
	return out, err
}

// PutAnalyzer stores a custom analyzer configuration under its name so it can be
// referenced by name at index and query time. The configuration is validated by
// building it once before it is stored.
func (db *DB) PutAnalyzer(cfg analysis.AnalyzerConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("search: analyzer config has no name")
	}
	if _, err := analysis.BuildAnalyzer(cfg); err != nil {
		return err
	}
	enc, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return db.Update(func(t *Txn) error {
		return t.Catalog().Put(catalog.NSAnalyzer, []byte(cfg.Name), enc)
	})
}

// Analyze runs the named analyzer (a built-in or a stored custom analyzer) over
// text and returns its tokens.
func (db *DB) Analyze(analyzerName, text string) ([]analysis.Token, error) {
	var out []analysis.Token
	err := db.View(func(t *Txn) error {
		a, err := resolveAnalyzer(t.Catalog(), analyzerName)
		if err != nil {
			return err
		}
		out = a.Analyze(text)
		return nil
	})
	return out, err
}

// AnalyzeField runs the analyzer configured for a schema field over text. A field
// with no explicit analyzer uses the standard analyzer; non-text fields are not
// analyzed and yield a single keyword token.
func (db *DB) AnalyzeField(field, text string) ([]analysis.Token, error) {
	var out []analysis.Token
	err := db.View(func(t *Txn) error {
		c := t.Catalog()
		s, ok, err := loadSchema(c)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoSchema
		}
		f, ok := s.Lookup(field)
		if !ok {
			return fmt.Errorf("search: unknown field %q", field)
		}
		name := fieldAnalyzerName(f)
		a, err := resolveAnalyzer(c, name)
		if err != nil {
			return err
		}
		out = a.Analyze(text)
		return nil
	})
	return out, err
}

// fieldAnalyzerName returns the analyzer name to use for a field: its configured
// analyzer if set, the standard analyzer for text fields, otherwise keyword.
func fieldAnalyzerName(f schema.Field) string {
	if f.Opts.Analyzer != "" {
		return f.Opts.Analyzer
	}
	if f.Type == schema.TypeText {
		return "standard"
	}
	return "keyword"
}

// resolveAnalyzer returns the analyzer for name, preferring a built-in and then a
// stored custom configuration.
func resolveAnalyzer(c *catalog.Catalog, name string) (*analysis.Analyzer, error) {
	if a, err := analysis.NewNamed(name); err == nil {
		return a, nil
	}
	b, ok, err := c.Get(catalog.NSAnalyzer, []byte(name))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAnalyzer, name)
	}
	var cfg analysis.AnalyzerConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return analysis.BuildAnalyzer(cfg)
}
