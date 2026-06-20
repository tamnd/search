package schema

import (
	"errors"
	"reflect"
	"testing"
)

func allTypesSchema(t *testing.T) *Schema {
	t.Helper()
	s := New()
	s.IDField = "sku"
	types := []FieldType{
		TypeText, TypeKeyword, TypeLong, TypeDouble, TypeBoolean,
		TypeDate, TypeGeoPoint, TypeStored,
	}
	for _, ft := range types {
		if err := s.Add(NewField(string(ft)+"_field", ft)); err != nil {
			t.Fatalf("add %s: %v", ft, err)
		}
	}
	vec := NewField("vec_field", TypeDenseVector)
	vec.Opts.Dims = 128
	vec.Opts.Metric = "dot_product"
	if err := s.Add(vec); err != nil {
		t.Fatalf("add vector: %v", err)
	}
	text := NewField("body", TypeText)
	text.Opts.Analyzer = "english"
	text.Opts.TermVectors = true
	if err := s.Add(text); err != nil {
		t.Fatalf("add body: %v", err)
	}
	return s
}

func TestSchemaSerialize(t *testing.T) {
	s := allTypesSchema(t)
	b, err := s.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Deserialize(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.PrimaryKey() != s.PrimaryKey() {
		t.Errorf("primary key %q, want %q", got.PrimaryKey(), s.PrimaryKey())
	}
	if len(got.Fields) != len(s.Fields) {
		t.Fatalf("got %d fields, want %d", len(got.Fields), len(s.Fields))
	}
	for i := range s.Fields {
		if !reflect.DeepEqual(got.Fields[i], s.Fields[i]) {
			t.Errorf("field %d:\n got %#v\nwant %#v", i, got.Fields[i], s.Fields[i])
		}
	}
}

func TestSchemaAddValidation(t *testing.T) {
	s := New()
	if err := s.Add(Field{Name: "", Type: TypeText}); err == nil {
		t.Error("empty name should fail")
	}
	if err := s.Add(Field{Name: "x", Type: FieldType("bogus")}); err == nil {
		t.Error("unknown type should fail")
	}
	if err := s.Add(Field{Name: "v", Type: TypeDenseVector}); err == nil {
		t.Error("dense_vector without dims should fail")
	}
	if err := s.Add(NewField("ok", TypeKeyword)); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(NewField("ok", TypeText)); err == nil {
		t.Error("duplicate field should fail")
	}
}

func TestCheckCompatible(t *testing.T) {
	base := New()
	_ = base.Add(NewField("title", TypeText))
	_ = base.Add(NewField("price", TypeLong))

	// Adding a field is allowed.
	add := New()
	_ = add.Add(NewField("title", TypeText))
	_ = add.Add(NewField("price", TypeLong))
	_ = add.Add(NewField("color", TypeKeyword))
	if err := base.CheckCompatible(add); err != nil {
		t.Errorf("adding a field should be compatible: %v", err)
	}

	// Changing a type is frozen.
	change := New()
	_ = change.Add(NewField("title", TypeText))
	_ = change.Add(NewField("price", TypeDouble))
	if err := base.CheckCompatible(change); !errors.Is(err, ErrSchemaFrozen) {
		t.Errorf("type change error = %v, want ErrSchemaFrozen", err)
	}

	// Removing a field is frozen.
	remove := New()
	_ = remove.Add(NewField("title", TypeText))
	if err := base.CheckCompatible(remove); !errors.Is(err, ErrSchemaFrozen) {
		t.Errorf("removal error = %v, want ErrSchemaFrozen", err)
	}
}

func TestDefaultOptions(t *testing.T) {
	if o := DefaultOptions(TypeText); !o.Indexed || !o.Stored || !o.Positions || o.DocValues {
		t.Errorf("text defaults wrong: %#v", o)
	}
	if o := DefaultOptions(TypeKeyword); !o.Indexed || !o.DocValues {
		t.Errorf("keyword defaults wrong: %#v", o)
	}
	if o := DefaultOptions(TypeStored); o.Indexed {
		t.Errorf("stored should not be indexed: %#v", o)
	}
}
