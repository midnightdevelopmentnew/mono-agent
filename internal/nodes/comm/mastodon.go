package comm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"monoagent/internal/workflow"
)

// MastodonNode posts statuses and reads their engagement metrics via a
// Mastodon instance's REST API using a personal access token.
// Type: "comm.mastodon"
//
// Config fields:
//
//	"operation"     (string, required): "create_status" | "get_status_metrics"
//	"instance_url"  (string, required): e.g. "https://fosstodon.org"
//	"access_token"  (string, required): personal access token
//	"text"          (string, required for create_status): status text
//	"visibility"    (string): "public" (default) | "unlisted" | "private" | "direct"
//	"in_reply_to_id" (string): status ID to reply to
//	"status_id"     (string, required for get_status_metrics)
type MastodonNode struct{}

func (n *MastodonNode) Type() string { return "comm.mastodon" }

func (n *MastodonNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	instanceURL := mastodonNormalizeInstanceURL(strVal2(config, "instance_url"))
	if instanceURL == "" {
		return nil, fmt.Errorf("comm.mastodon: instance_url is required")
	}
	accessToken := strVal2(config, "access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("comm.mastodon: access_token is required")
	}
	operation := strVal2(config, "operation")

	switch operation {
	case "create_status":
		text := strVal2(config, "text")
		if text == "" {
			return nil, fmt.Errorf("comm.mastodon: text is required for create_status")
		}
		visibility := strVal2(config, "visibility")
		if visibility == "" {
			visibility = "public"
		}
		body := map[string]interface{}{
			"status":     text,
			"visibility": visibility,
		}
		if replyID := strVal2(config, "in_reply_to_id"); replyID != "" {
			body["in_reply_to_id"] = replyID
		}

		raw, err := mastodonRequest(ctx, http.MethodPost, instanceURL+"/api/v1/statuses", accessToken, body)
		if err != nil {
			return nil, fmt.Errorf("comm.mastodon create_status: %w", err)
		}
		result, err := mastodonParseStatus(raw)
		if err != nil {
			return nil, fmt.Errorf("comm.mastodon create_status: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil

	case "get_status_metrics":
		statusID := strVal2(config, "status_id")
		if statusID == "" {
			return nil, fmt.Errorf("comm.mastodon: status_id is required for get_status_metrics")
		}
		raw, err := mastodonRequest(ctx, http.MethodGet, instanceURL+"/api/v1/statuses/"+statusID, accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("comm.mastodon get_status_metrics: %w", err)
		}
		result, err := mastodonParseStatus(raw)
		if err != nil {
			return nil, fmt.Errorf("comm.mastodon get_status_metrics: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil

	default:
		return nil, fmt.Errorf("comm.mastodon: unsupported operation %q", operation)
	}
}

// mastodonNormalizeInstanceURL trims a trailing slash and adds a default
// https:// scheme if the caller passed a bare hostname.
func mastodonNormalizeInstanceURL(u string) string {
	u = strings.TrimSuffix(strings.TrimSpace(u), "/")
	if u == "" {
		return ""
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "https://" + u
	}
	return u
}

// mastodonRequest performs an authenticated JSON request against a Mastodon instance.
func mastodonRequest(ctx context.Context, method, endpoint, accessToken string, body interface{}) ([]byte, error) {
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
	req.Header.Set("Authorization", "Bearer "+accessToken)

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

// mastodonParseStatus extracts the fields this node exposes from a Mastodon
// Status API object.
func mastodonParseStatus(raw []byte) (map[string]interface{}, error) {
	var status struct {
		ID              string `json:"id"`
		URL             string `json:"url"`
		FavouritesCount int    `json:"favourites_count"`
		ReblogsCount    int    `json:"reblogs_count"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return map[string]interface{}{
		"id":               status.ID,
		"url":              status.URL,
		"favourites_count": float64(status.FavouritesCount),
		"reblogs_count":    float64(status.ReblogsCount),
	}, nil
}

// strVal2 safely extracts a string from a config map. Named to avoid
// colliding with the service package's unexported strVal (different
// package, but keeps a grep for "strVal" unambiguous across the repo).
func strVal2(config map[string]interface{}, key string) string {
	v, _ := config[key].(string)
	return v
}
