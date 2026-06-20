package segment

import (
	"encoding/binary"
	"fmt"

	"github.com/tamnd/search/catalog"
)

// Meta and per-field statistics are encoded with a small deterministic binary
// format so the golden segment fixture is stable across runs.

// writeMeta serializes meta into the manifest namespace.
func writeMeta(kv KV, m *Meta) error {
	return kv.Put(catalog.NSSegmentManifest, metaKey(m.ID), encodeMeta(m))
}

// encodeMeta serializes a segment's metadata.
func encodeMeta(m *Meta) []byte {
	var b []byte
	b = binary.AppendUvarint(b, m.ID)
	b = binary.AppendUvarint(b, uint64(m.BaseDoc))
	b = binary.AppendUvarint(b, uint64(m.MaxDoc))
	b = binary.AppendUvarint(b, uint64(m.DocCount))
	b = binary.AppendUvarint(b, uint64(len(m.Fields)))
	for _, f := range m.Fields {
		b = binary.AppendUvarint(b, uint64(len(f.Name)))
		b = append(b, f.Name...)
		b = binary.AppendUvarint(b, f.TermCount)
		b = binary.AppendUvarint(b, uint64(f.DocCount))
		b = binary.AppendUvarint(b, f.SumDocFreq)
		b = binary.AppendUvarint(b, f.SumTotalTermFreq)
		if f.Positional {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
	}
	return b
}

// decodeMeta reverses encodeMeta.
func decodeMeta(b []byte) (*Meta, error) {
	d := &decoder{buf: b}
	m := &Meta{}
	m.ID = d.uvarint()
	m.BaseDoc = uint32(d.uvarint())
	m.MaxDoc = uint32(d.uvarint())
	m.DocCount = uint32(d.uvarint())
	n := d.uvarint()
	for range n {
		var f FieldMeta
		f.Name = d.str()
		f.TermCount = d.uvarint()
		f.DocCount = uint32(d.uvarint())
		f.SumDocFreq = d.uvarint()
		f.SumTotalTermFreq = d.uvarint()
		f.Positional = d.byte() == 1
		m.Fields = append(m.Fields, f)
	}
	if d.err != nil {
		return nil, d.err
	}
	return m, nil
}

// FieldStats is the index-wide accumulation for one field, used by scoring.
type FieldStats struct {
	DocCount         uint64
	SumDocFreq       uint64
	SumTotalTermFreq uint64
}

// mergeStats folds a flushed segment's per-field totals into the index-wide
// statistics under NSStats.
func mergeStats(kv KV, m *Meta) error {
	for _, f := range m.Fields {
		cur, err := loadFieldStats(kv, f.Name)
		if err != nil {
			return err
		}
		cur.DocCount += uint64(f.DocCount)
		cur.SumDocFreq += f.SumDocFreq
		cur.SumTotalTermFreq += f.SumTotalTermFreq
		if err := kv.Put(catalog.NSStats, []byte(f.Name), encodeFieldStats(cur)); err != nil {
			return err
		}
	}
	return nil
}

// LoadFieldStats returns the index-wide statistics for a field.
func LoadFieldStats(kv KV, field string) (FieldStats, error) {
	return loadFieldStats(kv, field)
}

func loadFieldStats(kv KV, field string) (FieldStats, error) {
	b, ok, err := kv.Get(catalog.NSStats, []byte(field))
	if err != nil || !ok {
		return FieldStats{}, err
	}
	return decodeFieldStats(b)
}

func encodeFieldStats(s FieldStats) []byte {
	var b []byte
	b = binary.AppendUvarint(b, s.DocCount)
	b = binary.AppendUvarint(b, s.SumDocFreq)
	b = binary.AppendUvarint(b, s.SumTotalTermFreq)
	return b
}

func decodeFieldStats(b []byte) (FieldStats, error) {
	d := &decoder{buf: b}
	s := FieldStats{
		DocCount:         d.uvarint(),
		SumDocFreq:       d.uvarint(),
		SumTotalTermFreq: d.uvarint(),
	}
	return s, d.err
}

// decoder is a tiny cursor over a uvarint-framed buffer.
type decoder struct {
	buf []byte
	p   int
	err error
}

func (d *decoder) uvarint() uint64 {
	if d.err != nil {
		return 0
	}
	v, m := binary.Uvarint(d.buf[d.p:])
	if m <= 0 {
		d.err = fmt.Errorf("segment: bad uvarint at %d", d.p)
		return 0
	}
	d.p += m
	return v
}

func (d *decoder) byte() byte {
	if d.err != nil || d.p >= len(d.buf) {
		if d.err == nil {
			d.err = fmt.Errorf("segment: truncated at %d", d.p)
		}
		return 0
	}
	v := d.buf[d.p]
	d.p++
	return v
}

func (d *decoder) str() string {
	n := d.uvarint()
	if d.err != nil {
		return ""
	}
	if d.p+int(n) > len(d.buf) {
		d.err = fmt.Errorf("segment: truncated string at %d", d.p)
		return ""
	}
	s := string(d.buf[d.p : d.p+int(n)])
	d.p += int(n)
	return s
}
