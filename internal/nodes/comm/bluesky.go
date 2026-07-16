package comm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"monoagent/internal/workflow"
)

// BlueskyNode creates posts and reads engagement metrics via the AT
// Protocol (Bluesky) API using app-password session auth.
// Type: "comm.bluesky"
//
// Config fields:
//
//	"operation"    (string, required): "create_post" | "get_post_metrics"
//	"identifier"   (string, required): handle or email
//	"app_password" (string, required): app password from bsky.app/settings/app-passwords
//	"text"         (string, required for create_post): post text
//	"post_uri"     (string, required for get_post_metrics): "at://did/app.bsky.feed.post/rkey" URI
type BlueskyNode struct{}

func (n *BlueskyNode) Type() string { return "comm.bluesky" }

const blueskyBase = "https://bsky.social"

// blueskySession is the subset of com.atproto.server.createSession's
// response this node needs.
type blueskySession struct {
	DID       string `json:"did"`
	Handle    string `json:"handle"`
	AccessJwt string `json:"accessJwt"`
}

func (n *BlueskyNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	identifier := strVal2(config, "identifier")
	appPassword := strVal2(config, "app_password")
	if identifier == "" || appPassword == "" {
		return nil, fmt.Errorf("comm.bluesky: identifier and app_password are required")
	}
	operation := strVal2(config, "operation")

	switch operation {
	case "create_post":
		text := strVal2(config, "text")
		if text == "" {
			return nil, fmt.Errorf("comm.bluesky: text is required for create_post")
		}
		sess, err := blueskyCreateSession(ctx, identifier, appPassword)
		if err != nil {
			return nil, fmt.Errorf("comm.bluesky create_post: %w", err)
		}
		record := map[string]interface{}{
			"$type":     "app.bsky.feed.post",
			"text":      text,
			"createdAt": time.Now().UTC().Format(time.RFC3339),
		}
		body := map[string]interface{}{
			"repo":       sess.DID,
			"collection": "app.bsky.feed.post",
			"record":     record,
		}
		raw, err := blueskyRequest(ctx, http.MethodPost, blueskyBase+"/xrpc/com.atproto.repo.createRecord", sess.AccessJwt, body)
		if err != nil {
			return nil, fmt.Errorf("comm.bluesky create_post: %w", err)
		}
		var result map[string]interface{}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("comm.bluesky create_post: parsing response: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil

	case "get_post_metrics":
		postURI := strVal2(config, "post_uri")
		if postURI == "" {
			return nil, fmt.Errorf("comm.bluesky: post_uri is required for get_post_metrics")
		}
		sess, err := blueskyCreateSession(ctx, identifier, appPassword)
		if err != nil {
			return nil, fmt.Errorf("comm.bluesky get_post_metrics: %w", err)
		}
		endpoint := blueskyBase + "/xrpc/app.bsky.feed.getPostThread?uri=" + url.QueryEscape(postURI)
		raw, err := blueskyRequest(ctx, http.MethodGet, endpoint, sess.AccessJwt, nil)
		if err != nil {
			return nil, fmt.Errorf("comm.bluesky get_post_metrics: %w", err)
		}
		result, err := blueskyParsePostMetrics(raw)
		if err != nil {
			return nil, fmt.Errorf("comm.bluesky get_post_metrics: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil

	default:
		return nil, fmt.Errorf("comm.bluesky: unsupported operation %q", operation)
	}
}

// blueskyCreateSession exchanges identifier/app_password for a session JWT.
func blueskyCreateSession(ctx context.Context, identifier, appPassword string) (*blueskySession, error) {
	body, err := json.Marshal(map[string]string{"identifier": identifier, "password": appPassword})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, blueskyBase+"/xrpc/com.atproto.server.createSession", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return blueskyParseSession(respBody)
}

func blueskyParseSession(raw []byte) (*blueskySession, error) {
	var sess blueskySession
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("parsing session response: %w", err)
	}
	return &sess, nil
}

// blueskyRequest performs an authenticated JSON request against the AT Protocol API.
func blueskyRequest(ctx context.Context, method, endpoint, accessJwt string, body interface{}) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling body: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+accessJwt)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// blueskyParsePostMetrics extracts like/repost/reply counts from an
// app.bsky.feed.getPostThread response.
func blueskyParsePostMetrics(raw []byte) (map[string]interface{}, error) {
	var thread struct {
		Thread struct {
			Post struct {
				LikeCount   int `json:"likeCount"`
				RepostCount int `json:"repostCount"`
				ReplyCount  int `json:"replyCount"`
			} `json:"post"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &thread); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return map[string]interface{}{
		"like_count":   float64(thread.Thread.Post.LikeCount),
		"repost_count": float64(thread.Thread.Post.RepostCount),
		"reply_count":  float64(thread.Thread.Post.ReplyCount),
	}, nil
}
