package service

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"monoagent/internal/workflow"
)

// MastodonNode publishes statuses and reads timeline/account data via the
// Mastodon REST API (v1).
// Type: "service.mastodon"
//
// Config fields:
//
//	"operation"     (string, required): "publish_status" | "get_timeline" | "get_account" | "favourite" | "boost"
//	"access_token"  (string, required): OAuth access token from Mastodon settings
//	"instance"      (string, optional, default mastodonDefaultInstance): Mastodon instance URL
//	"text"          (string, required for publish_status): status text
//	"visibility"    (string, optional): public | unlisted | private | direct
//	"spoiler_text"  (string, optional): content warning text
//	"media_ids"     ([]string, optional): attached media IDs
//	"limit"         (int, optional, default 20): get_timeline page size
//	"status_id"     (string or number, required for favourite, boost): target status ID
type MastodonNode struct{}

func (n *MastodonNode) Type() string { return "service.mastodon" }

// mastodonDefaultInstance is a var (not const) so tests can point it at an httptest server.
var mastodonDefaultInstance = "https://mastodon.social"

func (n *MastodonNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	accessToken := strVal(config, "access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("service.mastodon: 'access_token' is required")
	}
	operation := strVal(config, "operation")

	instance := strVal(config, "instance")
	if instance == "" {
		instance = mastodonDefaultInstance
	}
	baseURL := instance + "/api/v1"

	switch operation {
	case "publish_status":
		return n.publishStatus(ctx, baseURL, accessToken, config)
	case "get_timeline":
		return n.getTimeline(ctx, baseURL, accessToken, config)
	case "get_account":
		return n.getAccount(ctx, baseURL, accessToken)
	case "favourite":
		return n.favourite(ctx, baseURL, accessToken, config)
	case "boost":
		return n.boost(ctx, baseURL, accessToken, config)
	default:
		return nil, fmt.Errorf("service.mastodon: unknown operation %q", operation)
	}
}

func (n *MastodonNode) publishStatus(ctx context.Context, baseURL, accessToken string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	text := strVal(config, "text")
	if text == "" {
		return nil, fmt.Errorf("service.mastodon: 'text' is required for publish_status")
	}

	body := map[string]interface{}{"status": text}
	if visibility := strVal(config, "visibility"); visibility != "" {
		body["visibility"] = visibility
	}
	if spoilerText := strVal(config, "spoiler_text"); spoilerText != "" {
		body["spoiler_text"] = spoilerText
	}
	if mediaIDs := strSliceVal(config, "media_ids"); len(mediaIDs) > 0 {
		body["media_ids"] = mediaIDs
	}

	result, err := apiRequest(ctx, "POST", baseURL+"/statuses", accessToken, body)
	if err != nil {
		return nil, fmt.Errorf("service.mastodon publish_status: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

func (n *MastodonNode) getTimeline(ctx context.Context, baseURL, accessToken string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	limit := intVal(config, "limit")
	if limit <= 0 {
		limit = 20
	}
	endpoint := fmt.Sprintf("%s/timelines/home?limit=%d", baseURL, limit)
	results, err := apiRequestList(ctx, "GET", endpoint, accessToken, nil)
	if err != nil {
		return nil, fmt.Errorf("service.mastodon get_timeline: %w", err)
	}
	items := mastodonItemsFromList(results)
	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

func (n *MastodonNode) getAccount(ctx context.Context, baseURL, accessToken string) ([]workflow.NodeOutput, error) {
	result, err := apiRequest(ctx, "GET", baseURL+"/accounts/verify_credentials", accessToken, nil)
	if err != nil {
		return nil, fmt.Errorf("service.mastodon get_account: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

func (n *MastodonNode) favourite(ctx context.Context, baseURL, accessToken string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	statusID := mastodonStatusID(config)
	if statusID == "" {
		return nil, fmt.Errorf("service.mastodon: 'status_id' is required for favourite")
	}
	endpoint := fmt.Sprintf("%s/statuses/%s/favourite", baseURL, url.PathEscape(statusID))
	result, err := apiRequest(ctx, "POST", endpoint, accessToken, nil)
	if err != nil {
		return nil, fmt.Errorf("service.mastodon favourite: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

func (n *MastodonNode) boost(ctx context.Context, baseURL, accessToken string, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	statusID := mastodonStatusID(config)
	if statusID == "" {
		return nil, fmt.Errorf("service.mastodon: 'status_id' is required for boost")
	}
	endpoint := fmt.Sprintf("%s/statuses/%s/reblog", baseURL, url.PathEscape(statusID))
	result, err := apiRequest(ctx, "POST", endpoint, accessToken, nil)
	if err != nil {
		return nil, fmt.Errorf("service.mastodon boost: %w", err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil
}

// mastodonItemsFromList converts a []interface{} of JSON objects into workflow.Items.
func mastodonItemsFromList(results []interface{}) []workflow.Item {
	items := make([]workflow.Item, 0, len(results))
	for _, r := range results {
		if m, ok := r.(map[string]interface{}); ok {
			items = append(items, workflow.NewItem(m))
		}
	}
	return items
}

// mastodonStatusID extracts "status_id" from config as a string, accepting
// either a string or a JSON number (float64/int).
func mastodonStatusID(config map[string]interface{}) string {
	switch v := config["status_id"].(type) {
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
