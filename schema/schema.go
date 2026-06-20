// Package schema is the field mapping for an index (spec 2063 doc 06 §2-§4). A
// schema is an ordered list of typed fields plus the designated primary-key
// field. It is serialized as MessagePack and stored in the catalog under the
// NSSchema namespace, so the file is self-describing: reopening an index
// reconstructs the exact field types and per-field options it was written with.
//
// A schema is frozen once any document has been indexed against it. Adding a new
// field is always allowed (older documents simply lack a value for it); changing
// the type of an existing field, or removing a field, requires a new index.
// CheckCompatible enforces that rule; the engine calls it only when the index
// already holds documents.
//
// The S2 type set is the core covered by the roadmap: text, keyword, long,
// double, boolean, date, geo_point, dense_vector, and stored. The wider doc 06
// enum (short, byte, float, ip, binary, object, nested, completion, and the
// sparse vector) is deferred to later milestones.
package schema

import (
	"errors"
	"fmt"

	"github.com/tamnd/search/msgpack"
)

// ErrSchemaFrozen is returned when a schema change is rejected because the index
// already holds documents and the change is not an additive one.
var ErrSchemaFrozen = errors.New("schema: cannot change or remove an existing field after documents are indexed")

// DefaultIDField is the field designated as the primary key when none is set.
const DefaultIDField = "_id"

// FieldType is the type of a field's values.
type FieldType string

// The S2 field types.
const (
	TypeText        FieldType = "text"         // analyzed full-text
	TypeKeyword     FieldType = "keyword"      // exact string, not analyzed
	TypeLong        FieldType = "long"         // 64-bit signed integer
	TypeDouble      FieldType = "double"       // 64-bit IEEE 754 float
	TypeBoolean     FieldType = "boolean"      // true / false
	TypeDate        FieldType = "date"         // RFC3339, stored as int64 unix nanos
	TypeGeoPoint    FieldType = "geo_point"    // WGS-84 lat/lon pair
	TypeDenseVector FieldType = "dense_vector" // fixed-dimension float32 vector
	TypeStored      FieldType = "stored"       // opaque blob, not indexed
)

// validTypes is the set of recognized field types.
var validTypes = map[FieldType]bool{
	TypeText: true, TypeKeyword: true, TypeLong: true, TypeDouble: true,
	TypeBoolean: true, TypeDate: true, TypeGeoPoint: true, TypeDenseVector: true,
	TypeStored: true,
}

// Valid reports whether t is a recognized field type.
func (t FieldType) Valid() bool { return validTypes[t] }

// FieldOptions are the per-field indexing and storage knobs (doc 06 §4). The
// zero value is not the default; construct options with DefaultOptions, which
// applies the type-appropriate defaults from the roadmap.
type FieldOptions struct {
	Indexed     bool
	Stored      bool
	DocValues   bool
	Positions   bool
	TermVectors bool
	Analyzer    string // text only; "" means the standard analyzer
	Dims        int    // dense_vector only
	Metric      string // dense_vector only: cosine|dot_product|l2_norm
}

// Field is one entry in the mapping: a name, a type, and its options.
type Field struct {
	Name string
	Type FieldType
	Opts FieldOptions
}

// DefaultOptions returns the default options for a field of type t, following the
// roadmap defaults: text/keyword/numeric are indexed; everything is stored;
// numeric, keyword, and date carry doc-values; text keeps positions.
func DefaultOptions(t FieldType) FieldOptions {
	o := FieldOptions{Stored: true}
	switch t {
	case TypeText:
		o.Indexed = true
		o.Positions = true
	case TypeKeyword:
		o.Indexed = true
		o.DocValues = true
	case TypeLong, TypeDouble:
		o.Indexed = true
		o.DocValues = true
	case TypeDate:
		o.Indexed = true
		o.DocValues = true
	case TypeBoolean:
		o.Indexed = true
		o.DocValues = true
	case TypeGeoPoint:
		o.Indexed = true
		o.DocValues = true
	case TypeDenseVector:
		o.Indexed = true
		o.Metric = "cosine"
	case TypeStored:
		// indexed stays false: a stored blob is retrieval-only.
	}
	return o
}

// NewField returns a field of type t with the default options for that type.
func NewField(name string, t FieldType) Field {
	return Field{Name: name, Type: t, Opts: DefaultOptions(t)}
}

// Schema is the full field mapping for an index.
type Schema struct {
	IDField string  // the primary-key field name; defaults to DefaultIDField
	Fields  []Field // fields in mapping-definition order
}

// New returns an empty schema with the default primary-key field.
func New() *Schema {
	return &Schema{IDField: DefaultIDField}
}

// Add appends a field to the schema. It returns an error if the field name is
// empty, longer than 512 bytes, the type is unknown, or the name is a duplicate.
func (s *Schema) Add(f Field) error {
	if f.Name == "" {
		return errors.New("schema: empty field name")
	}
	if len(f.Name) > 512 {
		return fmt.Errorf("schema: field name %q exceeds 512 bytes", f.Name)
	}
	if !f.Type.Valid() {
		return fmt.Errorf("schema: field %q has unknown type %q", f.Name, f.Type)
	}
	if f.Type == TypeDenseVector && f.Opts.Dims <= 0 {
		return fmt.Errorf("schema: dense_vector field %q needs a positive dims", f.Name)
	}
	if _, ok := s.Lookup(f.Name); ok {
		return fmt.Errorf("schema: duplicate field %q", f.Name)
	}
	s.Fields = append(s.Fields, f)
	return nil
}

// Lookup returns the field with the given name and whether it exists.
func (s *Schema) Lookup(name string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// PrimaryKey returns the configured primary-key field name, defaulting to
// DefaultIDField when unset.
func (s *Schema) PrimaryKey() string {
	if s.IDField == "" {
		return DefaultIDField
	}
	return s.IDField
}

// CheckCompatible reports whether evolving from s to next is allowed once
// documents exist. Adding new fields is fine; changing the primary key, changing
// an existing field's type, or removing an existing field returns ErrSchemaFrozen.
func (s *Schema) CheckCompatible(next *Schema) error {
	if s.PrimaryKey() != next.PrimaryKey() {
		return fmt.Errorf("%w: primary key changed from %q to %q", ErrSchemaFrozen, s.PrimaryKey(), next.PrimaryKey())
	}
	for _, old := range s.Fields {
		nf, ok := next.Lookup(old.Name)
		if !ok {
			return fmt.Errorf("%w: field %q removed", ErrSchemaFrozen, old.Name)
		}
		if nf.Type != old.Type {
			return fmt.Errorf("%w: field %q type changed from %q to %q", ErrSchemaFrozen, old.Name, old.Type, nf.Type)
		}
	}
	return nil
}

// Serialize encodes the schema as MessagePack for storage in the catalog.
func (s *Schema) Serialize() ([]byte, error) {
	fields := make([]any, len(s.Fields))
	for i, f := range s.Fields {
		fields[i] = map[string]any{
			"name":         f.Name,
			"type":         string(f.Type),
			"indexed":      f.Opts.Indexed,
			"stored":       f.Opts.Stored,
			"doc_values":   f.Opts.DocValues,
			"positions":    f.Opts.Positions,
			"term_vectors": f.Opts.TermVectors,
			"analyzer":     f.Opts.Analyzer,
			"dims":         int64(f.Opts.Dims),
			"metric":       f.Opts.Metric,
		}
	}
	return msgpack.Marshal(map[string]any{
		"id_field": s.PrimaryKey(),
		"fields":   fields,
	})
}

// Deserialize decodes a schema previously produced by Serialize.
func Deserialize(b []byte) (*Schema, error) {
	v, _, err := msgpack.Unmarshal(b)
	if err != nil {
		return nil, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema: malformed top-level value %T", v)
	}
	s := New()
	if idf, ok := top["id_field"].(string); ok && idf != "" {
		s.IDField = idf
	}
	raw, ok := top["fields"].([]any)
	if !ok {
		return s, nil
	}
	for _, fv := range raw {
		fm, ok := fv.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema: malformed field entry %T", fv)
		}
		f := Field{
			Name: asString(fm["name"]),
			Type: FieldType(asString(fm["type"])),
			Opts: FieldOptions{
				Indexed:     asBool(fm["indexed"]),
				Stored:      asBool(fm["stored"]),
				DocValues:   asBool(fm["doc_values"]),
				Positions:   asBool(fm["positions"]),
				TermVectors: asBool(fm["term_vectors"]),
				Analyzer:    asString(fm["analyzer"]),
				Dims:        int(asInt(fm["dims"])),
				Metric:      asString(fm["metric"]),
			},
		}
		s.Fields = append(s.Fields, f)
	}
	return s, nil
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case uint64:
		return int64(x)
	default:
		return 0
	}
}
