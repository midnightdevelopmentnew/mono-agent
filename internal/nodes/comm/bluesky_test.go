package comm

import "testing"

func TestBlueskyParseSession(t *testing.T) {
	raw := []byte(`{"did":"did:plc:abc123","handle":"user.bsky.social","accessJwt":"jwt-token"}`)
	sess, err := blueskyParseSession(raw)
	if err != nil {
		t.Fatalf("blueskyParseSession: %v", err)
	}
	if sess.DID != "did:plc:abc123" {
		t.Errorf("DID = %q, want did:plc:abc123", sess.DID)
	}
	if sess.AccessJwt != "jwt-token" {
		t.Errorf("AccessJwt = %q, want jwt-token", sess.AccessJwt)
	}
}

func TestBlueskyParsePostMetrics(t *testing.T) {
	raw := []byte(`{"thread":{"post":{"likeCount":5,"repostCount":2,"replyCount":1}}}`)
	result, err := blueskyParsePostMetrics(raw)
	if err != nil {
		t.Fatalf("blueskyParsePostMetrics: %v", err)
	}
	if result["like_count"] != float64(5) {
		t.Errorf("like_count = %v, want 5", result["like_count"])
	}
	if result["repost_count"] != float64(2) {
		t.Errorf("repost_count = %v, want 2", result["repost_count"])
	}
}
