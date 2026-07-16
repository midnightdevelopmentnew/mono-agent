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
