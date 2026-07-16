package hackernews

import "testing"

func TestExtractItemID(t *testing.T) {
	cases := map[string]string{
		"https://news.ycombinator.com/item?id=12345":       "12345",
		"https://news.ycombinator.com/item?id=12345&p=2":   "12345",
		"item?id=999":                                      "999",
		"https://news.ycombinator.com/newest":               "",
	}
	b := &HackerNewsBot{}
	for in, want := range cases {
		if got := b.ExtractUsername(in); got != "" {
			t.Errorf("ExtractUsername(%q) should be empty for non-profile URLs, got %q", in, got)
		}
		if got := extractItemID(in); got != want {
			t.Errorf("extractItemID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHackerNewsResolveURL(t *testing.T) {
	b := &HackerNewsBot{}
	if got := b.ResolveURL("/item?id=1"); got != "https://news.ycombinator.com/item?id=1" {
		t.Errorf("ResolveURL relative = %q", got)
	}
	if got := b.ResolveURL("https://news.ycombinator.com/item?id=1"); got != "https://news.ycombinator.com/item?id=1" {
		t.Errorf("ResolveURL absolute changed unexpectedly: %q", got)
	}
}
