package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"monoagent/internal/workflow"
)

// withDevtoServer starts an httptest server, points devtoBaseURL at it for
// the duration of the test, and restores the original value on cleanup.
func withDevtoServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := devtoBaseURL
	devtoBaseURL = srv.URL
	t.Cleanup(func() { devtoBaseURL = orig })
	return srv
}

func devtoExecute(t *testing.T, config map[string]interface{}) []workflow.NodeOutput {
	t.Helper()
	n := &DevToNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return out
}

func devtoMainItems(out []workflow.NodeOutput) []workflow.Item {
	for _, o := range out {
		if o.Handle == "main" {
			return o.Items
		}
	}
	return nil
}

func TestDevToNode_Type(t *testing.T) {
	n := &DevToNode{}
	if got := n.Type(); got != "service.devto" {
		t.Errorf("Type() = %q, want service.devto", got)
	}
}

func TestDevToNode_RequiresAPIKey(t *testing.T) {
	n := &DevToNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "list_articles",
	})
	if err == nil {
		t.Fatal("expected error when api_key is missing")
	}
}

func TestDevToNode_UnknownOperation(t *testing.T) {
	n := &DevToNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "bogus",
		"api_key":   "test-key",
	})
	if err == nil {
		t.Fatal("expected error for unknown operation")
	}
}

func TestDevToNode_PublishArticle(t *testing.T) {
	var seenHeader string
	var gotPath string
	var gotBody map[string]interface{}

	withDevtoServer(t, func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get("api-key")
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    123,
			"title": "Hello World",
		})
	})

	out := devtoExecute(t, map[string]interface{}{
		"operation":     "publish_article",
		"api_key":       "test-key",
		"title":         "Hello World",
		"body_markdown": "# Hello\n\nWorld.",
		"tags":          "go, api, testing",
		"series":        "My Series",
		"canonical_url": "https://example.com/hello",
	})

	if seenHeader != "test-key" {
		t.Errorf("api-key header = %q, want test-key", seenHeader)
	}
	if gotPath != "/articles" {
		t.Errorf("path = %q, want /articles", gotPath)
	}

	article, ok := gotBody["article"].(map[string]interface{})
	if !ok {
		t.Fatalf("request body missing 'article' key: %v", gotBody)
	}
	if article["title"] != "Hello World" {
		t.Errorf("title = %v, want Hello World", article["title"])
	}
	if article["body_markdown"] != "# Hello\n\nWorld." {
		t.Errorf("body_markdown = %v", article["body_markdown"])
	}
	if article["series"] != "My Series" {
		t.Errorf("series = %v, want My Series", article["series"])
	}
	if article["canonical_url"] != "https://example.com/hello" {
		t.Errorf("canonical_url = %v", article["canonical_url"])
	}
	if article["published"] != true {
		t.Errorf("published = %v, want true (default)", article["published"])
	}
	tags, ok := article["tags"].([]interface{})
	if !ok || len(tags) != 3 {
		t.Fatalf("tags = %v, want 3 tags", article["tags"])
	}
	if tags[0] != "go" || tags[1] != "api" || tags[2] != "testing" {
		t.Errorf("tags = %v, want [go api testing]", tags)
	}

	items := devtoMainItems(out)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].JSON["id"] != float64(123) {
		t.Errorf("id = %v, want 123", items[0].JSON["id"])
	}
}

func TestDevToNode_PublishArticleUnpublished(t *testing.T) {
	var gotBody map[string]interface{}
	withDevtoServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": 1})
	})

	devtoExecute(t, map[string]interface{}{
		"operation":     "publish_article",
		"api_key":       "test-key",
		"title":         "Draft",
		"body_markdown": "Draft body",
		"published":     false,
	})

	article, _ := gotBody["article"].(map[string]interface{})
	if article["published"] != false {
		t.Errorf("published = %v, want false", article["published"])
	}
}

func TestDevToNode_PublishArticleRequiresTitleAndBody(t *testing.T) {
	n := &DevToNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "publish_article",
		"api_key":   "test-key",
	})
	if err == nil {
		t.Fatal("expected error when title/body_markdown are missing")
	}
}

func TestDevToNode_ListArticles(t *testing.T) {
	var gotQuery string
	withDevtoServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/articles/me/published" {
			t.Errorf("path = %q, want /articles/me/published", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id": 1, "title": "Post 1"},
			{"id": 2, "title": "Post 2"},
		})
	})

	out := devtoExecute(t, map[string]interface{}{
		"operation": "list_articles",
		"api_key":   "test-key",
		"page":      float64(2),
		"per_page":  float64(10),
	})

	if gotQuery != "page=2&per_page=10" {
		t.Errorf("query = %q, want page=2&per_page=10", gotQuery)
	}
	items := devtoMainItems(out)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].JSON["title"] != "Post 1" {
		t.Errorf("title = %v, want Post 1", items[0].JSON["title"])
	}
}

func TestDevToNode_ListArticlesDefaultsPagination(t *testing.T) {
	var gotQuery string
	withDevtoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	})

	devtoExecute(t, map[string]interface{}{
		"operation": "list_articles",
		"api_key":   "test-key",
	})

	if gotQuery != "page=1&per_page=30" {
		t.Errorf("query = %q, want page=1&per_page=30", gotQuery)
	}
}

func TestDevToNode_GetArticle(t *testing.T) {
	var gotPath string
	withDevtoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": 42, "title": "Fetched"})
	})

	out := devtoExecute(t, map[string]interface{}{
		"operation":  "get_article",
		"api_key":    "test-key",
		"article_id": float64(42),
	})

	if gotPath != "/articles/42" {
		t.Errorf("path = %q, want /articles/42", gotPath)
	}
	items := devtoMainItems(out)
	if len(items) != 1 || items[0].JSON["title"] != "Fetched" {
		t.Fatalf("unexpected items: %v", items)
	}
}

func TestDevToNode_GetArticleRequiresArticleID(t *testing.T) {
	n := &DevToNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "get_article",
		"api_key":   "test-key",
	})
	if err == nil {
		t.Fatal("expected error when article_id is missing")
	}
}

func TestDevToNode_ListComments(t *testing.T) {
	var gotPath, gotQuery string
	withDevtoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"id_code": "abc", "body_html": "<p>Nice post!</p>"},
		})
	})

	out := devtoExecute(t, map[string]interface{}{
		"operation":  "list_comments",
		"api_key":    "test-key",
		"article_id": "99",
	})

	if gotPath != "/comments" {
		t.Errorf("path = %q, want /comments", gotPath)
	}
	if gotQuery != "a_id=99" {
		t.Errorf("query = %q, want a_id=99", gotQuery)
	}
	items := devtoMainItems(out)
	if len(items) != 1 || items[0].JSON["id_code"] != "abc" {
		t.Fatalf("unexpected items: %v", items)
	}
}

func TestDevToNode_ListCommentsRequiresArticleID(t *testing.T) {
	n := &DevToNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation": "list_comments",
		"api_key":   "test-key",
	})
	if err == nil {
		t.Fatal("expected error when article_id is missing")
	}
}

func TestDevToNode_CreateComment(t *testing.T) {
	var gotPath string
	var gotBody map[string]interface{}
	withDevtoServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id_code": "xyz"})
	})

	out := devtoExecute(t, map[string]interface{}{
		"operation":     "create_comment",
		"api_key":       "test-key",
		"article_id":    float64(55),
		"body_markdown": "Great article!",
	})

	if gotPath != "/comments" {
		t.Errorf("path = %q, want /comments", gotPath)
	}
	comment, ok := gotBody["comment"].(map[string]interface{})
	if !ok {
		t.Fatalf("request body missing 'comment' key: %v", gotBody)
	}
	if comment["body_markdown"] != "Great article!" {
		t.Errorf("body_markdown = %v", comment["body_markdown"])
	}
	if comment["commentable_id"] != float64(55) {
		t.Errorf("commentable_id = %v, want 55", comment["commentable_id"])
	}
	if comment["commentable_type"] != "Article" {
		t.Errorf("commentable_type = %v, want Article", comment["commentable_type"])
	}

	items := devtoMainItems(out)
	if len(items) != 1 || items[0].JSON["id_code"] != "xyz" {
		t.Fatalf("unexpected items: %v", items)
	}
}

func TestDevToNode_CreateCommentRequiresBodyMarkdown(t *testing.T) {
	n := &DevToNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":  "create_comment",
		"api_key":    "test-key",
		"article_id": "1",
	})
	if err == nil {
		t.Fatal("expected error when body_markdown is missing")
	}
}

func TestDevToNode_CreateCommentRequiresNumericArticleID(t *testing.T) {
	n := &DevToNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":     "create_comment",
		"api_key":       "test-key",
		"article_id":    "not-a-number",
		"body_markdown": "Nice!",
	})
	if err == nil {
		t.Fatal("expected error when article_id is not numeric")
	}
}

func TestDevToNode_ErrorResponsePropagates(t *testing.T) {
	withDevtoServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	})

	n := &DevToNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"operation":  "get_article",
		"api_key":    "bad-key",
		"article_id": "1",
	})
	if err == nil {
		t.Fatal("expected error on 401 response")
	}
}

func TestDevtoSplitTags(t *testing.T) {
	got := devtoSplitTags(" go, api ,, testing")
	want := []string{"go", "api", "testing"}
	if len(got) != len(want) {
		t.Fatalf("devtoSplitTags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("devtoSplitTags[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDevtoArticleID(t *testing.T) {
	cases := []struct {
		config map[string]interface{}
		want   string
	}{
		{map[string]interface{}{"article_id": "abc"}, "abc"},
		{map[string]interface{}{"article_id": float64(42)}, "42"},
		{map[string]interface{}{"article_id": 7}, "7"},
		{map[string]interface{}{}, ""},
	}
	for _, c := range cases {
		if got := devtoArticleID(c.config); got != c.want {
			t.Errorf("devtoArticleID(%v) = %q, want %q", c.config, got, c.want)
		}
	}
}
