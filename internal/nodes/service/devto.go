package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"monoagent/internal/workflow"
)

// DevToNode publishes articles and reads articles/comments via the Dev.to
// (Forem) API.
// Type: "service.devto"
//
// Config fields:
//
//	"operation"      (string, required): "publish_article" | "list_articles" | "get_article" | "list_comments" | "create_comment"
//	"api_key"        (string, required): Dev.to API key (sent as the "api-key" header)
//	"title"          (string, required for publish_article)
//	"body_markdown"  (string, required for publish_article and create_comment)
//	"tags"           (string, optional): comma-separated tags for publish_article
//	"series"         (string, optional): series name for publish_article
//	"published"      (bool, optional, default true): publish_article publish flag
//	"canonical_url"  (string, optional): publish_article canonical URL
//	"page"           (int, optional, default 1): list_articles pagination page
//	"per_page"       (int, optional, default 30): list_articles page size
//	"article_id"     (string or number, required for get_article, list_comments, create_comment)
type DevToNode struct{}

func (n *DevToNode) Type() string { return "service.devto" }

// devtoBaseURL is a var (not const) so tests can point it at an httptest server.
var devtoBaseURL = "https://dev.to/api"

func (n *DevToNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	apiKey := strVal(config, "api_key")
	if apiKey == "" {
		return nil, fmt.Errorf("service.devto: 'api_key' is required")
	}
	operation := strVal(config, "operation")

	switch operation {
	case "publish_article":
		return n.publishArticle(ctx, apiKey, config)
	case "list_articles":
		return n.listArticles(ctx, apiKey, config)
	case "get_article":
		return n.getArticle(ctx, apiKey, config)
	case "list_comments":
		return n.listComments(ctx, apiKey, config)
	case "create_comment":
		return n.createComment(ctx, apiKey, config)
	default:
		return nil, fmt.Errorf("service.devto: unknown operation %q", operation)
	}
}

func (n *DevToNode) publishArticle(ctx context.Context, apiKey string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	title := strVal(config, "title")
	bodyMarkdown := strVal(config, "body_markdown")
	if title == "" || bodyMarkdown == "" {
		return nil, fmt.Errorf("service.devto: 'title' and 'body_markdown' are required for publish_article")
	}

	article := map[string]interface{}{
		"title":         title,
		"body_markdown": bodyMarkdown,
	}
	if tags := strVal(config, "tags"); tags != "" {
		article["tags"] = devtoSplitTags(tags)
	}
	if series := strVal(config, "series"); series != "" {
		article["series"] = series
	}
	if canonicalURL := strVal(config, "canonical_url"); canonicalURL != "" {
		article["canonical_url"] = canonicalURL
	}
	published := true
	if v, ok := config["published"].(bool); ok {
		published = v
	}
	article["published"] = published

	body := map[string]interface{}{"article": article}
	result, err := devtoRequest(ctx, "POST", devtoBaseURL+"/articles", apiKey, body)
	if err != nil {
		return nil, fmt.Errorf("service.devto publish_article: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

func (n *DevToNode) listArticles(ctx context.Context, apiKey string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	page := intVal(config, "page")
	if page <= 0 {
		page = 1
	}
	perPage := intVal(config, "per_page")
	if perPage <= 0 {
		perPage = 30
	}
	endpoint := fmt.Sprintf("%s/articles/me/published?page=%d&per_page=%d", devtoBaseURL, page, perPage)
	results, err := devtoRequestList(ctx, "GET", endpoint, apiKey, nil)
	if err != nil {
		return nil, fmt.Errorf("service.devto list_articles: %w", err)
	}
	items := devtoItemsFromList(results)
	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

func (n *DevToNode) getArticle(ctx context.Context, apiKey string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	articleID := devtoArticleID(config)
	if articleID == "" {
		return nil, fmt.Errorf("service.devto: 'article_id' is required for get_article")
	}
	endpoint := devtoBaseURL + "/articles/" + url.PathEscape(articleID)
	result, err := devtoRequest(ctx, "GET", endpoint, apiKey, nil)
	if err != nil {
		return nil, fmt.Errorf("service.devto get_article: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

func (n *DevToNode) listComments(ctx context.Context, apiKey string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	articleID := devtoArticleID(config)
	if articleID == "" {
		return nil, fmt.Errorf("service.devto: 'article_id' is required for list_comments")
	}
	endpoint := devtoBaseURL + "/comments?a_id=" + url.QueryEscape(articleID)
	results, err := devtoRequestList(ctx, "GET", endpoint, apiKey, nil)
	if err != nil {
		return nil, fmt.Errorf("service.devto list_comments: %w", err)
	}
	items := devtoItemsFromList(results)
	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

func (n *DevToNode) createComment(ctx context.Context, apiKey string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	bodyMarkdown := strVal(config, "body_markdown")
	if bodyMarkdown == "" {
		return nil, fmt.Errorf("service.devto: 'body_markdown' is required for create_comment")
	}
	articleID := devtoArticleID(config)
	if articleID == "" {
		return nil, fmt.Errorf("service.devto: 'article_id' is required for create_comment")
	}
	commentableID, err := strconv.Atoi(articleID)
	if err != nil {
		return nil, fmt.Errorf("service.devto: 'article_id' must be numeric for create_comment: %w", err)
	}

	body := map[string]interface{}{
		"comment": map[string]interface{}{
			"body_markdown":    bodyMarkdown,
			"commentable_id":   commentableID,
			"commentable_type": "Article",
		},
	}
	result, err := devtoRequest(ctx, "POST", devtoBaseURL+"/comments", apiKey, body)
	if err != nil {
		return nil, fmt.Errorf("service.devto create_comment: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

// devtoItemsFromList converts a []interface{} of JSON objects into workflow.Items.
func devtoItemsFromList(results []interface{}) []workflow.Item {
	items := make([]workflow.Item, 0, len(results))
	for _, r := range results {
		if m, ok := r.(map[string]interface{}); ok {
			items = append(items, workflow.NewItem(m))
		}
	}
	return items
}

// devtoArticleID extracts "article_id" from config as a string, accepting
// either a string or a JSON number (float64/int).
func devtoArticleID(config map[string]interface{}) string {
	switch v := config["article_id"].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	default:
		return ""
	}
}

// devtoSplitTags splits a comma-separated tag string into a trimmed,
// non-empty slice for the Forem API's "tags" article field.
func devtoSplitTags(tags string) []string {
	parts := strings.Split(tags, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// devtoRequest performs an authenticated request against the Dev.to API
// using the "api-key" header (Dev.to does not use Authorization: Bearer)
// and returns a parsed JSON object response.
func devtoRequest(ctx context.Context, method, endpoint, apiKey string, body interface{}) (map[string]interface{}, error) {
	respBytes, err := devtoDo(ctx, method, endpoint, apiKey, body)
	if err != nil {
		return nil, err
	}
	if len(respBytes) == 0 {
		return map[string]interface{}{}, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing JSON response: %w (body: %s)", err, string(respBytes))
	}
	return result, nil
}

// devtoRequestList is like devtoRequest but returns []interface{} for array responses.
func devtoRequestList(ctx context.Context, method, endpoint, apiKey string, body interface{}) ([]interface{}, error) {
	respBytes, err := devtoDo(ctx, method, endpoint, apiKey, body)
	if err != nil {
		return nil, err
	}
	if len(respBytes) == 0 {
		return []interface{}{}, nil
	}
	var result []interface{}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing JSON array response: %w (body: %s)", err, string(respBytes))
	}
	return result, nil
}

// devtoDo builds a request via buildRequest (no bearer token), swaps in the
// "api-key" header Dev.to expects, executes it, and returns the raw response
// body for a successful (2xx) response.
func devtoDo(ctx context.Context, method, endpoint, apiKey string, body interface{}) ([]byte, error) {
	req, err := buildRequest(ctx, method, endpoint, "", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("api-key", apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP %s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBytes))
	}
	return respBytes, nil
}
