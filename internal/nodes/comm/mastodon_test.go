package comm

import "testing"

func TestMastodonNormalizeInstanceURL(t *testing.T) {
	cases := map[string]string{
		"https://fosstodon.org":  "https://fosstodon.org",
		"https://fosstodon.org/": "https://fosstodon.org",
		"fosstodon.org":          "https://fosstodon.org",
	}
	for in, want := range cases {
		if got := mastodonNormalizeInstanceURL(in); got != want {
			t.Errorf("mastodonNormalizeInstanceURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMastodonParseStatusResponse(t *testing.T) {
	raw := []byte(`{"id":"110","url":"https://fosstodon.org/@x/110","favourites_count":3,"reblogs_count":1}`)
	result, err := mastodonParseStatus(raw)
	if err != nil {
		t.Fatalf("mastodonParseStatus: %v", err)
	}
	if result["id"] != "110" {
		t.Errorf("id = %v, want 110", result["id"])
	}
	if result["favourites_count"] != float64(3) {
		t.Errorf("favourites_count = %v, want 3", result["favourites_count"])
	}
}
