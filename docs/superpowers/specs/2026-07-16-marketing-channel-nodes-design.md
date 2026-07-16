# Marketing Channel Nodes — Design Spec

**Date:** 2026-07-16
**Status:** Approved

---

## Overview

Add node support for six marketing/growth channels referenced in the monomind-growth org's
channel strategy docs (`monomind` repo, `.monomind/orgs/monomind-growth/workspace/channels/`)
plus Product Hunt: **Reddit, YouTube, Bluesky, Mastodon, Hacker News, Product Hunt**.

Channels already covered by existing nodes are out of scope:
- **Discord** — `internal/nodes/comm/discord.go` (bot-token API, send_message/send_embed/get_channels)
- **Newsletter** — reuses existing `comm.email_send` / `service.gmail` / `service.outlook_mail`
- **Instagram / LinkedIn / TikTok / X** — already implemented (browser bot + action JSON pattern)

Scope is the callable node/action surface only — not workflow templates, not running any
actual campaign content.

---

## Routing: API vs. Browser

| Channel | Route | Why |
|---|---|---|
| Reddit | API | OAuth2 script-app API supports submit/comment/read |
| YouTube | API | YouTube Data API v3 supports upload, stats, comments |
| Bluesky | API | AT Protocol REST, app-password auth |
| Mastodon | API | Standard REST API, access-token auth |
| Hacker News | Browser | No public write API — submit/comment is web-form only |
| Product Hunt | Browser | Public API has no launch/comment-submission endpoint |

---

## API Nodes (Discord pattern)

Each is a single Go file in `internal/nodes/comm/`, config-driven dispatch on an `operation`
field, registered in `internal/nodes/comm/register.go`, plus a matching schema JSON in
`internal/workflow/schemas/` and a connection-registry entry in `internal/connections/registry.go`
+ validator in `internal/connections/validate.go` (same shape as `validateDiscord`).

### `comm.reddit`
- **Auth:** OAuth2 script-app (client_id, client_secret, username, password OR refresh_token)
- **Operations:** `submit_post` (subreddit, title, text|url), `reply_to_comment` (thing_id, text),
  `list_comments` (post_id), `get_post_metrics` (post_id → score, num_comments)

### `service.youtube`
- **Auth:** OAuth2 access token (Google, YouTube Data API v3 scope)
- **Operations:** `upload_video` (title, description, tags, category_id, privacy_status,
  video_file_path, thumbnail_path?), `get_video_stats` (video_id → views, likes, comment_count),
  `list_comments` (video_id), `reply_to_comment` (comment_id, text)

### `comm.bluesky`
- **Auth:** identifier + app password (AT Protocol `com.atproto.server.createSession`)
- **Operations:** `create_post` (text, reply_to?, embed_link?), `get_post_metrics` (post_uri →
  like_count, repost_count)

### `comm.mastodon`
- **Auth:** instance base URL + access token
- **Operations:** `create_status` (text, visibility, in_reply_to_id?), `get_status_metrics`
  (status_id → favourites_count, reblogs_count)

---

## Browser Nodes (Instagram/LinkedIn/TikTok pattern)

Each gets a `internal/bot/<platform>/` package (`bot.go` + `actions.go`) implementing both
`bot.BotAdapter` and `action.BotAdapter.GetMethodByName`, a `data/actions/<platform>/*.json`
step-template file per action, and relies on the existing `RegisterBrowserNodes()` /
`action.GetLoader().ListAvailable()` mechanism for node-type registration — no new plumbing.
DOM selectors follow the three-tier fallback (`call_bot_method` → hardcoded XPath →
`configKey`/AI-assisted) per `THREE_TIER_FALLBACK.md`.

### `internal/bot/hackernews/`
- **Actions:** `submit_post` (title, url|text), `reply_to_comment` (item_id, text),
  `list_comments` (item_id), `get_post_metrics` (item_id → points, comment_count)

### `internal/bot/producthunt/`
- **Actions:** `comment_on_launch` (launch_url, text), `list_comments` (launch_url),
  `get_launch_metrics` (launch_url → upvotes, comment_count)

Product Hunt launch *submission* is a manual scheduled dashboard flow on PH's side and is
explicitly not automated — only comment engagement and metrics reading.

---

## Testing

Each API node gets a unit test with a mocked HTTP transport (matching existing `comm` node
test patterns). Each browser node's actions get JSON-template validation tests plus an
integration test skeleton matching `internal/action/instagram_integration_test.go`'s shape
(skipped without live credentials).

---

## Out of Scope

- Executing any of the actual marketing campaign content described in the channel docs
- Workflow templates/wiring these nodes into a specific automation
- Product Hunt launch submission automation
- YouTube Analytics API (only Data API v3 stats)
