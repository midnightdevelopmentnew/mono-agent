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
