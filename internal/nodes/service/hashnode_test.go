package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"monoagent/internal/workflow"
)

// withHashnodeTestServer points hashnodeGraphQLURL at an httptest server for
// the duration of the test and restores the original value afterward.
func withHashnodeTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	orig := hashnodeGraphQLURL
	hashnodeGraphQLURL = srv.URL
	t.Cleanup(func() {
		srv.Close()
		hashnodeGraphQLURL = orig
	})
	return srv
}

func decodeGraphQLBody(t *testing.T, r *http.Request) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
	return body
}

func TestHashnodeNode_Type(t *testing.T) {
	n := &HashnodeNode{}
	if got := n.Type(); got != "service.hashnode" {
		t.Errorf("Type() = %q, want %q", got, "service.hashnode")
	}
}

func TestHashnodeNode_RequiresToken(t *testing.T) {
	n := &HashnodeNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "list_posts",
	})
	if err == nil {
		t.Fatal("expected error when token is missing")
	}
}

func TestHashnodeNode_UnknownOperation(t *testing.T) {
	n := &HashnodeNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"token":     "tok",
		"operation": "does_not_exist",
	})
	if err == nil {
		t.Fatal("expected error for unknown operation")
	}
}

func TestHashnodeNode_PublishPost(t *testing.T) {
	withHashnodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "test-token" {
			t.Errorf("Authorization header = %q, want %q (no Bearer prefix)", got, "test-token")
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		body := decodeGraphQLBody(t, r)
		vars, _ := body["variables"].(map[string]interface{})
		input, _ := vars["input"].(map[string]interface{})
		if input["publicationId"] != "pub-123" {
			t.Errorf("publicationId = %v, want pub-123", input["publicationId"])
		}
		if input["title"] != "My Post" {
			t.Errorf("title = %v, want My Post", input["title"])
		}
		if input["contentMarkdown"] != "# Hello" {
			t.Errorf("contentMarkdown = %v, want '# Hello'", input["contentMarkdown"])
		}
		tags, _ := input["tags"].([]interface{})
		if len(tags) != 2 {
			t.Fatalf("tags = %v, want 2 entries", tags)
		}
		firstTag, _ := tags[0].(map[string]interface{})
		if firstTag["slug"] != "go" {
			t.Errorf("tags[0].slug = %v, want go", firstTag["slug"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"publishPost": map[string]interface{}{
					"post": map[string]interface{}{
						"id":    "post-1",
						"title": "My Post",
						"slug":  "my-post",
						"url":   "https://myblog.hashnode.dev/my-post",
					},
				},
			},
		})
	})

	n := &HashnodeNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"token":            "test-token",
		"operation":        "publish_post",
		"publication_id":   "pub-123",
		"title":            "My Post",
		"content_markdown": "# Hello",
		"tags":             []interface{}{"go", "backend"},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	items := handleItemsHashnode(out, "main")
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].JSON["id"] != "post-1" {
		t.Errorf("id = %v, want post-1", items[0].JSON["id"])
	}
	if items[0].JSON["url"] != "https://myblog.hashnode.dev/my-post" {
		t.Errorf("url = %v, want https://myblog.hashnode.dev/my-post", items[0].JSON["url"])
	}
}

func TestHashnodeNode_PublishPost_RequiresFields(t *testing.T) {
	n := &HashnodeNode{}
	base := map[string]interface{}{
		"token":     "test-token",
		"operation": "publish_post",
	}
	cases := []map[string]interface{}{
		{},
		{"publication_id": "pub-1"},
		{"publication_id": "pub-1", "title": "T"},
	}
	for _, extra := range cases {
		cfg := map[string]interface{}{}
		for k, v := range base {
			cfg[k] = v
		}
		for k, v := range extra {
			cfg[k] = v
		}
		if _, err := n.Execute(context.Background(), workflow.NodeInput{}, cfg); err == nil {
			t.Errorf("Execute(%v) = nil error, want validation error", cfg)
		}
	}
}

func TestHashnodeNode_ListPosts(t *testing.T) {
	withHashnodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeGraphQLBody(t, r)
		vars, _ := body["variables"].(map[string]interface{})
		if vars["host"] != "myblog.hashnode.dev" {
			t.Errorf("host = %v, want myblog.hashnode.dev", vars["host"])
		}
		if vars["first"] != float64(10) {
			t.Errorf("first = %v, want 10", vars["first"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"publication": map[string]interface{}{
					"posts": map[string]interface{}{
						"edges": []interface{}{
							map[string]interface{}{
								"node": map[string]interface{}{
									"id":    "post-1",
									"title": "First Post",
									"slug":  "first-post",
								},
							},
							map[string]interface{}{
								"node": map[string]interface{}{
									"id":    "post-2",
									"title": "Second Post",
									"slug":  "second-post",
								},
							},
						},
					},
				},
			},
		})
	})

	n := &HashnodeNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"token":            "test-token",
		"operation":        "list_posts",
		"publication_host": "myblog.hashnode.dev",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	items := handleItemsHashnode(out, "main")
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].JSON["slug"] != "first-post" {
		t.Errorf("items[0].slug = %v, want first-post", items[0].JSON["slug"])
	}
	if items[1].JSON["slug"] != "second-post" {
		t.Errorf("items[1].slug = %v, want second-post", items[1].JSON["slug"])
	}
}

func TestHashnodeNode_ListPosts_RequiresHost(t *testing.T) {
	n := &HashnodeNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"token":     "test-token",
		"operation": "list_posts",
	})
	if err == nil {
		t.Fatal("expected error when publication_host is missing")
	}
}

func TestHashnodeNode_GetPost(t *testing.T) {
	withHashnodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeGraphQLBody(t, r)
		vars, _ := body["variables"].(map[string]interface{})
		if vars["host"] != "myblog.hashnode.dev" {
			t.Errorf("host = %v, want myblog.hashnode.dev", vars["host"])
		}
		if vars["slug"] != "my-post" {
			t.Errorf("slug = %v, want my-post", vars["slug"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"publication": map[string]interface{}{
					"post": map[string]interface{}{
						"id":    "post-1",
						"title": "My Post",
						"slug":  "my-post",
						"content": map[string]interface{}{
							"markdown": "# Hello",
						},
					},
				},
			},
		})
	})

	n := &HashnodeNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"token":            "test-token",
		"operation":        "get_post",
		"publication_host": "myblog.hashnode.dev",
		"slug":             "my-post",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	items := handleItemsHashnode(out, "main")
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	content, _ := items[0].JSON["content"].(map[string]interface{})
	if content == nil || content["markdown"] != "# Hello" {
		t.Errorf("content.markdown = %v, want '# Hello'", content)
	}
}

func TestHashnodeNode_GetPost_RequiresSlug(t *testing.T) {
	n := &HashnodeNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"token":            "test-token",
		"operation":        "get_post",
		"publication_host": "myblog.hashnode.dev",
	})
	if err == nil {
		t.Fatal("expected error when slug is missing")
	}
}

func TestHashnodeNode_GraphQLErrors(t *testing.T) {
	withHashnodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"errors": []map[string]interface{}{
				{"message": "not authorized"},
			},
		})
	})

	n := &HashnodeNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"token":            "test-token",
		"operation":        "get_post",
		"publication_host": "myblog.hashnode.dev",
		"slug":             "my-post",
	})
	if err == nil {
		t.Fatal("expected error when GraphQL response contains errors")
	}
}

func TestHashnodeNode_HTTPError(t *testing.T) {
	withHashnodeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	})

	n := &HashnodeNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"token":            "bad-token",
		"operation":        "list_posts",
		"publication_host": "myblog.hashnode.dev",
	})
	if err == nil {
		t.Fatal("expected error on non-2xx HTTP response")
	}
}

// handleItemsHashnode returns the items for the given output handle.
func handleItemsHashnode(out []workflow.NodeOutput, handle string) []workflow.Item {
	for _, o := range out {
		if o.Handle == handle {
			return o.Items
		}
	}
	return nil
}
