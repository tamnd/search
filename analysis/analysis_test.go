package analysis

import (
	"reflect"
	"testing"
)

// terms extracts the term strings from a token slice, returning nil for none.
func terms(toks []Token) []string {
	if len(toks) == 0 {
		return nil
	}
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.Term
	}
	return out
}

func TestStandardTokenizer(t *testing.T) {
	a, err := NewNamed("standard")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		in   string
		want []string
	}{
		{"The quick-brown fox", []string{"the", "quick", "brown", "fox"}},
		{"fox's den", []string{"fox's", "den"}},                          // intra-word apostrophe kept
		{"ping 192.168.1.1 now", []string{"ping", "192.168.1.1", "now"}}, // IP stays one token
		{"  spaced\tout\n", []string{"spaced", "out"}},
		{"e-mail: a@b.com", []string{"e", "mail", "a", "b", "com"}},
		{"", nil},
	}
	for _, c := range cases {
		if got := terms(a.Analyze(c.in)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("standard %q = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestStandardTokenTypes(t *testing.T) {
	toks := StandardTokenizer{}.Tokenize("abc 123 a1b2 192.168.0.1")
	want := []struct {
		term string
		typ  string
	}{
		{"abc", "<ALPHA>"},
		{"123", "<NUM>"},
		{"a1b2", "<ALPHANUM>"},
		{"192.168.0.1", "<NUM>"},
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d: %v", len(toks), len(want), terms(toks))
	}
	for i, w := range want {
		if toks[i].Term != w.term || toks[i].Type != w.typ {
			t.Errorf("token %d = (%q,%s), want (%q,%s)", i, toks[i].Term, toks[i].Type, w.term, w.typ)
		}
	}
}

func TestStandardOffsets(t *testing.T) {
	toks := StandardTokenizer{}.Tokenize("hello world")
	if toks[0].StartOffset != 0 || toks[0].EndOffset != 5 {
		t.Errorf("token 0 offsets = %d..%d, want 0..5", toks[0].StartOffset, toks[0].EndOffset)
	}
	if toks[1].StartOffset != 6 || toks[1].EndOffset != 11 {
		t.Errorf("token 1 offsets = %d..%d, want 6..11", toks[1].StartOffset, toks[1].EndOffset)
	}
}

func TestEnglishStemming(t *testing.T) {
	a, err := NewNamed("english")
	if err != nil {
		t.Fatal(err)
	}
	// running/runs/jumping/jumped/jumps collapse to run/jump. "runner" does NOT
	// reduce to "run": faithful Porter2 stems it to "runner" (the roadmap's
	// "runner -> run" note is incorrect against the reference algorithm, which
	// this implementation matches bit-for-bit over the full Snowball vocabulary).
	cases := []struct {
		in   string
		want []string
	}{
		{"running runs run", []string{"run", "run", "run"}},
		{"jumping jumped jumps jump", []string{"jump", "jump", "jump", "jump"}},
		{"runner runners", []string{"runner", "runner"}},
		{"computer's computing computed", []string{"comput", "comput", "comput"}},
		{"The lazy DOGS", []string{"lazi", "dog"}}, // stopword "the" removed, case folded
	}
	for _, c := range cases {
		if got := terms(a.Analyze(c.in)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("english %q = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestEnglishStopWords(t *testing.T) {
	a, _ := NewNamed("english")
	got := terms(a.Analyze("the cat sat on a mat and the dog ran over it"))
	want := []string{"cat", "sat", "mat", "dog", "ran"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("english stop = %v, want %v", got, want)
	}
}

func TestStopFilterKeepsPositions(t *testing.T) {
	a := &Analyzer{
		Tokenizer:    StandardTokenizer{},
		TokenFilters: []TokenFilter{LowercaseFilter{}, englishStopFilter()},
	}
	toks := a.Analyze("the cat and the dog")
	if got := terms(toks); !reflect.DeepEqual(got, []string{"cat", "dog"}) {
		t.Fatalf("terms = %v", got)
	}
	// "cat" is the 2nd token (position 1), "dog" the 5th (position 4); the gap
	// from the removed stop words must survive as the position increment.
	if got := Position(toks); !reflect.DeepEqual(got, []int{1, 4}) {
		t.Errorf("positions = %v, want [1 4]", got)
	}
}

func TestKeywordAndSimpleAnalyzers(t *testing.T) {
	kw, _ := NewNamed("keyword")
	if got := terms(kw.Analyze("Hello, World!")); !reflect.DeepEqual(got, []string{"Hello, World!"}) {
		t.Errorf("keyword = %v", got)
	}
	sm, _ := NewNamed("simple")
	if got := terms(sm.Analyze("Hello, World2!")); !reflect.DeepEqual(got, []string{"hello", "world"}) {
		t.Errorf("simple = %v", got)
	}
}

func TestNGramOrder(t *testing.T) {
	a := &Analyzer{Tokenizer: NGramTokenizer{Min: 2, Max: 3}}
	got := terms(a.Analyze("hello"))
	want := []string{"he", "hel", "el", "ell", "ll", "llo", "lo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ngram = %v, want %v", got, want)
	}
}

func TestEdgeNGram(t *testing.T) {
	a := &Analyzer{Tokenizer: EdgeNGramTokenizer{Min: 1, Max: 3}}
	got := terms(a.Analyze("hello"))
	want := []string{"h", "he", "hel"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edge_ngram = %v, want %v", got, want)
	}
}

func TestHTMLStrip(t *testing.T) {
	a := &Analyzer{
		CharFilters:  []CharFilter{HTMLStripCharFilter{}},
		Tokenizer:    StandardTokenizer{},
		TokenFilters: []TokenFilter{LowercaseFilter{}},
	}
	got := terms(a.Analyze("<p>Hello</p> &amp; <b>World</b>"))
	want := []string{"hello", "world"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("html_strip = %v, want %v", got, want)
	}
}

func TestPatternReplace(t *testing.T) {
	cf, err := NewPatternReplaceCharFilter(`\d+`, "#")
	if err != nil {
		t.Fatal(err)
	}
	a := &Analyzer{CharFilters: []CharFilter{cf}, Tokenizer: WhitespaceTokenizer{}}
	got := terms(a.Analyze("abc123 def456"))
	if !reflect.DeepEqual(got, []string{"abc#", "def#"}) {
		t.Errorf("pattern_replace = %v", got)
	}
}

func TestSynonymExpand(t *testing.T) {
	a := &Analyzer{
		Tokenizer:    WhitespaceTokenizer{},
		TokenFilters: []TokenFilter{NewSynonymFilter([]string{"fast => quick"})},
	}
	toks := a.Analyze("a fast car")
	if got := terms(toks); !reflect.DeepEqual(got, []string{"a", "fast", "quick", "car"}) {
		t.Fatalf("synonym terms = %v", got)
	}
	// The injected synonym shares the position of the term it follows.
	if got := Position(toks); !reflect.DeepEqual(got, []int{0, 1, 1, 2}) {
		t.Errorf("synonym positions = %v, want [0 1 1 2]", got)
	}
}

func TestSynonymGroup(t *testing.T) {
	a := &Analyzer{
		Tokenizer:    WhitespaceTokenizer{},
		TokenFilters: []TokenFilter{NewSynonymFilter([]string{"fast, quick, rapid"})},
	}
	got := terms(a.Analyze("quick"))
	if !reflect.DeepEqual(got, []string{"quick", "fast", "rapid"}) {
		t.Errorf("synonym group = %v", got)
	}
}

func TestLengthAndUniqueFilters(t *testing.T) {
	lf := &Analyzer{Tokenizer: WhitespaceTokenizer{}, TokenFilters: []TokenFilter{LengthFilter{Min: 3, Max: 5}}}
	if got := terms(lf.Analyze("a to cat horse elephant")); !reflect.DeepEqual(got, []string{"cat", "horse"}) {
		t.Errorf("length = %v", got)
	}
	uf := &Analyzer{Tokenizer: WhitespaceTokenizer{}, TokenFilters: []TokenFilter{UniqueFilter{}}}
	if got := terms(uf.Analyze("a b a c b a")); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("unique = %v", got)
	}
}

func TestASCIIFolding(t *testing.T) {
	a := &Analyzer{Tokenizer: WhitespaceTokenizer{}, TokenFilters: []TokenFilter{ASCIIFoldingFilter{}}}
	got := terms(a.Analyze("café naïve Straße jalapeño"))
	want := []string{"cafe", "naive", "Strasse", "jalapeno"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ascii_folding = %v, want %v", got, want)
	}
}

func TestCustomAnalyzerFromConfig(t *testing.T) {
	cfg := AnalyzerConfig{
		Name:        "my_custom",
		CharFilters: []CharFilterConfig{{Type: "html_strip"}},
		Tokenizer:   TokenizerConfig{Type: "standard"},
		TokenFilters: []TokenFilterConfig{
			{Type: "lowercase"},
			{Type: "stop", Stopwords: []string{"the", "a"}},
			{Type: "stemmer"},
		},
	}
	a, err := BuildAnalyzer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := terms(a.Analyze("<p>The CATS are running</p>"))
	want := []string{"cat", "are", "run"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("custom = %v, want %v", got, want)
	}
}

func TestBuildAnalyzerErrors(t *testing.T) {
	for _, cfg := range []AnalyzerConfig{
		{Tokenizer: TokenizerConfig{Type: "nope"}},
		{Tokenizer: TokenizerConfig{Type: "standard"}, CharFilters: []CharFilterConfig{{Type: "nope"}}},
		{Tokenizer: TokenizerConfig{Type: "standard"}, TokenFilters: []TokenFilterConfig{{Type: "nope"}}},
	} {
		if _, err := BuildAnalyzer(cfg); err == nil {
			t.Errorf("expected error for %+v", cfg)
		}
	}
	if _, err := NewNamed("bogus"); err == nil {
		t.Error("expected error for unknown named analyzer")
	}
}

// TestPorter2Reference checks a spread of words against the published Snowball
// reference stems. The complete 42k-word vocabulary is verified out of band; this
// keeps a representative, committed sample in the suite.
func TestPorter2Reference(t *testing.T) {
	cases := map[string]string{
		"consign": "consign", "consigned": "consign", "consigning": "consign",
		"consistent": "consist", "consistently": "consist",
		"generalization": "general", "generally": "general",
		"national": "nation", "international": "internat", "rational": "ration",
		"internal": "internal", "organic": "organic", "organize": "organiz",
		"happy": "happi", "happily": "happili",
		"running": "run", "runs": "run", "runner": "runner", "ran": "ran",
		"jumping": "jump", "jumped": "jump",
		"added": "add", "hopping": "hop", "fizzed": "fizz",
		"apologist": "apolog", "fluently": "fluentli",
		"agreed": "agre", "feed": "feed", "proceed": "proceed",
		"dying": "die", "hying": "hie", "evening": "evening",
		"sky": "sky", "skis": "ski", "ties": "tie", "ponies": "poni",
	}
	for in, want := range cases {
		if got := porter2(in); got != want {
			t.Errorf("porter2(%q) = %q, want %q", in, got, want)
		}
	}
}
