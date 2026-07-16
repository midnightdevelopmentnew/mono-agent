# JSON Action Definitions

Action definitions are embedded at compile time via `data/embed.go` and loaded by `internal/action/loader.go`.

## Directory Structure

```
actions/
├── instagram/
│   ├── BULK_MESSAGING.json
│   ├── BULK_REPLYING.json
│   ├── KEYWORD_SEARCH.json
│   ├── POST_COMMENTING.json
│   ├── POST_LIKING.json
│   ├── PROFILE_FETCH.json
│   ├── PROFILE_INTERACTION.json
│   ├── PROFILE_SEARCH.json
│   ├── PUBLISH_CONTENT.json
│   └── USER_POSTS_INTERACTION.json
├── linkedin/
│   ├── BULK_MESSAGING.json, BULK_REPLYING.json, KEYWORD_SEARCH.json
│   ├── PROFILE_FETCH.json, PROFILE_INTERACTION.json
│   ├── PROFILE_SEARCH.json, PUBLISH_CONTENT.json
├── tiktok/
│   └── (same action types as linkedin)
├── x/
│   └── (same action types as linkedin)
├── hackernews/
│   ├── submit_post.json, reply_to_comment.json
│   ├── list_comments.json, get_post_metrics.json
├── producthunt/
│   ├── comment_on_launch.json, list_comments.json
│   └── get_launch_metrics.json
```

File naming: `actions/<platform_lowercase>/<ACTION_TYPE_UPPERCASE>.json`

## JSON Schema

```json
{
  "actionType": "ACTION_TYPE",
  "platform": "PLATFORM",
  "version": "1.0.0",
  "description": "Human-readable description",
  "metadata": {
    "requiresAuth": true,
    "supportsPagination": false,
    "supportsRetry": true
  },
  "inputs": {
    "required": ["field1", "field2"],
    "optional": ["field3"]
  },
  "outputs": {
    "success": ["output1", "output2"],
    "failure": ["error1"]
  },
  "steps": [...],
  "loops": [...],
  "errorHandling": {
    "globalRetries": 3,
    "retryDelay": 2000,
    "onFinalFailure": "log_and_continue"
  }
}
```

## Step Types

### Navigation
- `navigate` — Navigate to a URL (`url`, `waitFor`, `timeout`)
- `wait` — Wait for condition or duration (`duration`, `waitFor: "time"`)
- `refresh` — Refresh current page

### Element Interaction
- `find_element` — Locate element (`xpath`, `selector`, `configKey`, `alternatives[]`, `timeout`)
- `click` — Click element (`elementRef`, `waitFor`)
- `type` — Type text (`elementRef`, `text`, `humanLike`)
- `scroll` — Scroll to element (`elementRef`, `direction`)
- `hover` — Hover over element

### Data Extraction
- `extract_text` — Extract text from element
- `extract_attribute` — Extract attribute value
- `extract_multiple` — Extract list of items

### Control Flow
- `condition` — Branch on condition (`condition`, `then[]`, `else[]`)
- `log` — Log a message (`description`, `value`)

### State Management
- `update_progress` — Update variables (`set: {key: value}`)
- `save_data` — Persist extracted data
- `mark_failed` — Mark item as failed

### Bot Method Delegation (Preferred for complex interactions)
- `call_bot_method` — Call a Go method on the platform's BotAdapter

```json
{
  "id": "like_post",
  "type": "call_bot_method",
  "methodName": "like_post",
  "args": ["{{item.url}}"],
  "variable_name": "likeResult",
  "timeout": 30,
  "onError": { "action": "mark_failed" },
  "onSuccess": { "action": "update_progress", "increment": "reachedIndex" }
}
```

The executor auto-prepends the Rod `*Page` as the first arg. Bot methods are registered via `GetMethodByName()` in each platform's bot.go.

**Use `call_bot_method` when**: DOM selectors are fragile, multi-step verification is needed, or the interaction requires programmatic logic (retries, fallback strategies, state checks).

## Variables

Template syntax: `{{variable}}` or `{{item.field}}`

| Variable | Description |
|----------|-------------|
| `{{item.url}}` | Current loop item's URL |
| `{{item.platform}}` | Current loop item's platform |
| `{{messageText}}` | Message text input |
| `{{commentText}}` | Comment text input |
| `{{searchKeyword}}` | Search keyword |
| `selectedListItems` | Array of `{url, platform, status}` to iterate |
| `reachedIndex` | Current loop progress index |

## Error Handling

Per-step:
```json
{
  "onError": {
    "action": "retry",       // retry | try_alternative | mark_failed | skip | abort
    "maxRetries": 3,
    "onFailure": "mark_failed"
  }
}
```

Global (action-level):
```json
{
  "errorHandling": {
    "globalRetries": 3,
    "retryDelay": 2000,
    "onFinalFailure": "log_and_continue"
  }
}
```

## Loops

```json
{
  "loops": [{
    "id": "process_items",
    "iterator": "selectedListItems",
    "indexVar": "reachedIndex",
    "steps": ["step1", "step2", "step3"],
    "onComplete": "update_action_state"
  }]
}
```

## Adding a New Action

1. Create `actions/<platform>/<ACTION_TYPE>.json` following the schema above
2. If using `call_bot_method`, implement the Go method in `internal/bot/<platform>/bot.go`
3. Add integration test in `internal/action/<platform>_integration_test.go`
4. Run `go build ./...` to verify (embedded FS picks up new files automatically)
