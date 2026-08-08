package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"monoagent/internal/workflow"
)

// withBlueskyServer starts an httptest server, points blueskyBaseURL at it
// for the duration of the test, and restores the original value on cleanup.
// The provided handler is invoked for all requests except
// com.atproto.server.createSession, which is answered automatically with a
// successful auth response (accessJwt/did) unless authStatus is non-zero, in
// which case the auth call itself fails with that status.
func withBlueskyServer(t *testing.T, authStatus int, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/com.atproto.server.createSession" {
			if authStatus != 0 {
				w.WriteHeader(authStatus)
				_, _ = w.Write([]byte(`{"error":"AuthenticationRequired"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"accessJwt": "test-jwt",
				"did":       "did:plc:testuser",
			})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	orig := blueskyBaseURL
	blueskyBaseURL = srv.URL
	t.Cleanup(func() { blueskyBaseURL = orig })
	return srv
}

func blueskyExecute(t *testing.T, config map[string]interface{}) []workflow.NodeOutput {
	t.Helper()
	n := &BlueskyNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return out
}

func blueskyMainItems(out []workflow.NodeOutput) []workflow.Item {
	for _, o := range out {
		if o.Handle == "main" {
			return o.Items
		}
	}
	return nil
}

func blueskyBaseConfig() map[string]interface{} {
	return map[string]interface{}{
		"identifier":   "monoes_me.bsky.social",
		"app_password": "test-app-password",
	}
}

func TestBlueskyNode_Type(t *testing.T) {
	n := &BlueskyNode{}
	if got := n.Type(); got != "service.bluesky" {
		t.Errorf("Type() = %q, want service.bluesky", got)
	}
}

func TestBlueskyNode_RequiresIdentifier(t *testing.T) {
	n := &BlueskyNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_timeline",
		"app_password": "test-app-password",
	})
	if err == nil {
		t.Fatal("expected error when identifier is missing")
	}
}

func TestBlueskyNode_RequiresAppPassword(t *testing.T) {
	n := &BlueskyNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":  "get_timeline",
		"identifier": "monoes_me.bsky.social",
	})
	if err == nil {
		t.Fatal("expected error when app_password is missing")
	}
}

func TestBlueskyNode_UnknownOperation(t *testing.T) {
	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	})

	n := &BlueskyNode{}
	config := blueskyBaseConfig()
	config["operation"] = "bogus"
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err == nil {
		t.Fatal("expected error for unknown operation")
	}
}

func TestBlueskyNode_AuthErrorPropagates(t *testing.T) {
	withBlueskyServer(t, http.StatusUnauthorized, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	})

	n := &BlueskyNode{}
	config := blueskyBaseConfig()
	config["operation"] = "get_timeline"
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err == nil {
		t.Fatal("expected error when auth fails")
	}
}

func TestBlueskyNode_CreatePost(t *testing.T) {
	var seenAuth, gotPath string
	var gotBody map[string]interface{}

	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"uri": "at://did:plc:testuser/app.bsky.feed.post/abc123",
			"cid": "bafyabc123",
		})
	})

	config := blueskyBaseConfig()
	config["operation"] = "create_post"
	config["text"] = "Hello Bluesky!"
	out := blueskyExecute(t, config)

	if seenAuth != "Bearer test-jwt" {
		t.Errorf("Authorization header = %q, want Bearer test-jwt", seenAuth)
	}
	if gotPath != "/com.atproto.repo.createRecord" {
		t.Errorf("path = %q, want /com.atproto.repo.createRecord", gotPath)
	}
	if gotBody["repo"] != "did:plc:testuser" {
		t.Errorf("repo = %v, want did:plc:testuser", gotBody["repo"])
	}
	if gotBody["collection"] != "app.bsky.feed.post" {
		t.Errorf("collection = %v, want app.bsky.feed.post", gotBody["collection"])
	}
	record, ok := gotBody["record"].(map[string]interface{})
	if !ok {
		t.Fatalf("request body missing 'record' key: %v", gotBody)
	}
	if record["$type"] != "app.bsky.feed.post" {
		t.Errorf("record.$type = %v, want app.bsky.feed.post", record["$type"])
	}
	if record["text"] != "Hello Bluesky!" {
		t.Errorf("record.text = %v, want 'Hello Bluesky!'", record["text"])
	}
	if record["createdAt"] == nil || record["createdAt"] == "" {
		t.Errorf("record.createdAt = %v, want non-empty timestamp", record["createdAt"])
	}

	items := blueskyMainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].JSON["uri"] != "at://did:plc:testuser/app.bsky.feed.post/abc123" {
		t.Errorf("uri = %v", items[0].JSON["uri"])
	}
}

func TestBlueskyNode_CreatePostRequiresText(t *testing.T) {
	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	})

	n := &BlueskyNode{}
	config := blueskyBaseConfig()
	config["operation"] = "create_post"
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err == nil {
		t.Fatal("expected error when text is missing")
	}
}

func TestBlueskyNode_GetTimeline(t *testing.T) {
	var gotPath, gotQuery string
	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"feed": []interface{}{
				map[string]interface{}{"post": map[string]interface{}{"uri": "at://post1"}},
			},
		})
	})

	config := blueskyBaseConfig()
	config["operation"] = "get_timeline"
	config["limit"] = float64(10)
	out := blueskyExecute(t, config)

	if gotPath != "/app.bsky.feed.getTimeline" {
		t.Errorf("path = %q, want /app.bsky.feed.getTimeline", gotPath)
	}
	if gotQuery != "limit=10" {
		t.Errorf("query = %q, want limit=10", gotQuery)
	}
	items := blueskyMainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestBlueskyNode_GetTimelineDefaultsLimit(t *testing.T) {
	var gotQuery string
	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"feed": []interface{}{}})
	})

	config := blueskyBaseConfig()
	config["operation"] = "get_timeline"
	blueskyExecute(t, config)

	if gotQuery != "limit=30" {
		t.Errorf("query = %q, want limit=30", gotQuery)
	}
}

func TestBlueskyNode_GetProfile(t *testing.T) {
	var gotPath, gotQuery string
	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"handle":      "monoes_me.bsky.social",
			"displayName": "Monoes",
		})
	})

	config := blueskyBaseConfig()
	config["operation"] = "get_profile"
	config["actor"] = "monoes_me.bsky.social"
	out := blueskyExecute(t, config)

	if gotPath != "/app.bsky.actor.getProfile" {
		t.Errorf("path = %q, want /app.bsky.actor.getProfile", gotPath)
	}
	if gotQuery != "actor=monoes_me.bsky.social" {
		t.Errorf("query = %q, want actor=monoes_me.bsky.social", gotQuery)
	}
	items := blueskyMainItems(out)
	if len(items) != 1 || items[0].JSON["handle"] != "monoes_me.bsky.social" {
		t.Fatalf("unexpected items: %v", items)
	}
}

func TestBlueskyNode_GetProfileRequiresActor(t *testing.T) {
	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	})

	n := &BlueskyNode{}
	config := blueskyBaseConfig()
	config["operation"] = "get_profile"
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err == nil {
		t.Fatal("expected error when actor is missing")
	}
}

func TestBlueskyNode_LikePost(t *testing.T) {
	var gotPath string
	var gotBody map[string]interface{}
	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"uri": "at://did:plc:testuser/app.bsky.feed.like/like1",
		})
	})

	config := blueskyBaseConfig()
	config["operation"] = "like_post"
	config["uri"] = "at://did:plc:other/app.bsky.feed.post/xyz"
	config["cid"] = "bafyxyz"
	out := blueskyExecute(t, config)

	if gotPath != "/com.atproto.repo.createRecord" {
		t.Errorf("path = %q, want /com.atproto.repo.createRecord", gotPath)
	}
	if gotBody["collection"] != "app.bsky.feed.like" {
		t.Errorf("collection = %v, want app.bsky.feed.like", gotBody["collection"])
	}
	record, ok := gotBody["record"].(map[string]interface{})
	if !ok {
		t.Fatalf("request body missing 'record' key: %v", gotBody)
	}
	subject, ok := record["subject"].(map[string]interface{})
	if !ok {
		t.Fatalf("record missing 'subject' key: %v", record)
	}
	if subject["uri"] != "at://did:plc:other/app.bsky.feed.post/xyz" {
		t.Errorf("subject.uri = %v", subject["uri"])
	}
	if subject["cid"] != "bafyxyz" {
		t.Errorf("subject.cid = %v", subject["cid"])
	}

	items := blueskyMainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestBlueskyNode_LikePostRequiresUriAndCid(t *testing.T) {
	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	})

	n := &BlueskyNode{}
	config := blueskyBaseConfig()
	config["operation"] = "like_post"
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err == nil {
		t.Fatal("expected error when uri/cid are missing")
	}
}

func TestBlueskyNode_Repost(t *testing.T) {
	var gotPath string
	var gotBody map[string]interface{}
	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"uri": "at://did:plc:testuser/app.bsky.feed.repost/rp1",
		})
	})

	config := blueskyBaseConfig()
	config["operation"] = "repost"
	config["uri"] = "at://did:plc:other/app.bsky.feed.post/xyz"
	config["cid"] = "bafyxyz"
	out := blueskyExecute(t, config)

	if gotPath != "/com.atproto.repo.createRecord" {
		t.Errorf("path = %q, want /com.atproto.repo.createRecord", gotPath)
	}
	if gotBody["collection"] != "app.bsky.feed.repost" {
		t.Errorf("collection = %v, want app.bsky.feed.repost", gotBody["collection"])
	}
	record, ok := gotBody["record"].(map[string]interface{})
	if !ok {
		t.Fatalf("request body missing 'record' key: %v", gotBody)
	}
	if record["$type"] != "app.bsky.feed.repost" {
		t.Errorf("record.$type = %v, want app.bsky.feed.repost", record["$type"])
	}

	items := blueskyMainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestBlueskyNode_RepostRequiresUriAndCid(t *testing.T) {
	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	})

	n := &BlueskyNode{}
	config := blueskyBaseConfig()
	config["operation"] = "repost"
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err == nil {
		t.Fatal("expected error when uri/cid are missing")
	}
}

func TestBlueskyNode_ErrorResponsePropagates(t *testing.T) {
	withBlueskyServer(t, 0, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"InvalidRequest"}`))
	})

	n := &BlueskyNode{}
	config := blueskyBaseConfig()
	config["operation"] = "get_profile"
	config["actor"] = "monoes_me.bsky.social"
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err == nil {
		t.Fatal("expected error on 400 response")
	}
}
