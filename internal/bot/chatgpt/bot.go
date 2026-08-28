// Package chatgpt drives chatgpt.com through the user's own logged-in Chrome
// session, the same way the gemini bot drives gemini.google.com.
//
// No API key is involved: the session comes from `monoagentcli login chatgpt`.
// The heavy lifting lives in data/actions/chatgpt/*.json — this adapter only
// supplies the login URL, the logged-in check, and the response-extraction
// helper that the action's call_bot_method step dispatches to.
package chatgpt

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	botpkg "monoagent/internal/bot"
	"monoagent/internal/browser"
	"monoagent/internal/extension"
)

// ChatGPTBot implements botpkg.BotAdapter for chatgpt.com.
type ChatGPTBot struct{}

func init() {
	botpkg.PlatformRegistry["CHATGPT"] = func() botpkg.BotAdapter {
		return &ChatGPTBot{}
	}
}

func (b *ChatGPTBot) Platform() string { return "CHATGPT" }

func (b *ChatGPTBot) LoginURL() string { return "https://chatgpt.com/" }

// IsLoggedIn looks for the composer, which chatgpt.com only renders once a
// session exists. A logged-out visit shows the marketing/login wall instead.
func (b *ChatGPTBot) IsLoggedIn(p browser.PageInterface) (bool, error) {
	for _, sel := range []string{
		"#prompt-textarea",
		"div[contenteditable='true'][id='prompt-textarea']",
		"form textarea",
	} {
		if has, err := p.Has(sel); err == nil && has {
			return true, nil
		}
	}
	return false, nil
}

func (b *ChatGPTBot) ResolveURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "/") {
		return "https://chatgpt.com" + rawURL
	}
	if !strings.HasPrefix(rawURL, "http") {
		return "https://chatgpt.com/" + rawURL
	}
	return rawURL
}

// ExtractUsername is not meaningful for a chat surface.
func (b *ChatGPTBot) ExtractUsername(pageURL string) string { return "" }

func (b *ChatGPTBot) SearchURL(keyword string) string { return "https://chatgpt.com/" }

func (b *ChatGPTBot) SendMessage(ctx context.Context, p browser.PageInterface, username, message string) error {
	return fmt.Errorf("chatgpt: direct messaging is not supported by this bot")
}

func (b *ChatGPTBot) GetProfileData(ctx context.Context, p browser.PageInterface) (map[string]interface{}, error) {
	return nil, fmt.Errorf("chatgpt: profile scraping is not implemented")
}

// isReasoningPlaceholder reports whether text is one of ChatGPT's pre-answer
// states rather than an actual reply.
//
// These hold perfectly steady across polls, so the "text stopped changing"
// stability check accepts them as a finished answer. On 2026-08-20 that
// published the literal 8-character post "Thinking", and shorter variants of
// the same race truncated several other posts mid-sentence.
func isReasoningPlaceholder(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" {
		return true
	}
	if strings.HasPrefix(t, "thought for ") || strings.HasPrefix(t, "reasoned for ") {
		return true
	}
	for _, p := range []string{
		"thinking", "reasoning", "searching", "analyzing", "analysing",
		"working on it", "thinking...", "let me think",
	} {
		if t == p || t == p+"…" || t == p+"..." {
			return true
		}
	}
	return false
}

// waitForResponse blocks until the assistant has finished streaming.
//
// Reading the DOM the moment a reply appears returns a half-written answer —
// the same failure that produced "Refining the MessageGemini said" from the
// gemini path. Completion is detected by the stop button disappearing AND the
// text going quiet for two consecutive polls.
func (b *ChatGPTBot) waitForResponse(p browser.PageInterface, maxWait time.Duration) (string, error) {
	deadline := time.Now().Add(maxWait)
	var last string
	stable := 0

	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)

		streaming := false
		for _, sel := range []string{
			"button[data-testid='stop-button']",
			"button[aria-label='Stop streaming']",
			"button[aria-label*='Stop' i]",
		} {
			if has, err := p.Has(sel); err == nil && has {
				streaming = true
				break
			}
		}
		if streaming {
			stable = 0
			continue
		}

		text, err := b.extractLast(p)
		if err != nil || text == "" {
			continue
		}
		// A reasoning placeholder is not an answer, however stable it looks.
		if isReasoningPlaceholder(text) {
			stable = 0
			last = text
			continue
		}
		if text == last {
			stable++
			if stable >= 2 {
				return text, nil
			}
		} else {
			stable = 0
			last = text
		}
	}
	if last != "" && !isReasoningPlaceholder(last) {
		return last, nil
	}
	return "", fmt.Errorf("chatgpt: still %q after %s, no answer produced", last, maxWait)
}

// extractLast returns the text of the most recent assistant turn.
//
// chatgpt.com's CSP blocks the content-script eval path, which returns nil
// there. EvalCDP runs in the page's main world via the debugger and is the
// only route that works — the gemini bot does the same.
func (b *ChatGPTBot) extractLast(p browser.PageInterface) (string, error) {
	js := `(() => {
	  const nodes = document.querySelectorAll('[data-message-author-role="assistant"]');
	  if (!nodes.length) return "";
	  const el = nodes[nodes.length - 1];
	  const body = el.querySelector('.markdown') || el;
	  return (body.innerText || body.textContent || "").trim();
	})()`

	if ep, ok := p.(*extension.ExtensionPage); ok {
		raw, err := ep.EvalCDP(js)
		if err != nil {
			return "", err
		}
		return cleanEval(raw), nil
	}

	res, err := p.Eval(js)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", nil
	}
	return cleanEval(res.Raw()), nil
}

// cleanEval normalises an eval result to a string, treating the nil/"<nil>"
// cases as empty rather than letting them reach a post as literal text.
func cleanEval(raw interface{}) string {
	if raw == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprintf("%v", raw))
	if s == "<nil>" || s == "null" || s == "undefined" {
		return ""
	}
	return s
}

// waitForImage blocks until ChatGPT has finished rendering a generated image.
//
// Image generation streams a low-res preview first, so reading too early yields
// a blurry placeholder. Completion is taken as: the stop button gone, at least
// one image present in the last assistant turn, and the image count steady
// across two polls.
func (b *ChatGPTBot) waitForImage(p browser.PageInterface, maxWait time.Duration) (int, error) {
	deadline := time.Now().Add(maxWait)
	lastCount, stable := 0, 0

	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)

		streaming := false
		for _, sel := range []string{
			"button[data-testid='stop-button']",
			"button[aria-label*='Stop' i]",
		} {
			if has, err := p.Has(sel); err == nil && has {
				streaming = true
				break
			}
		}
		if streaming {
			stable = 0
			continue
		}

		count := b.countImages(p)
		if count > 0 && count == lastCount {
			stable++
			if stable >= 2 {
				return count, nil
			}
		} else {
			stable = 0
			lastCount = count
		}
	}
	if lastCount > 0 {
		return lastCount, nil
	}
	return 0, fmt.Errorf("chatgpt: no image within %s", maxWait)
}

func (b *ChatGPTBot) countImages(p browser.PageInterface) int {
	js := `(() => {
	  const turns = document.querySelectorAll('[data-message-author-role="assistant"]');
	  if (!turns.length) return 0;
	  const last = turns[turns.length - 1];
	  return last.querySelectorAll('img').length;
	})()`
	if ep, ok := p.(*extension.ExtensionPage); ok {
		raw, err := ep.EvalCDP(js)
		if err != nil {
			return 0
		}
		var n int
		if _, err := fmt.Sscanf(cleanEval(raw), "%d", &n); err != nil {
			return 0
		}
		return n
	}
	return 0
}

// downloadImages pulls the images out of the last assistant turn and writes
// them to disk, returning their paths.
func (b *ChatGPTBot) downloadImages(p browser.PageInterface, dir string) ([]map[string]interface{}, error) {
	ep, ok := p.(*extension.ExtensionPage)
	if !ok {
		return nil, fmt.Errorf("chatgpt: image download requires the extension path")
	}

	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".monoagent", "downloads")
	}
	if strings.HasPrefix(dir, "~/") {
		dir = filepath.Join(os.Getenv("HOME"), dir[2:])
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("chatgpt: create download dir: %w", err)
	}

	// A generated image is served from ChatGPT's own content endpoint, so match
	// on that rather than on the message container. The previous selector,
	// `[data-message-author-role="assistant"] img`, returned nothing on
	// 2026-08-25: a DOM probe found the image at 1254x1254 with a
	// /backend-api/estuary/content src, outside that container. Avatars and UI
	// icons are not served from backend-api, so this stays narrow, and the
	// 5000-byte floor below drops anything small that slips through.
	//
	// The broader `img` fallback exists because this endpoint path has changed
	// before and a selector is the most brittle part of a browser integration.
	images, err := ep.FetchImageBase64(`img[src*="/backend-api/"]`)
	if err != nil || len(images) == 0 {
		images, err = ep.FetchImageBase64(`main img`)
	}
	if err != nil {
		return nil, fmt.Errorf("chatgpt: fetch images: %w", err)
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("chatgpt: no images found in the response")
	}

	stamp := time.Now().Unix()
	var out []map[string]interface{}
	// ChatGPT renders the same generated image into several <img> elements (a
	// DOM probe on 2026-08-25 found three sharing one src), so without this the
	// same picture is written to disk three or four times.
	seen := make(map[[32]byte]bool)
	for i, img := range images {
		b64, _ := img["data"].(string)
		if b64 == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(decoded) < 5000 {
			// Small payloads are icons and spinners, not a generated image.
			continue
		}
		sum := sha256.Sum256(decoded)
		if seen[sum] {
			continue
		}
		seen[sum] = true
		name := fmt.Sprintf("chatgpt_%d_%d.png", stamp, i)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, decoded, 0600); err != nil {
			continue
		}
		out = append(out, map[string]interface{}{
			"path": path, "filename": name, "size_bytes": len(decoded),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("chatgpt: images found but none were large enough to be real output")
	}
	return out, nil
}

// toSeconds coerces a JSON-ish value to a duration in seconds.
func toSeconds(v interface{}) time.Duration {
	switch t := v.(type) {
	case float64:
		return time.Duration(t) * time.Second
	case int:
		return time.Duration(t) * time.Second
	case string:
		var secs int
		if _, err := fmt.Sscanf(t, "%d", &secs); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

// GetMethodByName exposes the helpers the action definition dispatches to.
func (b *ChatGPTBot) GetMethodByName(name string) (func(ctx context.Context, args ...interface{}) (interface{}, error), bool) {
	switch name {
	case "wait_for_response":
		return func(ctx context.Context, args ...interface{}) (interface{}, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("wait_for_response requires (page[, maxWaitSeconds])")
			}
			page, ok := args[0].(browser.PageInterface)
			if !ok {
				return nil, fmt.Errorf("wait_for_response: first arg must be browser.PageInterface")
			}
			wait := 180 * time.Second
			if len(args) > 1 {
				switch v := args[1].(type) {
				case float64:
					wait = time.Duration(v) * time.Second
				case int:
					wait = time.Duration(v) * time.Second
				case string:
					var secs int
					if _, err := fmt.Sscanf(v, "%d", &secs); err == nil && secs > 0 {
						wait = time.Duration(secs) * time.Second
					}
				}
			}
			text, err := b.waitForResponse(page, wait)
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"response_text": text}, nil
		}, true

	case "wait_for_image":
		return func(ctx context.Context, args ...interface{}) (interface{}, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("wait_for_image requires (page[, maxWaitSeconds])")
			}
			page, ok := args[0].(browser.PageInterface)
			if !ok {
				return nil, fmt.Errorf("wait_for_image: first arg must be browser.PageInterface")
			}
			wait := 300 * time.Second
			if len(args) > 1 {
				if secs := toSeconds(args[1]); secs > 0 {
					wait = secs
				}
			}
			n, err := b.waitForImage(page, wait)
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"image_count": n}, nil
		}, true

	case "download_images":
		return func(ctx context.Context, args ...interface{}) (interface{}, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("download_images requires (page[, downloadDir])")
			}
			page, ok := args[0].(browser.PageInterface)
			if !ok {
				return nil, fmt.Errorf("download_images: first arg must be browser.PageInterface")
			}
			dir := ""
			if len(args) > 1 {
				dir, _ = args[1].(string)
			}
			imgs, err := b.downloadImages(page, dir)
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"images": imgs, "image_count": len(imgs)}, nil
		}, true

	case "extract_text_response":
		return func(ctx context.Context, args ...interface{}) (interface{}, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("extract_text_response requires (page)")
			}
			page, ok := args[0].(browser.PageInterface)
			if !ok {
				return nil, fmt.Errorf("extract_text_response: first arg must be browser.PageInterface")
			}
			text, err := b.extractLast(page)
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"response_text": text}, nil
		}, true
	}
	return nil, false
}
