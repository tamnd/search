package analysis

import (
	"fmt"
)

// Analyzer is a complete analysis pipeline: zero or more char filters, exactly
// one tokenizer, and zero or more token filters, applied in order (doc 07 §1.1).
// The same Analyzer instance is used at index time and query time.
type Analyzer struct {
	Name         string
	CharFilters  []CharFilter
	Tokenizer    Tokenizer
	TokenFilters []TokenFilter
}

// Analyze runs the full pipeline over text and returns the resulting tokens with
// absolute positions filled in. The position of a token is the running sum of
// position increments, starting at the first token's increment (so the first
// token is at position 0 when its increment is 1).
func (a *Analyzer) Analyze(text string) []Token {
	for _, cf := range a.CharFilters {
		text = cf.Filter(text)
	}
	tok := a.Tokenizer
	if tok == nil {
		tok = StandardTokenizer{}
	}
	toks := tok.Tokenize(text)
	for _, tf := range a.TokenFilters {
		toks = tf.Filter(toks)
	}
	return toks
}

// Position returns the absolute position of each token, computed from the
// per-token increments. It is a helper for callers (the indexer, explain output)
// that need positions rather than increments.
func Position(toks []Token) []int {
	pos := make([]int, len(toks))
	cur := -1
	for i, t := range toks {
		cur += t.PositionIncr
		pos[i] = cur
	}
	return pos
}

// NewNamed returns one of the four predefined analyzers (doc 07 §5): standard,
// english, keyword, simple. It returns an error for an unknown name.
func NewNamed(name string) (*Analyzer, error) {
	switch name {
	case "", "standard":
		return &Analyzer{
			Name:         "standard",
			Tokenizer:    StandardTokenizer{},
			TokenFilters: []TokenFilter{LowercaseFilter{}},
		}, nil
	case "english":
		return &Analyzer{
			Name:      "english",
			Tokenizer: StandardTokenizer{},
			TokenFilters: []TokenFilter{
				EnglishPossessiveFilter{},
				LowercaseFilter{},
				englishStopFilter(),
				StemmerFilter{},
			},
		}, nil
	case "keyword":
		return &Analyzer{
			Name:      "keyword",
			Tokenizer: KeywordTokenizer{},
		}, nil
	case "simple":
		return &Analyzer{
			Name:         "simple",
			Tokenizer:    LetterTokenizer{},
			TokenFilters: []TokenFilter{LowercaseFilter{}},
		}, nil
	default:
		return nil, fmt.Errorf("analysis: unknown analyzer %q", name)
	}
}

// CharFilterConfig describes one char filter in an analyzer definition.
type CharFilterConfig struct {
	Type        string `json:"type"`
	Pattern     string `json:"pattern,omitempty"`
	Replacement string `json:"replacement,omitempty"`
}

// TokenizerConfig describes the tokenizer in an analyzer definition.
type TokenizerConfig struct {
	Type string `json:"type"`
	Min  int    `json:"min,omitempty"`
	Max  int    `json:"max,omitempty"`
}

// TokenFilterConfig describes one token filter in an analyzer definition.
type TokenFilterConfig struct {
	Type      string   `json:"type"`
	Stopwords []string `json:"stopwords,omitempty"`
	Synonyms  []string `json:"synonyms,omitempty"`
	Min       int      `json:"min,omitempty"`
	Max       int      `json:"max,omitempty"`
}

// AnalyzerConfig is the JSON-serializable definition of a custom analyzer
// (doc 07 §6). It is stored under the catalog NSAnalyzer namespace and rebuilt
// with BuildAnalyzer so index-time and query-time analysis match exactly.
type AnalyzerConfig struct {
	Name         string              `json:"name"`
	CharFilters  []CharFilterConfig  `json:"char_filters,omitempty"`
	Tokenizer    TokenizerConfig     `json:"tokenizer"`
	TokenFilters []TokenFilterConfig `json:"token_filters,omitempty"`
}

// BuildAnalyzer constructs the Analyzer described by cfg.
func BuildAnalyzer(cfg AnalyzerConfig) (*Analyzer, error) {
	a := &Analyzer{Name: cfg.Name}
	for _, cf := range cfg.CharFilters {
		f, err := buildCharFilter(cf)
		if err != nil {
			return nil, err
		}
		a.CharFilters = append(a.CharFilters, f)
	}
	tok, err := buildTokenizer(cfg.Tokenizer)
	if err != nil {
		return nil, err
	}
	a.Tokenizer = tok
	for _, tf := range cfg.TokenFilters {
		f, err := buildTokenFilter(tf)
		if err != nil {
			return nil, err
		}
		a.TokenFilters = append(a.TokenFilters, f)
	}
	return a, nil
}

func buildCharFilter(cfg CharFilterConfig) (CharFilter, error) {
	switch cfg.Type {
	case "html_strip":
		return HTMLStripCharFilter{}, nil
	case "pattern_replace":
		return NewPatternReplaceCharFilter(cfg.Pattern, cfg.Replacement)
	default:
		return nil, fmt.Errorf("analysis: unknown char filter %q", cfg.Type)
	}
}

func buildTokenizer(cfg TokenizerConfig) (Tokenizer, error) {
	switch cfg.Type {
	case "", "standard":
		return StandardTokenizer{}, nil
	case "whitespace":
		return WhitespaceTokenizer{}, nil
	case "keyword":
		return KeywordTokenizer{}, nil
	case "letter":
		return LetterTokenizer{}, nil
	case "ngram":
		return NGramTokenizer{Min: cfg.Min, Max: cfg.Max}, nil
	case "edge_ngram":
		return EdgeNGramTokenizer{Min: cfg.Min, Max: cfg.Max}, nil
	default:
		return nil, fmt.Errorf("analysis: unknown tokenizer %q", cfg.Type)
	}
}

func buildTokenFilter(cfg TokenFilterConfig) (TokenFilter, error) {
	switch cfg.Type {
	case "lowercase":
		return LowercaseFilter{}, nil
	case "stop":
		if len(cfg.Stopwords) == 0 {
			return englishStopFilter(), nil
		}
		return NewStopFilter(cfg.Stopwords), nil
	case "stemmer":
		return StemmerFilter{}, nil
	case "english_possessive_stemmer":
		return EnglishPossessiveFilter{}, nil
	case "synonym":
		return NewSynonymFilter(cfg.Synonyms), nil
	case "length":
		return LengthFilter{Min: cfg.Min, Max: cfg.Max}, nil
	case "unique":
		return UniqueFilter{}, nil
	case "ascii_folding":
		return ASCIIFoldingFilter{}, nil
	default:
		return nil, fmt.Errorf("analysis: unknown token filter %q", cfg.Type)
	}
}
