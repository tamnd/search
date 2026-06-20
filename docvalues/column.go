package docvalues

import "fmt"

// ColumnKind is the structural kind of a doc-values column (doc 14 §2.1). The
// kind is the first byte of a serialized column blob, so a reader dispatches on
// it without consulting the schema.
type ColumnKind byte

// The column kinds.
const (
	KindNumeric   ColumnKind = 0x01 // single int64 (or sortable-float) per doc
	KindSortedNum ColumnKind = 0x02 // multiple int64 per doc, sorted ascending
	KindSorted    ColumnKind = 0x03 // single keyword ordinal per doc
	KindSortedSet ColumnKind = 0x04 // multiple keyword ordinals per doc
	KindGeoPoint  ColumnKind = 0x06 // single Morton-coded lat/lon per doc
)

// Column is the base interface implemented by every column kind.
type Column interface {
	// Kind returns the column's structural kind.
	Kind() ColumnKind
	// DocCount returns the number of documents the column spans, including
	// documents that have no value (missing docs).
	DocCount() uint32
	// HasValue reports whether the segment-local doc index has a value.
	HasValue(i uint32) bool
}

// NumericColumn gives random access to one int64 value per document. Float and
// date fields are stored here too: floats as the order-preserving sortable-int
// transform (doc 14 §3.4), dates as epoch nanoseconds.
type NumericColumn interface {
	Column
	// Int64 returns the stored value for doc index i, or 0 for a missing doc.
	Int64(i uint32) int64
	// Float64 returns the value decoded from the sortable-float transform.
	Float64(i uint32) float64
}

// SortedNumericColumn gives access to the sorted set of int64 values per
// document. Float and date values are stored here too via the sortable-float
// transform (use Floats to decode them).
type SortedNumericColumn interface {
	Column
	// Values returns the sorted int64 values for doc index i.
	Values(i uint32) []int64
	// Floats returns the values for doc index i decoded as float64.
	Floats(i uint32) []float64
}

// SortedColumn gives random access to one keyword ordinal per document. Ordinals
// are assigned in lexicographic order of the keyword bytes, so comparing
// ordinals is equivalent to comparing the keyword values.
type SortedColumn interface {
	Column
	// OrdAt returns the ordinal for doc index i, or -1 for a missing doc.
	OrdAt(i uint32) int32
	// OrdCount returns the number of distinct ordinals.
	OrdCount() uint32
	// LookupOrd returns the keyword bytes for an ordinal.
	LookupOrd(ord uint32) []byte
}

// SortedSetColumn gives access to the sorted set of keyword ordinals per
// document.
type SortedSetColumn interface {
	Column
	// OrdCount returns the number of distinct ordinals.
	OrdCount() uint32
	// LookupOrd returns the keyword bytes for an ordinal.
	LookupOrd(ord uint32) []byte
	// OrdinalsFor returns the sorted ordinals for doc index i.
	OrdinalsFor(i uint32) []uint32
}

// GeoColumn gives access to one geographic point per document.
type GeoColumn interface {
	Column
	// LatLon returns the latitude and longitude for doc index i.
	LatLon(i uint32) (lat, lon float64)
	// Morton returns the raw 64-bit Morton code for doc index i.
	Morton(i uint32) uint64
}

// OpenColumn parses a serialized column blob and returns a reader of the right
// kind. The blob is the value stored under NSSegDocValues for one field.
func OpenColumn(blob []byte) (Column, error) {
	if len(blob) == 0 {
		return nil, fmt.Errorf("docvalues: empty column blob")
	}
	switch ColumnKind(blob[0]) {
	case KindNumeric:
		return openNumeric(blob)
	case KindGeoPoint:
		return openGeo(blob)
	case KindSortedNum:
		return openSortedNumeric(blob)
	case KindSorted:
		return openSorted(blob)
	case KindSortedSet:
		return openSortedSet(blob)
	default:
		return nil, fmt.Errorf("docvalues: unknown column kind 0x%02x", blob[0])
	}
}

// presence is a dense per-doc present/absent bitset (doc 14 §3.5). A bit set
// means the doc has a value. The engine bounds a segment's doc-id span by the
// batch size, so a dense bitset stays small; this is the same deviation from
// Roaring the deletion vector makes.
type presence struct {
	bits []byte // nil means every doc is present
}

func (p presence) has(i uint32) bool {
	if p.bits == nil {
		return true
	}
	idx := int(i >> 3)
	if idx >= len(p.bits) {
		return false
	}
	return p.bits[idx]&(1<<(i&7)) != 0
}

// encodePresence serializes a presence set: a leading flag byte, then the dense
// bitset when some docs are missing.
func encodePresence(present []bool) []byte {
	allPresent := true
	for _, ok := range present {
		if !ok {
			allPresent = false
			break
		}
	}
	if allPresent {
		return []byte{0}
	}
	out := make([]byte, 1+(len(present)+7)/8)
	out[0] = 1
	for i, ok := range present {
		if ok {
			out[1+i>>3] |= 1 << (uint(i) & 7)
		}
	}
	return out
}

// decodePresenceN reads a presence set knowing the doc count, returning it and
// the number of bytes consumed.
func decodePresenceN(buf []byte, docCount uint32) (presence, int) {
	if len(buf) == 0 || buf[0] == 0 {
		return presence{}, 1
	}
	nbytes := (int(docCount) + 7) / 8
	bits := make([]byte, nbytes)
	copy(bits, buf[1:1+nbytes])
	return presence{bits: bits}, 1 + nbytes
}
