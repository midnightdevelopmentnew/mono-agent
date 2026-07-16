package comm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"monoagent/internal/workflow"
)

// RedditNode submits posts, replies to comments, lists comments, and reads
// post metrics via the Reddit OAuth2 API (oauth.reddit.com).
// Type: "comm.reddit"
//
// Config fields:
//
//	"operation"    (string, required): "submit_post" | "reply_to_comment" | "list_comments" | "get_post_metrics"
//	"access_token" (string, required): OAuth2 access token
//	"subreddit"    (string, required for submit_post): subreddit name, no "r/" prefix
//	"title"        (string, required for submit_post): post title
//	"text"         (string): self-post body (submit_post) or comment body (reply_to_comment)
//	"url"          (string): link-post URL (submit_post) — mutually exclusive with "text"
//	"thing_id"     (string, required for reply_to_comment/list_comments/get_post_metrics):
//	  Reddit "fullname" (e.g. "t3_abc123" for a post, "t1_xyz" for a comment) or the bare ID
type RedditNode struct{}

func (n *RedditNode) Type() string { return "comm.reddit" }

const redditAPIBase = "https://oauth.reddit.com"

func (n *RedditNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	accessToken, _ := config["access_token"].(string)
	if accessToken == "" {
		return nil, fmt.Errorf("comm.reddit: access_token is required")
	}
	operation, _ := config["operation"].(string)

	switch operation {
	case "submit_post":
		subreddit, _ := config["subreddit"].(string)
		title, _ := config["title"].(string)
		if subreddit == "" || title == "" {
			return nil, fmt.Errorf("comm.reddit: subreddit and title are required for submit_post")
		}
		text, _ := config["text"].(string)
		linkURL, _ := config["url"].(string)

		form := url.Values{}
		form.Set("sr", subreddit)
		form.Set("title", title)
		form.Set("api_type", "json")
		if linkURL != "" {
			form.Set("kind", "link")
			form.Set("url", linkURL)
		} else {
			form.Set("kind", "self")
			form.Set("text", text)
		}

		raw, err := redditPost(ctx, redditAPIBase+"/api/submit", accessToken, form)
		if err != nil {
			return nil, fmt.Errorf("comm.reddit submit_post: %w", err)
		}
		result, err := redditParseAPIResponse(raw)
		if err != nil {
			return nil, fmt.Errorf("comm.reddit submit_post: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil

	case "reply_to_comment":
		thingID, _ := config["thing_id"].(string)
		text, _ := config["text"].(string)
		if thingID == "" || text == "" {
			return nil, fmt.Errorf("comm.reddit: thing_id and text are required for reply_to_comment")
		}

		form := url.Values{}
		form.Set("thing_id", redditEnsureFullname(thingID, "t3"))
		form.Set("text", text)
		form.Set("api_type", "json")

		raw, err := redditPost(ctx, redditAPIBase+"/api/comment", accessToken, form)
		if err != nil {
			return nil, fmt.Errorf("comm.reddit reply_to_comment: %w", err)
		}
		result, err := redditParseAPIResponse(raw)
		if err != nil {
			return nil, fmt.Errorf("comm.reddit reply_to_comment: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil

	case "list_comments":
		thingID, _ := config["thing_id"].(string)
		if thingID == "" {
			return nil, fmt.Errorf("comm.reddit: thing_id is required for list_comments")
		}
		postID := strings.TrimPrefix(thingID, "t3_")
		endpoint := fmt.Sprintf("%s/comments/%s?raw_json=1", redditAPIBase, postID)

		items, err := redditListComments(ctx, endpoint, accessToken)
		if err != nil {
			return nil, fmt.Errorf("comm.reddit list_comments: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil

	case "get_post_metrics":
		thingID, _ := config["thing_id"].(string)
		if thingID == "" {
			return nil, fmt.Errorf("comm.reddit: thing_id is required for get_post_metrics")
		}
		fullname := redditEnsureFullname(thingID, "t3")
		endpoint := fmt.Sprintf("%s/api/info?id=%s", redditAPIBase, url.QueryEscape(fullname))

		raw, err := redditGet(ctx, endpoint, accessToken)
		if err != nil {
			return nil, fmt.Errorf("comm.reddit get_post_metrics: %w", err)
		}
		result, err := redditParseInfoResponse(raw)
		if err != nil {
			return nil, fmt.Errorf("comm.reddit get_post_metrics: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil

	default:
		return nil, fmt.Errorf("comm.reddit: unsupported operation %q", operation)
	}
}

// redditEnsureFullname prefixes a bare Reddit ID with the given type prefix
// (e.g. "t3" for posts, "t1" for comments) if it isn't already a fullname.
func redditEnsureFullname(id, prefix string) string {
	if strings.Contains(id, "_") {
		return id
	}
	return prefix + "_" + id
}

// redditPost performs an authenticated form POST against the Reddit API.
func redditPost(ctx context.Context, endpoint, accessToken string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", redditUserAgent)
	return redditDo(req)
}

// redditGet performs an authenticated GET against the Reddit API.
func redditGet(ctx context.Context, endpoint, accessToken string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", redditUserAgent)
	return redditDo(req)
}

// redditUserAgent must match the constant defined in internal/connections/validate.go —
// duplicated here because the two packages don't share an internal helper package.
const redditUserAgent = "monoagent:workflow-node:1.0 (by /u/monoagent)"

func redditDo(req *http.Request) ([]byte, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// redditParseAPIResponse parses Reddit's api_type=json envelope, returning
// the flattened "data" object, or an error built from the "errors" array if
// Reddit reports any (Reddit returns HTTP 200 even on logical errors like
// rate limits, so the errors array must be checked explicitly).
func redditParseAPIResponse(raw []byte) (map[string]interface{}, error) {
	var envelope struct {
		JSON struct {
			Errors [][]string             `json:"errors"`
			Data   map[string]interface{} `json:"data"`
		} `json:"json"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	if len(envelope.JSON.Errors) > 0 {
		parts := make([]string, 0, len(envelope.JSON.Errors))
		for _, e := range envelope.JSON.Errors {
			parts = append(parts, strings.Join(e, ": "))
		}
		return nil, fmt.Errorf("reddit API error: %s", strings.Join(parts, "; "))
	}
	return envelope.JSON.Data, nil
}

// redditParseInfoResponse parses a /api/info Listing response and returns
// the first child's score/num_comments/permalink as a flat map.
func redditParseInfoResponse(raw []byte) (map[string]interface{}, error) {
	var listing struct {
		Data struct {
			Children []struct {
				Data map[string]interface{} `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	if len(listing.Data.Children) == 0 {
		return nil, fmt.Errorf("no post found for the given thing_id")
	}
	post := listing.Data.Children[0].Data
	return map[string]interface{}{
		"score":        post["score"],
		"num_comments": post["num_comments"],
		"permalink":    post["permalink"],
	}, nil
}

// redditListComments fetches a post's comment tree and flattens the
// top-level comments into workflow items.
func redditListComments(ctx context.Context, endpoint, accessToken string) ([]workflow.Item, error) {
	raw, err := redditGet(ctx, endpoint, accessToken)
	if err != nil {
		return nil, err
	}

	// The comments endpoint returns a 2-element array: [post listing, comment listing].
	var pair []struct {
		Data struct {
			Children []struct {
				Data map[string]interface{} `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &pair); err != nil {
		return nil, fmt.Errorf("parsing comments response: %w", err)
	}
	if len(pair) < 2 {
		return []workflow.Item{}, nil
	}

	items := make([]workflow.Item, 0, len(pair[1].Data.Children))
	for _, child := range pair[1].Data.Children {
		id, _ := child.Data["id"].(string)
		author, _ := child.Data["author"].(string)
		body, _ := child.Data["body"].(string)
		score := 0
		if s, ok := child.Data["score"].(float64); ok {
			score = int(s)
		}
		items = append(items, workflow.NewItem(map[string]interface{}{
			"id":     id,
			"author": author,
			"body":   body,
			"score":  score,
		}))
	}
	return items, nil
}
