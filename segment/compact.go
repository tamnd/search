package segment

import (
	"sort"

	"github.com/tamnd/search/catalog"
	"github.com/tamnd/search/fst"
	"github.com/tamnd/search/postings"
)

// Merge compacts segs into one new segment with id newID, omitting every
// document marked deleted in the input segments' bitmaps (compaction is the
// point at which tombstones are reaped). The engine uses one global doc-id space
// and gives each batch a disjoint ascending range, so the merge keeps doc-ids as
// they are: it never remaps them, which means the document store and the
// external-id map need no rewrite. The input segments hold disjoint ascending
// ranges, so for any term the live postings concatenated in segment order are
// already globally ascending.
//
// Merge writes the new segment's per-field FST, postings, and norms and its
// manifest entry. It does not touch the input segments or the index-wide stats;
// the caller removes the inputs and recomputes stats after the new segment is in
// place. See spec 2063 doc 10 §5-7; keeping the global doc-id space rather than
// remapping to a dense 0-based space is a documented deviation that follows from
// the global-doc-id design in doc 12.
func Merge(kv KV, newID uint64, segs []*Segment) (*Meta, error) {
	ordered := make([]*Segment, len(segs))
	copy(ordered, segs)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].meta.BaseDoc < ordered[j].meta.BaseDoc })

	dels := make([]*DeleteBitmap, len(ordered))
	var baseDoc, maxDoc uint32
	var liveDocs uint32
	for i, s := range ordered {
		d, err := LoadDeletes(kv, s.meta)
		if err != nil {
			return nil, err
		}
		dels[i] = d
		if i == 0 || s.meta.BaseDoc < baseDoc {
			baseDoc = s.meta.BaseDoc
		}
		if s.meta.MaxDoc > maxDoc {
			maxDoc = s.meta.MaxDoc
		}
		liveDocs += s.meta.DocCount - d.Count()
	}

	meta := &Meta{ID: newID, BaseDoc: baseDoc, MaxDoc: maxDoc, DocCount: liveDocs}
	for _, name := range mergedFieldNames(ordered) {
		fm, err := mergeField(kv, newID, name, ordered, dels, baseDoc, maxDoc)
		if err != nil {
			return nil, err
		}
		meta.Fields = append(meta.Fields, fm)
	}
	if err := writeMeta(kv, meta); err != nil {
		return nil, err
	}
	return meta, nil
}

// mergedFieldNames returns the sorted union of field names across segs.
func mergedFieldNames(segs []*Segment) []string {
	seen := map[string]struct{}{}
	for _, s := range segs {
		for _, f := range s.meta.Fields {
			seen[f.Name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// fieldCursor walks one input segment's terms for a single field during a merge.
type fieldCursor struct {
	fr    *FieldReader
	del   *DeleteBitmap
	terms []string
	pos   int
}

// mergeField merges one field across the input segments and writes its FST,
// postings region, and norms for the output segment.
func mergeField(kv KV, newID uint64, name string, segs []*Segment, dels []*DeleteBitmap, baseDoc, maxDoc uint32) (FieldMeta, error) {
	var cursors []*fieldCursor
	positional := false
	for i, s := range segs {
		fr, ok, err := s.Field(kv, name)
		if err != nil {
			return FieldMeta{}, err
		}
		if !ok {
			continue
		}
		terms, err := fr.Terms()
		if err != nil {
			return FieldMeta{}, err
		}
		positional = positional || fr.Positional()
		cursors = append(cursors, &fieldCursor{fr: fr, del: dels[i], terms: terms})
	}

	fm := FieldMeta{Name: name, Positional: positional}
	docSet := make(map[uint32]struct{})
	var region []byte
	b := fst.NewBuilder()

	for {
		term, active := minTerm(cursors)
		if !active {
			break
		}
		docs, freqs, positions, err := gatherTerm(term, cursors, positional)
		if err != nil {
			return FieldMeta{}, err
		}
		if len(docs) == 0 {
			continue // every posting for this term was deleted
		}
		for _, d := range docs {
			docSet[d] = struct{}{}
		}
		fm.SumDocFreq += uint64(len(docs))
		for _, f := range freqs {
			fm.SumTotalTermFreq += uint64(f)
		}
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
		fm.TermCount++
	}

	dict, err := b.Finish()
	if err != nil {
		return FieldMeta{}, err
	}
	fm.DocCount = uint32(len(docSet))

	norms := mergeNorms(cursors, baseDoc, maxDoc)
	if err := kv.Put(catalog.NSSegFST, segKey(newID, name), dict.Bytes()); err != nil {
		return FieldMeta{}, err
	}
	if err := kv.Put(catalog.NSSegPostings, segKey(newID, name), region); err != nil {
		return FieldMeta{}, err
	}
	if err := kv.Put(catalog.NSSegNorms, segKey(newID, name), norms); err != nil {
		return FieldMeta{}, err
	}
	return fm, nil
}

// minTerm returns the smallest term at the cursors' current positions and
// whether any cursor still has a term.
func minTerm(cursors []*fieldCursor) (string, bool) {
	var best string
	found := false
	for _, c := range cursors {
		if c.pos >= len(c.terms) {
			continue
		}
		if !found || c.terms[c.pos] < best {
			best = c.terms[c.pos]
			found = true
		}
	}
	return best, found
}

// gatherTerm collects the live postings for term across every cursor positioned
// on it, in segment order, advancing those cursors past the term. Doc-ids are
// globally ascending because the segments hold disjoint ascending ranges.
func gatherTerm(term string, cursors []*fieldCursor, positional bool) (docs, freqs []uint32, positions [][]uint32, err error) {
	for _, c := range cursors {
		if c.pos >= len(c.terms) || c.terms[c.pos] != term {
			continue
		}
		c.pos++
		r, ok, err := c.fr.Postings(term)
		if err != nil {
			return nil, nil, nil, err
		}
		if !ok {
			continue
		}
		for {
			doc, freq, ok, err := r.Next()
			if err != nil {
				return nil, nil, nil, err
			}
			if !ok {
				break
			}
			if c.del.Contains(doc) {
				continue
			}
			docs = append(docs, doc)
			freqs = append(freqs, freq)
			if positional {
				var pos []uint32
				if r.Positional() {
					pos, err = r.Positions()
					if err != nil {
						return nil, nil, nil, err
					}
				}
				positions = append(positions, pos)
			}
		}
	}
	if !positional {
		positions = nil
	}
	return docs, freqs, positions, nil
}

// mergeNorms builds the output field's dense norm array over [baseDoc, maxDoc) by
// copying each input segment's norm byte for every doc-id it covers.
func mergeNorms(cursors []*fieldCursor, baseDoc, maxDoc uint32) []byte {
	span := uint32(0)
	if maxDoc > baseDoc {
		span = maxDoc - baseDoc
	}
	norms := make([]byte, span)
	for _, c := range cursors {
		for d := c.fr.baseDoc; d < c.fr.baseDoc+uint32(len(c.fr.norms)); d++ {
			if c.del.Contains(d) {
				continue
			}
			if d >= baseDoc && d-baseDoc < span {
				norms[d-baseDoc] = c.fr.Norm(d)
			}
		}
	}
	return norms
}

// Remove deletes every catalog entry of a segment: its manifest record, its
// per-field FST, postings, and norms, and its delete bitmap. The pages those
// values occupied are reclaimed by the copy-on-write freelist.
func Remove(kv KV, s *Segment) error {
	for _, f := range s.meta.Fields {
		k := segKey(s.meta.ID, f.Name)
		if err := kv.Delete(catalog.NSSegFST, k); err != nil {
			return err
		}
		if err := kv.Delete(catalog.NSSegPostings, k); err != nil {
			return err
		}
		if err := kv.Delete(catalog.NSSegNorms, k); err != nil {
			return err
		}
	}
	if err := kv.Delete(catalog.NSDeletionState, metaKey(s.meta.ID)); err != nil {
		return err
	}
	return kv.Delete(catalog.NSSegmentManifest, metaKey(s.meta.ID))
}

// RecomputeStats rebuilds the index-wide per-field statistics from the live
// segment manifest. Compaction drops deleted documents from the new segment's
// field metadata, so summing the live segments after a compaction yields the
// corrected collection statistics used by scoring.
func RecomputeStats(kv KV) error {
	set, err := LoadSet(kv)
	if err != nil {
		return err
	}
	agg := map[string]FieldStats{}
	for _, s := range set.Segments() {
		for _, f := range s.meta.Fields {
			cur := agg[f.Name]
			cur.DocCount += uint64(f.DocCount)
			cur.SumDocFreq += f.SumDocFreq
			cur.SumTotalTermFreq += f.SumTotalTermFreq
			agg[f.Name] = cur
		}
	}
	for name, s := range agg {
		if err := kv.Put(catalog.NSStats, []byte(name), encodeFieldStats(s)); err != nil {
			return err
		}
	}
	return nil
}
