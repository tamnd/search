package search

import "github.com/tamnd/search/segment"

// SegmentInfo summarizes one flushed segment for inspection.
type SegmentInfo struct {
	ID       uint64
	DocCount uint32
	MaxDoc   uint32
	Fields   []FieldInfo
}

// FieldInfo summarizes one field within a segment.
type FieldInfo struct {
	Name             string
	TermCount        uint64
	DocCount         uint32
	SumDocFreq       uint64
	SumTotalTermFreq uint64
	Positional       bool
}

// Segments returns a summary of every flushed segment, ordered by id, for
// inspection and diagnostics.
func (db *DB) Segments() ([]SegmentInfo, error) {
	var out []SegmentInfo
	err := db.View(func(t *Txn) error {
		set, err := segment.LoadSet(t.Catalog())
		if err != nil {
			return err
		}
		for _, s := range set.Segments() {
			m := s.Meta()
			si := SegmentInfo{ID: m.ID, DocCount: m.DocCount, MaxDoc: m.MaxDoc}
			for _, f := range m.Fields {
				si.Fields = append(si.Fields, FieldInfo{
					Name:             f.Name,
					TermCount:        f.TermCount,
					DocCount:         f.DocCount,
					SumDocFreq:       f.SumDocFreq,
					SumTotalTermFreq: f.SumTotalTermFreq,
					Positional:       f.Positional,
				})
			}
			out = append(out, si)
		}
		return nil
	})
	return out, err
}
