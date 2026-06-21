package query

import (
	"encoding/json"
	"fmt"
)

// This file extends the JSON DSL (doc 11 §4) to the advanced query types and the
// scoring-shaping nodes added at S8, so the full query surface is reachable from
// ParseJSON and therefore from the C ABI and any other JSON consumer. The shapes
// mirror the Go constructors in advanced.go and scoring.go:
//
//	{"fuzzy":         {"field": "title", "term": "serch", "max_edits": 1}}
//	{"wildcard":      {"field": "title", "value": "qu*ck"}}
//	{"regexp":        {"field": "code", "value": "[0-9]{4}"}}
//	{"geo_distance":  {"field": "loc", "lat": 40.7, "lon": -74.0, "distance": 5000}}
//	{"span_near":     {"field": "body", "terms": ["quick","fox"], "slop": 2, "in_order": true}}
//	{"function_score":{"query": {...}, "functions": [...], "score_mode": "sum",
//	                   "boost_mode": "multiply", "min_score": 0, "max_boost": 10}}
//	{"bm25f":         {"terms": ["fox"], "fields": [{"name":"title","boost":2}], "k1": 1.2}}
//	{"rescore":       {"query": {...}, "rescore": {...}, "window_size": 100,
//	                   "query_weight": 1, "rescore_weight": 1}}

func parseFuzzyJSON(body json.RawMessage) (Query, error) {
	var b struct {
		Field    string
		Term     string
		Value    string // accepted as an alias for term
		MaxEdits *int   `json:"max_edits"`
		Boost    *float32
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, &Error{Msg: "bad fuzzy body"}
	}
	term := b.Term
	if term == "" {
		term = b.Value
	}
	q := Fuzzy(b.Field, term)
	if b.MaxEdits != nil {
		q.MaxEdits = *b.MaxEdits
		q.AutoEdits = false
	}
	return withBoost(q, b.Boost), nil
}

func parseWildcardJSON(body json.RawMessage) (Query, error) {
	var b struct {
		Field   string
		Pattern string
		Value   string // accepted as an alias for pattern
		Boost   *float32
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, &Error{Msg: "bad wildcard body"}
	}
	pat := b.Pattern
	if pat == "" {
		pat = b.Value
	}
	return withBoost(Wildcard(b.Field, pat), b.Boost), nil
}

func parseRegexpJSON(body json.RawMessage) (Query, error) {
	var b struct {
		Field   string
		Pattern string
		Value   string // accepted as an alias for pattern
		Boost   *float32
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, &Error{Msg: "bad regexp body"}
	}
	pat := b.Pattern
	if pat == "" {
		pat = b.Value
	}
	return withBoost(Regexp(b.Field, pat), b.Boost), nil
}

func parseGeoDistanceJSON(body json.RawMessage) (Query, error) {
	var b struct {
		Field    string
		Lat      float64
		Lon      float64
		Distance *float64
		Meters   *float64 // accepted as an alias for distance
		Boost    *float32
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, &Error{Msg: "bad geo_distance body"}
	}
	meters := 0.0
	switch {
	case b.Distance != nil:
		meters = *b.Distance
	case b.Meters != nil:
		meters = *b.Meters
	}
	return withBoost(GeoDistance(b.Field, b.Lat, b.Lon, meters), b.Boost), nil
}

func parseSpanNearJSON(body json.RawMessage) (Query, error) {
	var b struct {
		Field   string
		Terms   []string
		Slop    int
		InOrder *bool `json:"in_order"`
		Boost   *float32
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, &Error{Msg: "bad span_near body"}
	}
	q := SpanNear(b.Field, b.Terms, b.Slop)
	if b.InOrder != nil {
		q.InOrder = *b.InOrder
	}
	return withBoost(q, b.Boost), nil
}

func parseFunctionScoreJSON(body json.RawMessage) (Query, error) {
	var b struct {
		Query     json.RawMessage
		Functions []json.RawMessage
		ScoreMode string  `json:"score_mode"`
		BoostMode string  `json:"boost_mode"`
		MinScore  float32 `json:"min_score"`
		MaxBoost  float32 `json:"max_boost"`
		Boost     *float32
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, &Error{Msg: "bad function_score body"}
	}
	base, err := parseSubQuery(b.Query, "function_score query")
	if err != nil {
		return nil, err
	}
	fns := make([]ScoreFunction, 0, len(b.Functions))
	for _, raw := range b.Functions {
		fn, err := parseScoreFunctionJSON(raw)
		if err != nil {
			return nil, err
		}
		fns = append(fns, fn)
	}
	q := FunctionScore(base, fns...)
	if b.ScoreMode != "" {
		m, ok := scoreModes[b.ScoreMode]
		if !ok {
			return nil, &Error{Msg: "unknown score_mode " + b.ScoreMode}
		}
		q.ScoreMode = m
	}
	if b.BoostMode != "" {
		m, ok := boostModes[b.BoostMode]
		if !ok {
			return nil, &Error{Msg: "unknown boost_mode " + b.BoostMode}
		}
		q.BoostMode = m
	}
	q.MinScore = b.MinScore
	q.MaxBoost = b.MaxBoost
	return withBoost(q, b.Boost), nil
}

func parseScoreFunctionJSON(raw json.RawMessage) (ScoreFunction, error) {
	var b struct {
		Filter           json.RawMessage
		Weight           float32
		FieldValueFactor *struct {
			Field    string
			Factor   float32
			Modifier string
			Missing  float32
		} `json:"field_value_factor"`
		Decay *struct {
			Field  string
			Kind   string
			Origin float64
			Scale  float64
			Offset float64
			Decay  float64
		}
		RandomScore *struct {
			Seed int64
		} `json:"random_score"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return ScoreFunction{}, &Error{Msg: "bad function_score function"}
	}
	fn := ScoreFunction{Weight: b.Weight}
	if len(b.Filter) > 0 {
		f, err := parseSubQuery(b.Filter, "function filter")
		if err != nil {
			return ScoreFunction{}, err
		}
		fn.Filter = f
	}
	if b.FieldValueFactor != nil {
		mod := ModNone
		if b.FieldValueFactor.Modifier != "" {
			m, ok := fieldModifiers[b.FieldValueFactor.Modifier]
			if !ok {
				return ScoreFunction{}, &Error{Msg: "unknown modifier " + b.FieldValueFactor.Modifier}
			}
			mod = m
		}
		fn.FieldValue = &FieldValueFactor{
			Field:    b.FieldValueFactor.Field,
			Factor:   b.FieldValueFactor.Factor,
			Modifier: mod,
			Missing:  b.FieldValueFactor.Missing,
		}
	}
	if b.Decay != nil {
		kind := DecayGauss
		if b.Decay.Kind != "" {
			k, ok := decayKinds[b.Decay.Kind]
			if !ok {
				return ScoreFunction{}, &Error{Msg: "unknown decay kind " + b.Decay.Kind}
			}
			kind = k
		}
		fn.Decay = &DecayFunction{
			Field:  b.Decay.Field,
			Kind:   kind,
			Origin: b.Decay.Origin,
			Scale:  b.Decay.Scale,
			Offset: b.Decay.Offset,
			Decay:  b.Decay.Decay,
		}
	}
	if b.RandomScore != nil {
		fn.Random = &RandomScore{Seed: b.RandomScore.Seed}
	}
	return fn, nil
}

func parseBM25FJSON(body json.RawMessage) (Query, error) {
	var b struct {
		Terms  []string
		Fields []struct {
			Name  string
			Boost float32
			B     float32
		}
		K1    float32
		Boost *float32
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, &Error{Msg: "bad bm25f body"}
	}
	fields := make([]BM25FField, len(b.Fields))
	for i, f := range b.Fields {
		fields[i] = BM25FField{Name: f.Name, Boost: f.Boost, B: f.B}
	}
	q := BM25F(b.Terms, fields...)
	q.K1 = b.K1
	return withBoost(q, b.Boost), nil
}

func parseRescoreJSON(body json.RawMessage) (Query, error) {
	var b struct {
		Query         json.RawMessage
		Rescore       json.RawMessage
		WindowSize    int     `json:"window_size"`
		QueryWeight   float32 `json:"query_weight"`
		RescoreWeight float32 `json:"rescore_weight"`
		Boost         *float32
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, &Error{Msg: "bad rescore body"}
	}
	base, err := parseSubQuery(b.Query, "rescore query")
	if err != nil {
		return nil, err
	}
	second, err := parseSubQuery(b.Rescore, "rescore secondary query")
	if err != nil {
		return nil, err
	}
	q := Rescore(base, second, b.WindowSize)
	if b.QueryWeight != 0 {
		q.QueryWeight = b.QueryWeight
	}
	if b.RescoreWeight != 0 {
		q.RescoreWeight = b.RescoreWeight
	}
	return withBoost(q, b.Boost), nil
}

// parseSubQuery parses a nested query object, giving a clear error when it is
// missing or malformed.
func parseSubQuery(raw json.RawMessage, what string) (Query, error) {
	if len(raw) == 0 {
		return nil, &Error{Msg: fmt.Sprintf("%s is required", what)}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, &Error{Msg: "bad " + what}
	}
	return parseNode(m)
}

var scoreModes = map[string]ScoreMode{
	"multiply": ScoreMultiply,
	"sum":      ScoreSum,
	"avg":      ScoreAvg,
	"max":      ScoreMax,
	"min":      ScoreMin,
	"first":    ScoreFirst,
}

var boostModes = map[string]BoostMode{
	"multiply": BoostMultiply,
	"replace":  BoostReplace,
	"sum":      BoostSum,
	"avg":      BoostAvg,
	"max":      BoostMax,
	"min":      BoostMin,
}

var fieldModifiers = map[string]FieldModifier{
	"none":       ModNone,
	"log":        ModLog,
	"log1p":      ModLog1p,
	"log2p":      ModLog2p,
	"ln":         ModLn,
	"ln1p":       ModLn1p,
	"ln2p":       ModLn2p,
	"square":     ModSquare,
	"sqrt":       ModSqrt,
	"reciprocal": ModReciprocal,
}

var decayKinds = map[string]DecayKind{
	"gauss":  DecayGauss,
	"exp":    DecayExp,
	"linear": DecayLinear,
}
