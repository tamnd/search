package segment

import "github.com/tamnd/search/catalog"

// Doc-values and points storage for a segment (spec 2063 doc 14). Like the rest
// of the segment layer, the columnar doc-values and the BKD points index ride
// the catalog key/value seam rather than raw page extents: a segment's per-field
// column blob lives under NSSegDocValues keyed by (segment id, field name), and
// its per-field points index lives under NSSegPoints keyed the same way. The
// spec's page-aligned column directory and footer (doc 14 §5) are folded into
// each self-describing per-field blob, the same documented deviation the FST and
// postings storage makes.

// WriteDocValues stores one field's serialized doc-values column for a segment.
func WriteDocValues(kv KV, id uint64, field string, blob []byte) error {
	return kv.Put(catalog.NSSegDocValues, segKey(id, field), blob)
}

// WritePoints stores one field's serialized BKD points index for a segment.
func WritePoints(kv KV, id uint64, field string, blob []byte) error {
	return kv.Put(catalog.NSSegPoints, segKey(id, field), blob)
}

// DocValues returns the raw doc-values column blob for a field in this segment,
// or false when the field has no column.
func (s *Segment) DocValues(kv KV, field string) ([]byte, bool, error) {
	return kv.Get(catalog.NSSegDocValues, segKey(s.meta.ID, field))
}

// Points returns the raw BKD points index blob for a field in this segment, or
// false when the field has no points index.
func (s *Segment) Points(kv KV, field string) ([]byte, bool, error) {
	return kv.Get(catalog.NSSegPoints, segKey(s.meta.ID, field))
}
