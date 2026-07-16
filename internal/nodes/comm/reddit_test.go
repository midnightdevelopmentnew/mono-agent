package comm

import "testing"

func TestRedditParseSubmitResponse(t *testing.T) {
	raw := []byte(`{"json":{"errors":[],"data":{"id":"abc123","name":"t3_abc123","url":"https://reddit.com/r/test/comments/abc123/x/"}}}`)
	result, err := redditParseAPIResponse(raw)
	if err != nil {
		t.Fatalf("redditParseAPIResponse: %v", err)
	}
	if result["id"] != "abc123" {
		t.Errorf("id = %v, want abc123", result["id"])
	}
	if result["name"] != "t3_abc123" {
		t.Errorf("name = %v, want t3_abc123", result["name"])
	}
	if result["url"] != "https://reddit.com/r/test/comments/abc123/x/" {
		t.Errorf("url = %v, want the permalink", result["url"])
	}
}

func TestRedditParseSubmitResponseErrors(t *testing.T) {
	raw := []byte(`{"json":{"errors":[["RATELIMIT","you are doing that too much","ratelimit"]],"data":{}}}`)
	_, err := redditParseAPIResponse(raw)
	if err == nil {
		t.Fatal("expected error for a Reddit API error response, got nil")
	}
}

func TestRedditFullnamePrefix(t *testing.T) {
	cases := map[string]string{
		"abc123":     "t3_abc123",
		"t3_abc123":  "t3_abc123",
		"t1_xyz":     "t1_xyz",
	}
	for in, want := range cases {
		if got := redditEnsureFullname(in, "t3"); got != want {
			t.Errorf("redditEnsureFullname(%q, t3) = %q, want %q", in, got, want)
		}
	}
}
