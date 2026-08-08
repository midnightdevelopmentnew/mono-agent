package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"monoagent/internal/workflow"
)

// withMastodonServer starts an httptest server, points mastodonDefaultInstance
// at it for the duration of the test, and restores the original value on
// cleanup.
func withMastodonServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := mastodonDefaultInstance
	mastodonDefaultInstance = srv.URL
	t.Cleanup(func() { mastodonDefaultInstance = orig })
	return srv
}

func mastodonExecute(t *testing.T, config map[string]interface{}) []workflow.NodeOutput {
	t.Helper()
	n := &MastodonNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return out
}

func mastodonMainItems(out []workflow.NodeOutput) []workflow.Item {
	for _, o := range out {
		if o.Handle == "main" {
			return o.Items
		}
	}
	return nil
}

func TestMastodonNode_Type(t *testing.T) {
	n := &MastodonNode{}
	if got := n.Type(); got != "service.mastodon" {
		t.Errorf("Type() = %q, want service.mastodon", got)
	}
}

func TestMastodonNode_RequiresAccessToken(t *testing.T) {
	n := &MastodonNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "get_account",
	})
	if err == nil {
		t.Fatal("expected error when access_token is missing")
	}
}

func TestMastodonNode_UnknownOperation(t *testing.T) {
	n := &MastodonNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "bogus",
		"access_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error for unknown operation")
	}
}

func TestMastodonNode_PublishStatus(t *testing.T) {
	var seenAuth string
	var gotPath string
	var gotBody map[string]interface{}

	withMastodonServer(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "12345",
			"content": "Hello World",
		})
	})

	out := mastodonExecute(t, map[string]interface{}{
		"operation":    "publish_status",
		"access_token": "test-token",
		"text":         "Hello World",
		"visibility":   "unlisted",
		"spoiler_text": "cw",
		"media_ids":    []interface{}{"1", "2"},
	})

	if seenAuth != "Bearer test-token" {
		t.Errorf("Authorization header = %q, want Bearer test-token", seenAuth)
	}
	if gotPath != "/api/v1/statuses" {
		t.Errorf("path = %q, want /api/v1/statuses", gotPath)
	}
	if gotBody["status"] != "Hello World" {
		t.Errorf("status = %v, want Hello World", gotBody["status"])
	}
	if gotBody["visibility"] != "unlisted" {
		t.Errorf("visibility = %v, want unlisted", gotBody["visibility"])
	}
	if gotBody["spoiler_text"] != "cw" {
		t.Errorf("spoiler_text = %v, want cw", gotBody["spoiler_text"])
	}
	mediaIDs, ok := gotBody["media_ids"].([]interface{})
	if !ok || len(mediaIDs) != 2 {
		t.Fatalf("media_ids = %v, want 2 ids", gotBody["media_ids"])
	}
	if mediaIDs[0] != "1" || mediaIDs[1] != "2" {
		t.Errorf("media_ids = %v, want [1 2]", mediaIDs)
	}

	items := mastodonMainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].JSON["id"] != "12345" {
		t.Errorf("id = %v, want 12345", items[0].JSON["id"])
	}
}

func TestMastodonNode_PublishStatusRequiresText(t *testing.T) {
	n := &MastodonNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "publish_status",
		"access_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error when text is missing")
	}
}

func TestMastodonNode_GetTimeline(t *testing.T) {
	var gotPath, gotQuery string
	withMastodonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": "1", "content": "Post 1"},
			{"id": "2", "content": "Post 2"},
		})
	})

	out := mastodonExecute(t, map[string]interface{}{
		"operation":    "get_timeline",
		"access_token": "test-token",
		"limit":        float64(5),
	})

	if gotPath != "/api/v1/timelines/home" {
		t.Errorf("path = %q, want /api/v1/timelines/home", gotPath)
	}
	if gotQuery != "limit=5" {
		t.Errorf("query = %q, want limit=5", gotQuery)
	}
	items := mastodonMainItems(out)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].JSON["content"] != "Post 1" {
		t.Errorf("content = %v, want Post 1", items[0].JSON["content"])
	}
}

func TestMastodonNode_GetTimelineDefaultsLimit(t *testing.T) {
	var gotQuery string
	withMastodonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	})

	mastodonExecute(t, map[string]interface{}{
		"operation":    "get_timeline",
		"access_token": "test-token",
	})

	if gotQuery != "limit=20" {
		t.Errorf("query = %q, want limit=20", gotQuery)
	}
}

func TestMastodonNode_GetAccount(t *testing.T) {
	var gotPath string
	withMastodonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "1", "username": "me"})
	})

	out := mastodonExecute(t, map[string]interface{}{
		"operation":    "get_account",
		"access_token": "test-token",
	})

	if gotPath != "/api/v1/accounts/verify_credentials" {
		t.Errorf("path = %q, want /api/v1/accounts/verify_credentials", gotPath)
	}
	items := mastodonMainItems(out)
	if len(items) != 1 || items[0].JSON["username"] != "me" {
		t.Fatalf("unexpected items: %v", items)
	}
}

func TestMastodonNode_Favourite(t *testing.T) {
	var gotPath string
	withMastodonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "99", "favourited": true})
	})

	out := mastodonExecute(t, map[string]interface{}{
		"operation":    "favourite",
		"access_token": "test-token",
		"status_id":    float64(99),
	})

	if gotPath != "/api/v1/statuses/99/favourite" {
		t.Errorf("path = %q, want /api/v1/statuses/99/favourite", gotPath)
	}
	items := mastodonMainItems(out)
	if len(items) != 1 || items[0].JSON["favourited"] != true {
		t.Fatalf("unexpected items: %v", items)
	}
}

func TestMastodonNode_FavouriteRequiresStatusID(t *testing.T) {
	n := &MastodonNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "favourite",
		"access_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error when status_id is missing")
	}
}

func TestMastodonNode_Boost(t *testing.T) {
	var gotPath string
	withMastodonServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "99", "reblogged": true})
	})

	out := mastodonExecute(t, map[string]interface{}{
		"operation":    "boost",
		"access_token": "test-token",
		"status_id":    "99",
	})

	if gotPath != "/api/v1/statuses/99/reblog" {
		t.Errorf("path = %q, want /api/v1/statuses/99/reblog", gotPath)
	}
	items := mastodonMainItems(out)
	if len(items) != 1 || items[0].JSON["reblogged"] != true {
		t.Fatalf("unexpected items: %v", items)
	}
}

func TestMastodonNode_BoostRequiresStatusID(t *testing.T) {
	n := &MastodonNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "boost",
		"access_token": "test-token",
	})
	if err == nil {
		t.Fatal("expected error when status_id is missing")
	}
}

func TestMastodonNode_ErrorResponsePropagates(t *testing.T) {
	withMastodonServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	})

	n := &MastodonNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_account",
		"access_token": "bad-token",
	})
	if err == nil {
		t.Fatal("expected error on 401 response")
	}
}

func TestMastodonNode_InstanceOverride(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "1"})
	}))
	defer srv.Close()

	n := &MastodonNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_account",
		"access_token": "test-token",
		"instance":     srv.URL,
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	wantHost := srv.URL[len("http://"):]
	if gotHost != wantHost {
		t.Errorf("host = %q, want %q", gotHost, wantHost)
	}
}

func TestMastodonStatusID(t *testing.T) {
	cases := []struct {
		config map[string]interface{}
		want   string
	}{
		{map[string]interface{}{"status_id": "abc"}, "abc"},
		{map[string]interface{}{"status_id": float64(42)}, "42"},
		{map[string]interface{}{"status_id": 7}, "7"},
		{map[string]interface{}{}, ""},
	}
	for _, c := range cases {
		if got := mastodonStatusID(c.config); got != c.want {
			t.Errorf("mastodonStatusID(%v) = %q, want %q", c.config, got, c.want)
		}
	}
}
