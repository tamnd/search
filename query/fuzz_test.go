package query

import (
	"reflect"
	"testing"
)

// FuzzQueryParse feeds arbitrary strings to the query-string parser and asserts
// it never panics and is deterministic: a string that parses once parses the
// same way every time, and rewriting the result is idempotent. A parse error is
// a normal outcome for malformed input and is not a failure.
func FuzzQueryParse(f *testing.F) {
	seeds := []string{
		"",
		"hello",
		"+must -mustnot maybe",
		`title:"a phrase" body:term*`,
		"price:[10 TO 20] qty:{1 TO 5}",
		"a AND b OR c NOT d",
		`field:"unterminated`,
		"((()))",
		":::",
		"\x00\x01\x02",
		"term^2.5 boosted^",
		"a:b:c:d",
	}
	for _, s := range seeds {
		f.Add(s, "body")
	}
	f.Fuzz(func(t *testing.T, input, defaultField string) {
		q1, err1 := ParseString(input, defaultField)
		q2, err2 := ParseString(input, defaultField)

		// Parsing is deterministic: same input, same outcome.
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("nondeterministic parse of %q: err1=%v err2=%v", input, err1, err2)
		}
		if err1 != nil {
			return
		}
		if q1 == nil || q2 == nil {
			t.Fatalf("nil query without error for %q", input)
		}
		if !reflect.DeepEqual(q1, q2) {
			t.Fatalf("nondeterministic AST for %q:\n %#v\n %#v", input, q1, q2)
		}

		// Rewrite must not panic and must be a fixed point: rewriting the
		// canonical form again yields the same tree.
		r1 := q1.Rewrite()
		if r1 == nil {
			t.Fatalf("Rewrite returned nil for %q", input)
		}
		r2 := r1.Rewrite()
		if !reflect.DeepEqual(r1, r2) {
			t.Fatalf("Rewrite not idempotent for %q:\n %#v\n %#v", input, r1, r2)
		}
	})
}
