---
name: action-template-generator
description: Generate monoagent ActionDef JSON templates from captured HTML. Analyzes page structure using Claude's built-in reasoning to produce declarative step definitions that can be installed and run without any API key. Invoke when the user wants to crawl, scrape, or automate a new website or social media platform not already supported by monoagent.
---

# Action Template Generator

You are generating a **monoagent ActionDef JSON template** by analyzing captured HTML from a web or social media profile page. The output is a JSON file that monoagent's browser automation engine executes — no AI is needed at runtime.

## Context

This skill is invoked after the user runs:
```bash
monoagent crawl <url>
# or
monoagent action template capture <url>
```

That command opens a real browser, renders the page (including JavaScript), and saves the full DOM to `~/.monoes/captures/<domain>_<timestamp>.html`.

## Your task

1. Ask the user for the captured HTML file path (printed by `monoagent crawl` or `monoagent action template capture`)
2. Read the file with the Read tool
3. Analyze the HTML structure to identify data elements
4. Generate a valid ActionDef JSON template
5. Save to `~/.monoes/actions/<platform>/<action_type>.json`
6. Tell the user: `monoagent action template install <path>` then `monoagent node run <platform>.<type>`

---

## ActionDef JSON Schema

```json
{
  "actionType": "scrape_profile_info",
  "platform": "example",
  "version": "1.0.0",
  "description": "Scrape profile info from example.com",
  "metadata": {
    "author": "generated",
    "tags": ["scrape", "profile"]
  },
  "inputs": {
    "required": [
      { "name": "url", "type": "string", "description": "Profile URL to scrape" }
    ],
    "optional": []
  },
  "outputs": {
    "profile": ["username", "bio", "followers"]
  },
  "steps": [],
  "errorHandling": {
    "globalRetries": 2,
    "retryDelay": 3000,
    "onFinalFailure": "mark_failed"
  }
}
```

---

## Step Types — DECLARATIVE ONLY

**CRITICAL**: Never use `call_bot_method`. That only works for compiled-in platforms (instagram, linkedin, tiktok, x, gemini). For new platforms, use only these declarative steps:

### navigate
```json
{ "type": "navigate", "url": "{{item.url}}", "waitFor": "networkidle" }
```

### wait
```json
{ "type": "wait", "duration": 2000 }
```

### find_element
```json
{ "type": "find_element", "xpath": "//h1[@data-testid='username']", "as": "usernameEl", "optional": false }
```

### extract_text
```json
{ "type": "extract_text", "xpath": "//span[@data-testid='bio']", "as": "bio", "trim": true }
```

### extract_attribute
```json
{ "type": "extract_attribute", "xpath": "//img[@data-testid='avatar']", "attribute": "src", "as": "avatarUrl" }
```

### extract_multiple
```json
{
  "type": "extract_multiple",
  "xpath": "//div[@data-testid='post-item']",
  "as": "postItems",
  "limit": 12,
  "fields": {
    "imageUrl": { "attribute": "data-src" },
    "caption":  { "text": true },
    "link":     { "attribute": "href" }
  }
}
```

### click
```json
{ "type": "click", "xpath": "//button[@aria-label='Load more']" }
```

### scroll
```json
{ "type": "scroll", "direction": "down", "amount": 500 }
```

### type
```json
{ "type": "type", "xpath": "//input[@name='search']", "value": "{{inputs.keyword}}" }
```

### condition
```json
{ "type": "condition", "check": "{{bio}} != ''", "then": [], "else": [] }
```

### log
```json
{ "type": "log", "message": "Found: {{username}}" }
```

### save_data
```json
{
  "type": "save_data",
  "data": {
    "username":  "{{username}}",
    "bio":       "{{bio}}",
    "followers": "{{followers}}"
  }
}
```

### mark_failed
```json
{ "type": "mark_failed", "reason": "Profile not found or private" }
```

### Per-step error handling
```json
{ "type": "extract_text", "xpath": "...", "as": "followers", "onError": { "action": "skip", "default": "0" } }
```
`action` options: `"retry"`, `"skip"`, `"mark_failed"`, `"abort"`

---

## Variable Reference

| Variable | Source |
|----------|--------|
| `{{item.url}}` | URL from action target |
| `{{item.platform}}` | Platform from action target |
| `{{inputs.keyword}}` | Named input from action |
| `{{selectedListItems}}` | Total target count |
| `{{reachedIndex}}` | Current target index |
| `{{varName}}` | Any variable set via `as:` |

---

## XPath Rules

1. **Never use `@class`** — class names change on deploy. Use `@data-testid`, `@aria-label`, `@role`, `@id`, `@name`
2. **One element only** — each XPath must match exactly one element
3. **No hardcoded content** — no usernames, specific IDs, text values
4. **Structural over positional** — `//section//h2[1]` beats `//h2[3]`
5. Priority: `@data-testid` > `@aria-label` > `@role` > positional

---

## Output Instructions

1. Read the HTML file
2. Detect the platform from domain or `<meta property="og:site_name">`
3. Identify data elements: name, bio, counts (followers/following/posts), avatar, links
4. Build XPaths following the rules above
5. Write the complete ActionDef JSON
6. Save to `~/.monoes/actions/<platform>/scrape_profile_info.json`
7. Print:
```
Template saved: ~/.monoes/actions/<platform>/scrape_profile_info.json

Install:  monoagent action template install ~/.monoes/actions/<platform>/scrape_profile_info.json
Run:      monoagent node run <platform>.scrape_profile_info
```
