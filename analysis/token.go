// Package analysis is the text analysis pipeline (spec 2063 doc 07). It turns a
// field value into the token stream that the inverted index consumes, in three
// ordered stages: character filters rewrite the raw text, a tokenizer splits it
// into terms, and token filters transform or drop those terms. The same analyzer
// must run at index time and query time, so an analyzer is fully described by its
// configuration and rebuilt identically from it (doc 07 §1.3).
//
// At S2 the pipeline produces a materialized []Token rather than the streaming
// TokenStream of doc 07 §2; the streaming, zero-allocation form is a later
// optimization. Token offsets are byte offsets into the text the tokenizer saw;
// when char filters are present those are offsets into the filtered text, since
// full offset correction back to the original is deferred.
package analysis

// FlagKeyword marks a token that transforming filters (stemmers, folders) must
// leave unchanged (doc 07 §1.2). It is bit 0 of Token.Flags.
const FlagKeyword uint32 = 1

// Token is one unit of the analysis pipeline.
type Token struct {
	Term           string // the analyzed term (UTF-8)
	StartOffset    int    // byte offset of the first byte of this token
	EndOffset      int    // byte offset one past the last byte of this token
	PositionIncr   int    // position increment vs the previous token (1 normally, 0 = synonym)
	PositionLength int    // positions this token spans (1 normally)
	Type           string // token type tag, e.g. "<ALPHANUM>", "<NUM>"
	Flags          uint32 // reserved bit flags; bit 0 = keyword
}

// newToken returns a token with the default increment and length of 1.
func newToken(term string, start, end int, typ string) Token {
	return Token{
		Term:           term,
		StartOffset:    start,
		EndOffset:      end,
		PositionIncr:   1,
		PositionLength: 1,
		Type:           typ,
	}
}

// isKeyword reports whether the keyword flag is set on the token.
func (t Token) isKeyword() bool { return t.Flags&FlagKeyword != 0 }
