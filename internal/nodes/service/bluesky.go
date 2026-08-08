package service

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"monoagent/internal/workflow"
)

// BlueskyNode posts, reads, likes, and reposts via the Bluesky (AT Protocol) API.
// Type: "service.bluesky"
//
// Config fields:
//
//	"operation"    (string, required): "create_post" | "get_timeline" | "get_profile" | "like_post" | "repost"
//	"identifier"   (string, required): Bluesky handle (e.g. "monoes_me.bsky.social")
//	"app_password" (string, required): app password from account settings
//	"text"         (string, required for create_post)
//	"limit"        (int, optional, default 30): get_timeline page size
//	"actor"        (string, required for get_profile): handle or DID to fetch
//	"uri"          (string, required for like_post, repost): subject record URI
//	"cid"          (string, required for like_post, repost): subject record CID
type BlueskyNode struct{}

func (n *BlueskyNode) Type() string { return "service.bluesky" }

// blueskyBaseURL is a var (not const) so tests can point it at an httptest server.
var blueskyBaseURL = "https://bsky.social/xrpc"

func (n *BlueskyNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	identifier := strVal(config, "identifier")
	appPassword := strVal(config, "app_password")
	if identifier == "" || appPassword == "" {
		return nil, fmt.Errorf("service.bluesky: 'identifier' and 'app_password' are required")
	}
	operation := strVal(config, "operation")

	accessJwt, did, err := blueskyAuth(ctx, identifier, appPassword)
	if err != nil {
		return nil, fmt.Errorf("service.bluesky auth: %w", err)
	}

	switch operation {
	case "create_post":
		return n.createPost(ctx, accessJwt, did, config)
	case "get_timeline":
		return n.getTimeline(ctx, accessJwt, config)
	case "get_profile":
		return n.getProfile(ctx, accessJwt, config)
	case "like_post":
		return n.likePost(ctx, accessJwt, did, config)
	case "repost":
		return n.repost(ctx, accessJwt, did, config)
	default:
		return nil, fmt.Errorf("service.bluesky: unknown operation %q", operation)
	}
}

// blueskyAuth calls com.atproto.server.createSession and returns the access
// JWT and DID to use for subsequent authenticated requests.
func blueskyAuth(ctx context.Context, identifier, appPassword string) (string, string, error) {
	body := map[string]interface{}{
		"identifier": identifier,
		"password":   appPassword,
	}
	result, err := apiRequest(ctx, "POST", blueskyBaseURL+"/com.atproto.server.createSession", "", body)
	if err != nil {
		return "", "", err
	}
	accessJwt, _ := result["accessJwt"].(string)
	did, _ := result["did"].(string)
	if accessJwt == "" || did == "" {
		return "", "", fmt.Errorf("createSession response missing accessJwt or did")
	}
	return accessJwt, did, nil
}

func (n *BlueskyNode) createPost(ctx context.Context, accessJwt, did string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	text := strVal(config, "text")
	if text == "" {
		return nil, fmt.Errorf("service.bluesky: 'text' is required for create_post")
	}

	body := map[string]interface{}{
		"repo":       did,
		"collection": "app.bsky.feed.post",
		"record": map[string]interface{}{
			"$type":     "app.bsky.feed.post",
			"text":      text,
			"createdAt": time.Now().UTC().Format(time.RFC3339),
		},
	}
	result, err := apiRequest(ctx, "POST", blueskyBaseURL+"/com.atproto.repo.createRecord", accessJwt, body)
	if err != nil {
		return nil, fmt.Errorf("service.bluesky create_post: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

func (n *BlueskyNode) getTimeline(ctx context.Context, accessJwt string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	limit := intVal(config, "limit")
	if limit <= 0 {
		limit = 30
	}
	endpoint := fmt.Sprintf("%s/app.bsky.feed.getTimeline?limit=%d", blueskyBaseURL, limit)
	result, err := apiRequest(ctx, "GET", endpoint, accessJwt, nil)
	if err != nil {
		return nil, fmt.Errorf("service.bluesky get_timeline: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

func (n *BlueskyNode) getProfile(ctx context.Context, accessJwt string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	actor := strVal(config, "actor")
	if actor == "" {
		return nil, fmt.Errorf("service.bluesky: 'actor' is required for get_profile")
	}
	endpoint := blueskyBaseURL + "/app.bsky.actor.getProfile?actor=" + url.QueryEscape(actor)
	result, err := apiRequest(ctx, "GET", endpoint, accessJwt, nil)
	if err != nil {
		return nil, fmt.Errorf("service.bluesky get_profile: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

func (n *BlueskyNode) likePost(ctx context.Context, accessJwt, did string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	uri := strVal(config, "uri")
	cid := strVal(config, "cid")
	if uri == "" || cid == "" {
		return nil, fmt.Errorf("service.bluesky: 'uri' and 'cid' are required for like_post")
	}

	body := map[string]interface{}{
		"repo":       did,
		"collection": "app.bsky.feed.like",
		"record": map[string]interface{}{
			"$type": "app.bsky.feed.like",
			"subject": map[string]interface{}{
				"uri": uri,
				"cid": cid,
			},
			"createdAt": time.Now().UTC().Format(time.RFC3339),
		},
	}
	result, err := apiRequest(ctx, "POST", blueskyBaseURL+"/com.atproto.repo.createRecord", accessJwt, body)
	if err != nil {
		return nil, fmt.Errorf("service.bluesky like_post: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

func (n *BlueskyNode) repost(ctx context.Context, accessJwt, did string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	uri := strVal(config, "uri")
	cid := strVal(config, "cid")
	if uri == "" || cid == "" {
		return nil, fmt.Errorf("service.bluesky: 'uri' and 'cid' are required for repost")
	}

	body := map[string]interface{}{
		"repo":       did,
		"collection": "app.bsky.feed.repost",
		"record": map[string]interface{}{
			"$type": "app.bsky.feed.repost",
			"subject": map[string]interface{}{
				"uri": uri,
				"cid": cid,
			},
			"createdAt": time.Now().UTC().Format(time.RFC3339),
		},
	}
	result, err := apiRequest(ctx, "POST", blueskyBaseURL+"/com.atproto.repo.createRecord", accessJwt, body)
	if err != nil {
		return nil, fmt.Errorf("service.bluesky repost: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}
