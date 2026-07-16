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
