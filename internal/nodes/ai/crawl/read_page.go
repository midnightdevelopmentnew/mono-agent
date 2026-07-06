package crawl

import (
	"context"
	"fmt"
	"time"

	"monoagent/internal/workflow"
)

// ReadPageNode fetches a web page, cleans its content, and outputs structured
// data (markdown, metadata, links, images, tables, token count).
// Type: "ai.read_page"
type ReadPageNode struct{}

func (n *ReadPageNode) Type() string { return "ai.read_page" }

func (n *ReadPageNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	// --- url (required) ---
	pageURL, _ := config["url"].(string)
	if pageURL == "" && len(input.Items) > 0 {
		pageURL, _ = input.Items[0].JSON["url"].(string)
	}
	if pageURL == "" {
		return nil, fmt.Errorf("ai.read_page: 'url' is required")
	}

	// --- optional config ---
	renderMode, _ := config["render_mode"].(string)
	waitSelector, _ := config["wait_selector"].(string)
	keepNav := boolDefault(config, "keep_nav", false)
	includeLinks := boolDefault(config, "include_links", true)
	includeImages := boolDefault(config, "include_images", true)
	includeTables := boolDefault(config, "include_tables", true)
	maxTokens := intVal(config, "max_tokens")

	// --- fetch ---
	fetchOpts := FetchOptions{
		RenderMode:   renderMode,
		WaitSelector: waitSelector,
	}
	fetchRes, err := FetchPage(ctx, pageURL, fetchOpts)
	if err != nil {
		errItem := workflow.NewItem(map[string]interface{}{
			"url":   pageURL,
			"error": err.Error(),
		})
		return []workflow.NodeOutput{{Handle: "error", Items: []workflow.Item{errItem}}}, nil
	}

	// --- clean ---
	cleanOpts := CleanOptions{KeepNav: keepNav}
	content, err := CleanContent(fetchRes.HTML, fetchRes.FinalURL, cleanOpts)
	if err != nil {
		errItem := workflow.NewItem(map[string]interface{}{
			"url":   pageURL,
			"error": fmt.Sprintf("clean content: %s", err.Error()),
		})
		return []workflow.NodeOutput{{Handle: "error", Items: []workflow.Item{errItem}}}, nil
	}

	// --- truncate ---
	markdown := content.Markdown
	tokenCount := content.TokenCount
	if maxTokens > 0 {
		markdown = TruncateToTokens(markdown, maxTokens)
		tokenCount = EstimateTokens(markdown)
	}

	// --- build output ---
	data := map[string]interface{}{
		"url":             content.URL,
		"title":           content.Title,
		"description":     content.Description,
		"author":          content.Author,
		"published_at":    content.PublishedAt,
		"markdown":        markdown,
		"main_text":       content.MainText,
		"headings":        content.Headings,
		"token_count":     tokenCount,
		"render_mode_used": fetchRes.Mode,
		"fetch_time_ms":   fetchRes.Duration / time.Millisecond,
	}

	if includeLinks {
		data["links"] = content.Links
	}
	if includeImages {
		data["images"] = content.Images
	}
	if includeTables {
		data["tables"] = content.Tables
	}

	item := workflow.NewItem(data)
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{item}}}, nil
}

// boolDefault reads a bool from config, returning defaultVal when the key is
// absent or not a bool.
func boolDefault(config map[string]interface{}, key string, defaultVal bool) bool {
	v, ok := config[key].(bool)
	if !ok {
		return defaultVal
	}
	return v
}

// intVal reads an int from config. JSON numbers arrive as float64, so both
// float64 and int are handled. Returns 0 when the key is absent.
func intVal(config map[string]interface{}, key string) int {
	switch v := config[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
