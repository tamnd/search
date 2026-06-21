package main

import (
	"encoding/json"
	"fmt"

	"github.com/tamnd/search/schema"
)

// fieldDTO is the JSON shape of one field definition accepted by sx_define_field
// and produced by sx_get_mapping (doc 16 §7). Unset options fall back to the
// type defaults from schema.DefaultOptions.
type fieldDTO struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	Stored         *bool  `json:"stored,omitempty"`
	Indexed        *bool  `json:"indexed,omitempty"`
	DocValues      *bool  `json:"doc_values,omitempty"`
	Positions      *bool  `json:"positions,omitempty"`
	Analyzer       string `json:"analyzer,omitempty"`
	VectorDim      int    `json:"vector_dim,omitempty"`
	Metric         string `json:"metric,omitempty"`
	Quantization   string `json:"quantization,omitempty"`
	M              int    `json:"m,omitempty"`
	EfConstruction int    `json:"ef_construction,omitempty"`
}

// mappingJSON is the document SetMapping accepts: an object with a "fields"
// array, plus an optional primary-key field name.
type mappingJSONDoc struct {
	IDField string     `json:"id_field,omitempty"`
	Fields  []fieldDTO `json:"fields"`
}

// fieldFromJSON builds a schema.Field from a JSON field definition.
func fieldFromJSON(data []byte) (schema.Field, error) {
	var dto fieldDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		return schema.Field{}, err
	}
	return dto.toField()
}

func (dto fieldDTO) toField() (schema.Field, error) {
	if dto.Name == "" {
		return schema.Field{}, fmt.Errorf("field definition has no name")
	}
	ft := schema.FieldType(dto.Type)
	if !ft.Valid() {
		return schema.Field{}, fmt.Errorf("field %q has unknown type %q", dto.Name, dto.Type)
	}
	opts := schema.DefaultOptions(ft)
	if dto.Stored != nil {
		opts.Stored = *dto.Stored
	}
	if dto.Indexed != nil {
		opts.Indexed = *dto.Indexed
	}
	if dto.DocValues != nil {
		opts.DocValues = *dto.DocValues
	}
	if dto.Positions != nil {
		opts.Positions = *dto.Positions
	}
	if dto.Analyzer != "" {
		opts.Analyzer = dto.Analyzer
	}
	if dto.VectorDim != 0 {
		opts.Dims = dto.VectorDim
	}
	if dto.Metric != "" {
		opts.Metric = dto.Metric
	}
	if dto.Quantization != "" {
		opts.Quantization = dto.Quantization
	}
	if dto.M != 0 {
		opts.M = dto.M
	}
	if dto.EfConstruction != 0 {
		opts.EfConstruction = dto.EfConstruction
	}
	return schema.Field{Name: dto.Name, Type: ft, Opts: opts}, nil
}

// schemaFromMapping builds a schema from a full mapping document. It accepts
// either {"fields":[...]} or a bare [...] array of field objects.
func schemaFromMapping(data []byte) (*schema.Schema, error) {
	var doc mappingJSONDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		// Fall back to a bare array form.
		var arr []fieldDTO
		if err2 := json.Unmarshal(data, &arr); err2 != nil {
			return nil, err
		}
		doc.Fields = arr
	}
	s := schema.New()
	if doc.IDField != "" {
		s.IDField = doc.IDField
	}
	for _, dto := range doc.Fields {
		f, err := dto.toField()
		if err != nil {
			return nil, err
		}
		if err := s.Add(f); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// mappingDTO renders a schema as the mapping document sx_get_mapping returns.
func mappingDTO(s *schema.Schema) mappingJSONDoc {
	out := mappingJSONDoc{IDField: s.IDField}
	for _, f := range s.Fields {
		stored, indexed := f.Opts.Stored, f.Opts.Indexed
		dv, pos := f.Opts.DocValues, f.Opts.Positions
		dto := fieldDTO{
			Name:      f.Name,
			Type:      string(f.Type),
			Stored:    &stored,
			Indexed:   &indexed,
			DocValues: &dv,
			Positions: &pos,
			Analyzer:  f.Opts.Analyzer,
		}
		if f.Type == schema.TypeDenseVector {
			dto.VectorDim = f.Opts.Dims
			dto.Metric = f.Opts.Metric
			dto.Quantization = f.Opts.Quantization
			dto.M = f.Opts.M
			dto.EfConstruction = f.Opts.EfConstruction
		}
		out.Fields = append(out.Fields, dto)
	}
	return out
}
