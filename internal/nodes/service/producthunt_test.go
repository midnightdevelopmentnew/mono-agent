package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"monoagent/internal/workflow"
)

// withProducthuntTestServer points producthuntGraphQLURL at an httptest
// server for the duration of the test and restores it afterward.
func withProducthuntTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	orig := producthuntGraphQLURL
	producthuntGraphQLURL = srv.URL
	t.Cleanup(func() {
		srv.Close()
		producthuntGraphQLURL = orig
	})
	return srv
}

// decodeGraphQLBody is shared across the GraphQL-based service node tests
// (see hashnode_test.go).

func TestProductHuntNode_Type(t *testing.T) {
	n := &ProductHuntNode{}
	if got := n.Type(); got != "service.producthunt" {
		t.Errorf("Type() = %q, want service.producthunt", got)
	}
}

func TestProductHuntNode_MissingAccessToken(t *testing.T) {
	n := &ProductHuntNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "get_post",
		"slug":      "some-post",
	})
	if err == nil {
		t.Fatal("expected error when access_token is missing, got nil")
	}
}

func TestProductHuntNode_UnknownOperation(t *testing.T) {
	n := &ProductHuntNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "does_not_exist",
		"access_token": "token-123",
	})
	if err == nil {
		t.Fatal("expected error for unknown operation, got nil")
	}
}

func TestProductHuntNode_GetPost(t *testing.T) {
	withProducthuntTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer token-123" {
			t.Errorf("Authorization = %q, want Bearer token-123", auth)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		body := decodeGraphQLBody(t, r)
		vars, _ := body["variables"].(map[string]interface{})
		if vars["slug"] != "my-cool-launch" {
			t.Errorf("variables.slug = %v, want my-cool-launch", vars["slug"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"post": map[string]interface{}{
					"id":            "1",
					"name":          "My Cool Launch",
					"tagline":       "It's cool",
					"url":           "https://producthunt.com/posts/my-cool-launch",
					"votesCount":    42,
					"commentsCount": 7,
					"description":   "A very cool launch",
					"createdAt":     "2026-01-01T00:00:00Z",
					"topics": map[string]interface{}{
						"edges": []interface{}{
							map[string]interface{}{
								"node": map[string]interface{}{"name": "Productivity", "slug": "productivity"},
							},
						},
					},
					"makers": []interface{}{
						map[string]interface{}{"id": "10", "name": "Jane Doe", "username": "janedoe"},
					},
				},
			},
		})
	})

	n := &ProductHuntNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_post",
		"access_token": "token-123",
		"slug":         "my-cool-launch",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	items := mainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].JSON["name"] != "My Cool Launch" {
		t.Errorf("name = %v, want My Cool Launch", items[0].JSON["name"])
	}
	if items[0].JSON["votesCount"] != float64(42) {
		t.Errorf("votesCount = %v, want 42", items[0].JSON["votesCount"])
	}
}

func TestProductHuntNode_GetPostMissingSlugAndID(t *testing.T) {
	n := &ProductHuntNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_post",
		"access_token": "token-123",
	})
	if err == nil {
		t.Fatal("expected error when slug and id are both missing, got nil")
	}
}

func TestProductHuntNode_GetPostNotFound(t *testing.T) {
	withProducthuntTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"post": nil},
		})
	})

	n := &ProductHuntNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_post",
		"access_token": "token-123",
		"slug":         "missing-post",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if items := mainItems(out); len(items) != 0 {
		t.Errorf("expected 0 items for missing post, got %d", len(items))
	}
}

func TestProductHuntNode_ListPostsDefaults(t *testing.T) {
	withProducthuntTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeGraphQLBody(t, r)
		vars, _ := body["variables"].(map[string]interface{})
		if vars["order"] != "RANKING" {
			t.Errorf("variables.order = %v, want RANKING", vars["order"])
		}
		if vars["first"] != float64(10) {
			t.Errorf("variables.first = %v, want 10", vars["first"])
		}
		if _, ok := vars["topic"]; ok {
			t.Errorf("variables.topic should be omitted when empty, got %v", vars["topic"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"posts": map[string]interface{}{
					"edges": []interface{}{
						map[string]interface{}{
							"node": map[string]interface{}{
								"id": "1", "name": "First Post", "votesCount": 10,
							},
						},
						map[string]interface{}{
							"node": map[string]interface{}{
								"id": "2", "name": "Second Post", "votesCount": 5,
							},
						},
					},
				},
			},
		})
	})

	n := &ProductHuntNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "list_posts",
		"access_token": "token-123",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	items := mainItems(out)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].JSON["name"] != "First Post" {
		t.Errorf("items[0].name = %v, want First Post", items[0].JSON["name"])
	}
	if items[1].JSON["name"] != "Second Post" {
		t.Errorf("items[1].name = %v, want Second Post", items[1].JSON["name"])
	}
}

func TestProductHuntNode_ListPostsWithTopicAndOrder(t *testing.T) {
	withProducthuntTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeGraphQLBody(t, r)
		vars, _ := body["variables"].(map[string]interface{})
		if vars["order"] != "NEWEST" {
			t.Errorf("variables.order = %v, want NEWEST", vars["order"])
		}
		if vars["first"] != float64(5) {
			t.Errorf("variables.first = %v, want 5", vars["first"])
		}
		if vars["topic"] != "artificial-intelligence" {
			t.Errorf("variables.topic = %v, want artificial-intelligence", vars["topic"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"posts": map[string]interface{}{"edges": []interface{}{}},
			},
		})
	})

	n := &ProductHuntNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "list_posts",
		"access_token": "token-123",
		"order":        "NEWEST",
		"first":        5,
		"topic":        "artificial-intelligence",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if items := mainItems(out); len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestProductHuntNode_GetPostMetrics(t *testing.T) {
	withProducthuntTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"post": map[string]interface{}{
					"id":            "1",
					"votesCount":    123,
					"commentsCount": 8,
					"reviewsRating": 4.5,
				},
			},
		})
	})

	n := &ProductHuntNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_post_metrics",
		"access_token": "token-123",
		"slug":         "my-cool-launch",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	items := mainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].JSON["votes_count"] != float64(123) {
		t.Errorf("votes_count = %v, want 123", items[0].JSON["votes_count"])
	}
	if items[0].JSON["comments_count"] != float64(8) {
		t.Errorf("comments_count = %v, want 8", items[0].JSON["comments_count"])
	}
	if items[0].JSON["review_rating"] != 4.5 {
		t.Errorf("review_rating = %v, want 4.5", items[0].JSON["review_rating"])
	}
}

func TestProductHuntNode_GetPostMetricsNotFound(t *testing.T) {
	withProducthuntTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"post": nil},
		})
	})

	n := &ProductHuntNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_post_metrics",
		"access_token": "token-123",
		"slug":         "missing-post",
	})
	if err == nil {
		t.Fatal("expected error when post is not found, got nil")
	}
}

func TestProductHuntNode_CreateComment(t *testing.T) {
	withProducthuntTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeGraphQLBody(t, r)
		vars, _ := body["variables"].(map[string]interface{})
		input, _ := vars["input"].(map[string]interface{})
		if input["postId"] != "post-1" {
			t.Errorf("input.postId = %v, want post-1", input["postId"])
		}
		if input["body"] != "Great launch!" {
			t.Errorf("input.body = %v, want Great launch!", input["body"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"commentCreate": map[string]interface{}{
					"comment": map[string]interface{}{
						"id":        "c1",
						"body":      "Great launch!",
						"createdAt": "2026-01-01T00:00:00Z",
					},
				},
			},
		})
	})

	n := &ProductHuntNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "create_comment",
		"access_token": "token-123",
		"post_id":      "post-1",
		"body":         "Great launch!",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	items := mainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].JSON["id"] != "c1" {
		t.Errorf("id = %v, want c1", items[0].JSON["id"])
	}
	if items[0].JSON["body"] != "Great launch!" {
		t.Errorf("body = %v, want Great launch!", items[0].JSON["body"])
	}
}

func TestProductHuntNode_CreateCommentMissingFields(t *testing.T) {
	n := &ProductHuntNode{}

	if _, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "create_comment",
		"access_token": "token-123",
		"body":         "Great launch!",
	}); err == nil {
		t.Fatal("expected error when post_id is missing, got nil")
	}

	if _, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "create_comment",
		"access_token": "token-123",
		"post_id":      "post-1",
	}); err == nil {
		t.Fatal("expected error when body is missing, got nil")
	}
}

func TestProductHuntNode_GraphQLErrorSurfaced(t *testing.T) {
	withProducthuntTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"errors": []map[string]interface{}{
				{"message": "invalid token"},
			},
		})
	})

	n := &ProductHuntNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_post",
		"access_token": "bad-token",
		"slug":         "my-cool-launch",
	})
	if err == nil {
		t.Fatal("expected error when GraphQL response contains errors, got nil")
	}
}

func TestProductHuntNode_HTTPErrorStatus(t *testing.T) {
	withProducthuntTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	})

	n := &ProductHuntNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":    "get_post",
		"access_token": "bad-token",
		"slug":         "my-cool-launch",
	})
	if err == nil {
		t.Fatal("expected error on HTTP 401, got nil")
	}
}

// mainItems extracts the "main" handle's items from Execute's output.
func mainItems(out []workflow.NodeOutput) []workflow.Item {
	for _, o := range out {
		if o.Handle == "main" {
			return o.Items
		}
	}
	return nil
}
