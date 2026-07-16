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
