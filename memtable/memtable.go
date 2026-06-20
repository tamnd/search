// Package memtable is the in-memory write buffer that accumulates analyzed
// tokens before they are flushed to an immutable segment (spec 2063 doc 10
// §2). Each indexed field holds a map from term to a posting list of
// (doc-id, term-frequency, positions); doc-ids are added in ascending order, so
// each list is sorted by construction and needs no sort at flush time. The
// memtable tracks a running byte estimate and document count and reports when it
// has crossed a flush threshold.
package memtable

// Default flush thresholds (doc 10 §2.7). The byte budget bounds heap use; the
// doc-count cap keeps a segment's local doc-id space within range.
const (
	DefaultMaxBytes = 64 << 20 // 64 MiB
	DefaultMaxDocs  = 1 << 20  // 1,048,576 documents
)

// Posting is one document's occurrence of a term.
type Posting struct {
	DocID     uint32
	Freq      uint32
	Positions []uint32
}

// PostingList is the in-memory posting list for a single term.
type PostingList struct {
	Postings []Posting
}

// Field holds every term seen for one indexed field.
type Field struct {
	Terms map[string]*PostingList
	// positional records whether positions are kept for this field.
	positional bool
}

// MemTable accumulates analyzed tokens for a batch of documents.
type MemTable struct {
	fields   map[string]*Field
	docCount int
	bytes    int64
	maxBytes int64
	maxDocs  int
}

// New returns an empty memtable using the given thresholds. Non-positive values
// fall back to the defaults.
func New(maxBytes int64, maxDocs int) *MemTable {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxDocs <= 0 {
		maxDocs = DefaultMaxDocs
	}
	return &MemTable{
		fields:   make(map[string]*Field),
		maxBytes: maxBytes,
		maxDocs:  maxDocs,
	}
}

// AddToken records one occurrence of term in field for document docID at the
// given position. Positions are kept only when positional is true for the field;
// the first call for a field fixes whether it is positional.
func (m *MemTable) AddToken(field, term string, docID uint32, position uint32, positional bool) {
	f := m.fields[field]
	if f == nil {
		f = &Field{Terms: make(map[string]*PostingList), positional: positional}
		m.fields[field] = f
		m.bytes += int64(len(field)) + 48
	}
	pl := f.Terms[term]
	if pl == nil {
		pl = &PostingList{}
		f.Terms[term] = pl
		m.bytes += int64(len(term)) + 48
	}
	// Extend or create the posting for this document. Because doc-ids arrive in
	// ascending order, the relevant posting is always the last one.
	if n := len(pl.Postings); n > 0 && pl.Postings[n-1].DocID == docID {
		p := &pl.Postings[n-1]
		p.Freq++
		if f.positional {
			p.Positions = append(p.Positions, position)
			m.bytes += 4
		}
	} else {
		p := Posting{DocID: docID, Freq: 1}
		if f.positional {
			p.Positions = []uint32{position}
		}
		pl.Postings = append(pl.Postings, p)
		m.bytes += 12 // docID + freq + slice overhead estimate
		if f.positional {
			m.bytes += 4
		}
	}
}

// AddDoc records that one document has been fully added. Callers invoke it once
// per indexed document so the doc-count threshold is tracked.
func (m *MemTable) AddDoc() { m.docCount++ }

// DocCount returns the number of documents added.
func (m *MemTable) DocCount() int { return m.docCount }

// EstimatedBytes returns the running estimate of the memtable's heap footprint.
func (m *MemTable) EstimatedBytes() int64 { return m.bytes }

// Empty reports whether nothing has been added.
func (m *MemTable) Empty() bool { return m.docCount == 0 && len(m.fields) == 0 }

// NeedsFlush reports whether the memtable has crossed a flush threshold.
func (m *MemTable) NeedsFlush() bool {
	return m.bytes >= m.maxBytes || m.docCount >= m.maxDocs
}

// Field returns the accumulated field, or nil if no token was added for it.
func (m *MemTable) Field(name string) *Field { return m.fields[name] }

// FieldNames returns the names of every field with accumulated terms.
func (m *MemTable) FieldNames() []string {
	out := make([]string, 0, len(m.fields))
	for name := range m.fields {
		out = append(out, name)
	}
	return out
}

// Positional reports whether a field keeps positions.
func (f *Field) Positional() bool { return f.positional }
