# Marketing Channel Nodes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add node support for six marketing/growth channels (Reddit, YouTube, Bluesky, Mastodon, Hacker News, Product Hunt) so monoagent workflows can post/read on each, following the codebase's existing API-node (`comm.discord`-style) and browser-node (`internal/bot/tiktok`-style) patterns.

**Architecture:** Four channels (Reddit, YouTube, Bluesky, Mastodon) have public write APIs and get a single Go node file each in `internal/nodes/comm/` or `internal/nodes/service/`, config-driven dispatch on an `operation` field, registered in the existing node-type registry, with a matching credential entry in `internal/connections/registry.go` + validator in `internal/connections/validate.go`. Two channels (Hacker News, Product Hunt) have no public write API and get a browser-automation bot package under `internal/bot/`, JSON action-step templates under `data/actions/`, auto-registered as workflow nodes by the existing `RegisterBrowserNodes()` loader.

**Tech Stack:** Go. No new third-party dependencies — every new API node uses `net/http` + `encoding/json` directly (matching `twilio.go`/`gmail.go`), and every new browser bot uses the existing `go-rod` + `internal/action` step-interpreter stack (matching `internal/bot/tiktok`).

## Global Constraints

- Follow `internal/nodes/comm/discord.go`'s node shape exactly: `Type() string` + `Execute(ctx, input, config) ([]workflow.NodeOutput, error)`, dispatch on `config["operation"]`.
- No new Go dependencies — reuse `net/http`/`encoding/json` for every API node (per spec: "no new plumbing").
- Keep files under 500 lines (project-wide constraint from `.agents/shared_instructions.md`).
- Every exported function has doc comments matching the existing `comm`/`service` package style.
- Tests follow this codebase's existing convention for these packages: pure-logic/parsing unit tests (no `httptest` network mocking is used anywhere in `internal/nodes/comm` or `internal/nodes/service` today — match that, don't introduce a new test style).
- Out of scope (per spec): executing real campaign content, workflow templates, Product Hunt launch-submission automation, YouTube Analytics API. Also out of scope, as a deliberate deviation from the design spec's Testing section: a `//go:build integration` live-browser test skeleton per browser bot (`internal/action/instagram_integration_test.go`'s pattern) — a real version of that test exercises live DOM selectors against the actual site, which can't be authored or verified without a live browser session; faking one would just be an unexecuted placeholder. Verify HN/PH selectors live via `monobrowse`/`claude-in-chrome` as a follow-up, outside this plan.

---

### Task 1: Reddit — `comm.reddit` node

**Files:**
- Create: `internal/nodes/comm/reddit.go`
- Create: `internal/nodes/comm/reddit_test.go`
- Modify: `internal/nodes/comm/register.go`
- Create: `internal/workflow/schemas/comm.reddit.json`
- Modify: `internal/connections/registry.go`
- Modify: `internal/connections/validate.go`

**Interfaces:**
- Produces: `RedditNode` (`Type() string` → `"comm.reddit"`, `Execute(ctx, input, config) ([]workflow.NodeOutput, error)`), config `operation`: `submit_post` | `reply_to_comment` | `list_comments` | `get_post_metrics`. Config `access_token` (string) is expected pre-resolved by the connections layer (same as `gmail.go`'s `access_token`).

- [ ] **Step 1: Add the Reddit platform entry to the connections registry**

Reddit's OAuth2 "installed app" type requires no client secret and uses PKCE — this matches the existing generic `RunOAuthFlow`/`exchangeCode` PKCE implementation in `internal/connections/oauth.go` with zero changes needed there. Add this entry to `internal/connections/registry.go`, in the "─── Social ────" section right after the `"tiktok"` entry (around line 87):

```go
	"reddit": {
		ID:         "reddit",
		Name:       "Reddit",
		Category:   "social",
		ConnectVia: "API",
		// Register Reddit's OAuth app as "installed app" type (no client
		// secret) — this fits the existing generic PKCE flow in oauth.go
		// with no code changes; Reddit's "web app" type requires HTTP Basic
		// auth on token exchange, which the generic flow doesn't do.
		Methods: []AuthMethod{MethodOAuth},
		Fields:  map[AuthMethod][]CredentialField{},
		OAuth: &OAuthConfig{
			AuthURL:      "https://www.reddit.com/api/v1/authorize",
			TokenURL:     "https://www.reddit.com/api/v1/access_token",
			Scopes:       []string{"submit", "read", "identity"},
			CallbackPort: 9876,
			ExtraParams:  map[string]string{"duration": "permanent"},
		},
		IconEmoji: "👽",
	},
```

- [ ] **Step 2: Add the Reddit validator**

In `internal/connections/validate.go`, add a case to the `ValidateConnection` switch (near the `"discord"` case, around line 37):

```go
	case "reddit":
		return validateReddit(ctx, c)
```

Then add the function near `validateDiscord` (Reddit requires a custom `User-Agent` header on every request or it gets hard-rate-limited — this applies to the validator and to every `RedditNode` call in Step 4):

```go
// redditUserAgent is Reddit's required custom User-Agent format:
// "platform:app_id:version (by /u/username)". Reddit aggressively
// rate-limits requests using default/generic User-Agent strings.
const redditUserAgent = "monoagent:workflow-node:1.0 (by /u/monoagent)"

// validateReddit validates a Reddit OAuth connection using the access_token field.
func validateReddit(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "access_token")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://oauth.reddit.com/api/v1/me", nil)
	if err != nil {
		return "", fmt.Errorf("validateReddit: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", redditUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateReddit: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("validateReddit: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateReddit: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("validateReddit: parse response: %w", err)
	}
	return result.Name, nil
}
```

- [ ] **Step 3: Write the failing tests for reddit.go's pure-logic helpers**

Create `internal/nodes/comm/reddit_test.go`:

```go
package comm

import "testing"

func TestRedditParseSubmitResponse(t *testing.T) {
	raw := []byte(`{"json":{"errors":[],"data":{"id":"abc123","name":"t3_abc123","url":"https://reddit.com/r/test/comments/abc123/x/"}}}`)
	result, err := redditParseAPIResponse(raw)
	if err != nil {
		t.Fatalf("redditParseAPIResponse: %v", err)
	}
	if result["id"] != "abc123" {
		t.Errorf("id = %v, want abc123", result["id"])
	}
	if result["name"] != "t3_abc123" {
		t.Errorf("name = %v, want t3_abc123", result["name"])
	}
	if result["url"] != "https://reddit.com/r/test/comments/abc123/x/" {
		t.Errorf("url = %v, want the permalink", result["url"])
	}
}

func TestRedditParseSubmitResponseErrors(t *testing.T) {
	raw := []byte(`{"json":{"errors":[["RATELIMIT","you are doing that too much","ratelimit"]],"data":{}}}`)
	_, err := redditParseAPIResponse(raw)
	if err == nil {
		t.Fatal("expected error for a Reddit API error response, got nil")
	}
}

func TestRedditFullnamePrefix(t *testing.T) {
	cases := map[string]string{
		"abc123":     "t3_abc123",
		"t3_abc123":  "t3_abc123",
		"t1_xyz":     "t1_xyz",
	}
	for in, want := range cases {
		if got := redditEnsureFullname(in, "t3"); got != want {
			t.Errorf("redditEnsureFullname(%q, t3) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `go test ./internal/nodes/comm/... -run TestReddit -v`
Expected: FAIL with `undefined: redditParseAPIResponse` (and `redditEnsureFullname`)

- [ ] **Step 5: Implement `internal/nodes/comm/reddit.go`**

```go
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

// redditCommentNode mirrors the subset of Reddit's comment JSON this node exposes.
type redditCommentNode struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	Body   string `json:"body"`
	Score  int    `json:"score"`
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
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/nodes/comm/... -run TestReddit -v`
Expected: PASS

- [ ] **Step 7: Register the node**

In `internal/nodes/comm/register.go`, add after the `comm.discord` line:

```go
	r.Register("comm.reddit", func() workflow.NodeExecutor { return &RedditNode{} })
```

- [ ] **Step 8: Add the workflow schema**

Create `internal/workflow/schemas/comm.reddit.json`:

```json
{
  "credential_platform": "reddit",
  "fields": [
    { "key": "credential_id", "label": "Reddit Connection", "type": "credential_picker", "required": true },
    { "key": "operation", "label": "Operation", "type": "select", "required": true, "options": ["submit_post", "reply_to_comment", "list_comments", "get_post_metrics"], "default": "submit_post" },
    { "key": "subreddit", "label": "Subreddit", "type": "text", "required": false, "depends_on": { "key": "operation", "values": ["submit_post"] } },
    { "key": "title", "label": "Title", "type": "text", "required": false, "depends_on": { "key": "operation", "values": ["submit_post"] } },
    { "key": "text", "label": "Body / Comment Text", "type": "textarea", "required": false, "rows": 4, "depends_on": { "key": "operation", "values": ["submit_post", "reply_to_comment"] } },
    { "key": "url", "label": "Link URL (link post instead of self post)", "type": "text", "required": false, "depends_on": { "key": "operation", "values": ["submit_post"] } },
    { "key": "thing_id", "label": "Post/Comment ID (e.g. t3_abc123)", "type": "text", "required": false, "depends_on": { "key": "operation", "values": ["reply_to_comment", "list_comments", "get_post_metrics"] } }
  ]
}
```

- [ ] **Step 9: Build and commit**

Run: `go build ./... && go test ./internal/nodes/comm/... ./internal/connections/... -v`
Expected: all PASS

```bash
git add internal/nodes/comm/reddit.go internal/nodes/comm/reddit_test.go internal/nodes/comm/register.go internal/workflow/schemas/comm.reddit.json internal/connections/registry.go internal/connections/validate.go
git commit -m "feat: add comm.reddit node (submit post, reply, list comments, metrics)"
```

---

### Task 2: Mastodon — `comm.mastodon` node

**Files:**
- Create: `internal/nodes/comm/mastodon.go`
- Create: `internal/nodes/comm/mastodon_test.go`
- Modify: `internal/nodes/comm/register.go`
- Create: `internal/workflow/schemas/comm.mastodon.json`
- Modify: `internal/connections/registry.go`
- Modify: `internal/connections/validate.go`

**Interfaces:**
- Produces: `MastodonNode` (`Type()` → `"comm.mastodon"`), config `operation`: `create_status` | `get_status_metrics`. Config fields `instance_url`, `access_token` resolved from the connection (`MethodAPIKey`, matching Twilio's two-field shape).

- [ ] **Step 1: Add the Mastodon platform entry**

In `internal/connections/registry.go`, add after the `"reddit"` entry from Task 1:

```go
	"mastodon": {
		ID:         "mastodon",
		Name:       "Mastodon",
		Category:   "social",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{Key: "instance_url", Label: "Instance URL (e.g. https://fosstodon.org)", Secret: false, Required: true},
				{Key: "access_token", Label: "Access Token", Secret: true, Required: true, HelpURL: "https://docs.joinmastodon.org/client/token/"},
			},
		},
		IconEmoji: "🐘",
	},
```

- [ ] **Step 2: Add the Mastodon validator**

In `internal/connections/validate.go`, add a case near `"discord"`:

```go
	case "mastodon":
		return validateMastodon(ctx, c)
```

Add the function near `validateDiscord`:

```go
// validateMastodon validates a Mastodon connection using instance_url and access_token.
func validateMastodon(ctx context.Context, c *Connection) (string, error) {
	instanceURL := strings.TrimSuffix(getStr(c.Data, "instance_url"), "/")
	token := getStr(c.Data, "access_token")
	if instanceURL == "" {
		return "", fmt.Errorf("validateMastodon: missing instance_url")
	}
	body, status, err := doGET(ctx, instanceURL+"/api/v1/accounts/verify_credentials", "Bearer "+token)
	if err != nil {
		return "", fmt.Errorf("validateMastodon: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("validateMastodon: unexpected status %d", status)
	}

	var resp struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("validateMastodon: parse response: %w", err)
	}
	return resp.Username, nil
}
```

- [ ] **Step 3: Write the failing test**

Create `internal/nodes/comm/mastodon_test.go`:

```go
package comm

import "testing"

func TestMastodonNormalizeInstanceURL(t *testing.T) {
	cases := map[string]string{
		"https://fosstodon.org":  "https://fosstodon.org",
		"https://fosstodon.org/": "https://fosstodon.org",
		"fosstodon.org":          "https://fosstodon.org",
	}
	for in, want := range cases {
		if got := mastodonNormalizeInstanceURL(in); got != want {
			t.Errorf("mastodonNormalizeInstanceURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMastodonParseStatusResponse(t *testing.T) {
	raw := []byte(`{"id":"110","url":"https://fosstodon.org/@x/110","favourites_count":3,"reblogs_count":1}`)
	result, err := mastodonParseStatus(raw)
	if err != nil {
		t.Fatalf("mastodonParseStatus: %v", err)
	}
	if result["id"] != "110" {
		t.Errorf("id = %v, want 110", result["id"])
	}
	if result["favourites_count"] != float64(3) {
		t.Errorf("favourites_count = %v, want 3", result["favourites_count"])
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/nodes/comm/... -run TestMastodon -v`
Expected: FAIL with `undefined: mastodonNormalizeInstanceURL` (and `mastodonParseStatus`)

- [ ] **Step 5: Implement `internal/nodes/comm/mastodon.go`**

```go
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
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/nodes/comm/... -run TestMastodon -v`
Expected: PASS

- [ ] **Step 7: Register the node**

In `internal/nodes/comm/register.go`, add:

```go
	r.Register("comm.mastodon", func() workflow.NodeExecutor { return &MastodonNode{} })
```

- [ ] **Step 8: Add the workflow schema**

Create `internal/workflow/schemas/comm.mastodon.json`:

```json
{
  "credential_platform": "mastodon",
  "fields": [
    { "key": "credential_id", "label": "Mastodon Connection", "type": "credential_picker", "required": true },
    { "key": "operation", "label": "Operation", "type": "select", "required": true, "options": ["create_status", "get_status_metrics"], "default": "create_status" },
    { "key": "text", "label": "Status Text", "type": "textarea", "required": false, "rows": 4, "depends_on": { "key": "operation", "values": ["create_status"] } },
    { "key": "visibility", "label": "Visibility", "type": "select", "required": false, "options": ["public", "unlisted", "private", "direct"], "default": "public", "depends_on": { "key": "operation", "values": ["create_status"] } },
    { "key": "in_reply_to_id", "label": "In Reply To (status ID)", "type": "text", "required": false, "depends_on": { "key": "operation", "values": ["create_status"] } },
    { "key": "status_id", "label": "Status ID", "type": "text", "required": false, "depends_on": { "key": "operation", "values": ["get_status_metrics"] } }
  ]
}
```

- [ ] **Step 9: Build and commit**

Run: `go build ./... && go test ./internal/nodes/comm/... ./internal/connections/... -v`
Expected: all PASS

```bash
git add internal/nodes/comm/mastodon.go internal/nodes/comm/mastodon_test.go internal/nodes/comm/register.go internal/workflow/schemas/comm.mastodon.json internal/connections/registry.go internal/connections/validate.go
git commit -m "feat: add comm.mastodon node (create status, read metrics)"
```

---

### Task 3: Bluesky — `comm.bluesky` node

**Files:**
- Create: `internal/nodes/comm/bluesky.go`
- Create: `internal/nodes/comm/bluesky_test.go`
- Modify: `internal/nodes/comm/register.go`
- Create: `internal/workflow/schemas/comm.bluesky.json`
- Modify: `internal/connections/registry.go`
- Modify: `internal/connections/validate.go`

**Interfaces:**
- Produces: `BlueskyNode` (`Type()` → `"comm.bluesky"`), config `operation`: `create_post` | `get_post_metrics`. Config fields `identifier` (handle or email), `app_password` (`MethodAPIKey`, two fields like Twilio). The node calls `com.atproto.server.createSession` internally on every `Execute` — AT Protocol sessions are short-lived JWTs, not something the connections layer pre-resolves like an OAuth `access_token`.

- [ ] **Step 1: Add the Bluesky platform entry**

In `internal/connections/registry.go`, add after `"mastodon"`:

```go
	"bluesky": {
		ID:         "bluesky",
		Name:       "Bluesky",
		Category:   "social",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodAPIKey},
		Fields: map[AuthMethod][]CredentialField{
			MethodAPIKey: {
				{Key: "identifier", Label: "Handle or Email", Secret: false, Required: true},
				{Key: "app_password", Label: "App Password", Secret: true, Required: true, HelpURL: "https://bsky.app/settings/app-passwords"},
			},
		},
		IconEmoji: "🦋",
	},
```

- [ ] **Step 2: Add the Bluesky validator**

In `internal/connections/validate.go`, add a case near `"discord"`:

```go
	case "bluesky":
		return validateBluesky(ctx, c)
```

Add the function near `validateDiscord`:

```go
// validateBluesky validates a Bluesky connection by creating a session
// with the identifier/app_password fields — AT Protocol has no long-lived
// token to independently verify, so a successful session creation IS the check.
func validateBluesky(ctx context.Context, c *Connection) (string, error) {
	identifier := getStr(c.Data, "identifier")
	password := getStr(c.Data, "app_password")

	body, err := json.Marshal(map[string]string{"identifier": identifier, "password": password})
	if err != nil {
		return "", fmt.Errorf("validateBluesky: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://bsky.social/xrpc/com.atproto.server.createSession", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("validateBluesky: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("validateBluesky: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("validateBluesky: read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("validateBluesky: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("validateBluesky: parse response: %w", err)
	}
	return result.Handle, nil
}
```

Note: `validate.go` already imports `bytes`, `context`, `encoding/json`, `fmt`, `io`, `net/http` (confirmed in the file header) — no new imports needed for this step.

- [ ] **Step 3: Write the failing test**

Create `internal/nodes/comm/bluesky_test.go`:

```go
package comm

import "testing"

func TestBlueskyParseSession(t *testing.T) {
	raw := []byte(`{"did":"did:plc:abc123","handle":"user.bsky.social","accessJwt":"jwt-token"}`)
	sess, err := blueskyParseSession(raw)
	if err != nil {
		t.Fatalf("blueskyParseSession: %v", err)
	}
	if sess.DID != "did:plc:abc123" {
		t.Errorf("DID = %q, want did:plc:abc123", sess.DID)
	}
	if sess.AccessJwt != "jwt-token" {
		t.Errorf("AccessJwt = %q, want jwt-token", sess.AccessJwt)
	}
}

func TestBlueskyParsePostMetrics(t *testing.T) {
	raw := []byte(`{"thread":{"post":{"likeCount":5,"repostCount":2,"replyCount":1}}}`)
	result, err := blueskyParsePostMetrics(raw)
	if err != nil {
		t.Fatalf("blueskyParsePostMetrics: %v", err)
	}
	if result["like_count"] != float64(5) {
		t.Errorf("like_count = %v, want 5", result["like_count"])
	}
	if result["repost_count"] != float64(2) {
		t.Errorf("repost_count = %v, want 2", result["repost_count"])
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/nodes/comm/... -run TestBluesky -v`
Expected: FAIL with `undefined: blueskyParseSession` (and `blueskyParsePostMetrics`)

- [ ] **Step 5: Implement `internal/nodes/comm/bluesky.go`**

```go
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
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/nodes/comm/... -run TestBluesky -v`
Expected: PASS

- [ ] **Step 7: Register the node**

In `internal/nodes/comm/register.go`, add:

```go
	r.Register("comm.bluesky", func() workflow.NodeExecutor { return &BlueskyNode{} })
```

- [ ] **Step 8: Add the workflow schema**

Create `internal/workflow/schemas/comm.bluesky.json`:

```json
{
  "credential_platform": "bluesky",
  "fields": [
    { "key": "credential_id", "label": "Bluesky Connection", "type": "credential_picker", "required": true },
    { "key": "operation", "label": "Operation", "type": "select", "required": true, "options": ["create_post", "get_post_metrics"], "default": "create_post" },
    { "key": "text", "label": "Post Text", "type": "textarea", "required": false, "rows": 4, "depends_on": { "key": "operation", "values": ["create_post"] } },
    { "key": "post_uri", "label": "Post URI (at://...)", "type": "text", "required": false, "depends_on": { "key": "operation", "values": ["get_post_metrics"] } }
  ]
}
```

- [ ] **Step 9: Build and commit**

Run: `go build ./... && go test ./internal/nodes/comm/... ./internal/connections/... -v`
Expected: all PASS

```bash
git add internal/nodes/comm/bluesky.go internal/nodes/comm/bluesky_test.go internal/nodes/comm/register.go internal/workflow/schemas/comm.bluesky.json internal/connections/registry.go internal/connections/validate.go
git commit -m "feat: add comm.bluesky node (create post, read metrics)"
```

---

### Task 4: YouTube — `service.youtube` node

**Files:**
- Create: `internal/nodes/service/youtube.go`
- Create: `internal/nodes/service/youtube_test.go`
- Modify: `internal/nodes/service/register_b.go`
- Create: `internal/workflow/schemas/service.youtube.json`
- Modify: `internal/connections/registry.go`
- Modify: `internal/connections/validate.go`

**Interfaces:**
- Produces: `YouTubeNode` (`Type()` → `"service.youtube"`), config `operation`: `upload_video` | `get_video_stats` | `list_comments` | `reply_to_comment`. Config `access_token` pre-resolved by connections (same as `gmail.go`).
- Consumes: `apiRequest`/`strVal`/`intVal` from `internal/nodes/service/helpers.go` (already defined, no changes needed).

- [ ] **Step 1: Add the YouTube platform entry**

In `internal/connections/registry.go`, add right after `"google_drive"` (around line 391):

```go
	"youtube": {
		ID:         "youtube",
		Name:       "YouTube",
		Category:   "service",
		ConnectVia: "API",
		Methods:    []AuthMethod{MethodOAuth},
		Fields:     map[AuthMethod][]CredentialField{},
		OAuth: &OAuthConfig{
			AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			Scopes:       []string{"https://www.googleapis.com/auth/youtube.upload", "https://www.googleapis.com/auth/youtube.force-ssl"},
			CallbackPort: 9876,
			ExtraParams:  map[string]string{"access_type": "offline", "prompt": "consent"},
		},
		IconEmoji: "📺",
	},
```

- [ ] **Step 2: Add the YouTube validator**

In `internal/connections/validate.go`, add a case near the existing Google cases (around line 42):

```go
	case "youtube":
		return validateYouTube(ctx, c)
```

Add the function near `validateGoogle` (find it with `grep -n "func validateGoogle" internal/connections/validate.go` first to match its exact style):

```go
// validateYouTube validates a YouTube OAuth connection using the access_token field.
func validateYouTube(ctx context.Context, c *Connection) (string, error) {
	token := getStr(c.Data, "access_token")
	body, status, err := doGET(ctx, "https://www.googleapis.com/youtube/v3/channels?part=snippet&mine=true", "Bearer "+token)
	if err != nil {
		return "", fmt.Errorf("validateYouTube: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("validateYouTube: unexpected status %d", status)
	}

	var resp struct {
		Items []struct {
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("validateYouTube: parse response: %w", err)
	}
	if len(resp.Items) == 0 {
		return "", fmt.Errorf("validateYouTube: no channel found for this account")
	}
	return resp.Items[0].Snippet.Title, nil
}
```

- [ ] **Step 3: Write the failing test**

Create `internal/nodes/service/youtube_test.go`:

```go
package service

import "testing"

func TestYoutubeParseVideoStats(t *testing.T) {
	raw := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{
				"id": "abc123",
				"statistics": map[string]interface{}{
					"viewCount":    "1000",
					"likeCount":    "50",
					"commentCount": "10",
				},
			},
		},
	}
	result, err := youtubeParseVideoStats(raw)
	if err != nil {
		t.Fatalf("youtubeParseVideoStats: %v", err)
	}
	if result["view_count"] != "1000" {
		t.Errorf("view_count = %v, want 1000", result["view_count"])
	}
	if result["like_count"] != "50" {
		t.Errorf("like_count = %v, want 50", result["like_count"])
	}
}

func TestYoutubeParseVideoStatsNoItems(t *testing.T) {
	raw := map[string]interface{}{"items": []interface{}{}}
	_, err := youtubeParseVideoStats(raw)
	if err == nil {
		t.Fatal("expected error when no video is found, got nil")
	}
}

func TestYoutubeBuildUploadMetadata(t *testing.T) {
	meta := youtubeBuildUploadMetadata("Title", "Description", []string{"tag1", "tag2"}, "22", "public")
	snippet := meta["snippet"].(map[string]interface{})
	if snippet["title"] != "Title" {
		t.Errorf("title = %v, want Title", snippet["title"])
	}
	tags := snippet["tags"].([]string)
	if len(tags) != 2 || tags[0] != "tag1" {
		t.Errorf("tags = %v, want [tag1 tag2]", tags)
	}
	status := meta["status"].(map[string]interface{})
	if status["privacyStatus"] != "public" {
		t.Errorf("privacyStatus = %v, want public", status["privacyStatus"])
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `go test ./internal/nodes/service/... -run TestYoutube -v`
Expected: FAIL with `undefined: youtubeParseVideoStats` (and `youtubeBuildUploadMetadata`)

- [ ] **Step 5: Implement `internal/nodes/service/youtube.go`**

YouTube video upload uses the resumable-upload protocol: an initial metadata POST returns a `Location` header session URL, then the video bytes are PUT to that URL. This can't reuse `apiRequest` (which only handles JSON bodies), so it gets its own small HTTP call.

```go
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"monoagent/internal/workflow"
)

// YouTubeNode uploads videos and reads stats/comments via the YouTube Data API v3.
// Type: "service.youtube"
//
// Config fields:
//
//	"operation"        (string, required): "upload_video" | "get_video_stats" | "list_comments" | "reply_to_comment"
//	"access_token"     (string, required): OAuth2 access token
//	"title"            (string, required for upload_video)
//	"description"      (string): video description
//	"tags"             ([]interface{} of string): video tags
//	"category_id"      (string): YouTube category ID, default "22" (People & Blogs)
//	"privacy_status"   (string): "public" (default) | "unlisted" | "private"
//	"video_file_path"  (string, required for upload_video): local path to the video file
//	"video_id"         (string, required for get_video_stats/list_comments)
//	"comment_id"       (string, required for reply_to_comment)
//	"text"             (string, required for reply_to_comment)
type YouTubeNode struct{}

func (n *YouTubeNode) Type() string { return "service.youtube" }

const youtubeUploadURL = "https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&part=snippet,status"
const youtubeAPIBase = "https://www.googleapis.com/youtube/v3"

func (n *YouTubeNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	accessToken := strVal(config, "access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("youtube: access_token is required")
	}
	operation := strVal(config, "operation")

	switch operation {
	case "upload_video":
		title := strVal(config, "title")
		videoPath := strVal(config, "video_file_path")
		if title == "" || videoPath == "" {
			return nil, fmt.Errorf("youtube: title and video_file_path are required for upload_video")
		}
		categoryID := strVal(config, "category_id")
		if categoryID == "" {
			categoryID = "22"
		}
		privacyStatus := strVal(config, "privacy_status")
		if privacyStatus == "" {
			privacyStatus = "public"
		}
		metadata := youtubeBuildUploadMetadata(title, strVal(config, "description"), strSliceVal(config, "tags"), categoryID, privacyStatus)

		result, err := youtubeUploadVideo(ctx, accessToken, metadata, videoPath)
		if err != nil {
			return nil, fmt.Errorf("youtube upload_video: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil

	case "get_video_stats":
		videoID := strVal(config, "video_id")
		if videoID == "" {
			return nil, fmt.Errorf("youtube: video_id is required for get_video_stats")
		}
		endpoint := fmt.Sprintf("%s/videos?part=statistics&id=%s", youtubeAPIBase, videoID)
		raw, err := apiRequest(ctx, "GET", endpoint, accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("youtube get_video_stats: %w", err)
		}
		result, err := youtubeParseVideoStats(raw)
		if err != nil {
			return nil, fmt.Errorf("youtube get_video_stats: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(result)}}}, nil

	case "list_comments":
		videoID := strVal(config, "video_id")
		if videoID == "" {
			return nil, fmt.Errorf("youtube: video_id is required for list_comments")
		}
		endpoint := fmt.Sprintf("%s/commentThreads?part=snippet&videoId=%s&maxResults=100", youtubeAPIBase, videoID)
		raw, err := apiRequest(ctx, "GET", endpoint, accessToken, nil)
		if err != nil {
			return nil, fmt.Errorf("youtube list_comments: %w", err)
		}
		items, err := youtubeParseCommentThreads(raw)
		if err != nil {
			return nil, fmt.Errorf("youtube list_comments: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil

	case "reply_to_comment":
		commentID := strVal(config, "comment_id")
		text := strVal(config, "text")
		if commentID == "" || text == "" {
			return nil, fmt.Errorf("youtube: comment_id and text are required for reply_to_comment")
		}
		body := map[string]interface{}{
			"snippet": map[string]interface{}{
				"parentId":     commentID,
				"textOriginal": text,
			},
		}
		endpoint := youtubeAPIBase + "/comments?part=snippet"
		raw, err := apiRequest(ctx, "POST", endpoint, accessToken, body)
		if err != nil {
			return nil, fmt.Errorf("youtube reply_to_comment: %w", err)
		}
		return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(raw)}}}, nil

	default:
		return nil, fmt.Errorf("youtube: unsupported operation %q", operation)
	}
}

// youtubeBuildUploadMetadata builds the videos.insert request body's snippet/status.
func youtubeBuildUploadMetadata(title, description string, tags []string, categoryID, privacyStatus string) map[string]interface{} {
	return map[string]interface{}{
		"snippet": map[string]interface{}{
			"title":       title,
			"description": description,
			"tags":        tags,
			"categoryId":  categoryID,
		},
		"status": map[string]interface{}{
			"privacyStatus": privacyStatus,
		},
	}
}

// youtubeUploadVideo performs the two-step resumable upload: an initial
// metadata POST to obtain a session URL (from the Location header), then a
// PUT of the video file bytes to that session URL.
func youtubeUploadVideo(ctx context.Context, accessToken string, metadata map[string]interface{}, videoPath string) (map[string]interface{}, error) {
	metaBody, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshaling metadata: %w", err)
	}

	initReq, err := http.NewRequestWithContext(ctx, http.MethodPost, youtubeUploadURL, bytes.NewReader(metaBody))
	if err != nil {
		return nil, err
	}
	initReq.Header.Set("Authorization", "Bearer "+accessToken)
	initReq.Header.Set("Content-Type", "application/json; charset=UTF-8")
	initReq.Header.Set("X-Upload-Content-Type", "video/*")

	initResp, err := httpClient.Do(initReq)
	if err != nil {
		return nil, fmt.Errorf("initiating upload session: %w", err)
	}
	defer initResp.Body.Close()
	if initResp.StatusCode < 200 || initResp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(initResp.Body)
		return nil, fmt.Errorf("initiating upload session: HTTP %d: %s", initResp.StatusCode, string(errBody))
	}
	sessionURL := initResp.Header.Get("Location")
	if sessionURL == "" {
		return nil, fmt.Errorf("upload session did not return a Location header")
	}

	f, err := os.Open(videoPath)
	if err != nil {
		return nil, fmt.Errorf("opening video file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat video file: %w", err)
	}

	uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, f)
	if err != nil {
		return nil, err
	}
	uploadReq.ContentLength = info.Size()
	uploadReq.Header.Set("Content-Type", "video/*")

	uploadResp, err := httpClient.Do(uploadReq)
	if err != nil {
		return nil, fmt.Errorf("uploading video bytes: %w", err)
	}
	defer uploadResp.Body.Close()
	respBody, err := io.ReadAll(uploadResp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading upload response: %w", err)
	}
	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		return nil, fmt.Errorf("uploading video bytes: HTTP %d: %s", uploadResp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parsing upload response: %w", err)
	}
	return result, nil
}

// youtubeParseVideoStats extracts view/like/comment counts from a
// videos.list(part=statistics) response.
func youtubeParseVideoStats(raw map[string]interface{}) (map[string]interface{}, error) {
	items, _ := raw["items"].([]interface{})
	if len(items) == 0 {
		return nil, fmt.Errorf("no video found for the given video_id")
	}
	item, _ := items[0].(map[string]interface{})
	stats, _ := item["statistics"].(map[string]interface{})
	return map[string]interface{}{
		"view_count":    stats["viewCount"],
		"like_count":    stats["likeCount"],
		"comment_count": stats["commentCount"],
	}, nil
}

// youtubeParseCommentThreads flattens a commentThreads.list response into
// workflow items (one per top-level comment).
func youtubeParseCommentThreads(raw map[string]interface{}) ([]workflow.Item, error) {
	items, _ := raw["items"].([]interface{})
	out := make([]workflow.Item, 0, len(items))
	for _, it := range items {
		thread, _ := it.(map[string]interface{})
		snippet, _ := thread["snippet"].(map[string]interface{})
		topLevel, _ := snippet["topLevelComment"].(map[string]interface{})
		commentSnippet, _ := topLevel["snippet"].(map[string]interface{})
		out = append(out, workflow.NewItem(map[string]interface{}{
			"comment_id":   topLevel["id"],
			"author":       commentSnippet["authorDisplayName"],
			"text":         commentSnippet["textDisplay"],
			"like_count":   commentSnippet["likeCount"],
			"published_at": commentSnippet["publishedAt"],
		}))
	}
	return out, nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/nodes/service/... -run TestYoutube -v`
Expected: PASS

- [ ] **Step 7: Register the node**

In `internal/nodes/service/register_b.go`, add after `service.google_drive`:

```go
	r.Register("service.youtube", func() workflow.NodeExecutor { return &YouTubeNode{} })
```

- [ ] **Step 8: Add the workflow schema**

Create `internal/workflow/schemas/service.youtube.json`:

```json
{
  "credential_platform": "youtube",
  "fields": [
    { "key": "credential_id", "label": "YouTube Connection", "type": "credential_picker", "required": true },
    { "key": "operation", "label": "Operation", "type": "select", "required": true, "options": ["upload_video", "get_video_stats", "list_comments", "reply_to_comment"], "default": "upload_video" },
    { "key": "title", "label": "Title", "type": "text", "required": false, "depends_on": { "key": "operation", "values": ["upload_video"] } },
    { "key": "description", "label": "Description", "type": "textarea", "required": false, "rows": 4, "depends_on": { "key": "operation", "values": ["upload_video"] } },
    { "key": "tags", "label": "Tags", "type": "tag_list", "required": false, "depends_on": { "key": "operation", "values": ["upload_video"] } },
    { "key": "category_id", "label": "Category ID", "type": "text", "required": false, "default": "22", "depends_on": { "key": "operation", "values": ["upload_video"] } },
    { "key": "privacy_status", "label": "Privacy Status", "type": "select", "required": false, "options": ["public", "unlisted", "private"], "default": "public", "depends_on": { "key": "operation", "values": ["upload_video"] } },
    { "key": "video_file_path", "label": "Video File Path", "type": "text", "required": false, "depends_on": { "key": "operation", "values": ["upload_video"] } },
    { "key": "video_id", "label": "Video ID", "type": "text", "required": false, "depends_on": { "key": "operation", "values": ["get_video_stats", "list_comments"] } },
    { "key": "comment_id", "label": "Comment ID", "type": "text", "required": false, "depends_on": { "key": "operation", "values": ["reply_to_comment"] } },
    { "key": "text", "label": "Reply Text", "type": "textarea", "required": false, "rows": 3, "depends_on": { "key": "operation", "values": ["reply_to_comment"] } }
  ]
}
```

- [ ] **Step 9: Build and commit**

Run: `go build ./... && go test ./internal/nodes/service/... ./internal/connections/... -v`
Expected: all PASS

```bash
git add internal/nodes/service/youtube.go internal/nodes/service/youtube_test.go internal/nodes/service/register_b.go internal/workflow/schemas/service.youtube.json internal/connections/registry.go internal/connections/validate.go
git commit -m "feat: add service.youtube node (upload, stats, comments)"
```

---

### Task 5: Hacker News — browser bot

**Files:**
- Create: `internal/bot/hackernews/bot.go`
- Create: `internal/bot/hackernews/actions.go`
- Create: `internal/bot/hackernews/bot_test.go`
- Create: `data/actions/hackernews/submit_post.json`
- Create: `data/actions/hackernews/reply_to_comment.json`
- Create: `data/actions/hackernews/list_comments.json`
- Create: `data/actions/hackernews/get_post_metrics.json`
- Modify: `cmd/monoagentcli/node.go`
- Modify: `internal/connections/registry.go`

**Interfaces:**
- Produces: `HackerNewsBot` implementing both `bot.BotAdapter` (`Platform()` → `"HACKERNEWS"`) and `action.BotAdapter` (`GetMethodByName`). Node types `hackernews.submit_post`, `hackernews.reply_to_comment`, `hackernews.list_comments`, `hackernews.get_post_metrics` are auto-registered by the existing `RegisterBrowserNodes()` — no new Go registration call needed for the nodes themselves.
- Consumes: `browser.PageInterface` (`Navigate`, `WaitLoad`, `Element`, `Eval`, `Has`, `Timeout`) — same interface `tiktok.go`/`actions.go` use.

Hacker News' markup is plain server-rendered HTML with `id`-based rows and has been stable for over a decade — the selectors below are written directly against that known-stable structure, which is a materially different confidence level than Task 6 (Product Hunt), whose markup is an unstable-classname SPA.

- [ ] **Step 1: Add the Hacker News platform entry**

In `internal/connections/registry.go`, add after the `"tiktok"` entry (before `"gemini"`):

```go
	"hackernews": {
		ID:         "hackernews",
		Name:       "Hacker News",
		Category:   "social",
		ConnectVia: "UI",
		Methods:    []AuthMethod{MethodBrowser},
		Fields:     map[AuthMethod][]CredentialField{},
		IconEmoji:  "🔶",
	},
```

- [ ] **Step 2: Wire Hacker News into the CLI's browser-node dispatch tables**

In `cmd/monoagentcli/node.go`:

Modify `isBrowserNodeType` (around line 54):

```go
func isBrowserNodeType(t string) bool {
	return strings.HasPrefix(t, "instagram.") || strings.HasPrefix(t, "linkedin.") ||
		strings.HasPrefix(t, "x.") || strings.HasPrefix(t, "tiktok.") ||
		strings.HasPrefix(t, "gemini.") || strings.HasPrefix(t, "hackernews.") ||
		strings.HasPrefix(t, "producthunt.")
}
```

Modify the `platformDomains` map (around line 179):

```go
	platformDomains := map[string]string{
		"gemini":      "https://gemini.google.com/app",
		"instagram":   "https://www.instagram.com",
		"linkedin":    "https://www.linkedin.com",
		"x":           "https://x.com",
		"tiktok":      "https://www.tiktok.com",
		"hackernews":  "https://news.ycombinator.com",
		"producthunt": "https://www.producthunt.com",
	}
```

Modify `nodeCategory` (around line 915):

```go
	case strings.HasPrefix(t, "instagram."), strings.HasPrefix(t, "linkedin."), strings.HasPrefix(t, "x."), strings.HasPrefix(t, "tiktok."), strings.HasPrefix(t, "hackernews."), strings.HasPrefix(t, "producthunt."):
		return "browser/social"
```

(Product Hunt's own entries are added in Task 6 — both channels' node.go edits land in the same three spots, so make both channels' edits to these three call sites in Task 5 to avoid touching the same lines twice; Task 6 then only adds `data/actions/producthunt/*.json` + `internal/bot/producthunt/*.go` + its own connections-registry entry.)

- [ ] **Step 3: Write the failing test for pure-logic helpers**

Create `internal/bot/hackernews/bot_test.go`:

```go
package hackernews

import "testing"

func TestExtractItemID(t *testing.T) {
	cases := map[string]string{
		"https://news.ycombinator.com/item?id=12345":       "12345",
		"https://news.ycombinator.com/item?id=12345&p=2":   "12345",
		"item?id=999":                                      "999",
		"https://news.ycombinator.com/newest":               "",
	}
	b := &HackerNewsBot{}
	for in, want := range cases {
		if got := b.ExtractUsername(in); got != "" {
			t.Errorf("ExtractUsername(%q) should be empty for non-profile URLs, got %q", in, got)
		}
		if got := extractItemID(in); got != want {
			t.Errorf("extractItemID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHackerNewsResolveURL(t *testing.T) {
	b := &HackerNewsBot{}
	if got := b.ResolveURL("/item?id=1"); got != "https://news.ycombinator.com/item?id=1" {
		t.Errorf("ResolveURL relative = %q", got)
	}
	if got := b.ResolveURL("https://news.ycombinator.com/item?id=1"); got != "https://news.ycombinator.com/item?id=1" {
		t.Errorf("ResolveURL absolute changed unexpectedly: %q", got)
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/bot/hackernews/... -v`
Expected: FAIL with `no such file or directory` / `undefined: HackerNewsBot` (package doesn't exist yet)

- [ ] **Step 5: Implement `internal/bot/hackernews/bot.go`**

```go
package hackernews

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	botpkg "monoagent/internal/bot"
	"monoagent/internal/browser"
)

// HackerNewsBot implements botpkg.BotAdapter for Hacker News.
type HackerNewsBot struct{}

func init() {
	botpkg.PlatformRegistry["HACKERNEWS"] = func() botpkg.BotAdapter {
		return &HackerNewsBot{}
	}
}

func (b *HackerNewsBot) Platform() string { return "HACKERNEWS" }

func (b *HackerNewsBot) LoginURL() string { return "https://news.ycombinator.com/login" }

// IsLoggedIn checks for the logged-in username link in the top nav bar
// (id="me"), which Hacker News only renders for authenticated sessions.
func (b *HackerNewsBot) IsLoggedIn(p browser.PageInterface) (bool, error) {
	has, err := p.Has("#me")
	if err != nil {
		return false, nil
	}
	return has, nil
}

// ResolveURL converts a relative Hacker News URL to an absolute URL.
func (b *HackerNewsBot) ResolveURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "/") {
		return "https://news.ycombinator.com" + rawURL
	}
	if !strings.HasPrefix(rawURL, "http") {
		return "https://news.ycombinator.com/" + rawURL
	}
	return rawURL
}

// ExtractUsername is not meaningful for Hacker News item URLs (Hacker News
// profile URLs are "user?id=<username>", not what this bot navigates to for
// its actions) — returns "" always, matching the BotAdapter contract for
// platforms where this concept doesn't apply to the automated flows.
func (b *HackerNewsBot) ExtractUsername(pageURL string) string {
	return ""
}

// SearchURL is not supported by this bot (no keyword-search action is in
// scope) — returns the front page.
func (b *HackerNewsBot) SearchURL(keyword string) string {
	return "https://news.ycombinator.com/"
}

// SendMessage is not supported — Hacker News has no direct-messaging feature.
func (b *HackerNewsBot) SendMessage(ctx context.Context, p browser.PageInterface, username, message string) error {
	return fmt.Errorf("hackernews: direct messaging is not supported by this platform")
}

// GetProfileData is not in scope for this bot (no profile-scraping action).
func (b *HackerNewsBot) GetProfileData(ctx context.Context, p browser.PageInterface) (map[string]interface{}, error) {
	return nil, fmt.Errorf("hackernews: profile scraping is not implemented")
}

// extractItemID parses the "id" query parameter from a Hacker News item URL.
func extractItemID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if !strings.Contains(u.Path, "item") {
		return ""
	}
	return u.Query().Get("id")
}

// GetMethodByName returns a dispatchable wrapper for the named Hacker News
// action method, satisfying action.BotAdapter for call_bot_method steps.
func (b *HackerNewsBot) GetMethodByName(name string) (func(ctx context.Context, args ...interface{}) (interface{}, error), bool) {
	switch name {
	case "submit_post":
		return func(ctx context.Context, args ...interface{}) (interface{}, error) {
			if len(args) < 4 {
				return nil, fmt.Errorf("submit_post requires (page, title, url, text)")
			}
			page, ok := args[0].(browser.PageInterface)
			if !ok {
				return nil, fmt.Errorf("submit_post: first arg must be browser.PageInterface")
			}
			title, _ := args[1].(string)
			linkURL, _ := args[2].(string)
			text, _ := args[3].(string)
			return b.SubmitPost(ctx, page, title, linkURL, text)
		}, true

	case "reply_to_comment":
		return func(ctx context.Context, args ...interface{}) (interface{}, error) {
			if len(args) < 3 {
				return nil, fmt.Errorf("reply_to_comment requires (page, itemID, text)")
			}
			page, ok := args[0].(browser.PageInterface)
			if !ok {
				return nil, fmt.Errorf("reply_to_comment: first arg must be browser.PageInterface")
			}
			itemID, _ := args[1].(string)
			text, _ := args[2].(string)
			if err := b.ReplyToComment(ctx, page, itemID, text); err != nil {
				return nil, err
			}
			return map[string]interface{}{"success": true, "itemID": itemID}, nil
		}, true

	case "list_comments":
		return func(ctx context.Context, args ...interface{}) (interface{}, error) {
			if len(args) < 2 {
				return nil, fmt.Errorf("list_comments requires (page, itemID)")
			}
			page, ok := args[0].(browser.PageInterface)
			if !ok {
				return nil, fmt.Errorf("list_comments: first arg must be browser.PageInterface")
			}
			itemID, _ := args[1].(string)
			return b.ListComments(ctx, page, itemID)
		}, true

	case "get_post_metrics":
		return func(ctx context.Context, args ...interface{}) (interface{}, error) {
			if len(args) < 2 {
				return nil, fmt.Errorf("get_post_metrics requires (page, itemID)")
			}
			page, ok := args[0].(browser.PageInterface)
			if !ok {
				return nil, fmt.Errorf("get_post_metrics: first arg must be browser.PageInterface")
			}
			itemID, _ := args[1].(string)
			return b.GetPostMetrics(ctx, page, itemID)
		}, true
	}
	return nil, false
}
```

- [ ] **Step 6: Implement `internal/bot/hackernews/actions.go`**

```go
package hackernews

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"monoagent/internal/browser"
)

// SubmitPost navigates to the Hacker News submit form, fills title/url/text,
// submits, then reads back the newly created item's ID from the user's
// submitted-items page (Hacker News' submit form doesn't redirect straight
// to the new item, so the freshest item on /submitted?id=<user> is used).
func (b *HackerNewsBot) SubmitPost(ctx context.Context, page browser.PageInterface, title, linkURL, text string) (map[string]interface{}, error) {
	if title == "" {
		return nil, fmt.Errorf("hackernews: title is required")
	}
	if err := page.Navigate("https://news.ycombinator.com/submit"); err != nil {
		return nil, fmt.Errorf("hackernews: navigate to submit: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("hackernews: submit page did not load: %w", err)
	}
	time.Sleep(1 * time.Second)

	titleInput, err := page.Element("input[name='title']", 5*time.Second)
	if err != nil || titleInput == nil {
		return nil, fmt.Errorf("hackernews: could not find title input: %w", err)
	}
	if err := titleInput.Input(title); err != nil {
		return nil, fmt.Errorf("hackernews: failed to type title: %w", err)
	}

	if linkURL != "" {
		urlInput, err := page.Element("input[name='url']", 5*time.Second)
		if err != nil || urlInput == nil {
			return nil, fmt.Errorf("hackernews: could not find url input: %w", err)
		}
		if err := urlInput.Input(linkURL); err != nil {
			return nil, fmt.Errorf("hackernews: failed to type url: %w", err)
		}
	} else if text != "" {
		textArea, err := page.Element("textarea[name='text']", 5*time.Second)
		if err != nil || textArea == nil {
			return nil, fmt.Errorf("hackernews: could not find text textarea: %w", err)
		}
		if err := textArea.Input(text); err != nil {
			return nil, fmt.Errorf("hackernews: failed to type text: %w", err)
		}
	}

	submitBtn, err := page.Element("input[type='submit']", 5*time.Second)
	if err != nil || submitBtn == nil {
		return nil, fmt.Errorf("hackernews: could not find submit button: %w", err)
	}
	if err := submitBtn.Click(); err != nil {
		return nil, fmt.Errorf("hackernews: failed to click submit: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("hackernews: page did not load after submit: %w", err)
	}
	time.Sleep(2 * time.Second)

	result, err := page.Eval(`() => {
		const row = document.querySelector('tr.athing');
		if (!row) return JSON.stringify(null);
		const titleLink = row.querySelector('.titleline > a');
		return JSON.stringify({
			id: row.id,
			title: titleLink ? titleLink.textContent : '',
			url: 'https://news.ycombinator.com/item?id=' + row.id,
		});
	}`)
	if err != nil {
		return nil, fmt.Errorf("hackernews: reading submitted item: %w", err)
	}
	var parsed map[string]interface{}
	if unmarshalErr := json.Unmarshal([]byte(result.Str()), &parsed); unmarshalErr != nil {
		return nil, fmt.Errorf("hackernews: parsing submitted item JSON: %w", unmarshalErr)
	}
	return parsed, nil
}

// ReplyToComment navigates to an item's page, clicks the "reply" link for
// the whole thread (item-level reply, at the bottom of the comment list),
// types the given text, and submits.
func (b *HackerNewsBot) ReplyToComment(ctx context.Context, page browser.PageInterface, itemID, text string) error {
	if itemID == "" || text == "" {
		return fmt.Errorf("hackernews: itemID and text are required")
	}
	replyURL := fmt.Sprintf("https://news.ycombinator.com/reply?id=%s&goto=item%%3Fid%%3D%s", itemID, itemID)
	if err := page.Navigate(replyURL); err != nil {
		return fmt.Errorf("hackernews: navigate to reply form: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return fmt.Errorf("hackernews: reply page did not load: %w", err)
	}
	time.Sleep(1 * time.Second)

	textArea, err := page.Element("textarea[name='text']", 5*time.Second)
	if err != nil || textArea == nil {
		return fmt.Errorf("hackernews: could not find reply textarea: %w", err)
	}
	if err := textArea.Input(text); err != nil {
		return fmt.Errorf("hackernews: failed to type reply: %w", err)
	}

	submitBtn, err := page.Element("input[type='submit']", 5*time.Second)
	if err != nil || submitBtn == nil {
		return fmt.Errorf("hackernews: could not find reply submit button: %w", err)
	}
	if err := submitBtn.Click(); err != nil {
		return fmt.Errorf("hackernews: failed to click reply submit: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return fmt.Errorf("hackernews: page did not load after reply: %w", err)
	}
	return nil
}

// ListComments navigates to an item's page and returns its top-level
// comments (id, author, text) by walking the comment table's rows.
func (b *HackerNewsBot) ListComments(ctx context.Context, page browser.PageInterface, itemID string) ([]map[string]interface{}, error) {
	if itemID == "" {
		return nil, fmt.Errorf("hackernews: itemID is required")
	}
	itemURL := fmt.Sprintf("https://news.ycombinator.com/item?id=%s", itemID)
	if err := page.Navigate(itemURL); err != nil {
		return nil, fmt.Errorf("hackernews: navigate to item: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("hackernews: item page did not load: %w", err)
	}
	time.Sleep(1 * time.Second)

	result, err := page.Eval(`() => {
		const rows = document.querySelectorAll('tr.athing.comtr');
		return JSON.stringify(Array.from(rows).map(row => {
			const author = row.querySelector('a.hnuser');
			const body = row.querySelector('.commtext');
			return {
				id: row.id,
				author: author ? author.textContent : '',
				text: body ? body.textContent : '',
			};
		}));
	}`)
	if err != nil {
		return nil, fmt.Errorf("hackernews: reading comments: %w", err)
	}
	var comments []map[string]interface{}
	if unmarshalErr := json.Unmarshal([]byte(result.Str()), &comments); unmarshalErr != nil {
		return nil, fmt.Errorf("hackernews: parsing comments JSON: %w", unmarshalErr)
	}
	return comments, nil
}

// GetPostMetrics navigates to an item's page and reads its point score and
// comment count from the subtext line under the title.
func (b *HackerNewsBot) GetPostMetrics(ctx context.Context, page browser.PageInterface, itemID string) (map[string]interface{}, error) {
	if itemID == "" {
		return nil, fmt.Errorf("hackernews: itemID is required")
	}
	itemURL := fmt.Sprintf("https://news.ycombinator.com/item?id=%s", itemID)
	if err := page.Navigate(itemURL); err != nil {
		return nil, fmt.Errorf("hackernews: navigate to item: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("hackernews: item page did not load: %w", err)
	}
	time.Sleep(1 * time.Second)

	result, err := page.Eval(fmt.Sprintf(`() => {
		const scoreEl = document.querySelector('#score_%s');
		const commentLink = Array.from(document.querySelectorAll('.subtext a'))
			.find(a => a.textContent.includes('comment'));
		return JSON.stringify({
			points: scoreEl ? parseInt(scoreEl.textContent) || 0 : 0,
			comments: commentLink ? parseInt(commentLink.textContent) || 0 : 0,
		});
	}`, itemID))
	if err != nil {
		return nil, fmt.Errorf("hackernews: reading metrics: %w", err)
	}
	var metrics map[string]interface{}
	if unmarshalErr := json.Unmarshal([]byte(result.Str()), &metrics); unmarshalErr != nil {
		return nil, fmt.Errorf("hackernews: parsing metrics JSON: %w", unmarshalErr)
	}
	return metrics, nil
}
```

- [ ] **Step 7: Run the test to verify it passes**

Run: `go test ./internal/bot/hackernews/... -v`
Expected: PASS

- [ ] **Step 8: Add the four JSON action-step templates**

Each follows `data/actions/instagram/follow_users.json`'s three-tier shape but with only Tier 1 (`call_bot_method`) since Hacker News' bot methods above are the primary and only implemented tier for these four actions — Tier 2/3 fallback selectors can be added later if Tier 1 selectors drift, following the same file shape.

Create `data/actions/hackernews/submit_post.json`:

```json
{
  "actionType": "submit_post",
  "platform": "HACKERNEWS",
  "version": "1.0.0",
  "description": "Submit a new post (link or text/Show HN) to Hacker News",
  "metadata": { "requiresAuth": true, "supportsPagination": false, "supportsRetry": false },
  "inputs": {
    "required": [
      { "name": "title", "type": "string", "description": "Post title" }
    ],
    "optional": [
      { "name": "url", "type": "string", "description": "Link URL (mutually exclusive with text)" },
      { "name": "text", "type": "string", "description": "Self-post body (Show HN style)" }
    ]
  },
  "outputs": {
    "success": ["id", "title", "url"],
    "failure": ["error"]
  },
  "steps": [
    {
      "id": "submit",
      "type": "call_bot_method",
      "methodName": "submit_post",
      "args": ["{{title}}", "{{url}}", "{{text}}"],
      "variable_name": "submitResult",
      "timeout": 30,
      "onError": { "action": "mark_failed" }
    }
  ],
  "loops": [],
  "errorHandling": { "globalRetries": 1, "retryDelay": 2000, "onFinalFailure": "log_and_continue" }
}
```

Create `data/actions/hackernews/reply_to_comment.json`:

```json
{
  "actionType": "reply_to_comment",
  "platform": "HACKERNEWS",
  "version": "1.0.0",
  "description": "Reply to a Hacker News item or comment thread",
  "metadata": { "requiresAuth": true, "supportsPagination": false, "supportsRetry": false },
  "inputs": {
    "required": [
      { "name": "itemID", "type": "string", "description": "Item or comment ID to reply to" },
      { "name": "text", "type": "string", "description": "Reply text" }
    ],
    "optional": []
  },
  "outputs": {
    "success": ["success", "itemID"],
    "failure": ["error"]
  },
  "steps": [
    {
      "id": "reply",
      "type": "call_bot_method",
      "methodName": "reply_to_comment",
      "args": ["{{itemID}}", "{{text}}"],
      "variable_name": "replyResult",
      "timeout": 30,
      "onError": { "action": "mark_failed" }
    }
  ],
  "loops": [],
  "errorHandling": { "globalRetries": 1, "retryDelay": 2000, "onFinalFailure": "log_and_continue" }
}
```

Create `data/actions/hackernews/list_comments.json`:

```json
{
  "actionType": "list_comments",
  "platform": "HACKERNEWS",
  "version": "1.0.0",
  "description": "List the top-level comments on a Hacker News item",
  "metadata": { "requiresAuth": false, "supportsPagination": false, "supportsRetry": true },
  "inputs": {
    "required": [
      { "name": "itemID", "type": "string", "description": "Item ID to read comments from" }
    ],
    "optional": []
  },
  "outputs": {
    "success": ["comments"],
    "failure": ["error"]
  },
  "steps": [
    {
      "id": "list",
      "type": "call_bot_method",
      "methodName": "list_comments",
      "args": ["{{itemID}}"],
      "variable_name": "commentsResult",
      "timeout": 30,
      "onError": { "action": "mark_failed" }
    }
  ],
  "loops": [],
  "errorHandling": { "globalRetries": 2, "retryDelay": 2000, "onFinalFailure": "log_and_continue" }
}
```

Create `data/actions/hackernews/get_post_metrics.json`:

```json
{
  "actionType": "get_post_metrics",
  "platform": "HACKERNEWS",
  "version": "1.0.0",
  "description": "Read the point score and comment count of a Hacker News item",
  "metadata": { "requiresAuth": false, "supportsPagination": false, "supportsRetry": true },
  "inputs": {
    "required": [
      { "name": "itemID", "type": "string", "description": "Item ID to read metrics for" }
    ],
    "optional": []
  },
  "outputs": {
    "success": ["points", "comments"],
    "failure": ["error"]
  },
  "steps": [
    {
      "id": "metrics",
      "type": "call_bot_method",
      "methodName": "get_post_metrics",
      "args": ["{{itemID}}"],
      "variable_name": "metricsResult",
      "timeout": 30,
      "onError": { "action": "mark_failed" }
    }
  ],
  "loops": [],
  "errorHandling": { "globalRetries": 2, "retryDelay": 2000, "onFinalFailure": "log_and_continue" }
}
```

- [ ] **Step 9: Build and verify the node types auto-register**

Run: `go build ./... && go test ./internal/bot/hackernews/... -v`
Expected: build succeeds, tests PASS

Run: `go run ./cmd/monoagentcli node list 2>/dev/null | grep hackernews` (or the equivalent `list` subcommand this CLI exposes — check with `go run ./cmd/monoagentcli node --help` first if `list` isn't the right subcommand name)
Expected: `hackernews.submit_post`, `hackernews.reply_to_comment`, `hackernews.list_comments`, `hackernews.get_post_metrics` all appear

- [ ] **Step 10: Commit**

```bash
git add internal/bot/hackernews/ data/actions/hackernews/ cmd/monoagentcli/node.go internal/connections/registry.go
git commit -m "feat: add Hacker News browser bot (submit, reply, list comments, metrics)"
```

---

### Task 6: Product Hunt — browser bot

**Files:**
- Create: `internal/bot/producthunt/bot.go`
- Create: `internal/bot/producthunt/actions.go`
- Create: `internal/bot/producthunt/bot_test.go`
- Create: `data/actions/producthunt/comment_on_launch.json`
- Create: `data/actions/producthunt/list_comments.json`
- Create: `data/actions/producthunt/get_launch_metrics.json`
- Modify: `internal/connections/registry.go`

Product Hunt's node.go wiring (`isBrowserNodeType`, `platformDomains`, `nodeCategory`) was already added in Task 5, Step 2 — this task only adds the bot package, action templates, and connections entry.

Product Hunt is a modern React/Next.js SPA with hashed CSS-module classnames — unlike Hacker News, there is no long-term-stable class-name structure to select against confidently. This bot leans on accessible/semantic attributes (`aria-label`, visible button text via XPath) instead of classnames, and is expected to rely more heavily on the three-tier fallback's Tier 3 (AI-assisted `configKey` resolution, already built into `internal/action`) than Hacker News does. Launch *submission* is intentionally not automated (per the design spec) — only comment engagement and metrics reading, which don't require navigating Product Hunt's submission wizard.

**Interfaces:**
- Produces: `ProductHuntBot` implementing `bot.BotAdapter` (`Platform()` → `"PRODUCTHUNT"`) and `action.BotAdapter`. Node types `producthunt.comment_on_launch`, `producthunt.list_comments`, `producthunt.get_launch_metrics`.

- [ ] **Step 1: Add the Product Hunt platform entry**

In `internal/connections/registry.go`, add after the `"hackernews"` entry from Task 5:

```go
	"producthunt": {
		ID:         "producthunt",
		Name:       "Product Hunt",
		Category:   "social",
		ConnectVia: "UI",
		Methods:    []AuthMethod{MethodBrowser},
		Fields:     map[AuthMethod][]CredentialField{},
		IconEmoji:  "🐱",
	},
```

- [ ] **Step 2: Write the failing test**

Create `internal/bot/producthunt/bot_test.go`:

```go
package producthunt

import "testing"

func TestProductHuntResolveURL(t *testing.T) {
	b := &ProductHuntBot{}
	if got := b.ResolveURL("/posts/monomind"); got != "https://www.producthunt.com/posts/monomind" {
		t.Errorf("ResolveURL relative = %q", got)
	}
	if got := b.ResolveURL("https://www.producthunt.com/posts/monomind"); got != "https://www.producthunt.com/posts/monomind" {
		t.Errorf("ResolveURL absolute changed unexpectedly: %q", got)
	}
}

func TestProductHuntPlatform(t *testing.T) {
	b := &ProductHuntBot{}
	if b.Platform() != "PRODUCTHUNT" {
		t.Errorf("Platform() = %q, want PRODUCTHUNT", b.Platform())
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/bot/producthunt/... -v`
Expected: FAIL — package doesn't exist yet

- [ ] **Step 4: Implement `internal/bot/producthunt/bot.go`**

```go
package producthunt

import (
	"context"
	"fmt"
	"strings"

	botpkg "monoagent/internal/bot"
	"monoagent/internal/browser"
)

// ProductHuntBot implements botpkg.BotAdapter for Product Hunt.
type ProductHuntBot struct{}

func init() {
	botpkg.PlatformRegistry["PRODUCTHUNT"] = func() botpkg.BotAdapter {
		return &ProductHuntBot{}
	}
}

func (b *ProductHuntBot) Platform() string { return "PRODUCTHUNT" }

func (b *ProductHuntBot) LoginURL() string { return "https://www.producthunt.com/login" }

// IsLoggedIn checks for the user avatar menu button, which Product Hunt
// only renders in the header nav for authenticated sessions.
func (b *ProductHuntBot) IsLoggedIn(p browser.PageInterface) (bool, error) {
	has, err := p.Has("[aria-label='User menu']")
	if err != nil {
		return false, nil
	}
	return has, nil
}

// ResolveURL converts a relative Product Hunt URL to an absolute URL.
func (b *ProductHuntBot) ResolveURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "/") {
		return "https://www.producthunt.com" + rawURL
	}
	if !strings.HasPrefix(rawURL, "http") {
		return "https://www.producthunt.com/" + rawURL
	}
	return rawURL
}

// ExtractUsername is not meaningful for the launch-page URLs this bot
// navigates to — returns "" always.
func (b *ProductHuntBot) ExtractUsername(pageURL string) string {
	return ""
}

// SearchURL is not supported by this bot (no keyword-search action is in scope).
func (b *ProductHuntBot) SearchURL(keyword string) string {
	return "https://www.producthunt.com/"
}

// SendMessage is not supported — this bot only comments on launches.
func (b *ProductHuntBot) SendMessage(ctx context.Context, p browser.PageInterface, username, message string) error {
	return fmt.Errorf("producthunt: direct messaging is not supported by this bot")
}

// GetProfileData is not in scope for this bot (no profile-scraping action).
func (b *ProductHuntBot) GetProfileData(ctx context.Context, p browser.PageInterface) (map[string]interface{}, error) {
	return nil, fmt.Errorf("producthunt: profile scraping is not implemented")
}

// GetMethodByName returns a dispatchable wrapper for the named Product Hunt
// action method, satisfying action.BotAdapter for call_bot_method steps.
func (b *ProductHuntBot) GetMethodByName(name string) (func(ctx context.Context, args ...interface{}) (interface{}, error), bool) {
	switch name {
	case "comment_on_launch":
		return func(ctx context.Context, args ...interface{}) (interface{}, error) {
			if len(args) < 3 {
				return nil, fmt.Errorf("comment_on_launch requires (page, launchURL, text)")
			}
			page, ok := args[0].(browser.PageInterface)
			if !ok {
				return nil, fmt.Errorf("comment_on_launch: first arg must be browser.PageInterface")
			}
			launchURL, _ := args[1].(string)
			text, _ := args[2].(string)
			if err := b.CommentOnLaunch(ctx, page, launchURL, text); err != nil {
				return nil, err
			}
			return map[string]interface{}{"success": true, "launchURL": launchURL}, nil
		}, true

	case "list_comments":
		return func(ctx context.Context, args ...interface{}) (interface{}, error) {
			if len(args) < 2 {
				return nil, fmt.Errorf("list_comments requires (page, launchURL)")
			}
			page, ok := args[0].(browser.PageInterface)
			if !ok {
				return nil, fmt.Errorf("list_comments: first arg must be browser.PageInterface")
			}
			launchURL, _ := args[1].(string)
			return b.ListComments(ctx, page, launchURL)
		}, true

	case "get_launch_metrics":
		return func(ctx context.Context, args ...interface{}) (interface{}, error) {
			if len(args) < 2 {
				return nil, fmt.Errorf("get_launch_metrics requires (page, launchURL)")
			}
			page, ok := args[0].(browser.PageInterface)
			if !ok {
				return nil, fmt.Errorf("get_launch_metrics: first arg must be browser.PageInterface")
			}
			launchURL, _ := args[1].(string)
			return b.GetLaunchMetrics(ctx, page, launchURL)
		}, true
	}
	return nil, false
}
```

- [ ] **Step 5: Implement `internal/bot/producthunt/actions.go`**

Selectors here use visible-text XPath and ARIA attributes rather than CSS classnames, since Product Hunt's classnames are hashed/unstable across deploys.

```go
package producthunt

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"monoagent/internal/browser"
)

// CommentOnLaunch navigates to a launch page, finds the comment composer by
// its placeholder text, types the comment, and submits it.
func (b *ProductHuntBot) CommentOnLaunch(ctx context.Context, page browser.PageInterface, launchURL, text string) error {
	if launchURL == "" || text == "" {
		return fmt.Errorf("producthunt: launchURL and text are required")
	}
	if err := page.Navigate(launchURL); err != nil {
		return fmt.Errorf("producthunt: navigate to launch: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return fmt.Errorf("producthunt: launch page did not load: %w", err)
	}
	time.Sleep(3 * time.Second)

	composer, err := page.Element("textarea[placeholder*='comment' i]", 8*time.Second)
	if err != nil || composer == nil {
		return fmt.Errorf("producthunt: could not find comment composer: %w", err)
	}
	if err := composer.Click(); err != nil {
		return fmt.Errorf("producthunt: failed to focus comment composer: %w", err)
	}
	if err := composer.Input(text); err != nil {
		return fmt.Errorf("producthunt: failed to type comment: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	submitBtn, err := page.ElementX("//button[contains(., 'Comment')]", 5*time.Second)
	if err != nil || submitBtn == nil {
		return fmt.Errorf("producthunt: could not find comment submit button: %w", err)
	}
	if err := submitBtn.Click(); err != nil {
		return fmt.Errorf("producthunt: failed to click comment submit: %w", err)
	}
	time.Sleep(1 * time.Second)
	return nil
}

// ListComments navigates to a launch page and returns its visible comments
// (author, text) by walking comment container elements identified by their
// data-test attribute, which Product Hunt keeps stable for automated-testing
// purposes even though visual classnames are hashed.
func (b *ProductHuntBot) ListComments(ctx context.Context, page browser.PageInterface, launchURL string) ([]map[string]interface{}, error) {
	if launchURL == "" {
		return nil, fmt.Errorf("producthunt: launchURL is required")
	}
	if err := page.Navigate(launchURL); err != nil {
		return nil, fmt.Errorf("producthunt: navigate to launch: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("producthunt: launch page did not load: %w", err)
	}
	time.Sleep(3 * time.Second)

	result, err := page.Eval(`() => {
		const items = document.querySelectorAll("[data-test*='comment']");
		return JSON.stringify(Array.from(items).map(el => {
			const authorEl = el.querySelector("a[href^='/@']");
			return {
				author: authorEl ? authorEl.textContent.trim() : '',
				text: el.textContent ? el.textContent.trim() : '',
			};
		}).filter(c => c.text !== ''));
	}`)
	if err != nil {
		return nil, fmt.Errorf("producthunt: reading comments: %w", err)
	}
	var comments []map[string]interface{}
	if unmarshalErr := json.Unmarshal([]byte(result.Str()), &comments); unmarshalErr != nil {
		return nil, fmt.Errorf("producthunt: parsing comments JSON: %w", unmarshalErr)
	}
	return comments, nil
}

// GetLaunchMetrics navigates to a launch page and reads its upvote count
// (via the vote button's accessible label, e.g. "Upvote (142)") and comment
// count (via the "Comments" section heading, e.g. "Comments (12)").
func (b *ProductHuntBot) GetLaunchMetrics(ctx context.Context, page browser.PageInterface, launchURL string) (map[string]interface{}, error) {
	if launchURL == "" {
		return nil, fmt.Errorf("producthunt: launchURL is required")
	}
	if err := page.Navigate(launchURL); err != nil {
		return nil, fmt.Errorf("producthunt: navigate to launch: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return nil, fmt.Errorf("producthunt: launch page did not load: %w", err)
	}
	time.Sleep(3 * time.Second)

	result, err := page.Eval(`() => {
		const voteBtn = document.querySelector("[aria-label*='pvote' i]");
		const voteMatch = voteBtn ? voteBtn.getAttribute('aria-label').match(/\d+/) : null;
		const commentHeading = Array.from(document.querySelectorAll('h2, h3'))
			.find(h => h.textContent.toLowerCase().includes('comment'));
		const commentMatch = commentHeading ? commentHeading.textContent.match(/\d+/) : null;
		return JSON.stringify({
			upvotes: voteMatch ? parseInt(voteMatch[0]) : 0,
			comments: commentMatch ? parseInt(commentMatch[0]) : 0,
		});
	}`)
	if err != nil {
		return nil, fmt.Errorf("producthunt: reading metrics: %w", err)
	}
	var metrics map[string]interface{}
	if unmarshalErr := json.Unmarshal([]byte(result.Str()), &metrics); unmarshalErr != nil {
		return nil, fmt.Errorf("producthunt: parsing metrics JSON: %w", unmarshalErr)
	}
	return metrics, nil
}
```

This uses `page.ElementX` (XPath) for the comment-submit button since Product Hunt's button has no stable `name`/`data-test` attribute to target with a CSS selector — `ElementX` is defined on `browser.PageInterface` (`internal/browser/interfaces.go`) alongside `Element` (CSS selector) and both return `(ElementHandle, error)`.

- [ ] **Step 6: Run the test to verify it passes**

Run: `goimports -w internal/bot/producthunt/*.go && go test ./internal/bot/producthunt/... -v`
Expected: PASS

- [ ] **Step 7: Add the three JSON action-step templates**

Create `data/actions/producthunt/comment_on_launch.json`:

```json
{
  "actionType": "comment_on_launch",
  "platform": "PRODUCTHUNT",
  "version": "1.0.0",
  "description": "Post a comment on a Product Hunt launch page",
  "metadata": { "requiresAuth": true, "supportsPagination": false, "supportsRetry": false },
  "inputs": {
    "required": [
      { "name": "launchURL", "type": "string", "description": "Product Hunt launch page URL" },
      { "name": "text", "type": "string", "description": "Comment text" }
    ],
    "optional": []
  },
  "outputs": {
    "success": ["success", "launchURL"],
    "failure": ["error"]
  },
  "steps": [
    {
      "id": "comment",
      "type": "call_bot_method",
      "methodName": "comment_on_launch",
      "args": ["{{launchURL}}", "{{text}}"],
      "variable_name": "commentResult",
      "timeout": 30,
      "onError": { "action": "mark_failed" }
    }
  ],
  "loops": [],
  "errorHandling": { "globalRetries": 1, "retryDelay": 2000, "onFinalFailure": "log_and_continue" }
}
```

Create `data/actions/producthunt/list_comments.json`:

```json
{
  "actionType": "list_comments",
  "platform": "PRODUCTHUNT",
  "version": "1.0.0",
  "description": "List the visible comments on a Product Hunt launch page",
  "metadata": { "requiresAuth": false, "supportsPagination": false, "supportsRetry": true },
  "inputs": {
    "required": [
      { "name": "launchURL", "type": "string", "description": "Product Hunt launch page URL" }
    ],
    "optional": []
  },
  "outputs": {
    "success": ["comments"],
    "failure": ["error"]
  },
  "steps": [
    {
      "id": "list",
      "type": "call_bot_method",
      "methodName": "list_comments",
      "args": ["{{launchURL}}"],
      "variable_name": "commentsResult",
      "timeout": 30,
      "onError": { "action": "mark_failed" }
    }
  ],
  "loops": [],
  "errorHandling": { "globalRetries": 2, "retryDelay": 2000, "onFinalFailure": "log_and_continue" }
}
```

Create `data/actions/producthunt/get_launch_metrics.json`:

```json
{
  "actionType": "get_launch_metrics",
  "platform": "PRODUCTHUNT",
  "version": "1.0.0",
  "description": "Read the upvote and comment count of a Product Hunt launch page",
  "metadata": { "requiresAuth": false, "supportsPagination": false, "supportsRetry": true },
  "inputs": {
    "required": [
      { "name": "launchURL", "type": "string", "description": "Product Hunt launch page URL" }
    ],
    "optional": []
  },
  "outputs": {
    "success": ["upvotes", "comments"],
    "failure": ["error"]
  },
  "steps": [
    {
      "id": "metrics",
      "type": "call_bot_method",
      "methodName": "get_launch_metrics",
      "args": ["{{launchURL}}"],
      "variable_name": "metricsResult",
      "timeout": 30,
      "onError": { "action": "mark_failed" }
    }
  ],
  "loops": [],
  "errorHandling": { "globalRetries": 2, "retryDelay": 2000, "onFinalFailure": "log_and_continue" }
}
```

- [ ] **Step 8: Build and verify the node types auto-register**

Run: `go build ./... && go test ./internal/bot/producthunt/... -v`
Expected: build succeeds, tests PASS

Run the same `node list`-equivalent check from Task 5 Step 9, filtered on `producthunt` this time.
Expected: `producthunt.comment_on_launch`, `producthunt.list_comments`, `producthunt.get_launch_metrics` all appear

- [ ] **Step 9: Commit**

```bash
git add internal/bot/producthunt/ data/actions/producthunt/ internal/connections/registry.go
git commit -m "feat: add Product Hunt browser bot (comment, list comments, metrics)"
```

---

## Final Verification

- [ ] **Run the full test suite and vet**

Run: `go build ./... && go vet ./... && go test ./... 2>&1 | tail -60`
Expected: build succeeds, vet is clean, all tests PASS (pre-existing unrelated test failures, if any, are not this plan's responsibility to fix)

- [ ] **Update `data/actions/README.md`'s directory tree**

Add `hackernews/` and `producthunt/` entries to the directory structure listing (around line 10-24) so the README stays accurate, following the same one-line-per-action-file style already used for `instagram/`/`linkedin/`/`tiktok/`/`x/`.
