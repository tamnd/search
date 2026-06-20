package analysis

import (
	"regexp"
	"strings"
)

// CharFilter rewrites raw text before tokenization (doc 07 §2.1). At S2 a char
// filter maps a string to a string; offset correction back to the original text
// is deferred, so downstream offsets refer to the filtered text.
type CharFilter interface {
	Filter(text string) string
}

// HTMLStripCharFilter removes HTML tags and decodes a small set of common
// character entities, leaving the textual content (doc 07 §2.2). "<b>hello</b>
// &amp; world" becomes "hello & world".
type HTMLStripCharFilter struct{}

var htmlTag = regexp.MustCompile(`(?s)<[^>]*>`)

var htmlEntities = strings.NewReplacer(
	"&amp;", "&",
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", `"`,
	"&apos;", "'",
	"&#39;", "'",
	"&nbsp;", " ",
)

// Filter implements CharFilter.
func (HTMLStripCharFilter) Filter(text string) string {
	stripped := htmlTag.ReplaceAllString(text, " ")
	return htmlEntities.Replace(stripped)
}

// PatternReplaceCharFilter replaces every match of a regular expression with a
// replacement string (doc 07 §2.3). The replacement uses Go's $1 / ${name}
// expansion syntax.
type PatternReplaceCharFilter struct {
	re          *regexp.Regexp
	replacement string
}

// NewPatternReplaceCharFilter compiles pattern and returns a char filter that
// rewrites each match to replacement.
func NewPatternReplaceCharFilter(pattern, replacement string) (*PatternReplaceCharFilter, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return &PatternReplaceCharFilter{re: re, replacement: replacement}, nil
}

// Filter implements CharFilter.
func (f *PatternReplaceCharFilter) Filter(text string) string {
	return f.re.ReplaceAllString(text, f.replacement)
}
