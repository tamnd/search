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
	"github.com/tamnd/search/score"
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
	BaseDoc  uint32 // smallest global doc-id the segment may hold
	MaxDoc   uint32 // one past the largest global doc-id the segment may hold
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
// caller assigns id (monotonic) and supplies the segment's global doc-id range
// [baseDoc, maxDoc): baseDoc is the smallest doc-id the batch carries and maxDoc
// is one past the largest. The per-field norm arrays are sized to this range, not
// to the whole index, so a segment's storage stays bounded by its own batch.
func Flush(kv KV, id uint64, mt *memtable.MemTable, baseDoc, maxDoc uint32) (*Meta, error) {
	meta := &Meta{ID: id, BaseDoc: baseDoc, MaxDoc: maxDoc, DocCount: uint32(mt.DocCount())}

	names := mt.FieldNames()
	sort.Strings(names)
	for _, name := range names {
		fm, err := flushField(kv, id, name, mt.Field(name), baseDoc, maxDoc)
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

// flushField encodes one field's term dictionary, postings region, and length
// norms. The norms are a dense array of one SmallFloat byte per doc-id over the
// segment's own doc-id range [baseDoc, maxDoc), indexed by (doc-id - baseDoc); a
// doc-id that carried no token for the field encodes to a zero norm and is never
// scored for this field anyway.
func flushField(kv KV, id uint64, name string, f *memtable.Field, baseDoc, maxDoc uint32) (FieldMeta, error) {
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

	span := uint32(0)
	if maxDoc > baseDoc {
		span = maxDoc - baseDoc
	}
	norms := make([]byte, span)
	for d := range norms {
		norms[d] = score.EncodeNorm(int32(f.Length(baseDoc + uint32(d))))
	}

	if err := kv.Put(catalog.NSSegFST, segKey(id, name), dict.Bytes()); err != nil {
		return FieldMeta{}, err
	}
	if err := kv.Put(catalog.NSSegPostings, segKey(id, name), region); err != nil {
		return FieldMeta{}, err
	}
	if err := kv.Put(catalog.NSSegNorms, segKey(id, name), norms); err != nil {
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

// FieldReader gives access to one field's term dictionary, postings, and norms.
type FieldReader struct {
	fst        *fst.FST
	region     []byte
	norms      []byte
	baseDoc    uint32 // global doc-id of norms[0]
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
	// Norms may be absent for segments written before norms existed; treat a
	// missing array as all-zero (length normalization falls back to avgdl).
	norms, _, err := kv.Get(catalog.NSSegNorms, segKey(s.meta.ID, name))
	if err != nil {
		return nil, false, err
	}
	return &FieldReader{fst: f, region: region, norms: norms, baseDoc: s.meta.BaseDoc, positional: fm.Positional}, true, nil
}

// Norm returns the length-norm byte for docID in this field, or zero when the
// doc-id is outside the segment or carried no token for the field. Norms are
// stored relative to the segment's base doc-id, so the global doc-id is offset by
// baseDoc before indexing.
func (fr *FieldReader) Norm(docID uint32) byte {
	if docID < fr.baseDoc {
		return 0
	}
	i := docID - fr.baseDoc
	if int(i) >= len(fr.norms) {
		return 0
	}
	return fr.norms[i]
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

// Positional reports whether the field keeps token positions.
func (fr *FieldReader) Positional() bool { return fr.positional }

// PrefixTerms returns every term in the field that begins with prefix, in
// lexicographic order.
func (fr *FieldReader) PrefixTerms(prefix string) ([]string, error) {
	entries, err := fr.fst.PrefixScan([]byte(prefix))
	if err != nil {
		return nil, err
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = string(e.Term)
	}
	return out, nil
}

// RangeTerms returns every term t with lo <= t < hi in lexicographic order; a nil
// bound is open on that side.
func (fr *FieldReader) RangeTerms(lo, hi []byte) ([]string, error) {
	entries, err := fr.fst.RangeScan(lo, hi)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = string(e.Term)
	}
	return out, nil
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

// Find returns the segment whose doc-id range contains docID. Segments hold
// disjoint ascending ranges, so at most one matches.
func (ss *SegmentSet) Find(docID uint32) (*Segment, bool) {
	for _, s := range ss.segments {
		if docID >= s.meta.BaseDoc && docID < s.meta.MaxDoc {
			return s, true
		}
	}
	return nil, false
}

// DeletedDocIDs returns the sorted union of every segment's deleted doc-ids.
// Query execution filters matches against this set so a deleted document never
// surfaces, even though its postings remain in an immutable segment until
// compaction reaps them.
func (ss *SegmentSet) DeletedDocIDs(kv KV) ([]uint32, error) {
	var out []uint32
	for _, s := range ss.segments {
		d, err := LoadDeletes(kv, s.meta)
		if err != nil {
			return nil, err
		}
		if d.Empty() {
			continue
		}
		out = d.AppendTo(out)
	}
	return out, nil
}
