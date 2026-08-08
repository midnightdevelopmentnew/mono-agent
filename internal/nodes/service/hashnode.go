package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"monoagent/internal/workflow"
)

// hashnodeGraphQLURL is the Hashnode GraphQL endpoint. Declared as a var
// (rather than const) so tests can point it at an httptest server.
var hashnodeGraphQLURL = "https://gql.hashnode.com"

// GraphQL query/mutation constants for each Hashnode operation.
const (
	hashnodeMutationPublishPost = `
mutation PublishPost($input: PublishPostInput!) {
  publishPost(input: $input) {
    post { id title slug url }
  }
}`

	hashnodeQueryListPosts = `
query ListPosts($host: String!, $first: Int!) {
  publication(host: $host) {
    posts(first: $first) {
      edges { node { id title slug url brief publishedAt } }
    }
  }
}`

	hashnodeQueryGetPost = `
query GetPost($host: String!, $slug: String!) {
  publication(host: $host) {
    post(slug: $slug) { id title slug url content { markdown } publishedAt }
  }
}`
)

// HashnodeNode publishes and reads posts via the Hashnode GraphQL API.
// Type: "service.hashnode"
//
// Config fields:
//
//	"operation" (string, required): "publish_post" | "list_posts" | "get_post"
//	"token"     (string, required): Hashnode personal access token
//
//	publish_post:
//	  "publication_id"    (string, required)
//	  "title"             (string, required)
//	  "content_markdown"  (string, required)
//	  "tags"              ([]string, optional): tag slugs
//	  "subtitle"          (string, optional)
//	  "slug"              (string, optional)
//
//	list_posts:
//	  "publication_host" (string, required): e.g. "myblog.hashnode.dev"
//	  "first"            (int, optional, default 10)
//
//	get_post:
//	  "publication_host" (string, required)
//	  "slug"             (string, required)
type HashnodeNode struct{}

func (n *HashnodeNode) Type() string { return "service.hashnode" }

func (n *HashnodeNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	token := strVal(config, "token")
	if token == "" {
		return nil, fmt.Errorf("service.hashnode: 'token' is required")
	}

	operation := strVal(config, "operation")

	var items []workflow.Item
	var err error

	switch operation {
	case "publish_post":
		items, err = n.publishPost(ctx, token, config)
	case "list_posts":
		items, err = n.listPosts(ctx, token, config)
	case "get_post":
		items, err = n.getPost(ctx, token, config)
	default:
		return nil, fmt.Errorf("service.hashnode: unknown operation %q", operation)
	}

	if err != nil {
		return nil, err
	}

	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

func (n *HashnodeNode) publishPost(ctx context.Context, token string, config map[string]interface{}) ([]workflow.Item, error) {
	publicationID := strVal(config, "publication_id")
	if publicationID == "" {
		return nil, fmt.Errorf("service.hashnode: 'publication_id' is required for publish_post")
	}
	title := strVal(config, "title")
	if title == "" {
		return nil, fmt.Errorf("service.hashnode: 'title' is required for publish_post")
	}
	contentMarkdown := strVal(config, "content_markdown")
	if contentMarkdown == "" {
		return nil, fmt.Errorf("service.hashnode: 'content_markdown' is required for publish_post")
	}

	postInput := map[string]interface{}{
		"publicationId":   publicationID,
		"title":           title,
		"contentMarkdown": contentMarkdown,
	}
	if subtitle := strVal(config, "subtitle"); subtitle != "" {
		postInput["subtitle"] = subtitle
	}
	if slug := strVal(config, "slug"); slug != "" {
		postInput["slug"] = slug
	}
	if tags := strSliceVal(config, "tags"); len(tags) > 0 {
		tagInputs := make([]map[string]interface{}, 0, len(tags))
		for _, slug := range tags {
			tagInputs = append(tagInputs, map[string]interface{}{"slug": slug})
		}
		postInput["tags"] = tagInputs
	}

	vars := map[string]interface{}{"input": postInput}
	data, err := hashnodeGraphQL(ctx, token, hashnodeMutationPublishPost, vars)
	if err != nil {
		return nil, fmt.Errorf("service.hashnode publish_post: %w", err)
	}
	result, _ := data["publishPost"].(map[string]interface{})
	if result == nil {
		return []workflow.Item{}, nil
	}
	post, _ := result["post"].(map[string]interface{})
	if post == nil {
		post = result
	}
	return []workflow.Item{workflow.NewItem(post)}, nil
}

func (n *HashnodeNode) listPosts(ctx context.Context, token string, config map[string]interface{}) ([]workflow.Item, error) {
	host := strVal(config, "publication_host")
	if host == "" {
		return nil, fmt.Errorf("service.hashnode: 'publication_host' is required for list_posts")
	}
	first := intVal(config, "first")
	if first <= 0 {
		first = 10
	}

	vars := map[string]interface{}{"host": host, "first": first}
	data, err := hashnodeGraphQL(ctx, token, hashnodeQueryListPosts, vars)
	if err != nil {
		return nil, fmt.Errorf("service.hashnode list_posts: %w", err)
	}
	publication, _ := data["publication"].(map[string]interface{})
	if publication == nil {
		return []workflow.Item{}, nil
	}
	posts, _ := publication["posts"].(map[string]interface{})
	if posts == nil {
		return []workflow.Item{}, nil
	}
	edges, _ := posts["edges"].([]interface{})
	items := make([]workflow.Item, 0, len(edges))
	for _, e := range edges {
		edge, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		node, ok := edge["node"].(map[string]interface{})
		if !ok {
			continue
		}
		items = append(items, workflow.NewItem(node))
	}
	return items, nil
}

func (n *HashnodeNode) getPost(ctx context.Context, token string, config map[string]interface{}) ([]workflow.Item, error) {
	host := strVal(config, "publication_host")
	if host == "" {
		return nil, fmt.Errorf("service.hashnode: 'publication_host' is required for get_post")
	}
	slug := strVal(config, "slug")
	if slug == "" {
		return nil, fmt.Errorf("service.hashnode: 'slug' is required for get_post")
	}

	vars := map[string]interface{}{"host": host, "slug": slug}
	data, err := hashnodeGraphQL(ctx, token, hashnodeQueryGetPost, vars)
	if err != nil {
		return nil, fmt.Errorf("service.hashnode get_post: %w", err)
	}
	publication, _ := data["publication"].(map[string]interface{})
	if publication == nil {
		return []workflow.Item{}, nil
	}
	post, _ := publication["post"].(map[string]interface{})
	if post == nil {
		return []workflow.Item{}, nil
	}
	return []workflow.Item{workflow.NewItem(post)}, nil
}

// hashnodeGraphQL executes a GraphQL query/mutation against the Hashnode API.
func hashnodeGraphQL(ctx context.Context, token, query string, variables map[string]interface{}) (map[string]interface{}, error) {
	payload := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling GraphQL payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", hashnodeGraphQLURL, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	// Hashnode uses the personal access token directly, without a "Bearer" prefix.
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP POST %s: %w", hashnodeGraphQLURL, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	var gqlResp struct {
		Data   map[string]interface{} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBytes, &gqlResp); err != nil {
		return nil, fmt.Errorf("parsing GraphQL response: %w", err)
	}
	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("GraphQL errors: %s", gqlResp.Errors[0].Message)
	}
	if gqlResp.Data == nil {
		return map[string]interface{}{}, nil
	}
	return gqlResp.Data, nil
}
