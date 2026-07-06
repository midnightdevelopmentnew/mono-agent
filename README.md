<p align="center">
  <img src="assets/banner.png" alt="Mono Agent" width="600" />
</p>

<div align="center">

```
███╗   ███╗ ██████╗ ███╗   ██╗ ██████╗      █████╗  ██████╗ ███████╗███╗   ██╗████████╗
████╗ ████║██╔═══██╗████╗  ██║██╔═══██╗    ██╔══██╗██╔════╝ ██╔════╝████╗  ██║╚══██╔══╝
██╔████╔██║██║   ██║██╔██╗ ██║██║   ██║    ███████║██║  ███╗█████╗  ██╔██╗ ██║   ██║
██║╚██╔╝██║██║   ██║██║╚██╗██║██║   ██║    ██╔══██║██║   ██║██╔══╝  ██║╚██╗██║   ██║
██║ ╚═╝ ██║╚██████╔╝██║ ╚████║╚██████╔╝    ██║  ██║╚██████╔╝███████╗██║ ╚████║   ██║
╚═╝     ╚═╝ ╚═════╝ ╚═╝  ╚═══╝ ╚═════╝     ╚═╝  ╚═╝ ╚═════╝ ╚══════╝╚═╝  ╚═══╝   ╚═╝
```

**Multi-profile workflow automation engine for social platforms, AI services, image processing, and browser automation**

[![Go Version](https://img.shields.io/badge/Go-1.25-00ADD8?style=for-the-badge&logo=go&logoColor=white)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-purple?style=for-the-badge)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-macOS%20%7C%20Linux%20%7C%20Windows-333?style=for-the-badge&logo=github)](https://github.com/monoes/mono-agent)
[![Build](https://img.shields.io/badge/Build-CGO__FREE-00b894?style=for-the-badge&logo=docker&logoColor=white)](#)

<br/>

[![Instagram](https://img.shields.io/badge/Instagram-E4405F?style=flat-square&logo=instagram&logoColor=white)](#)
[![LinkedIn](https://img.shields.io/badge/LinkedIn-0077B5?style=flat-square&logo=linkedin&logoColor=white)](#)
[![X](https://img.shields.io/badge/X-000000?style=flat-square&logo=x&logoColor=white)](#)
[![TikTok](https://img.shields.io/badge/TikTok-010101?style=flat-square&logo=tiktok&logoColor=white)](#)
[![Telegram](https://img.shields.io/badge/Telegram-2CA5E0?style=flat-square&logo=telegram&logoColor=white)](#)
[![Slack](https://img.shields.io/badge/Slack-4A154B?style=flat-square&logo=slack&logoColor=white)](#)
[![Discord](https://img.shields.io/badge/Discord-5865F2?style=flat-square&logo=discord&logoColor=white)](#)
[![Google Sheets](https://img.shields.io/badge/Google%20Sheets-34A853?style=flat-square&logo=googlesheets&logoColor=white)](#)
[![OpenAI](https://img.shields.io/badge/OpenRouter-7B2FBE?style=flat-square&logo=openai&logoColor=white)](#)

</div>

---

## ✦ What is Mono Agent?

**Mono Agent** is a production-grade automation orchestration platform that combines:

- 🔁 **Visual workflow engine** — DAG-based execution with 70+ built-in node types
- 🌐 **Real browser automation** — Stealth Chrome control for social platforms via Rod
- 🤖 **AI-powered intelligence** — OpenRouter, HuggingFace, and Gemini integrations
- 🖥️ **Desktop GUI** — Wails 2 + React canvas workflow editor with AI canvas chat
- ⚡ **CLI-first** — 70+ commands for scripting, scheduling, and automation
- 👤 **Multi-profile isolation** — switch between named profiles; all data fully isolated per profile
- 🖼️ **Image Vault** — built-in image storage, labeling, and fullscreen editor
- 🤝 **Human-in-Loop** — pause workflows for human review/editing before continuing
- 📧 **Outlook integration** — read and send Outlook mail natively

Think of it as **n8n meets Playwright** — a fully self-hosted, code-first automation platform with a visual editor, multi-account profiles, and human-in-the-loop controls.

<br/>

<div align="center">

```
┌──────────────────────────────────────────────────────────────────────┐
│                         MONO AGENT STACK                             │
│                                                                      │
│   ┌──────────────────────┐      ┌──────────────────────────────┐    │
│   │     Desktop GUI      │      │        CLI (70+ cmds)        │    │
│   │  Wails + React       │      │   cobra · zerolog · tabwriter│    │
│   │  AI Canvas Chat      │      │   --profile flag on all cmds │    │
│   │  Image Vault + Editor│      │                              │    │
│   │  HIL Review Panel    │      │                              │    │
│   └──────────┬───────────┘      └──────────────┬──────────────┘    │
│              │                                 │                    │
│              └────────────────┬────────────────┘                    │
│                               ▼                                     │
│   ┌──────────────────────────────────────────────────────────────┐  │
│   │                     Profile Layer                            │  │
│   │   Named profiles · per-profile DB isolation · context        │  │
│   │   actions / people / workflows / vault / sessions / HIL      │  │
│   └──────────────────────────┬───────────────────────────────────┘  │
│                              │                                      │
│   ┌──────────────────────────▼───────────────────────────────────┐  │
│   │                   Workflow Engine                            │  │
│   │   DAG executor · Trigger manager · Expression eval          │  │
│   │   Hybrid store (JSON files + SQLite) · BFS scheduler        │  │
│   └──────────────────────────┬───────────────────────────────────┘  │
│                              │                                      │
│       ┌──────────────────────┼─────────────────────┐               │
│       ▼                      ▼                     ▼               │
│  ┌──────────┐        ┌────────────┐       ┌─────────────┐          │
│  │  Browser │        │  Services  │       │   Control   │          │
│  │   Nodes  │        │   Nodes    │       │    Nodes    │          │
│  │ Rod/CDP  │        │ 18 APIs    │       │ 15 types    │          │
│  └──────────┘        └────────────┘       └──────┬──────┘          │
│       │                     │                    │                 │
│       ▼                     ▼                    ▼                 │
│  Instagram         Google Sheets          if · filter              │
│  LinkedIn          OpenRouter             set · code (JS)          │
│  X / TikTok        HuggingFace            cron · webhook           │
│  Telegram          GitHub · Notion        split · merge            │
│                    Outlook (read/send)    human_in_loop ← NEW      │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────┐      │
│  │                    Image Vault                           │      │
│  │   Register · Resolve · Label · Background removal        │      │
│  │   Profile-scoped · Fullscreen editor · Metadata          │      │
│  └──────────────────────────────────────────────────────────┘      │
└──────────────────────────────────────────────────────────────────────┘
```

</div>

---

## ✦ Feature Highlights

<table>
<tr>
<td width="50%">

### 🔄 Workflow Engine
- DAG execution with cycle detection (Kahn's algorithm)
- Template expressions `{{variable.path}}` with dot-notation, array index, fallback chains
- Multi-input / multi-output nodes
- Webhook, cron, and manual triggers
- Full execution history with state machine
- Hybrid storage: JSON workflow files + SQLite

</td>
<td width="50%">

### 🌐 Browser Automation
- Real Chrome via Rod (Chrome DevTools Protocol)
- Stealth mode — evades bot detection
- Instagram, LinkedIn, X, TikTok fully supported
- Human-like delays, typing, scrolling
- AI-assisted XPath/selector generation (Gemini)
- 53 embedded action definitions compiled-in

</td>
</tr>
<tr>
<td width="50%">

### 🖥️ Visual Workflow Editor
- Drag-and-drop canvas (React + SVG)
- Schema-driven inspector with live field types
- `credential_picker` — unified credential dropdown
- `resource_picker` — Google Sheets / Drive resource selection
- `depends_on` — conditional field visibility
- Dark-themed, keyboard-navigable

</td>
<td width="50%">

### 🤖 AI Integrations
- **OpenRouter** — 200+ models via single API key
- **HuggingFace** — image generation, text generation
- **Google Gemini** — auto-generate CSS/XPath selectors
- AI-powered post captions from spreadsheet rows
- Image generation → Instagram post pipeline built-in

</td>
</tr>
<tr>
<td width="50%">

### 🔐 Unified Credentials
- All credentials in one `connections` table
- Social logins (browser sessions) auto-mirror to connections
- Stable IDs: `social:{platform}:{username}`
- Per-node credential resolution via `credential_id`
- Zero manual config to connect a social account

</td>
<td width="50%">

### ⚡ Pure Go, Zero CGO
- `modernc.org/sqlite` — no CGO, single binary
- Cross-compiles for macOS (Intel + ARM), Linux, Windows
- Embedded action JSON, schemas, and migrations
- 70+ node types ship with the binary
- Self-contained — no external runtime needed

</td>
</tr>
<tr>
<td width="50%">

### 👤 Multi-Profile Isolation
- Create named profiles (`default`, `client-a`, `work`, …)
- Switch active profile in CLI (`--profile`) or GUI sidebar
- **Full data isolation per profile**: actions, people, sessions, workflows, connections, vault images, HIL items, social lists, tags — all scoped to the active profile
- Running workflows from one profile are unaffected when you switch to another
- Profile ID carried through context for vault, workflow engine, AI tools, and all browser nodes

</td>
<td width="50%">

### 🖼️ Image Vault & Processing
- Dedicated Image Vault tab in the GUI — browse, label, and manage all generated/imported images
- Fullscreen editor with crop, resize, rotate, and filter tools
- Detail modal with metadata and workflow provenance
- AI background removal (Tier-1 image processing nodes)
- Images are profile-scoped — each profile sees only its own vault

</td>
</tr>
<tr>
<td width="50%">

### 🤝 Human-in-Loop Node (`core.human_in_loop`)
- Drop a HIL node anywhere in a workflow to pause execution and request human review
- **Read-only section** — shows context data (e.g. contact name, LinkedIn URL, profile info)
- **Editable section** — lets the reviewer edit content before it proceeds (e.g. message text, images)
- **Rich text editor** built into the GUI review panel
- Configurable field visibility per node
- Approve → workflow resumes; Reject → item is dropped
- Optional timeout — auto-reject if no response within N minutes

</td>
<td width="50%">

### 🤖 AI Canvas Chat
- Conversational workflow builder inside the GUI
- Type natural-language instructions to create, modify, or explain workflows on the canvas
- AI generates nodes, wires connections, and sets config via chat
- Uses OpenRouter (any model) or Gemini
- Profile-aware — workflows created via chat are scoped to the active profile

</td>
</tr>
</table>

---

## ✦ Node Library

> 72+ built-in node types across 10 categories

<details>
<summary><strong>⚙️ Core Control (14 nodes)</strong></summary>

| Node | Description |
|------|-------------|
| `core.if` | Conditional branching — route items by expression |
| `core.switch` | Multi-way routing — up to N output handles |
| `core.set` | Assign or transform fields on items |
| `core.filter` | Keep only items matching a predicate |
| `core.code` | Execute JavaScript (Goja engine) on item stream |
| `core.merge` | Combine multiple input streams |
| `core.split_in_batches` | Chunk items into N-size groups |
| `core.wait` | Pause execution for N seconds |
| `core.limit` | Keep first N items |
| `core.sort` | Sort items by key ascending/descending |
| `core.remove_duplicates` | Deduplicate items by key |
| `core.compare_datasets` | Diff two item streams |
| `core.aggregate` | Sum, avg, count, min, max over a field |
| `core.stop_error` | Halt workflow with a custom error message |
| `core.human_in_loop` | Pause execution — human reviews read-only data, edits content, then approves or rejects |

</details>

<details>
<summary><strong>🔗 Services (16 nodes)</strong></summary>

| Node | Description |
|------|-------------|
| `service.google_sheets` | Read rows, append, update, clear ranges |
| `service.gmail` | Send and read Gmail messages |
| `service.google_drive` | File operations on Google Drive |
| `service.openrouter` | Generate text or images via 200+ AI models |
| `service.huggingface` | HuggingFace inference (text + images) |
| `service.github` | Issues, PRs, repos, and more |
| `service.notion` | Pages, databases, blocks |
| `service.airtable` | Records, bases, fields |
| `service.linear` | Issues, projects, teams |
| `service.jira` | Issues, sprints, projects |
| `service.asana` | Tasks, projects, teams |
| `service.stripe` | Payments, customers, subscriptions |
| `service.shopify` | Products, orders, customers |
| `service.salesforce` | CRM objects and records |
| `service.hubspot` | Contacts, deals, companies |

</details>

<details>
<summary><strong>📣 Communication (7 nodes)</strong></summary>

`comm.email_send` · `comm.email_read` · `comm.slack` · `comm.telegram` · `comm.discord` · `comm.twilio` · `comm.whatsapp` · `comm.outlook_read` · `comm.outlook_send`

</details>

<details>
<summary><strong>🗄️ Database (4 nodes)</strong></summary>

`db.mysql` · `db.postgres` · `db.mongodb` · `db.redis`

</details>

<details>
<summary><strong>🌐 HTTP & Network (3 nodes)</strong></summary>

`http.request` · `http.ftp` · `http.ssh`

</details>

<details>
<summary><strong>🔧 Data Transformation (8 nodes)</strong></summary>

`data.datetime` · `data.crypto` · `data.html` · `data.xml` · `data.markdown` · `data.spreadsheet` · `data.compression` · `data.write_binary_file`

</details>

<details>
<summary><strong>📱 Social Platform Actions</strong></summary>

| Platform | Actions |
|----------|---------|
| **Instagram** | `publish_post` · `like_posts` · `comment_on_posts` · `send_dms` · `auto_reply_dms` · `follow_users` · `unfollow_users` · `bulk_following` · `keyword_search` · `hashtag_search` · `story_interactions` |
| **LinkedIn** | `publish_post` · `list_user_posts` · `list_post_comments` · `like_post` · `comment_on_post` · `keyword_search` · `bulk_following` · `export_followers` · `send_connection_request` |
| **X (Twitter)** | `publish_post` · `like_posts` · `comment_on_posts` · `follow_users` · `keyword_search` · `bulk_following` · `send_dms` |
| **TikTok** | `publish_post` · `like_video` · `comment_on_video` · `follow_user` · `list_user_videos` · `list_video_comments` · `stitch_video` · `duet_video` · `share_video` |

</details>

<details>
<summary><strong>⏰ Triggers (3 types)</strong></summary>

| Trigger | Description |
|---------|-------------|
| `trigger.schedule` | Cron expression — `0 9 * * *` every day at 9am |
| `trigger.webhook` | HTTP endpoint — fire workflow on POST |
| `trigger.manual` | One-click run from CLI or GUI |

</details>

---

## ✦ Workflow Example

The **Instagram Daily Post** workflow — reads captions from Google Sheets, generates images with AI, posts to Instagram automatically:

```
[Schedule: 9am daily]
        │
        ▼
[Google Sheets: read_rows]   ← reads pending posts from spreadsheet
        │
        ▼
[Filter: status == "pending"]
        │
        ▼
[Limit: 1]                   ← one post per run
        │
        ▼
[Set: build prompt]          ← constructs image generation prompt
        │
        ▼
[HuggingFace: generate_image] ← FLUX.1-schnell text-to-image
        │
        ▼
[OpenRouter: generate_text]   ← writes caption via Claude/GPT
        │
        ▼
[Instagram: publish_post]     ← posts image + caption
        │
        ▼
[Google Sheets: update_rows]  ← marks row as "posted"
```

```json
{
  "id": "instagram-daily-post",
  "name": "Instagram Daily Post",
  "nodes": [
    { "id": "n1", "type": "trigger.schedule", "config": { "cron": "0 9 * * *" } },
    { "id": "n2", "type": "service.google_sheets", "config": {
        "operation": "read_rows", "use_header_row": true, "spreadsheet_id": "YOUR_SHEET_ID"
    }},
    { "id": "n3", "type": "core.filter",  "config": { "condition": "{{item.status}} == pending" } },
    { "id": "n4", "type": "core.limit",   "config": { "max": 1 } },
    { "id": "n5", "type": "service.huggingface", "config": {
        "operation": "generate_image", "prompt": "{{item.image_prompt}}", "credential_id": "YOUR_HF_CRED"
    }},
    { "id": "n6", "type": "service.openrouter", "config": {
        "operation": "generate_text", "model": "anthropic/claude-3-haiku",
        "prompt": "Write an Instagram caption for: {{item.topic}}", "credential_id": "YOUR_OR_CRED"
    }},
    { "id": "n7", "type": "action.instagram.publish_post", "config": {
        "credential_id": "social:instagram:yourusername",
        "text": "{{item.caption}}", "media": "{{item.image_path}}"
    }},
    { "id": "n8", "type": "service.google_sheets", "config": {
        "operation": "update_rows", "range": "{{item._row_range}}"
    }}
  ]
}
```

---

## ✦ Human-in-Loop Example

The **LinkedIn Outreach with Review** workflow — generates personalised messages, pauses for a human to review and edit each one, then sends only approved messages:

```
[Google Sheets: read_rows]     ← list of prospects
        │
        ▼
[OpenRouter: generate_text]    ← draft personalised LinkedIn message
        │
        ▼
[core.human_in_loop]           ← PAUSE — reviewer sees:
        │                          Read-only: name, company, LinkedIn URL
        │                          Editable:  message text
        │                        Approve → continue  |  Reject → drop item
        ▼
[action.linkedin.send_connection_request]  ← sends approved message
        │
        ▼
[Google Sheets: update_rows]   ← marks row "sent"
```

```json
{
  "id": "n3",
  "type": "core.human_in_loop",
  "config": {
    "readonly_fields": ["name", "company", "linkedin_url"],
    "editable_fields": ["message"],
    "timeout_minutes": 60
  }
}
```

---

## ✦ Getting Started

### Prerequisites

- Go 1.22+ (`brew install go`)
- Chrome/Chromium (for browser nodes)
- SQLite (bundled, no install needed)

### Install

```bash
# Clone
git clone https://github.com/monoes/mono-agent.git
cd mono-agent

# Build CLI
go build -o monoagentcli ./cmd/monoagentcli

# Or install globally
go install ./cmd/monoagentcli@latest
```

### First Run

```bash
# Check version
./monoes version

# Login to Instagram (opens browser)
./monoes login instagram

# List available workflow node types
./monoes node list

# Run a workflow
./monoes workflow run --id instagram-daily-post

# Watch mode (run every 30 seconds)
./monoes run --watch --interval 30s
```

### Desktop GUI

```bash
cd wails-app

# Install Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Run in development mode
wails dev

# Build desktop app
wails build
```

---

## ✦ CLI Reference

<details>
<summary><strong>Workflow Commands</strong></summary>

```bash
monoes workflow list                        # List all workflows
monoes workflow get <id>                    # Get workflow details
monoes workflow create --name "My Flow"     # Create new workflow
monoes workflow import --file flow.json     # Import from JSON
monoes workflow export --id <id>            # Export to JSON
monoes workflow run --id <id>               # Execute workflow
monoes workflow activate --id <id>          # Enable triggers
monoes workflow deactivate --id <id>        # Disable triggers
monoes workflow executions --id <id>        # View run history
monoes workflow node add --workflow <id> --type service.google_sheets
monoes workflow connect --from n1 --to n2 --workflow <id>
monoes workflow migrate --action <id>       # Migrate legacy action
```

</details>

<details>
<summary><strong>Node Commands</strong></summary>

```bash
monoes node list                            # List registered node types
monoes node run --type action.instagram.publish_post \
  --config '{"credential_id":"social:instagram:user","text":"Hello!"}'
```

</details>

<details>
<summary><strong>Auth & Connections</strong></summary>

```bash
monoes login instagram                      # Browser login (saves session)
monoes login linkedin
monoes login status                         # Show all active sessions
monoes connect list                         # List API credentials
monoes connect test --id <cred-id>          # Test a credential
monoes connect remove --id <cred-id>        # Remove credential
```

</details>

<details>
<summary><strong>Data & People</strong></summary>

```bash
monoes search --platform instagram --keyword "coffee lovers"
monoes people list                          # List saved contacts
monoes people import --file contacts.csv    # Import from CSV
monoes list create --name "Leads Q1"        # Create a list
monoes list add-item --list <id> --url https://instagram.com/user
monoes export --platform instagram --format csv
```

</details>

<details>
<summary><strong>Scheduling</strong></summary>

```bash
monoes schedule add --action <id> --cron "0 9 * * *"
monoes schedule list
monoes schedule remove --id <id>
```

</details>

<details>
<summary><strong>Profiles</strong></summary>

Every command accepts `--profile <name>` to scope operations to a named profile. All user data (actions, people, workflows, sessions, vault images, connections, HIL items, social lists, tags) is fully isolated per profile.

```bash
# Use a named profile (default is "default")
monoes --profile work workflow list
monoes --profile client-a login instagram
monoes --profile work run --workflow instagram-daily-post

# The GUI lets you switch profiles from the sidebar
# Switching profiles does NOT stop workflows running under other profiles
```

</details>

<details>
<summary><strong>Image Vault</strong></summary>

```bash
# Images are registered automatically when workflow nodes generate them.
# Browse, label, and manage images from the GUI Image Vault tab.
# Via CLI:
monoes workflow run --id my-image-workflow   # vault images are saved to active profile
```

</details>

---

## ✦ Architecture

```
mono-agent/
│
├── cmd/monoagentcli/        # CLI entry point (Cobra, 70+ commands)
│   ├── main.go
│   ├── workflow.go          # workflow subcommands + engine builder
│   ├── node.go              # node run + registry builder
│   ├── login.go             # browser session auth
│   ├── ref.go               # --profile flag wiring
│   ├── init.go              # project init
│   └── crawl.go             # crawler commands
│
├── internal/
│   ├── workflow/            # Core workflow engine
│   │   ├── engine.go        # WorkflowEngine — orchestration + profile isolation
│   │   ├── models.go        # Workflow, WorkflowNode, … (ProfileID field)
│   │   ├── storage.go       # SQLiteWorkflowStore — profile-scoped CRUD
│   │   ├── hybrid_store.go  # Hybrid (JSON files + SQLite)
│   │   ├── dag.go           # Topological sort, cycle detection
│   │   ├── execution.go     # Run state machine
│   │   ├── expression.go    # {{template}} evaluation
│   │   ├── trigger.go       # Cron / webhook triggers
│   │   └── schemas/         # 70+ embedded JSON schemas
│   │
│   ├── nodes/               # Node executor implementations
│   │   ├── browser_adapter.go  # BrowserNode → ActionExecutor bridge
│   │   ├── control/
│   │   │   ├── human_in_loop.go  # core.human_in_loop — pause + review
│   │   │   └── ...              # if, set, filter, code, merge…
│   │   ├── service/         # google_sheets, openrouter, huggingface…
│   │   ├── http/            # request, ftp, ssh
│   │   ├── db/              # mysql, postgres, mongodb, redis
│   │   ├── comm/            # email, slack, telegram, discord, outlook
│   │   ├── data/            # datetime, html, xml, crypto…
│   │   ├── people/          # save.go — profile-scoped contact upsert
│   │   └── system/          # execute_command, rss_read
│   │
│   ├── vault/               # Image Vault
│   │   └── vault.go         # Register/Resolve/ProfileIDFromContext
│   │
│   ├── ai/chat/             # AI Canvas Chat
│   │   ├── service.go       # ChatService + SetProfileID
│   │   └── tools.go         # CanvasTools — AI creates workflows
│   │
│   ├── action/              # Legacy browser action executor
│   │   ├── executor.go      # Step runner with variable resolution
│   │   ├── steps.go         # navigate, click, type, extract, loop…
│   │   └── variables.go     # {{path.to.variable}} resolver
│   │
│   ├── bot/                 # Platform browser adapters
│   │   ├── instagram/
│   │   ├── linkedin/
│   │   ├── tiktok/
│   │   └── x/
│   │
│   ├── connections/         # Unified credential storage
│   │   ├── storage.go       # Connection CRUD — profile-scoped Delete
│   │   ├── manager.go       # OAuth, API key, browser auth flows
│   │   └── registry.go      # Platform definitions + auth methods
│   │
│   ├── config/              # Profile config + AI-assisted selector generation
│   ├── scheduler/           # Cron scheduler wrapper
│   └── storage/             # DB init + migrations (profile_id columns)
│
├── wails-app/               # Desktop GUI
│   ├── app.go               # Wails App struct — all RPC methods, activeProfileID
│   └── frontend/src/
│       ├── pages/
│       │   ├── Workflow.jsx     # Visual workflow canvas editor
│       │   ├── ImageVault.jsx   # Image Vault browser + management
│       │   └── Settings.jsx     # Profile management + app settings
│       └── components/
│           ├── ImageDetailModal.jsx       # Image metadata + provenance
│           ├── ImageEditorPanel.jsx       # Inline image editor
│           ├── ImageEditorFullscreen.jsx  # Fullscreen editor with tools
│           └── Sidebar.jsx               # Profile switcher + navigation
│
└── data/actions/            # 53 embedded JSON action definitions
    ├── instagram/
    ├── linkedin/
    ├── tiktok/
    └── x/
```

---

## ✦ Tech Stack

| Layer | Technology |
|-------|-----------|
| **Language** | Go 1.25 (zero CGO) |
| **Browser** | [go-rod/rod](https://github.com/go-rod/rod) — Chrome DevTools Protocol |
| **Stealth** | [go-rod/stealth](https://github.com/go-rod/stealth) — anti-detection |
| **CLI** | [spf13/cobra](https://github.com/spf13/cobra) |
| **Logging** | [rs/zerolog](https://github.com/rs/zerolog) |
| **Database** | [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure Go |
| **Scheduling** | [robfig/cron](https://github.com/robfig/cron) |
| **JS Engine** | [dop251/goja](https://github.com/dop251/goja) — ECMAScript 5.1+ |
| **HTML** | [goquery](https://github.com/PuerkitoBio/goquery) |
| **Desktop GUI** | [Wails v2](https://wails.io) + React 18 |
| **AI APIs** | OpenRouter · HuggingFace · Google Gemini |
| **IDs** | [google/uuid](https://github.com/google/uuid) |

---

## ✦ Roadmap

**Recently shipped**
- [x] Multi-profile isolation — all user data scoped per named profile
- [x] Image Vault — built-in image storage, labeling, fullscreen editor
- [x] Human-in-Loop node (`core.human_in_loop`) — pause + review + approve/reject
- [x] AI Canvas Chat — conversational workflow builder in the GUI
- [x] Outlook integration — read and send email natively
- [x] AI background removal — Tier-1 image processing nodes

**Coming next**
- [ ] More trigger types — email, file watcher, database change
- [ ] Workflow versioning and rollback
- [ ] Sub-workflow / reusable workflow node
- [ ] Visual debugger — step-through execution in GUI
- [ ] Marketplace — shareable workflow templates
- [ ] WhatsApp & WeChat platform bots
- [ ] Metrics dashboard — success rates, throughput, latency per profile

---

## ✦ Contributing

Pull requests are welcome. For major changes, open an issue first.

```bash
# Run tests
go test ./...

# Run integration tests (requires Chrome)
go test -tags integration ./...

# Lint
go vet ./...
```

---

<div align="center">

Made with ☕ by [nokhodian](https://github.com/nokhodian)

</div>
