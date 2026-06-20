// Package segment is the immutable inverted-index segment: the on-disk form a
// memtable takes when it is flushed (spec 2063 doc 10 §4-5). A segment holds,
// for each indexed field, a term dictionary (an FST mapping each term to the
// byte offset of its postings) and a postings region (the delta-and-PFOR-encoded
// doc-ids, frequencies, and positions for every term). Segments never change
// after flush; updates and deletes are layered on by newer segments and a
// deletion state, and old segments are reclaimed by compaction (a later
// milestone).
//
// Storage rides the same catalog key/value seam the rest of the engine uses
// rather than raw page extents: a segment's per-field FST and postings live under
// the NSSegFST and NSSegPostings namespaces keyed by (segment id, field name),
// and the segment's metadata lives under NSSegmentManifest keyed by segment id.
// Index-wide per-field statistics accumulate under NSStats. This mirrors the S2
// docstore choice and keeps the segment layer independent of the pager's extent
// machinery; the page-extent layout from doc 02 §8-10 is deferred.
package segment

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/fst"
	"github.com/tamnd/search/memtable"
	"github.com/tamnd/search/postings"
)

// KV is the catalog surface the segment layer needs. catalog.Catalog satisfies
// it.
type KV interface {
	Get(ns byte, key []byte) ([]byte, bool, error)
	Put(ns byte, key, val []byte) error
	Scan(ns byte, fn func(key, val []byte) bool) error
}

// FieldMeta is the per-field summary stored in a segment's metadata.
type FieldMeta struct {
	Name             string
	TermCount        uint64
	DocCount         uint32 // distinct documents containing the field
	SumDocFreq       uint64 // sum of per-term document frequencies
	SumTotalTermFreq uint64 // sum of per-term total term frequencies
	Positional       bool
}

// Meta is a segment's metadata record.
type Meta struct {
	ID       uint64
	MaxDoc   uint32 // one past the largest local doc-id (doc-id space size)
	DocCount uint32 // number of documents flushed into the segment
	Fields   []FieldMeta
}

// segKey builds the (segment id, field) key for the per-field namespaces.
func segKey(id uint64, field string) []byte {
	out := make([]byte, 8+len(field))
	binary.BigEndian.PutUint64(out[:8], id)
	copy(out[8:], field)
	return out
}

// metaKey builds the segment-id key for the manifest namespace.
func metaKey(id uint64) []byte {
	var k [8]byte
	binary.BigEndian.PutUint64(k[:], id)
	return k[:]
}

// Flush writes the memtable as segment id into kv and returns its metadata. The
// caller assigns id (monotonic) and supplies the segment's doc-id space size.
func Flush(kv KV, id uint64, mt *memtable.MemTable, maxDoc uint32) (*Meta, error) {
	meta := &Meta{ID: id, MaxDoc: maxDoc, DocCount: uint32(mt.DocCount())}

	names := mt.FieldNames()
	sort.Strings(names)
	for _, name := range names {
		fm, err := flushField(kv, id, name, mt.Field(name))
		if err != nil {
			return nil, err
		}
		meta.Fields = append(meta.Fields, fm)
	}

	if err := writeMeta(kv, meta); err != nil {
		return nil, err
	}
	if err := mergeStats(kv, meta); err != nil {
		return nil, err
	}
	return meta, nil
}

// flushField encodes one field's term dictionary and postings region.
func flushField(kv KV, id uint64, name string, f *memtable.Field) (FieldMeta, error) {
	terms := make([]string, 0, len(f.Terms))
	for term := range f.Terms {
		terms = append(terms, term)
	}
	sort.Strings(terms)

	fm := FieldMeta{Name: name, Positional: f.Positional()}
	docSet := make(map[uint32]struct{})

	var region []byte
	b := fst.NewBuilder()
	for _, term := range terms {
		pl := f.Terms[term]
		docs := make([]uint32, len(pl.Postings))
		freqs := make([]uint32, len(pl.Postings))
		var positions [][]uint32
		if f.Positional() {
			positions = make([][]uint32, len(pl.Postings))
		}
		for i, p := range pl.Postings {
			docs[i] = p.DocID
			freqs[i] = p.Freq
			docSet[p.DocID] = struct{}{}
			fm.SumTotalTermFreq += uint64(p.Freq)
			if f.Positional() {
				positions[i] = p.Positions
			}
		}
		fm.SumDocFreq += uint64(len(pl.Postings))

		docBlob, posBlob, err := postings.Encode(docs, freqs, positions)
		if err != nil {
			return FieldMeta{}, err
		}
		offset := uint64(len(region))
		region = appendBlob(region, docBlob)
		region = appendBlob(region, posBlob)

		if err := b.Add([]byte(term), offset); err != nil {
			return FieldMeta{}, err
		}
	}
	dict, err := b.Finish()
	if err != nil {
		return FieldMeta{}, err
	}

	fm.TermCount = uint64(len(terms))
	fm.DocCount = uint32(len(docSet))

	if err := kv.Put(catalog.NSSegFST, segKey(id, name), dict.Bytes()); err != nil {
		return FieldMeta{}, err
	}
	if err := kv.Put(catalog.NSSegPostings, segKey(id, name), region); err != nil {
		return FieldMeta{}, err
	}
	return fm, nil
}

// appendBlob appends a length-prefixed blob.
func appendBlob(dst, blob []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(blob)))
	return append(dst, blob...)
}

// readBlob reads a length-prefixed blob at p, returning it and the next offset.
func readBlob(buf []byte, p int) ([]byte, int, error) {
	l, m := binary.Uvarint(buf[p:])
	if m <= 0 {
		return nil, 0, fmt.Errorf("segment: bad blob length")
	}
	p += m
	if p+int(l) > len(buf) {
		return nil, 0, fmt.Errorf("segment: truncated blob")
	}
	return buf[p : p+int(l)], p + int(l), nil
}

// Segment is a read handle over one flushed segment.
type Segment struct {
	meta *Meta
}

// Open loads the metadata for segment id.
func Open(kv KV, id uint64) (*Segment, error) {
	b, ok, err := kv.Get(catalog.NSSegmentManifest, metaKey(id))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("segment: %d not found", id)
	}
	m, err := decodeMeta(b)
	if err != nil {
		return nil, err
	}
	return &Segment{meta: m}, nil
}

// Meta returns the segment metadata.
func (s *Segment) Meta() *Meta { return s.meta }

// ID returns the segment id.
func (s *Segment) ID() uint64 { return s.meta.ID }

// FieldMeta returns the metadata for a field, or false if the field is absent.
func (s *Segment) FieldMeta(name string) (FieldMeta, bool) {
	for _, fm := range s.meta.Fields {
		if fm.Name == name {
			return fm, true
		}
	}
	return FieldMeta{}, false
}

// FieldReader gives access to one field's term dictionary and postings.
type FieldReader struct {
	fst        *fst.FST
	region     []byte
	positional bool
}

// Field opens a reader over the given field, or false if the field is absent.
func (s *Segment) Field(kv KV, name string) (*FieldReader, bool, error) {
	fm, ok := s.FieldMeta(name)
	if !ok {
		return nil, false, nil
	}
	fb, ok, err := kv.Get(catalog.NSSegFST, segKey(s.meta.ID, name))
	if err != nil || !ok {
		return nil, false, err
	}
	f, err := fst.Open(fb)
	if err != nil {
		return nil, false, err
	}
	region, ok, err := kv.Get(catalog.NSSegPostings, segKey(s.meta.ID, name))
	if err != nil || !ok {
		return nil, false, err
	}
	return &FieldReader{fst: f, region: region, positional: fm.Positional}, true, nil
}

// Postings returns an iterator over the postings of term, or false if the term
// is not in the field.
func (fr *FieldReader) Postings(term string) (*postings.Reader, bool, error) {
	off, ok, err := fr.fst.Lookup([]byte(term))
	if err != nil || !ok {
		return nil, false, err
	}
	docBlob, p, err := readBlob(fr.region, int(off))
	if err != nil {
		return nil, false, err
	}
	posBlob, _, err := readBlob(fr.region, p)
	if err != nil {
		return nil, false, err
	}
	if len(posBlob) == 0 {
		posBlob = nil
	}
	r, err := postings.Open(docBlob, posBlob)
	if err != nil {
		return nil, false, err
	}
	return r, true, nil
}

// Terms returns every term in the field in lexicographic order.
func (fr *FieldReader) Terms() ([]string, error) {
	entries, err := fr.fst.All()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = string(e.Term)
	}
	return out, nil
}

// SegmentSet is the ordered set of live segments in a snapshot.
type SegmentSet struct {
	segments []*Segment
}

// LoadSet loads every segment recorded in the manifest, ordered by id.
func LoadSet(kv KV) (*SegmentSet, error) {
	var ids []uint64
	err := kv.Scan(catalog.NSSegmentManifest, func(key, _ []byte) bool {
		if len(key) == 8 {
			ids = append(ids, binary.BigEndian.Uint64(key))
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	set := &SegmentSet{}
	for _, id := range ids {
		s, err := Open(kv, id)
		if err != nil {
			return nil, err
		}
		set.segments = append(set.segments, s)
	}
	return set, nil
}

// Segments returns the segments in id order.
func (ss *SegmentSet) Segments() []*Segment { return ss.segments }

// Len returns the number of segments.
func (ss *SegmentSet) Len() int { return len(ss.segments) }
