package query

import (
	"encoding/json"
	"fmt"
)

// ParseJSON parses the JSON query DSL into a query tree (spec 2063 doc 11 §4).
// Each object has exactly one key naming the query type, for example:
//
//	{"term":  {"field": "status", "value": "open"}}
//	{"match": {"field": "title", "query": "quick fox", "operator": "and"}}
//	{"match_phrase": {"field": "title", "query": "quick fox", "slop": 1}}
//	{"prefix": {"field": "title", "value": "qui"}}
//	{"range": {"field": "price", "gte": "10", "lt": "100"}}
//	{"bool":  {"must": [...], "should": [...], "must_not": [...], "filter": [...],
//	           "minimum_should_match": 1}}
//	{"match_all": {}}
//	{"match_none": {}}
//
// A "boost" key on any object multiplies the node's score.
func ParseJSON(data []byte) (Query, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &Error{Msg: "invalid JSON: " + err.Error()}
	}
	return parseNode(raw)
}

func parseNode(raw map[string]json.RawMessage) (Query, error) {
	// Pull out an optional sibling boost so {"term": {...}, "boost": 2} works as
	// well as a boost inside the body.
	var siblingBoost *float32
	if b, ok := raw["boost"]; ok {
		var f float32
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, &Error{Msg: "boost must be a number"}
		}
		siblingBoost = &f
		delete(raw, "boost")
	}
	if len(raw) != 1 {
		return nil, &Error{Msg: fmt.Sprintf("a query object must have exactly one type key, got %d", len(raw))}
	}
	var typ string
	var body json.RawMessage
	for k, v := range raw {
		typ, body = k, v
	}
	q, err := parseTyped(typ, body)
	if err != nil {
		return nil, err
	}
	if siblingBoost != nil {
		q = q.WithBoost(*siblingBoost)
	}
	return q, nil
}

func parseTyped(typ string, body json.RawMessage) (Query, error) {
	switch typ {
	case "term":
		var b struct {
			Field, Value string
			Boost        *float32
		}
		if err := json.Unmarshal(body, &b); err != nil {
			return nil, &Error{Msg: "bad term body"}
		}
		return withBoost(Term(b.Field, b.Value), b.Boost), nil
	case "match":
		var b struct {
			Field, Query, Operator string
			Boost                  *float32
		}
		if err := json.Unmarshal(body, &b); err != nil {
			return nil, &Error{Msg: "bad match body"}
		}
		m := Match(b.Field, b.Query)
		if b.Operator == "and" {
			m.Operator = Must
		}
		return withBoost(m, b.Boost), nil
	case "match_phrase":
		var b struct {
			Field, Query string
			Slop         int
			Boost        *float32
		}
		if err := json.Unmarshal(body, &b); err != nil {
			return nil, &Error{Msg: "bad match_phrase body"}
		}
		ph := Phrase(b.Field, b.Query)
		ph.Slop = b.Slop
		return withBoost(ph, b.Boost), nil
	case "prefix":
		var b struct {
			Field, Value string
			Boost        *float32
		}
		if err := json.Unmarshal(body, &b); err != nil {
			return nil, &Error{Msg: "bad prefix body"}
		}
		return withBoost(Prefix(b.Field, b.Value), b.Boost), nil
	case "range":
		return parseRangeJSON(body)
	case "bool":
		return parseBoolJSON(body)
	case "match_all":
		var b struct {
			Boost *float32
		}
		_ = json.Unmarshal(body, &b)
		return withBoost(MatchAll(), b.Boost), nil
	case "match_none":
		return MatchNone(), nil
	default:
		return nil, &Error{Msg: "unknown query type " + typ}
	}
}

func parseRangeJSON(body json.RawMessage) (Query, error) {
	var b struct {
		Field   string
		Gt, Gte *json.RawMessage
		Lt, Lte *json.RawMessage
		Boost   *float32
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, &Error{Msg: "bad range body"}
	}
	var lower, upper string
	var incLower, incUpper bool
	switch {
	case b.Gte != nil:
		lower, incLower = scalarString(*b.Gte), true
	case b.Gt != nil:
		lower, incLower = scalarString(*b.Gt), false
	}
	switch {
	case b.Lte != nil:
		upper, incUpper = scalarString(*b.Lte), true
	case b.Lt != nil:
		upper, incUpper = scalarString(*b.Lt), false
	}
	return withBoost(Range(b.Field, lower, upper, incLower, incUpper), b.Boost), nil
}

func parseBoolJSON(body json.RawMessage) (Query, error) {
	var b struct {
		Must               []json.RawMessage
		Should             []json.RawMessage
		MustNot            []json.RawMessage `json:"must_not"`
		Filter             []json.RawMessage
		MinimumShouldMatch *int `json:"minimum_should_match"`
		Boost              *float32
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, &Error{Msg: "bad bool body"}
	}
	q := Bool()
	add := func(list []json.RawMessage, o Occur) error {
		for _, item := range list {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(item, &m); err != nil {
				return &Error{Msg: "bad bool clause"}
			}
			sub, err := parseNode(m)
			if err != nil {
				return err
			}
			q.Add(o, sub)
		}
		return nil
	}
	if err := add(b.Must, Must); err != nil {
		return nil, err
	}
	if err := add(b.Should, Should); err != nil {
		return nil, err
	}
	if err := add(b.MustNot, MustNot); err != nil {
		return nil, err
	}
	if err := add(b.Filter, Filter); err != nil {
		return nil, err
	}
	if b.MinimumShouldMatch != nil {
		q.SetMinimumShouldMatch(*b.MinimumShouldMatch)
	}
	return withBoost(q, b.Boost), nil
}

// scalarString renders a JSON scalar (number, string, or bool) as the textual
// bound the planner later encodes to the field's term form.
func scalarString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func withBoost(q Query, b *float32) Query {
	if b != nil {
		return q.WithBoost(*b)
	}
	return q
}
