package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"time"

	"monoagent/internal/noderegistry"
	"monoagent/internal/workflow"
)

// ─────────────────────────────────────────────────────────────────────────────
// Node types registry
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) GetWorkflowNodeTypes() map[string]interface{} {
	type nodeDesc struct {
		Type        string               `json:"type"`
		Label       string               `json:"label"`
		Category    string               `json:"category"`
		Description string               `json:"description"`
		Schema      *workflow.NodeSchema `json:"schema,omitempty"`
	}
	mkNode := func(t, label, cat, desc string) nodeDesc {
		schema, _ := workflow.LoadDefaultSchema(t)
		return nodeDesc{Type: t, Label: label, Category: cat, Description: desc, Schema: schema}
	}

	catalog := map[string][]nodeDesc{
		"triggers": []nodeDesc{
			mkNode("trigger.manual", "Manual Trigger", "triggers", "Start a workflow run manually"),
			mkNode("trigger.schedule", "Schedule", "triggers", "Run workflow on a cron or interval schedule"),
			mkNode("trigger.webhook", "Webhook", "triggers", "Start workflow on incoming HTTP request"),
		},
		"control": []nodeDesc{
			mkNode("core.if", "If", "control", "Branch execution based on a condition"),
			mkNode("core.switch", "Switch", "control", "Route execution to one of multiple branches"),
			mkNode("core.merge", "Merge", "control", "Merge multiple input branches into one"),
			mkNode("core.split_in_batches", "Split In Batches", "control", "Process items in fixed-size batches"),
			mkNode("core.wait", "Wait", "control", "Pause execution for a specified duration"),
			mkNode("core.stop_error", "Stop And Error", "control", "Halt workflow with an error message"),
			mkNode("core.set", "Set", "control", "Set or transform data fields"),
			mkNode("core.code", "Code", "control", "Execute arbitrary JavaScript code"),
			mkNode("core.filter", "Filter", "control", "Keep only items matching a condition"),
			mkNode("core.sort", "Sort", "control", "Sort items by one or more fields"),
			mkNode("core.limit", "Limit", "control", "Limit the number of output items"),
			mkNode("core.remove_duplicates", "Remove Duplicates", "control", "Deduplicate items by key"),
			mkNode("core.compare_datasets", "Compare Datasets", "control", "Compare two datasets and output differences"),
			mkNode("core.aggregate", "Aggregate", "control", "Aggregate multiple items into one"),
			mkNode("core.human_in_loop", "Human in Loop", "control", "Pause and wait for a human to review, edit, and approve the data"),
		},
		"data": []nodeDesc{
			mkNode("data.datetime", "Date & Time", "data", "Parse, format, and manipulate date/time values"),
			mkNode("data.crypto", "Crypto", "data", "Hash, encrypt, or sign data"),
			mkNode("data.html", "HTML", "data", "Parse or generate HTML"),
			mkNode("data.xml", "XML", "data", "Parse or generate XML"),
			mkNode("data.markdown", "Markdown", "data", "Convert Markdown to HTML and vice-versa"),
			mkNode("data.spreadsheet", "Spreadsheet", "data", "Read or write spreadsheet data"),
			mkNode("data.compression", "Compression", "data", "Compress or decompress files"),
			mkNode("data.write_binary_file", "Write Binary File", "data", "Write binary data to disk"),
		},
		"http": []nodeDesc{
			mkNode("http.request", "HTTP Request", "http", "Make an HTTP/S request"),
			mkNode("http.ftp", "FTP", "http", "Transfer files via FTP/SFTP"),
			mkNode("http.ssh", "SSH", "http", "Execute commands over SSH"),
		},
		"system": []nodeDesc{
			mkNode("system.execute_command", "Execute Command", "system", "Run a shell command on the host"),
			mkNode("system.rss_read", "RSS Read", "system", "Fetch and parse an RSS/Atom feed"),
		},
		"db": []nodeDesc{
			mkNode("db.mysql", "MySQL", "db", "Query a MySQL/MariaDB database"),
			mkNode("db.postgres", "Postgres", "db", "Query a PostgreSQL database"),
			mkNode("db.mongodb", "MongoDB", "db", "Interact with a MongoDB collection"),
			mkNode("db.redis", "Redis", "db", "Read/write keys in a Redis store"),
		},
		"comm": []nodeDesc{
			mkNode("comm.email_send", "Send Email", "comm", "Send an email via SMTP"),
			mkNode("comm.email_read", "Read Email", "comm", "Read emails via IMAP"),
			mkNode("comm.slack", "Slack", "comm", "Send or read Slack messages"),
			mkNode("comm.telegram", "Telegram", "comm", "Send or receive Telegram messages"),
			mkNode("comm.discord", "Discord", "comm", "Send messages to a Discord channel"),
			mkNode("comm.twilio", "Twilio", "comm", "Send SMS or make calls via Twilio"),
			mkNode("comm.whatsapp", "WhatsApp", "comm", "Send WhatsApp messages"),
		},
		"service": []nodeDesc{
			mkNode("service.github", "GitHub", "service", "Interact with GitHub repositories and issues"),
			mkNode("service.airtable", "Airtable", "service", "Read/write Airtable bases"),
			mkNode("service.notion", "Notion", "service", "Read/write Notion pages and databases"),
			mkNode("service.jira", "Jira", "service", "Manage Jira issues and projects"),
			mkNode("service.linear", "Linear", "service", "Manage Linear issues and cycles"),
			mkNode("service.asana", "Asana", "service", "Manage Asana tasks and projects"),
			mkNode("service.stripe", "Stripe", "service", "Interact with Stripe payments"),
			mkNode("service.shopify", "Shopify", "service", "Manage Shopify orders and products"),
			mkNode("service.salesforce", "Salesforce", "service", "Read/write Salesforce records"),
			mkNode("service.hubspot", "HubSpot", "service", "Manage HubSpot CRM contacts and deals"),
			mkNode("service.google_sheets", "Google Sheets", "service", "Read/write Google Sheets"),
			mkNode("service.gmail", "Gmail", "service", "Send and read Gmail messages"),
			mkNode("service.outlook_mail", "Outlook", "service", "Send and read Outlook/Hotmail messages via Microsoft Graph"),
			mkNode("service.google_drive", "Google Drive", "service", "Manage Google Drive files"),
			mkNode("service.huggingface", "HuggingFace", "service", "Generate images or text via HuggingFace Inference API"),
			mkNode("service.openrouter", "OpenRouter", "service", "Generate text or images via OpenRouter AI API"),
		},
		"ai": []nodeDesc{
			mkNode("agent.ask", "Agent Ask", "ai", "Ask a locally-installed AI agent (claude, codex, …) one turn via the monomind engine"),
			mkNode("ai.chat", "AI Chat (deprecated)", "ai", "Deprecated — replace with Agent Ask"),
			mkNode("ai.extract", "AI Extract (deprecated)", "ai", "Deprecated — replace with Agent Ask"),
			mkNode("ai.classify", "AI Classify (deprecated)", "ai", "Deprecated — replace with Agent Ask"),
			mkNode("ai.transform", "AI Transform (deprecated)", "ai", "Deprecated — replace with Agent Ask"),
			mkNode("ai.embed", "AI Embed (deprecated)", "ai", "Deprecated — no local-agent equivalent"),
			mkNode("ai.agent", "AI Agent (deprecated)", "ai", "Deprecated — replace with Agent Ask"),
			mkNode("ai.read_page", "Read Page", "ai", "Crawl a webpage and return clean readable content (markdown, links, images)"),
			mkNode("ai.extract_page", "Extract Page", "ai", "Extract specific fields from a webpage using AI or CSS selectors"),
		},
		"browser": []nodeDesc{
			// Instagram
			mkNode("instagram.find_by_keyword", "Instagram: Find By Keyword", "browser", "Search Instagram users or posts by keyword"),
			mkNode("instagram.export_followers", "Instagram: Export Followers", "browser", "Export a profile's follower list"),
			mkNode("instagram.scrape_profile_info", "Instagram: Scrape Profile Info", "browser", "Collect profile metadata"),
			mkNode("instagram.engage_with_posts", "Instagram: Engage With Posts", "browser", "Like/comment on matched posts"),
			mkNode("instagram.send_dms", "Instagram: Send DMs", "browser", "Send direct messages"),
			mkNode("instagram.auto_reply_dms", "Instagram: Auto Reply DMs", "browser", "Automatically reply to incoming DMs"),
			mkNode("instagram.publish_post", "Instagram: Publish Post", "browser", "Publish a photo or reel"),
			mkNode("instagram.like_posts", "Instagram: Like Posts", "browser", "Like a list of posts"),
			mkNode("instagram.comment_on_posts", "Instagram: Comment On Posts", "browser", "Comment on a list of posts"),
			mkNode("instagram.like_comments_on_posts", "Instagram: Like Comments On Posts", "browser", "Like comments on posts"),
			mkNode("instagram.extract_post_data", "Instagram: Extract Post Data", "browser", "Extract structured data from posts"),
			mkNode("instagram.follow_users", "Instagram: Follow Users", "browser", "Follow a list of users"),
			mkNode("instagram.unfollow_users", "Instagram: Unfollow Users", "browser", "Unfollow a list of users"),
			mkNode("instagram.watch_stories", "Instagram: Watch Stories", "browser", "View stories for a list of users"),
			mkNode("instagram.engage_user_posts", "Instagram: Engage User Posts", "browser", "Engage with a specific user's posts"),
			mkNode("instagram.list_post_comments", "Instagram: List Post Comments", "browser", "List comments on a post"),
			mkNode("instagram.list_user_posts", "Instagram: List User Posts", "browser", "List posts from a user profile"),
			mkNode("instagram.reply_to_comments", "Instagram: Reply To Comments", "browser", "Reply to comments on posts"),
			// LinkedIn
			mkNode("linkedin.find_by_keyword", "LinkedIn: Find By Keyword", "browser", "Search LinkedIn profiles by keyword"),
			mkNode("linkedin.export_followers", "LinkedIn: Export Followers", "browser", "Export a profile's connections/followers"),
			mkNode("linkedin.scrape_profile_info", "LinkedIn: Scrape Profile Info", "browser", "Collect LinkedIn profile metadata"),
			mkNode("linkedin.engage_with_posts", "LinkedIn: Engage With Posts", "browser", "Like/comment on LinkedIn posts"),
			mkNode("linkedin.send_dms", "LinkedIn: Send DMs", "browser", "Send LinkedIn direct messages"),
			mkNode("linkedin.auto_reply_dms", "LinkedIn: Auto Reply DMs", "browser", "Automatically reply to LinkedIn messages"),
			mkNode("linkedin.publish_post", "LinkedIn: Publish Post", "browser", "Publish a LinkedIn post"),
			mkNode("linkedin.comment_on_posts", "LinkedIn: Comment On Posts", "browser", "Comment on LinkedIn posts"),
			mkNode("linkedin.like_comments", "LinkedIn: Like Comments", "browser", "Like comments on LinkedIn posts"),
			mkNode("linkedin.like_posts", "LinkedIn: Like Posts", "browser", "Like LinkedIn posts"),
			mkNode("linkedin.list_post_comments", "LinkedIn: List Post Comments", "browser", "List comments on a LinkedIn post"),
			mkNode("linkedin.list_user_posts", "LinkedIn: List User Posts", "browser", "List posts from a LinkedIn profile"),
			// X (Twitter)
			mkNode("x.find_by_keyword", "X: Find By Keyword", "browser", "Search X/Twitter by keyword"),
			mkNode("x.export_followers", "X: Export Followers", "browser", "Export a profile's followers on X"),
			mkNode("x.scrape_profile_info", "X: Scrape Profile Info", "browser", "Collect X profile metadata"),
			mkNode("x.engage_with_posts", "X: Engage With Posts", "browser", "Like/reply to X posts"),
			mkNode("x.send_dms", "X: Send DMs", "browser", "Send X direct messages"),
			mkNode("x.auto_reply_dms", "X: Auto Reply DMs", "browser", "Automatically reply to X DMs"),
			mkNode("x.publish_post", "X: Publish Post", "browser", "Publish a post on X"),
			// TikTok
			mkNode("tiktok.find_by_keyword", "TikTok: Find By Keyword", "browser", "Search TikTok by keyword"),
			mkNode("tiktok.export_followers", "TikTok: Export Followers", "browser", "Export a TikTok profile's followers"),
			mkNode("tiktok.scrape_profile_info", "TikTok: Scrape Profile Info", "browser", "Collect TikTok profile metadata"),
			mkNode("tiktok.engage_with_posts", "TikTok: Engage With Posts", "browser", "Like/comment on TikTok posts"),
			mkNode("tiktok.send_dms", "TikTok: Send DMs", "browser", "Send TikTok direct messages"),
			mkNode("tiktok.auto_reply_dms", "TikTok: Auto Reply DMs", "browser", "Automatically reply to TikTok DMs"),
			mkNode("tiktok.publish_post", "TikTok: Publish Post", "browser", "Publish a TikTok video"),
			mkNode("tiktok.comment_on_video", "TikTok: Comment On Video", "browser", "Comment on a TikTok video"),
			mkNode("tiktok.duet_video", "TikTok: Duet Video", "browser", "Create a duet with a TikTok video"),
			mkNode("tiktok.follow_user", "TikTok: Follow User", "browser", "Follow a TikTok user"),
			mkNode("tiktok.like_comment", "TikTok: Like Comment", "browser", "Like a comment on a TikTok video"),
			mkNode("tiktok.like_video", "TikTok: Like Video", "browser", "Like a TikTok video"),
			mkNode("tiktok.list_user_videos", "TikTok: List User Videos", "browser", "List videos from a TikTok profile"),
			mkNode("tiktok.list_video_comments", "TikTok: List Video Comments", "browser", "List comments on a TikTok video"),
			mkNode("tiktok.share_video", "TikTok: Share Video", "browser", "Share a TikTok video"),
			mkNode("tiktok.stitch_video", "TikTok: Stitch Video", "browser", "Create a stitch with a TikTok video"),

			mkNode("gemini.generate_text", "Gemini Text", "browser", "Send a prompt to Gemini and get a text response"),
			mkNode("gemini.generate_image", "Gemini Image", "browser", "Send a prompt to Gemini and download generated images"),
		},
		"people": []nodeDesc{
			mkNode("people.save", "Save to People", "people", "Upsert items into the People tab"),
			mkNode("people.sync_outlook_message", "Sync Email to People", "people", "Upsert the sender as a person and save the message to their history"),
		},
	}

	// Reconcile the hand-written catalog against the live node registry (the
	// same one the CLI runs) so the GUI always matches what can actually run:
	// stale entries drop out, newly registered types appear with a derived label.
	reg := noderegistry.Build(a.db)
	groupOf := func(t string) string {
		prefix, _, _ := strings.Cut(t, ".")
		switch prefix {
		case "core":
			return "control"
		case "data", "http", "system", "db", "comm", "service", "ai", "people", "image":
			return prefix
		default: // platform bots: instagram, linkedin, x, tiktok, gemini, hackernews, ...
			return "browser"
		}
	}
	title := func(s string) string {
		words := strings.Split(strings.ReplaceAll(s, "_", " "), " ")
		for i, w := range words {
			if w != "" {
				words[i] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		return strings.Join(words, " ")
	}
	deriveLabel := func(t string) string {
		prefix, rest, _ := strings.Cut(t, ".")
		if groupOf(t) == "browser" {
			return title(prefix) + ": " + title(rest)
		}
		return title(rest)
	}

	seen := map[string]bool{}
	for group, list := range catalog {
		if group == "triggers" {
			continue // triggers run via the trigger manager, not the node registry
		}
		kept := list[:0]
		for _, n := range list {
			if reg.Has(n.Type) {
				kept = append(kept, n)
				seen[n.Type] = true
			}
		}
		catalog[group] = kept
	}
	for _, t := range reg.Types() { // Types() is sorted, so extras append deterministically
		if !seen[t] {
			g := groupOf(t)
			catalog[g] = append(catalog[g], mkNode(t, deriveLabel(t), g, ""))
		}
	}

	out := make(map[string]interface{}, len(catalog))
	for group, list := range catalog {
		out[group] = list
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Node runner — execute any node type directly
// ─────────────────────────────────────────────────────────────────────────────

// NodeRunRequest is the payload sent by the frontend to run a node.
type NodeRunRequest struct {
	NodeType string                   `json:"node_type"`
	Config   map[string]interface{}   `json:"config"`
	Items    []map[string]interface{} `json:"items"` // each element is a JSON object (the item's .json field)
}

// NodeRunResult is returned after running a node.
type NodeRunResult struct {
	Outputs []NodeRunOutput `json:"outputs"`
	Error   string          `json:"error,omitempty"`
	DurationMs int64        `json:"duration_ms"`
}

// NodeRunOutput is one output handle's items.
type NodeRunOutput struct {
	Handle string                   `json:"handle"`
	Items  []map[string]interface{} `json:"items"`
}

// RunNode executes any registered node type directly via the CLI subprocess.
// Config and input items are passed as JSON; results are returned as structured data.
// legacyNodeTypes maps old short type names to their new prefixed equivalents.
var legacyNodeTypes = map[string]string{
	"if": "core.if", "switch": "core.switch", "merge": "core.merge",
	"split_in_batches": "core.split_in_batches", "wait": "core.wait",
	"stop_error": "core.stop_error", "set": "core.set", "code": "core.code",
	"filter": "core.filter", "sort": "core.sort", "limit": "core.limit",
	"remove_duplicates": "core.remove_duplicates", "compare_datasets": "core.compare_datasets",
	"aggregate": "core.aggregate",
	"datetime": "data.datetime", "crypto": "data.crypto", "html": "data.html",
	"xml": "data.xml", "markdown": "data.markdown", "spreadsheet": "data.spreadsheet",
	"compression": "data.compression", "write_binary_file": "data.write_binary_file",
	"mysql": "db.mysql", "postgres": "db.postgres", "mongodb": "db.mongodb", "redis": "db.redis",
	"email_send": "comm.email_send", "email_read": "comm.email_read",
	"slack": "comm.slack", "telegram": "comm.telegram", "discord": "comm.discord",
	"twilio": "comm.twilio", "whatsapp": "comm.whatsapp",
	"github": "service.github", "airtable": "service.airtable", "notion": "service.notion",
	"jira": "service.jira", "linear": "service.linear", "asana": "service.asana",
	"stripe": "service.stripe", "shopify": "service.shopify", "salesforce": "service.salesforce",
	"hubspot": "service.hubspot", "google_sheets": "service.google_sheets",
	"gmail": "service.gmail", "google_drive": "service.google_drive",
}

func (a *App) RunNode(req NodeRunRequest) NodeRunResult {
	if mapped, ok := legacyNodeTypes[req.NodeType]; ok {
		req.NodeType = mapped
	}

	// Extract credential_id and pass it to the CLI via --credential so the
	// subprocess resolves the stored tokens internally against the same DB.
	// Never merge plaintext credential data into --config: process arguments
	// are world-readable to other local processes (ps / /proc/<pid>/cmdline).
	credID, _ := req.Config["credential_id"].(string)
	delete(req.Config, "credential_id")

	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return NodeRunResult{Error: err.Error()}
	}

	configBytes, err := json.Marshal(req.Config)
	if err != nil {
		return NodeRunResult{Error: "invalid config: " + err.Error()}
	}

	items := req.Items
	if len(items) == 0 {
		items = []map[string]interface{}{}
	}
	inputItems := make([]map[string]interface{}, len(items))
	for i, it := range items {
		inputItems[i] = map[string]interface{}{"json": it}
	}
	inputBytes, err := json.Marshal(inputItems)
	if err != nil {
		return NodeRunResult{Error: "invalid input: " + err.Error()}
	}

	// Pass config + input via stdin, not argv: any secrets typed directly into
	// node fields (DB passwords, API keys) would otherwise be world-readable via
	// ps / /proc/<pid>/cmdline for the subprocess lifetime.
	payload, err := json.Marshal(map[string]json.RawMessage{
		"config": json.RawMessage(configBytes),
		"input":  json.RawMessage(inputBytes),
	})
	if err != nil {
		return NodeRunResult{Error: "encoding node payload: " + err.Error()}
	}

	start := time.Now()
	args := []string{
		"--profile", a.getActiveProfileID(),
		"node", "run", req.NodeType,
		"--stdin",
		"--output", "json",
	}
	if credID != "" {
		args = append(args, "--credential", credID)
	}
	cmd := exec.Command(cliBin, args...)
	cmd.Stdin = bytes.NewReader(payload)
	out, runErr := cmd.Output()
	elapsed := time.Since(start).Milliseconds()

	if runErr != nil {
		msg := runErr.Error()
		if exitErr, ok := runErr.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			msg = strings.TrimSpace(string(exitErr.Stderr))
		}
		return NodeRunResult{Error: msg, DurationMs: elapsed}
	}

	var raw map[string][]struct {
		JSON map[string]interface{} `json:"json"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return NodeRunResult{Error: "failed to parse output: " + err.Error(), DurationMs: elapsed}
	}

	var outputs []NodeRunOutput
	for handle, rawItems := range raw {
		flat := make([]map[string]interface{}, len(rawItems))
		for i, ri := range rawItems {
			flat[i] = ri.JSON
		}
		outputs = append(outputs, NodeRunOutput{Handle: handle, Items: flat})
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Handle < outputs[j].Handle })
	return NodeRunResult{Outputs: outputs, DurationMs: elapsed}
}
