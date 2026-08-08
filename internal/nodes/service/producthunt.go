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

// producthuntGraphQLURL is the ProductHunt GraphQL API endpoint. It is a var
// (not a const) so tests can point it at an httptest server.
var producthuntGraphQLURL = "https://api.producthunt.com/v2/api/graphql"

// GraphQL query/mutation constants for each ProductHunt operation.
const (
	producthuntQueryGetPost = `
query GetPost($id: ID, $slug: String) {
  post(id: $id, slug: $slug) {
    id
    name
    tagline
    url
    votesCount
    commentsCount
    description
    createdAt
    topics {
      edges {
        node {
          name
          slug
        }
      }
    }
    makers {
      id
      name
      username
    }
  }
}`

	producthuntQueryGetPostMetrics = `
query GetPostMetrics($id: ID, $slug: String) {
  post(id: $id, slug: $slug) {
    id
    votesCount
    commentsCount
    reviewsRating
  }
}`

	producthuntQueryListPosts = `
query ListPosts($order: PostsOrder!, $first: Int!, $topic: String) {
  posts(order: $order, first: $first, topic: $topic) {
    edges {
      node {
        id
        name
        tagline
        url
        votesCount
        commentsCount
        createdAt
      }
    }
  }
}`

	producthuntMutationCreateComment = `
mutation CreateComment($input: CommentCreateInput!) {
  commentCreate(input: $input) {
    comment {
      id
      body
      createdAt
    }
  }
}`
)

// ProductHuntNode interacts with the ProductHunt GraphQL API (v2).
// Type: "service.producthunt"
//
// This is the API-based alternative to the crawl-based ProductHunt actions
// in data/actions/producthunt/ — prefer this node when an access token is
// available, since it is more reliable than crawling.
//
// Config fields (common):
//
//	"operation"     (string, required): "get_post" | "list_posts" | "get_post_metrics" | "create_comment"
//	"access_token"  (string, required): OAuth2 access token or developer token
//
// Per-operation config:
//
//	get_post:          "slug" (string) and/or "id" (string) — at least one required
//	list_posts:        "order" (string, optional: "RANKING" | "NEWEST", default "RANKING"),
//	                    "first" (int, optional, default 10), "topic" (string, optional topic slug)
//	get_post_metrics:  "slug" (string) and/or "id" (string) — at least one required
//	create_comment:    "post_id" (string, required), "body" (string, required)
type ProductHuntNode struct{}

func (n *ProductHuntNode) Type() string { return "service.producthunt" }

func (n *ProductHuntNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	token := strVal(config, "access_token")
	if token == "" {
		return nil, fmt.Errorf("service.producthunt: 'access_token' is required")
	}

	operation := strVal(config, "operation")

	var items []workflow.Item
	var err error

	switch operation {
	case "get_post":
		items, err = n.getPost(ctx, token, config)
	case "list_posts":
		items, err = n.listPosts(ctx, token, config)
	case "get_post_metrics":
		items, err = n.getPostMetrics(ctx, token, config)
	case "create_comment":
		items, err = n.createComment(ctx, token, config)
	default:
		return nil, fmt.Errorf("service.producthunt: unknown operation %q", operation)
	}

	if err != nil {
		return nil, err
	}

	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

// phGraphQL executes a GraphQL query/mutation against the ProductHunt API.
func (n *ProductHuntNode) phGraphQL(ctx context.Context, token, query string, variables map[string]interface{}) (map[string]interface{}, error) {
	payload := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling GraphQL payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", producthuntGraphQLURL, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP POST %s: %w", producthuntGraphQLURL, err)
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

// nodesFromEdges extracts node objects from a GraphQL Relay-style connection.
// data["key"]["edges"][].node -> []interface{}
func nodesFromEdges(data map[string]interface{}, key string) []interface{} {
	top, _ := data[key].(map[string]interface{})
	if top == nil {
		return nil
	}
	edges, _ := top["edges"].([]interface{})
	if edges == nil {
		return nil
	}
	out := make([]interface{}, 0, len(edges))
	for _, e := range edges {
		edge, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		if node, ok := edge["node"]; ok {
			out = append(out, node)
		}
	}
	return out
}

func (n *ProductHuntNode) getPost(ctx context.Context, token string, config map[string]interface{}) ([]workflow.Item, error) {
	slug := strVal(config, "slug")
	id := strVal(config, "id")
	if slug == "" && id == "" {
		return nil, fmt.Errorf("service.producthunt: 'slug' (or 'id') is required for get_post")
	}

	vars := map[string]interface{}{}
	if slug != "" {
		vars["slug"] = slug
	}
	if id != "" {
		vars["id"] = id
	}

	data, err := n.phGraphQL(ctx, token, producthuntQueryGetPost, vars)
	if err != nil {
		return nil, fmt.Errorf("service.producthunt get_post: %w", err)
	}
	post, _ := data["post"].(map[string]interface{})
	if post == nil {
		return []workflow.Item{}, nil
	}
	return []workflow.Item{workflow.NewItem(post)}, nil
}

func (n *ProductHuntNode) listPosts(ctx context.Context, token string, config map[string]interface{}) ([]workflow.Item, error) {
	order := strVal(config, "order")
	if order == "" {
		order = "RANKING"
	}
	first := intVal(config, "first")
	if first == 0 {
		first = 10
	}

	vars := map[string]interface{}{
		"order": order,
		"first": first,
	}
	if topic := strVal(config, "topic"); topic != "" {
		vars["topic"] = topic
	}

	data, err := n.phGraphQL(ctx, token, producthuntQueryListPosts, vars)
	if err != nil {
		return nil, fmt.Errorf("service.producthunt list_posts: %w", err)
	}
	return listToItems(nodesFromEdges(data, "posts")), nil
}

func (n *ProductHuntNode) getPostMetrics(ctx context.Context, token string, config map[string]interface{}) ([]workflow.Item, error) {
	slug := strVal(config, "slug")
	id := strVal(config, "id")
	if slug == "" && id == "" {
		return nil, fmt.Errorf("service.producthunt: 'slug' (or 'id') is required for get_post_metrics")
	}

	vars := map[string]interface{}{}
	if slug != "" {
		vars["slug"] = slug
	}
	if id != "" {
		vars["id"] = id
	}

	data, err := n.phGraphQL(ctx, token, producthuntQueryGetPostMetrics, vars)
	if err != nil {
		return nil, fmt.Errorf("service.producthunt get_post_metrics: %w", err)
	}
	post, _ := data["post"].(map[string]interface{})
	if post == nil {
		return nil, fmt.Errorf("service.producthunt get_post_metrics: post not found")
	}

	result := map[string]interface{}{
		"votes_count":    post["votesCount"],
		"comments_count": post["commentsCount"],
		"review_rating":  post["reviewsRating"],
	}
	return []workflow.Item{workflow.NewItem(result)}, nil
}

func (n *ProductHuntNode) createComment(ctx context.Context, token string, config map[string]interface{}) ([]workflow.Item, error) {
	postID := strVal(config, "post_id")
	body := strVal(config, "body")
	if postID == "" {
		return nil, fmt.Errorf("service.producthunt: 'post_id' is required for create_comment")
	}
	if body == "" {
		return nil, fmt.Errorf("service.producthunt: 'body' is required for create_comment")
	}

	input := map[string]interface{}{
		"postId": postID,
		"body":   body,
	}
	vars := map[string]interface{}{"input": input}

	data, err := n.phGraphQL(ctx, token, producthuntMutationCreateComment, vars)
	if err != nil {
		return nil, fmt.Errorf("service.producthunt create_comment: %w", err)
	}
	result, _ := data["commentCreate"].(map[string]interface{})
	if result == nil {
		return []workflow.Item{}, nil
	}
	comment, _ := result["comment"].(map[string]interface{})
	if comment == nil {
		comment = result
	}
	return []workflow.Item{workflow.NewItem(comment)}, nil
}
