package gemini

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"monoagent/internal/browser"
	"monoagent/internal/extension"
	"monoagent/internal/vault"

	botpkg "monoagent/internal/bot"
)

// GeminiBot implements botpkg.BotAdapter for Google Gemini.
type GeminiBot struct{}

func init() {
	botpkg.PlatformRegistry["GEMINI"] = func() botpkg.BotAdapter {
		return &GeminiBot{}
	}
}

// Platform returns the canonical platform name.
func (b *GeminiBot) Platform() string { return "GEMINI" }

// LoginURL returns the Gemini login page URL.
func (b *GeminiBot) LoginURL() string { return "https://gemini.google.com" }

// IsLoggedIn checks whether the user is authenticated on Gemini by looking
// for sign-in buttons (NOT logged in) and prompt input (IS logged in).
func (b *GeminiBot) IsLoggedIn(page browser.PageInterface) (bool, error) {
	// Check for sign-in button — if present, NOT logged in.
	signInSelectors := []string{
		"a[href*='accounts.google.com/ServiceLogin']",
		"a[href*='accounts.google.com/signin']",
		"button[data-signin]",
	}
	for _, sel := range signInSelectors {
		has, err := page.Has(sel)
		if err != nil {
			continue
		}
		if has {
			return false, nil
		}
	}

	// Check for prompt input — only appears when logged in.
	promptSelectors := []string{
		"div.ql-editor[contenteditable='true']",
		"rich-textarea",
		"input-area-v2",
	}
	for _, sel := range promptSelectors {
		has, err := page.Has(sel)
		if err != nil {
			continue
		}
		if has {
			return true, nil
		}
	}

	return false, nil
}

// ResolveURL returns the Gemini base URL for any input.
func (b *GeminiBot) ResolveURL(_ string) string {
	return "https://gemini.google.com"
}

// ExtractUsername returns a placeholder — Gemini has no user profiles.
func (b *GeminiBot) ExtractUsername(_ string) string {
	return "gemini-user"
}

// SearchURL is not applicable for Gemini.
func (b *GeminiBot) SearchURL(_ string) string {
	return ""
}

// SendMessage is not supported for Gemini.
func (b *GeminiBot) SendMessage(_ context.Context, _ browser.PageInterface, _, _ string) error {
	return fmt.Errorf("gemini: SendMessage not supported")
}

// GetProfileData is not supported for Gemini.
func (b *GeminiBot) GetProfileData(_ context.Context, _ browser.PageInterface) (map[string]interface{}, error) {
	return nil, fmt.Errorf("gemini: GetProfileData not supported")
}

// GetMethodByName returns a dispatchable wrapper for the named Gemini action
// method. The executor calls this to resolve call_bot_method steps.
func (b *GeminiBot) GetMethodByName(name string) (func(ctx context.Context, args ...interface{}) (interface{}, error), bool) {
	switch name {
	case "find_prompt_input":
		return b.methodFindPromptInput, true
	case "type_prompt":
		return b.methodTypePrompt, true
	case "click_send":
		return b.methodClickSend, true
	case "wait_for_response":
		return b.methodWaitForResponse, true
	case "wait_for_image_response":
		return b.methodWaitForImageResponse, true
	case "extract_text_response":
		return b.methodExtractTextResponse, true
	case "extract_image_response":
		return b.methodExtractImageResponse, true
	case "download_images":
		return b.methodDownloadImages, true
	case "extract_and_download_images":
		return b.methodExtractAndDownloadImages, true
	case "upload_image":
		return b.methodUploadImage, true
	case "navigate_to_session":
		return b.methodNavigateToSession, true
	case "get_session_id":
		return b.methodGetSessionID, true
	case "wait_for_any_response":
		return b.methodWaitForAnyResponse, true
	case "extract_response_by_mode":
		return b.methodExtractResponseByMode, true
	default:
		return nil, false
	}
}

// ---------------------------------------------------------------------------
// Bot methods — all use browser.PageInterface for extension compatibility
// ---------------------------------------------------------------------------

// extractPage gets browser.PageInterface from args[0], accepting both
// PageInterface directly and *rod.Page (wrapped automatically).
func extractPage(args []interface{}, methodName string) (browser.PageInterface, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("%s: requires (page)", methodName)
	}
	if p, ok := args[0].(browser.PageInterface); ok {
		return p, nil
	}
	return nil, fmt.Errorf("%s: first arg must be browser.PageInterface, got %T", methodName, args[0])
}

func (b *GeminiBot) methodFindPromptInput(_ context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "find_prompt_input")
	if err != nil {
		return nil, err
	}
	selectors := []string{
		"div.ql-editor[contenteditable='true']",
		"[role='textbox'][aria-label*='prompt' i]",
		"rich-textarea .ql-editor",
		"rich-textarea",
		"[contenteditable='true'][data-placeholder]",
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, sel := range selectors {
			el, err := page.Element(sel, 2*time.Second)
			if err == nil && el != nil {
				return map[string]interface{}{"success": true, "selector": sel}, nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("find_prompt_input: could not find prompt input after 20s")
}

func (b *GeminiBot) methodTypePrompt(_ context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "type_prompt")
	if err != nil {
		return nil, err
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("type_prompt: requires (page, promptText)")
	}
	prompt, ok := args[1].(string)
	if !ok {
		return nil, fmt.Errorf("type_prompt: second arg must be string")
	}

	selectors := []string{
		"div.ql-editor[contenteditable='true']",
		"[role='textbox'][aria-label*='prompt' i]",
		"rich-textarea .ql-editor",
	}

	// Extension path: use TypeCDP (Chrome Debugger Input.insertText) which
	// reliably triggers Gemini's Quill editor — content-script paste doesn't.
	if ep, ok := page.(*extension.ExtensionPage); ok {
		for _, sel := range selectors {
			el, err := ep.Element(sel, 5*time.Second)
			if err == nil && el != nil {
				if ee, ok := el.(*extension.ExtensionElement); ok {
					_ = ep.TypeCDPOnElement(prompt, ee.ElementID())
					return map[string]interface{}{"success": true, "typed": len(prompt)}, nil
				}
			}
		}
		return nil, fmt.Errorf("type_prompt: could not find input to type into")
	}

	// Dismiss cookie consent dialog if present (blocks all Gemini UI interactions).
	consentResult, _ := page.Eval(`() => {
		var btns = document.querySelectorAll('button');
		for (var i = 0; i < btns.length; i++) {
			var t = btns[i].textContent.trim();
			if (t === 'Accept all' || t === 'Accept') {
				btns[i].click();
				return {dismissed: true};
			}
		}
		return {dismissed: false};
	}`)
	if consentResult != nil && consentResult.Get("dismissed").Bool() {
		time.Sleep(1 * time.Second)
	}

	// Rod path: inject text into Quill's model, trigger Angular CD, then poll
	// for the send button and click it — all in one Promise-based eval.
	// CDP keyboard events and Input.insertText bypass Quill's Delta model.
	// Angular's zone.js might not be loaded (Angular 17 zoneless), so we rely
	// on polling rather than zone-based change detection.
	quillJS := fmt.Sprintf(`() => new Promise(function(resolve) {
		var btnSels = [
			"button[aria-label='Send prompt']",
			"button[aria-label='Send message']",
			"button[aria-label*='send' i]",
			"button[data-testid='send-button']"
		];
		function tryClick() {
			for (var i = 0; i < btnSels.length; i++) {
				var btn = document.querySelector(btnSels[i]);
				if (btn && !btn.disabled && btn.offsetParent !== null) {
					btn.click();
					return resolve({ok: true, method: 'quill-api', clicked: true, sel: btnSels[i]});
				}
			}
		}
		// Find Quill instance
		var container = document.querySelector('.ql-container');
		var quill = null;
		if (container) {
			quill = container.__quill;
			if (!quill && window.Quill) quill = window.Quill.find(container);
		}
		if (!quill) {
			var editor = document.querySelector('.ql-editor');
			if (editor && editor.parentElement) {
				quill = editor.parentElement.__quill;
				if (!quill && window.Quill) quill = window.Quill.find(editor.parentElement);
			}
		}
		if (!quill) {
			return resolve({ok: false, reason: 'no quill instance found'});
		}
		// Use 'user' source: Gemini's Angular text-change handler likely checks
		// source === 'user' before enabling the send button.
		quill.setText(%q, 'user');
		quill.setSelection(quill.getLength(), 0);
		// Dispatch DOM events as belt-and-suspenders for Angular change detection.
		if (quill.root) {
			quill.root.dispatchEvent(new InputEvent('input', {
				bubbles: true, cancelable: true, inputType: 'insertText', data: %q
			}));
			quill.root.dispatchEvent(new Event('change', {bubbles: true}));
		}
		// Poll for send button up to 3s (Angular CD may take a few frames)
		var attempts = 0;
		function poll() {
			attempts++;
			for (var i = 0; i < btnSels.length; i++) {
				var btn = document.querySelector(btnSels[i]);
				if (btn && !btn.disabled && btn.offsetParent !== null) {
					btn.click();
					return resolve({ok: true, method: 'quill-api', clicked: true, sel: btnSels[i], attempts: attempts});
				}
			}
			if (attempts < 15) {
				setTimeout(poll, 200);
			} else {
				resolve({ok: true, method: 'quill-api', clicked: false, reason: 'btn-not-found', attempts: attempts});
			}
		}
		setTimeout(poll, 100);
	})`, prompt, prompt)
	qr, err := page.Eval(quillJS)
	if err != nil {
		return nil, fmt.Errorf("type_prompt: eval failed: %w", err)
	}
	if qr == nil || !qr.Get("ok").Bool() {
		reason := "unknown"
		if qr != nil {
			reason = qr.Get("reason").Str()
		}
		return nil, fmt.Errorf("type_prompt: could not inject text: %s", reason)
	}
	time.Sleep(300 * time.Millisecond)
	return map[string]interface{}{"success": true, "typed": len(prompt), "sent": qr.Get("clicked").Bool()}, nil
}

func (b *GeminiBot) methodClickSend(_ context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "click_send")
	if err != nil {
		return nil, err
	}

	// Extension path: use JS to find and click the send button, then fall back to Enter.
	if ep, ok := page.(*extension.ExtensionPage); ok {
		raw, _ := ep.EvalCDP(`(function() {
			// Try known button selectors in order of likelihood.
			var sels = [
				"button[aria-label='Send prompt']",
				"button[aria-label='Send message']",
				"button[aria-label*='send' i]",
				"button[data-testid='send-button']",
				"button.send-button",
				".send-button-container button",
				"form button[type='submit']",
				"footer button:last-of-type",
				"[class*='send'] button",
				"[class*='submit'] button"
			];
			for (var i = 0; i < sels.length; i++) {
				var el = document.querySelector(sels[i]);
				if (el && !el.disabled && el.offsetParent !== null) {
					el.click();
					return {clicked: true, sel: sels[i]};
				}
			}
			// Fallback: focus the contenteditable and dispatch Enter.
			var input = document.querySelector('.ql-editor[contenteditable="true"]') ||
				document.querySelector('[contenteditable="true"]') ||
				document.querySelector('[role="textbox"]');
			if (input) {
				input.focus();
				input.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true}));
				input.dispatchEvent(new KeyboardEvent('keyup', {key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true}));
				return {clicked: false, method: 'enter_dispatch'};
			}
			return {clicked: false, method: 'none'};
		})()`)
		if m, ok := raw.(map[string]interface{}); ok {
			if clicked, _ := m["clicked"].(bool); clicked {
				return map[string]interface{}{"success": true, "method": m["sel"]}, nil
			}
		}
		// Final fallback: press Enter via CDP keyboard.
		_ = page.KeyboardPress('\r')
		return map[string]interface{}{"success": true, "method": "enter_key"}, nil
	}

	// Rod path: try button selectors with short timeouts, then keyboard fallbacks.
	selectors := []string{
		"button[aria-label='Send prompt']",
		"button[aria-label='Send message']",
		"button[aria-label*='Send' i]",
		"button[data-testid='send-button']",
		"button.send-button",
		".send-button-container button",
		"form button[type='submit']",
	}
	for _, sel := range selectors {
		el, err := page.Element(sel, 3*time.Second)
		if err == nil && el != nil {
			_ = el.Click()
			return map[string]interface{}{"success": true, "selector": sel}, nil
		}
	}

	// Re-focus the input then try keyboard send variants.
	if el, err2 := page.Element("div.ql-editor[contenteditable='true']", 2*time.Second); err2 == nil && el != nil {
		_ = el.Click()
		time.Sleep(200 * time.Millisecond)
	}
	_ = page.KeyboardPress(rune(0x0D)) // Enter
	time.Sleep(300 * time.Millisecond)
	return map[string]interface{}{"success": true, "method": "enter_key"}, nil
}

func (b *GeminiBot) methodWaitForResponse(_ context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "wait_for_response")
	if err != nil {
		return nil, err
	}
	maxWait := 60
	if len(args) >= 2 {
		switch v := args[1].(type) {
		case string:
			fmt.Sscanf(v, "%d", &maxWait)
		case float64:
			maxWait = int(v)
		case int:
			maxWait = v
		}
	}

	deadline := time.Now().Add(time.Duration(maxWait) * time.Second)

	// Snapshot pre-send state.
	type preState struct {
		url         string
		textLen     int
		beforeCount int
	}
	var pre preState

	// pollJS checks for response content using three strategies.
	// Relies on stability (3 consecutive stable polls = 6s) to avoid false positives.
	pollJS := func(initURL string, initLen, beforeCnt int) string {
		return fmt.Sprintf(`(function(initURL, initLen, beforeCount) {
			// Strategy 1: named response elements (count increased + text present).
			var nameSels = [
				'message-content', 'model-response', 'ms-chat-turn',
				'response-container', 'chat-turn', 'model-turn',
				'[data-message-author-role="model"]'
			];
			for (var i = 0; i < nameSels.length; i++) {
				try {
					var els = document.querySelectorAll(nameSels[i]);
					if (els.length > beforeCount) {
						var t = (els[els.length-1].textContent || '').trim();
						if (t && t.length > 2) return {ready: true, text: t, method: 'element'};
					}
				} catch(e) {}
			}
			// Conversation area text.
			var convSels = ['chat-window-content', 'assistant-messages-primary', 'chat-message-list'];
			var chatText = '';
			for (var j = 0; j < convSels.length; j++) {
				var el = document.querySelector(convSels[j]);
				if (el) { chatText = (el.textContent || '').trim(); break; }
			}
			// Guard: page still in zero/start state → not ready regardless.
			var nowZeroState = !!(document.querySelector('modular-zero-state') || document.querySelector('zero-state-banners'));
			if (nowZeroState) { return {ready: false, text: '', method: 'zero_blocked', debug: 'isZero=true chatLen=' + chatText.length}; }
			// Strategy 2: URL changed (first message in new session from zero-state).
			if (location.href !== initURL && chatText.length > 5) {
				return {ready: true, text: chatText.substring(0, 2000), method: 'url_change'};
			}
			// Strategy 3: conversation content changed by ANY amount vs baseline.
			// Handles both zero-state→session (URL already changed at capture) and
			// continued sessions (new turn added). Stability check prevents early exit.
			if (chatText.length > 5 && Math.abs(chatText.length - initLen) >= 1) {
				return {ready: true, text: chatText.substring(0, 2000), method: 'content_change'};
			}
			return {ready: false, text: ''};
		})(%q, %d, %d)`, initURL, initLen, beforeCnt)
	}

	// Capture baseline state at the start of wait (right after click_send).
	captureJS := `(function() {
		var nameSels = ['message-content','model-response','ms-chat-turn','response-container','chat-turn'];
		var cnt = 0;
		for (var i = 0; i < nameSels.length; i++) {
			var n = document.querySelectorAll(nameSels[i]).length;
			if (n > cnt) cnt = n;
		}
		var convSels = ['chat-window-content','assistant-messages-primary'];
		var textLen = 0;
		for (var j = 0; j < convSels.length; j++) {
			var el = document.querySelector(convSels[j]);
			if (el) { textLen = (el.textContent||'').trim().length; break; }
		}
		return {url: location.href, textLen: textLen, cnt: cnt};
	})()`

	evalCapture := func() {
		if ep, ok := page.(*extension.ExtensionPage); ok {
			raw, _ := ep.EvalCDP(captureJS)
			if m, ok := raw.(map[string]interface{}); ok {
				pre.url, _ = m["url"].(string)
				if l, ok := m["textLen"].(float64); ok {
					pre.textLen = int(l)
				}
				if c, ok := m["cnt"].(float64); ok {
					pre.beforeCount = int(c)
				}
			}
		} else {
			r, _ := page.Eval(`() => ` + captureJS)
			if r != nil && !r.Nil() {
				pre.url = r.Get("url").Str()
				pre.textLen = r.Get("textLen").Int()
				pre.beforeCount = r.Get("cnt").Int()
			}
		}
	}
	evalCapture()

	// Build poll JS once — pre doesn't change after capture.
	js := pollJS(pre.url, pre.textLen, pre.beforeCount)
	_ = pre.url // used in pollJS

	prevText := ""
	stableCount := 0
	pollN := 0

	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		pollN++

		var ready bool
		var text string

		if ep, ok := page.(*extension.ExtensionPage); ok {
			raw, evalErr := ep.EvalCDP(js)
			if evalErr == nil {
				if m, ok := raw.(map[string]interface{}); ok {
					if r, _ := m["ready"].(bool); r {
						ready = true
						text, _ = m["text"].(string)
					}
				}
			}
		} else {
			result, evalErr := page.Eval(`() => ` + js)
			if evalErr != nil {
				continue
			}
			ready = result.Get("ready").Bool()
			text = result.Get("text").Str()
		}

		if !ready || text == "" {
			continue
		}
		if text == prevText {
			stableCount++
			// 3 consecutive stable polls = 6s of no text change → response complete.
			if stableCount >= 3 {
				return map[string]interface{}{"success": true, "ready": true}, nil
			}
		} else {
			stableCount = 0
		}
		prevText = text
	}
	return nil, fmt.Errorf("wait_for_response: timed out after %ds", maxWait)
}

func (b *GeminiBot) methodWaitForImageResponse(_ context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "wait_for_image_response")
	if err != nil {
		return nil, err
	}
	maxWait := 120
	if len(args) >= 2 {
		switch v := args[1].(type) {
		case string:
			fmt.Sscanf(v, "%d", &maxWait)
		case float64:
			maxWait = int(v)
		case int:
			maxWait = v
		}
	}

	deadline := time.Now().Add(time.Duration(maxWait) * time.Second)

	if ep, ok := page.(*extension.ExtensionPage); ok {
		// Extension path: use EvalCDP (page main world, bypasses CSP).
		for time.Now().Before(deadline) {
			time.Sleep(3 * time.Second)
			raw, _ := ep.EvalCDP(`(function() {
				var containers = document.querySelectorAll('model-response, message-content, .response-container');
				if (!containers.length) return {imgCount: 0, text: ''};
				var last = containers[containers.length - 1];
				var imgs = last.querySelectorAll('img');
				var valid = 0;
				for (var i = 0; i < imgs.length; i++) {
					var img = imgs[i];
					var w = img.width || img.naturalWidth || 0;
					if (w > 0 && w < 48) continue;
					var src = img.src || '';
					if (src.indexOf('blob:') === 0 || src.indexOf('data:image') === 0 || (src.indexOf('https://') === 0 && w >= 100)) valid++;
				}
				var text = '';
				var textSels = ['message-content', 'model-response'];
				for (var j = 0; j < textSels.length; j++) {
					var els = document.querySelectorAll(textSels[j]);
					if (els.length) { text = (els[els.length-1].textContent || '').trim(); break; }
				}
				return {imgCount: valid, text: text.substring(0, 300)};
			})()`)
			var imgCount int
			var text string
			if m, ok := raw.(map[string]interface{}); ok {
				if c, ok := m["imgCount"].(float64); ok {
					imgCount = int(c)
				}
				if t, ok := m["text"].(string); ok {
					text = t
				}
			}
			if imgCount > 0 {
				time.Sleep(3 * time.Second)
				return map[string]interface{}{"success": true, "ready": true}, nil
			}
			if text != "" {
				lower := strings.ToLower(text)
				if strings.Contains(lower, "can't create") || strings.Contains(lower, "image creation isn't available") {
					return nil, fmt.Errorf("wait_for_image_response: Gemini refused: %s", text[:min(len(text), 200)])
				}
			}
		}
		return nil, fmt.Errorf("wait_for_image_response: timed out after %ds", maxWait)
	}

	// Rod path: use Eval
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		result, err := page.Eval(`() => {
			const containers = document.querySelectorAll('model-response, message-content, .response-container');
			if (containers.length === 0) return {found: false, text: ''};
			const last = containers[containers.length - 1];
			const imgs = last.querySelectorAll('img');
			let valid = 0;
			for (const img of imgs) {
				const w = img.width || img.naturalWidth || 0;
				if (w > 0 && w < 48) continue;
				const src = img.src || '';
				if (src.startsWith('blob:') || src.startsWith('data:image') || (src.startsWith('https://') && w >= 100)) valid++;
			}
			return {found: valid > 0, text: (last.textContent || '').trim().substring(0, 300)};
		}`)
		if err != nil {
			continue
		}
		found := result.Get("found").Bool()
		text := result.Get("text").Str()
		if found {
			time.Sleep(3 * time.Second)
			return map[string]interface{}{"success": true, "ready": true}, nil
		}
		if text != "" {
			lower := strings.ToLower(text)
			if strings.Contains(lower, "can't create") || strings.Contains(lower, "image creation isn't available") {
				return nil, fmt.Errorf("wait_for_image_response: Gemini refused: %s", text[:min(len(text), 200)])
			}
		}
	}
	return nil, fmt.Errorf("wait_for_image_response: timed out after %ds", maxWait)
}

func (b *GeminiBot) methodExtractTextResponse(_ context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "extract_text_response")
	if err != nil {
		return nil, err
	}

	const extractJS = `(function() {
		// Strategy 1: named response elements (works when Gemini uses them).
		var sels = [
			'message-content div.markdown.markdown-main-panel',
			'message-content',
			'model-response',
			'ms-chat-turn model-response',
			'ms-chat-turn',
			'response-container',
			'chat-turn model-response',
			'structured-content-container.model-response-text',
			'[data-message-author-role="model"]',
			'[class*="model-response"]',
			'[class*="response-text"]'
		];
		for (var i = 0; i < sels.length; i++) {
			try {
				var els = document.querySelectorAll(sels[i]);
				if (!els.length) continue;
				var text = (els[els.length - 1].textContent || '').trim();
				if (text && text.length > 2) return text;
			} catch(e) {}
		}
		// Strategy 2: grab text from the conversation content container.
		var convSels = ['chat-window-content', 'assistant-messages-primary', 'chat-message-list'];
		for (var j = 0; j < convSels.length; j++) {
			var conv = document.querySelector(convSels[j]);
			if (!conv) continue;
			var children = Array.from(conv.children || []);
			// Walk children backwards: skip the LAST child if it looks like a user turn
			// (user turns often contain input-area-v2, rich-textarea, or are very short).
			// The model's response is the most recent NON-user child with substantial text.
			var lastModelText = '';
			for (var k = children.length - 1; k >= 0; k--) {
				var child = children[k];
				// Skip if this child contains input elements (= user-input area).
				if (child.querySelector && child.querySelector('rich-textarea, input-area-v2, [contenteditable="true"]')) continue;
				var t = (child.textContent || '').trim();
				if (t && t.length > 5) { lastModelText = t; break; }
			}
			if (lastModelText) return lastModelText.substring(0, 5000);
			// Fallback: return all conversation text.
			var allText = conv.textContent.trim();
			if (allText.length > 5) return allText.substring(0, 5000);
		}
		return '';
	})()`

	if ep, ok := page.(*extension.ExtensionPage); ok {
		raw, err := ep.EvalCDP(extractJS)
		if err == nil {
			if text, ok := raw.(string); ok && text != "" {
				return map[string]interface{}{"success": true, "response_text": text}, nil
			}
		}
		return nil, fmt.Errorf("extract_text_response: no response text found")
	}

	// Rod path: use Eval
	result, err := page.Eval(`() => ` + extractJS)
	if err != nil {
		return nil, fmt.Errorf("extract_text_response: eval failed: %w", err)
	}
	text := result.Str()
	if text == "" {
		return nil, fmt.Errorf("extract_text_response: no response text found")
	}
	return map[string]interface{}{"success": true, "response_text": text}, nil
}

func (b *GeminiBot) methodExtractImageResponse(_ context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "extract_image_response")
	if err != nil {
		return nil, err
	}
	result, err := page.Eval(`() => {
		const containers = document.querySelectorAll('model-response, message-content, .response-container');
		if (containers.length === 0) return [];
		const last = containers[containers.length - 1];
		const urls = [];
		for (const img of last.querySelectorAll('img')) {
			const src = img.src || '';
			const w = img.width || img.naturalWidth || 0;
			if (w > 0 && w < 48) continue;
			if (src.startsWith('blob:') || src.startsWith('data:image') || (src.startsWith('https://') && w >= 100)) urls.push(src);
		}
		return urls;
	}`)
	if err != nil {
		return nil, fmt.Errorf("extract_image_response: eval failed: %w", err)
	}
	var urls []string
	for _, item := range result.Arr() {
		if s := item.Str(); s != "" {
			urls = append(urls, s)
		}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("extract_image_response: no images found")
	}
	return map[string]interface{}{"success": true, "image_urls": urls}, nil
}

func (b *GeminiBot) methodDownloadImages(ctx context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "download_images")
	if err != nil {
		return nil, err
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("download_images: requires (page, imageUrls)")
	}

	var imageURLs []string
	switch v := args[1].(type) {
	case []string:
		imageURLs = v
	case []interface{}:
		for _, u := range v {
			if s, ok := u.(string); ok {
				imageURLs = append(imageURLs, s)
			}
		}
	case map[string]interface{}:
		if urls, ok := v["image_urls"].([]interface{}); ok {
			for _, u := range urls {
				if s, ok := u.(string); ok {
					imageURLs = append(imageURLs, s)
				}
			}
		}
	}

	downloadDir := filepath.Join(os.Getenv("HOME"), ".monoagent", "downloads")
	if len(args) >= 3 {
		if dir, ok := args[2].(string); ok && dir != "" {
			if strings.HasPrefix(dir, "~/") {
				dir = filepath.Join(os.Getenv("HOME"), dir[2:])
			}
			downloadDir = dir
		}
	}
	_ = os.MkdirAll(downloadDir, 0700)

	timestamp := time.Now().Unix()
	var downloaded []map[string]interface{}

	for i, imgURL := range imageURLs {
		filename := fmt.Sprintf("gemini_%d_%d.png", timestamp, i)
		filePath := filepath.Join(downloadDir, filename)

		var b64str string
		if strings.HasPrefix(imgURL, "data:image") {
			parts := strings.SplitN(imgURL, ",", 2)
			if len(parts) == 2 {
				b64str = parts[1]
			}
		} else {
			result, err := page.Eval(`(url) => {
				return fetch(url)
					.then(r => r.blob())
					.then(b => new Promise((resolve, reject) => {
						const reader = new FileReader();
						reader.onloadend = () => resolve(reader.result.split(',')[1]);
						reader.onerror = reject;
						reader.readAsDataURL(b);
					}));
			}`, imgURL)
			if err != nil {
				continue
			}
			b64str = result.Str()
		}

		if b64str == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(b64str)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(b64str)
			if err != nil {
				continue
			}
		}
		if len(decoded) < 100 {
			continue
		}
		if err := os.WriteFile(filePath, decoded, 0600); err != nil {
			continue
		}
		downloaded = append(downloaded, map[string]interface{}{
			"path":       filePath,
			"filename":   filename,
			"size_bytes": len(decoded),
		})
	}

	if len(downloaded) == 0 {
		return nil, fmt.Errorf("download_images: failed to download any images")
	}

	// Create latest symlink.
	if first, ok := downloaded[0]["path"].(string); ok {
		latestLink := filepath.Join(downloadDir, "latest_gemini.png")
		_ = os.Remove(latestLink)
		_ = os.Symlink(first, latestLink)
	}

	// Register each downloaded image in the vault.
	if vaultDB := vault.DBFromContext(ctx); vaultDB != nil {
		wfID, execID := vault.ExecIDsFromContext(ctx)
		for i, img := range downloaded {
			path, _ := img["path"].(string)
			if path == "" {
				continue
			}
			vaultID, err := vault.Register(ctx, vaultDB, path, "gemini", wfID, execID)
			if err == nil {
				downloaded[i]["vault_id"] = vaultID
			} else {
				fmt.Fprintf(os.Stderr, "vault: warning: register image: %v\n", err)
			}
		}
	}

	return map[string]interface{}{
		"success":     true,
		"images":      downloaded,
		"image_count": len(downloaded),
	}, nil
}

// methodUploadImage uploads a local image file to Gemini's chat input before
// submitting a prompt. If imagePath is empty or the file does not exist it is
// a no-op (returns success) so the node still works without a reference image.
//
// Gemini's upload flow (discovered via live DOM inspection):
//  1. Click "+" button (aria-label="Open upload file menu")
//  2. Click "Upload files" menuitem (aria-label contains "Upload files")
//  3. A hidden <input type="file" name="Filedata"> appears in the DOM
//  4. Call SetFiles on that input to trigger the upload
//  5. Wait for the image thumbnail to appear in the chat input area
func (b *GeminiBot) methodUploadImage(_ context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "upload_image")
	if err != nil {
		return nil, err
	}

	// Second arg: local image file path (may be empty — treated as no-op).
	if len(args) < 2 {
		return map[string]interface{}{"success": true, "skipped": true, "reason": "no image path"}, nil
	}
	imagePath, _ := args[1].(string)
	imagePath = strings.TrimSpace(imagePath)
	if imagePath == "" {
		return map[string]interface{}{"success": true, "skipped": true, "reason": "empty image path"}, nil
	}
	if strings.HasPrefix(imagePath, "~/") {
		imagePath = filepath.Join(os.Getenv("HOME"), imagePath[2:])
	}
	if _, err := os.Stat(imagePath); err != nil {
		return nil, fmt.Errorf("upload_image: image file not found: %s", imagePath)
	}

	// Step 1: Click the "+" button to open the upload menu.
	plusBtnSelectors := []string{
		"button[aria-label='Open upload file menu']",
		"button[aria-label*='upload file menu' i]",
		"button[aria-label*='Upload' i][aria-label*='menu' i]",
	}
	var plusClicked bool
	for _, sel := range plusBtnSelectors {
		el, err := page.Element(sel, 5*time.Second)
		if err == nil && el != nil {
			_ = el.Click()
			plusClicked = true
			time.Sleep(800 * time.Millisecond)
			break
		}
	}
	if !plusClicked {
		return nil, fmt.Errorf("upload_image: could not find the '+' upload menu button")
	}

	// Step 2: Click the "Upload files" menu item.
	uploadItemSelectors := []string{
		"[role='menuitem'][aria-label*='Upload files' i]",
		"[role='menuitem'][aria-label*='Upload' i]",
	}
	var uploadClicked bool
	for _, sel := range uploadItemSelectors {
		el, err := page.Element(sel, 5*time.Second)
		if err == nil && el != nil {
			_ = el.Click()
			uploadClicked = true
			time.Sleep(800 * time.Millisecond)
			break
		}
	}
	if !uploadClicked {
		return nil, fmt.Errorf("upload_image: could not find the 'Upload files' menu item")
	}

	// Step 3: Find the hidden file input and set files on it.
	fileInputSelectors := []string{
		"input[name='Filedata']",
		"input[type='file'][name='Filedata']",
		"input[type='file']",
	}

	// Extension path — uses CDP for file injection.
	if ep, ok := page.(*extension.ExtensionPage); ok {
		// Try SetFiles first (extension reads file on Go side, sends base64).
		for _, sel := range fileInputSelectors {
			el, err := ep.Element(sel, 5*time.Second)
			if err == nil && el != nil {
				if err := el.SetFiles([]string{imagePath}); err == nil {
					time.Sleep(3 * time.Second)
					return map[string]interface{}{"success": true, "uploaded": imagePath}, nil
				}
			}
		}

		// Fallback: inject file via CDP DataTransfer into the file input.
		data, err := os.ReadFile(imagePath)
		if err != nil {
			return nil, fmt.Errorf("upload_image: read file: %w", err)
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		mime := "image/jpeg"
		ext := strings.ToLower(filepath.Ext(imagePath))
		switch ext {
		case ".png":
			mime = "image/png"
		case ".gif":
			mime = "image/gif"
		case ".webp":
			mime = "image/webp"
		}
		name := filepath.Base(imagePath)

		js := fmt.Sprintf(`(function() {
			var input = document.querySelector("input[name='Filedata']") || document.querySelector("input[type='file']");
			if (!input) return {success: false, reason: "no file input found after menu click"};
			var b64 = %q;
			var mime = %q;
			var name = %q;
			var bin = atob(b64);
			var buf = new Uint8Array(bin.length);
			for (var k = 0; k < bin.length; k++) buf[k] = bin.charCodeAt(k);
			var file = new File([buf.buffer], name, {type: mime, lastModified: Date.now()});
			var dt = new DataTransfer();
			dt.items.add(file);
			Object.defineProperty(input, "files", {value: dt.files, configurable: true, writable: false});
			input.dispatchEvent(new Event("change", {bubbles: true}));
			input.dispatchEvent(new InputEvent("input", {bubbles: true}));
			return {success: true, name: name, size: file.size};
		})()`, b64, mime, name)

		raw, _ := ep.EvalCDP(js)
		if m, ok := raw.(map[string]interface{}); ok {
			if s, _ := m["success"].(bool); s {
				time.Sleep(3 * time.Second)
				return map[string]interface{}{"success": true, "uploaded": imagePath}, nil
			}
			if reason, _ := m["reason"].(string); reason != "" {
				return nil, fmt.Errorf("upload_image: CDP inject failed: %s", reason)
			}
		}
		return nil, fmt.Errorf("upload_image: could not set files via CDP")
	}

	// Rod path — standard SetFiles via file input.
	for _, sel := range fileInputSelectors {
		el, err := page.Element(sel, 5*time.Second)
		if err == nil && el != nil {
			if err := el.SetFiles([]string{imagePath}); err == nil {
				time.Sleep(3 * time.Second)
				return map[string]interface{}{"success": true, "uploaded": imagePath}, nil
			}
		}
	}

	return nil, fmt.Errorf("upload_image: could not find file input after opening upload menu")
}

// methodExtractAndDownloadImages combines image extraction + download in one step.
// Uses the content script FetchImageBase64 on extension path, Eval on Rod path.
func (b *GeminiBot) methodExtractAndDownloadImages(ctx context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "extract_and_download_images")
	if err != nil {
		return nil, err
	}

	downloadDir := filepath.Join(os.Getenv("HOME"), ".monoagent", "downloads")
	if len(args) >= 2 {
		if dir, ok := args[1].(string); ok && dir != "" {
			if strings.HasPrefix(dir, "~/") {
				dir = filepath.Join(os.Getenv("HOME"), dir[2:])
			}
			downloadDir = dir
		}
	}
	_ = os.MkdirAll(downloadDir, 0700)

	timestamp := time.Now().Unix()
	var downloaded []map[string]interface{}

	if ep, ok := page.(*extension.ExtensionPage); ok {
		// Extension path: use content script to fetch images as base64
		imgSelector := "model-response img, message-content img, .response-container img"
		images, err := ep.FetchImageBase64(imgSelector)
		if err != nil || len(images) == 0 {
			return nil, fmt.Errorf("extract_and_download_images: no images found via extension")
		}
		for i, img := range images {
			b64, _ := img["data"].(string)
			if b64 == "" {
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				continue
			}
			if len(decoded) < 100 {
				continue
			}
			filename := fmt.Sprintf("gemini_%d_%d.png", timestamp, i)
			filePath := filepath.Join(downloadDir, filename)
			if err := os.WriteFile(filePath, decoded, 0600); err != nil {
				continue
			}
			downloaded = append(downloaded, map[string]interface{}{
				"path":       filePath,
				"filename":   filename,
				"size_bytes": len(decoded),
			})
		}
	} else {
		// Rod path: use Eval to extract and download
		result, err := page.Eval(`() => {
			const containers = document.querySelectorAll('model-response, message-content, .response-container');
			if (containers.length === 0) return [];
			const last = containers[containers.length - 1];
			const imgs = last.querySelectorAll('img');
			const data = [];
			for (const img of imgs) {
				const w = img.naturalWidth || img.width || 0;
				if (w < 48) continue;
				try {
					const canvas = document.createElement('canvas');
					canvas.width = img.naturalWidth;
					canvas.height = img.naturalHeight;
					canvas.getContext('2d').drawImage(img, 0, 0);
					const b64 = canvas.toDataURL('image/png').split(',')[1];
					if (b64 && b64.length > 200) data.push(b64);
				} catch(e) {}
			}
			return data;
		}`)
		if err == nil {
			for i, item := range result.Arr() {
				b64 := item.Str()
				if b64 == "" {
					continue
				}
				decoded, err := base64.StdEncoding.DecodeString(b64)
				if err != nil || len(decoded) < 100 {
					continue
				}
				filename := fmt.Sprintf("gemini_%d_%d.png", timestamp, i)
				filePath := filepath.Join(downloadDir, filename)
				if err := os.WriteFile(filePath, decoded, 0600); err != nil {
					continue
				}
				downloaded = append(downloaded, map[string]interface{}{
					"path":       filePath,
					"filename":   filename,
					"size_bytes": len(decoded),
				})
			}
		}
	}

	if len(downloaded) == 0 {
		return nil, fmt.Errorf("extract_and_download_images: no images downloaded")
	}

	if first, ok := downloaded[0]["path"].(string); ok {
		latestLink := filepath.Join(downloadDir, "latest_gemini.png")
		_ = os.Remove(latestLink)
		_ = os.Symlink(first, latestLink)
	}

	// Register each downloaded image in the vault.
	if vaultDB := vault.DBFromContext(ctx); vaultDB != nil {
		wfID, execID := vault.ExecIDsFromContext(ctx)
		for i, img := range downloaded {
			path, _ := img["path"].(string)
			if path == "" {
				continue
			}
			vaultID, err := vault.Register(ctx, vaultDB, path, "gemini", wfID, execID)
			if err == nil {
				downloaded[i]["vault_id"] = vaultID
			} else {
				fmt.Fprintf(os.Stderr, "vault: warning: register image: %v\n", err)
			}
		}
	}

	return map[string]interface{}{
		"success":     true,
		"images":      downloaded,
		"image_count": len(downloaded),
	}, nil
}

// methodNavigateToSession navigates to an existing Gemini session by session ID.
// If session_id is empty or missing, it is a no-op (caller already navigated to /app).
func (b *GeminiBot) methodNavigateToSession(_ context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "navigate_to_session")
	if err != nil {
		return nil, err
	}
	if len(args) < 2 {
		return map[string]interface{}{"success": true, "skipped": true, "reason": "no session_id"}, nil
	}
	sessionID, _ := args[1].(string)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return map[string]interface{}{"success": true, "skipped": true, "reason": "empty session_id"}, nil
	}
	targetURL := "https://gemini.google.com/app/" + sessionID
	if err := page.Navigate(targetURL); err != nil {
		return nil, fmt.Errorf("navigate_to_session: %w", err)
	}
	time.Sleep(4 * time.Second)
	return map[string]interface{}{"success": true, "url": targetURL}, nil
}

// methodGetSessionID extracts the Gemini session ID from the current page URL.
// URL format: https://gemini.google.com/app/{sessionID}
func (b *GeminiBot) methodGetSessionID(_ context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "get_session_id")
	if err != nil {
		return nil, err
	}
	currentURL, err := page.GetURL()
	if err != nil {
		return nil, fmt.Errorf("get_session_id: could not get page URL: %w", err)
	}
	u, err := url.Parse(currentURL)
	if err != nil {
		return nil, fmt.Errorf("get_session_id: invalid URL %q: %w", currentURL, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// path is "app/{sessionID}"
	if len(parts) >= 2 && parts[0] == "app" && parts[1] != "" {
		return map[string]interface{}{"success": true, "session_id": parts[1]}, nil
	}
	return nil, fmt.Errorf("get_session_id: no session ID in URL: %s", currentURL)
}

// methodWaitForAnyResponse dispatches to the text or image wait method based on mode.
// args: page, mode ("text"|"image"), maxWaitSeconds (optional)
func (b *GeminiBot) methodWaitForAnyResponse(ctx context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "wait_for_any_response")
	if err != nil {
		return nil, err
	}
	mode := "text"
	if len(args) >= 2 {
		if m, ok := args[1].(string); ok && m != "" {
			mode = strings.ToLower(m)
		}
	}
	delegateArgs := []interface{}{page}
	if len(args) >= 3 {
		delegateArgs = append(delegateArgs, args[2])
	}
	if mode == "image" {
		return b.methodWaitForImageResponse(ctx, delegateArgs...)
	}
	return b.methodWaitForResponse(ctx, delegateArgs...)
}

// methodExtractResponseByMode dispatches to text or image extraction based on mode.
// args: page, mode ("text"|"image"), downloadDir (optional, image mode only)
func (b *GeminiBot) methodExtractResponseByMode(ctx context.Context, args ...interface{}) (interface{}, error) {
	page, err := extractPage(args, "extract_response_by_mode")
	if err != nil {
		return nil, err
	}
	mode := "text"
	if len(args) >= 2 {
		if m, ok := args[1].(string); ok && m != "" {
			mode = strings.ToLower(m)
		}
	}
	if mode == "image" {
		delegateArgs := []interface{}{page}
		if len(args) >= 3 {
			delegateArgs = append(delegateArgs, args[2])
		}
		return b.methodExtractAndDownloadImages(ctx, delegateArgs...)
	}
	return b.methodExtractTextResponse(ctx, page)
}
