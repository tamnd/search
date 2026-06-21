package search

import (
	"strings"
	"testing"

	"github.com/tamnd/search/highlight"
	"github.com/tamnd/search/query"
)

func TestSearchHighlight_FieldFragments(t *testing.T) {
	db := indexScore(t, []map[string]any{
		{"_id": "a", "title": "the quick brown fox", "body": "a fox crossed the quiet road at dawn"},
		{"_id": "b", "title": "lazy dog sleeps", "body": "nothing to match here"},
	})
	defer mustClose(t, db)

	req := SearchRequest{
		Query: query.Match("title", "fox"),
		K:     10,
		Highlight: map[string]highlight.Options{
			"title": {},
		},
	}
	res, err := db.SearchRequestExec(req)
	if err != nil {
		t.Fatal(err)
	}
	var got *Hit
	for i := range res.Hits {
		if res.Hits[i].ExternalID == "a" {
			got = &res.Hits[i]
		}
		if res.Hits[i].ExternalID == "b" && res.Hits[i].Highlights != nil {
			t.Fatalf("doc b has no title match, should carry no highlights: %v", res.Hits[i].Highlights)
		}
	}
	if got == nil {
		t.Fatalf("doc a missing from %v", extIDs(res.Hits))
	}
	frags := got.Highlights["title"]
	if len(frags) != 1 {
		t.Fatalf("expected one title fragment, got %v", frags)
	}
	if !strings.Contains(frags[0], "<em>fox</em>") {
		t.Errorf("matched term should be wrapped: %q", frags[0])
	}
}

func TestSearchHighlight_FragmentSize(t *testing.T) {
	db := indexScore(t, []map[string]any{
		{"_id": "a", "title": "intro", "body": "alpha beta gamma delta needle epsilon zeta eta theta iota"},
	})
	defer mustClose(t, db)

	req := SearchRequest{
		Query: query.Match("body", "needle"),
		K:     10,
		Highlight: map[string]highlight.Options{
			"body": {FragmentSize: 20, PreTag: "[", PostTag: "]"},
		},
	}
	res, err := db.SearchRequestExec(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected a hit")
	}
	frags := res.Hits[0].Highlights["body"]
	if len(frags) == 0 {
		t.Fatal("expected a body fragment")
	}
	if !strings.Contains(frags[0], "[needle]") {
		t.Errorf("term should be wrapped with custom tags: %q", frags[0])
	}
	// A bounded fragment must be shorter than the whole field value.
	if len(frags[0]) >= len("alpha beta gamma delta needle epsilon zeta eta theta iota") {
		t.Errorf("fragment should be a window, not the whole field: %q", frags[0])
	}
}
