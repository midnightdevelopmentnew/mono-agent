# Mono-Agent AI via Monomind Delegation — Capability & Transition Plan

- **Status**: Approved (v1 decisions locked 2026-08-24; v2 amendments 2026-08-25 from
  code-verified protocol review, marked ⓥ2; **v3 amendments 2026-08-25** from an independent
  re-verification pass against live code in both repos — one factual correction and six new risk
  items, marked ⓥ3, in D12 and §3.2/§5/§9. **Phase 0 implemented 2026-08-25** (monomind
  `agent-exec-protocol` worktree, commits 2accdcc27 + 76ec983d2 — protocol v1 rev 4, 51 tests,
  6 golden fixtures, version bumped to 2.10.0). **Phase 1 implemented 2026-08-25** (mono-agent
  `monomind-delegation-plan` worktree — `internal/monomind` Go client, `agent`+`chat` CLI,
  Wails agent-chat bindings, frontend runtime mode; all 33 Go packages green, canvas E2E
  verified against real monomind + stub codex runner)
- **Worktrees**: `mono-agent@.worktrees/monomind-delegation-plan` · `monomind@.worktrees/agent-exec-protocol`
- **Companion spec**: `monomind:doc/agent-exec-protocol.md` (public v1 protocol, rev 2, ships with Phase 0)

---

## 1. Goal

Replace every AI surface in mono-agent with **delegation to monomind**, which in turn hands off to
locally-installed AI agent CLIs (claude, codex, gemini, kimi, qwen, grok, crush, copilot, pi,
antigravity, opencode, …). Bring monomind's Org Runtime v2 (orgs, roles, handoffs, task DAG) to
mono-agent users natively through the same delegation. Remove all HTTP AI providers from
mono-agent.

**Doctrine (unchanged, strengthened):** `monoagentcli` holds all mono-agent logic; the Wails UI is
a runner of `monoagentcli`. monomind is "just another local process" mono-agent hands off to — the
single AI/agent engine. **Zero runner (agent-CLI) knowledge ever enters Go.**

## 2. Decisions register

| # | Decision |
|---|---|
| D1 | Remove **all** AI surfaces: `internal/ai` provider stack, `service.openrouter` node. ⓥ2 Tier-3 remote selector LLM (`apiv1.monoes.me`) is **delegated to `agent exec`** (default runtime) instead of removed — selector generation keeps working offline-first via local agents; the cache remains as fallback when no runtime is installed |
| D2 | **Full monomind delegation** — chat, workflow nodes, `agent_ask`, orgs all go through monomind subprocesses |
| D3 | **No orgrt port** — monomind is the org engine permanently; mono-agent wraps `org * --json` + reads state files read-only |
| D4 | Canvas chat kept via tool delegation — loop lives in monomind; CanvasTools SQL executes in monoagentcli over the stdio tool protocol. ⓥ2 Canvas tool definitions ship via `--tools-file` (JSON Schema), so native runners (claude) get real tool wiring and fence runners get rendered definitions — not prompt-only description |
| D5 | `ai.*` nodes → new `agent.ask` node + fail-fast deprecation (`ai.embed` dropped — no chat-CLI equivalent). ⓥ3 the fail-fast hint in §8 Phase 2's gate is written only for `ai.chat`; extend the same "workflow referencing `ai.X` fails with migration hint" gate to `ai.embed` explicitly, and audit shipped templates for `ai.embed` usage before Phase 2 (§9.14) |
| D6 | Orgs live project-local under monomind's `.monomind/orgs/` layout; mono-agent discovers via `org list --json` and spawns every org command with the **project root as cwd** (protocol §7.1) |
| D7 | `agent_ask(runtime, prompt, model?)` org tool added **in monomind orgrt** — in-process runner delegation for roles |
| D8 | `agent exec` stdio protocol is a **public, versioned spec** (`doc/agent-exec-protocol.md`, `v:1` frames, capability handshake, golden NDJSON fixtures for caller contract tests) |
| D9 | Distribution: **download-on-first-run** onboarding (system Node ≥20 → `npm i -g`; else platform bundle download); app stays small |
| D10 | New surfaces are CLI-first by construction; pre-existing UI doctrine violations are a separate workstream (§9.4) |
| D11 ⓥ2 | `agent exec/scan/test` join monomind's **existing** `agent` namespace (swarm lifecycle); the taken name `agent list` is NOT reused — installed-only view is `agent scan --installed` |
| D12 ⓥ3 | **Correction**: `org list` is NOT a new command — it already exists (`commands/org.ts:1354`, project-cwd-scoped, human-output only). Phase 0 M1 item 4 is reduced to "add `--json` to `org list`" (same as the other existing observe commands); **`org events` remains the only genuinely new org command** |

## 3. Current state (research findings — execution source of truth)

### 3.1 mono-agent AI surfaces today (all removed by D1)

| Surface | Location | Notes |
|---|---|---|
| Provider stack | `internal/ai/{client,registry,openai,anthropic,google,bedrock,store,migrate}.go` | `AIClient` interface; ~40-provider registry; 3 HTTP adapters + bedrock stub; SQLite `ai_providers` |
| ai.\* nodes (6) | `internal/ai/nodes/{chat,extract,classify,transform,embed,agent}.go`, wired at `internal/noderegistry/registry.go:43-48` | `ai.embed`/`ai.agent` are stubs/simplified |
| Canvas chat | `internal/ai/chat/{service,tools}.go`; Wails `app_ai.go:120-212`; frontend `AIChatPanel.jsx` in `AIProviders.jsx` | Runs in-process; 8 CanvasTools (`tools.go:82-227`) write workflows via SQL |
| OpenRouter node | `internal/nodes/service/openrouter.go` | Direct HTTP, key in node config |
| Tier-3 selector LLM | `internal/config/{apiclient.go, manager.go:283-315}` | Remote DOM-selector generation → ⓥ2 delegated to `agent exec`, cache fallback (D1) |
| CLI | `cmd/monoagentcli/ai.go` (`ai provider list/add/delete/test`), `root.go:151`, `secret_export.go:183-214` | `rematerializeProvider` must go |
| DB | `ai_providers`, `ai_chat_messages` tables; vault entries kind `ai_provider` | Migration in `initDB` ladder |

Precedents to keep (already local-agent-native): Gemini browser bot (`action.gemini.*`), Claude
skill install (`init --claude`), `crawl`→template flow. ⓥ3 **Caveat**: neither Gemini-bot (CDP
browser automation, no API key) nor `init --claude` (static skill-file copy) actually spawns a
peer CLI as an AI backend over stdio — they are not real precedent for the bidirectional
subprocess protocol below. There is **no existing local-agent-CLI-as-backend pattern** in
mono-agent today; Phase 0's protocol design should be treated as genuinely new ground, not an
extension of proven wiring (affects effort sizing in §8).

### 3.2 monomind engine facts (what mono-agent delegates to)

- **Org Runtime v2** lives in `packages/@monomind/cli/src/orgrt/` (~51 files): daemon, mailbox
  (async-queue prompt stream), bus (`bus.jsonl` ground truth), TaskDag (8 statuses), decision
  gates, tool approvals, `ask_human`, checkpoints (SHA-256 truncated to 16 hex chars/64-bit, not
  full digest — ⓥ3 corrected; 24h TTL confirmed), scheduler/serve, policy engine (globs, git
  levels, token+USD budgets — ⓥ3 this is the budget mechanism §9.10 wants `agent exec` to reuse),
  cross-org delivery (`org:role` addressing, broker HTTP, offline inbox), org memory, **12** ⓥ3
  `AgentRunner` impls (corrected from 13 — confirmed count: antigravity, codex, copilot, crush,
  grok, kimicode, opencode, pi, pi-rpc, qwen, qwen-rpc, vercel; `gemini`/`cursor` genuinely absent,
  matching D1/§4's "new runners" callout).
- **Runner contract** (`orgrt/agent-runner.ts:34-74`): `AgentRunArgs{tools: OrgToolDef[],
  prompt: AsyncIterable (mailbox stream), systemPrompt, model, cwd, env, maxTurns, resume,
  canUseTool, extras}` → `AsyncIterable<AgentMessage{type: assistant|result|tool_use,
  session_id, …usage}>`. ⓥ2 Two consequences for `agent exec`: (a) the one-shot `--prompt` is
  adapted into a single-message async stream; (b) tools are **in-process handlers** — with
  `--tools-file`, handler invocations are bridged to caller `tool_call`/`tool_result` frames, so
  SDK runners (claude) get native tool wiring + `canUseTool` gating instead of fence parsing.
- Subprocess runners: prompt-as-argv, NDJSON stdout, 2h turn timeout, 45s startup grace,
  SIGTERM→5s→SIGKILL, ENOENT install hints, fatal auth/quota classification, fence protocol
  (`tool-fence.ts`, `MAX_TOOL_ROUNDS=10`) for non-native-tool CLIs.
- **Existing `monomind agent` namespace** (`commands/agent.ts`): swarm lifecycle
  (`spawn/list/status/stop/metrics/pool/health`). `exec/scan/test` are added here; `list` is
  taken — see D11.
- **Key invariants mono-agent must respect** (not reimplement): exit 0 only on
  `closedBy=org-complete`; `runtime.json`/`bus.jsonl`/`gates.json`/`questions.json`/
  `approvals.json`/`inbox.jsonl` are monomind-owned — mono-agent reads them read-only, never
  writes.
- **Gap (why Phase 0 exists):** org commands emit human tables; `--json` is input-only today
  (verified: only `org inbox --json` payload input, `org-observe.ts:654`); `AgentRunner` has no
  external command surface; no `gemini` or `cursor` runner yet. ⓥ3 **Correction**: `org list`
  already exists (`org.ts:1354`) and just needs `--json` output added — only `org events` is
  genuinely missing (D12).
- ⓥ3 `daemon.ts:166` `resolveRunner(orgRuntime?, providerKind?, provider?): AgentRunner |
  undefined` can return **`undefined`** for the implicit Claude-default case (no explicit
  `ClaudeAgentRunner` branch found). `agent exec --runtime <id>` and `agent_ask` must treat "no
  matching runner resolved" as its own case — distinct from `missing-binary` — rather than
  assuming `resolveRunner` always yields a concrete runner.
- ⓥ3 Existing per-role token/USD budget enforcement already lives in `daemon.ts` (~lines 218-222,
  770-851) and gates org runs today. M2's `agent_ask` correctly bills through it (§5), but the
  **bare `monomind agent exec` CLI (Phase 0/1) has no cost cap at all** — `--max-turns` bounds
  turns, not spend. A user driving `chat`/`agent.ask` directly (outside an org) can run up
  unbounded cost with a verbose runner. See §9.10.

### 3.3 mono-agent CLI/UI contract facts

- UI→CLI subprocess pattern already proven: `RunWorkflow` (parses `Execution started: <id>`),
  `ExecuteAction`, `ExportData` (`--json` stdout), `app_vault.go` (`secret … --json`, documented
  doctrine-compliant), `RunNode` (`node run --stdin --output json`).
- Chat today violates doctrine: in-process `ChatService` + Wails events `ai:chunk`/`ai:tool`/
  `ai:error`. Phase 1 re-points the same events at a `chat` subprocess.
- Kill semantics: `runningCmds` tracks `*exec.Cmd` (`app_workflows.go:379-590`);
  CancelWorkflow kills or SIGTERMs DB pid. Must upgrade to **process-group kill** (setpgid /
  Windows Job Object) because monomind spawns agent-CLI grandchildren. ⓥ3 **Correction**: this is
  not an "upgrade" — a repo-wide search found **zero existing `Setpgid`/`SysProcAttr`/job-object
  code** anywhere in mono-agent today. Process-group kill for the monomind child (and its
  agent-CLI grandchild) is entirely new, platform-specific code; size Phase 1 accordingly and
  give it its own test gate rather than folding it into general chat/exec work (§8 Phase 1 gate
  already requires "zero orphan processes" — keep that, but budget the implementation as new).
- ⓥ3 Two **incompatible existing subprocess-IO idioms** already coexist in mono-agent:
  `app_vault.go`'s `runVaultCLI` does one-shot `cmd.Output()` + `json.Unmarshal` (no streaming, no
  stdin writes), while `RunWorkflow`/`RunNode` stream via `StdoutPipe` with JSON-per-line parsing.
  The new bidirectional protocol (NDJSON on stdout, `tool_result`/`cancel` frames written back on
  stdin) needs a **third idiom** — closer to the `RunWorkflow`/`RunNode` streaming pattern than to
  `app_vault.go`. State this explicitly in `internal/monomind/exec.go`'s design so implementers
  don't default to the simpler one-shot pattern, which cannot support tool-call round-trips.

## 4. Target architecture

```
┌────────────────────────── monoagentcli (domain logic, zero AI) ──────────────────────────┐
│  internal/monomind/          thin Go client (~800 LOC)                                   │
│   ├─ find.go   resolution: env → bundled → npm-global → PATH (+ capability handshake)    │
│   ├─ exec.go   spawn `monomind agent exec`, NDJSON stream, process-group kill            │
│   ├─ org.go    wrap `monomind org * --json` (cwd = project root); read state read-only   │
│   └─ types.go  versioned JSON contract types (mirror of doc/agent-exec-protocol.md)      │
│                                                                                          │
│  CLI: agent scan|test · chat --runtime <r> [--canvas <wf>] · org <subcommands>           │
│  agent.ask node · CanvasTools SQL (kept; executed in monoagentcli over stdio protocol)   │
└──────────────────────────────┬───────────────────────────────────────────────────────────┘
                               │ subprocess (NDJSON stdio, bidirectional for tools)
┌────────────────────────── monomind (THE engine — source of truth) ───────────────────────┐
│  agent exec --runtime <r> [--model] [--resume <sid>] [--tools stdio --tools-file f] …    │
│  agent scan --json · `--version --json` handshake · org … --json · org events --ndjson   │
│  orgrt untouched: daemon/mailbox/bus/TaskDag/gates/checkpoints/runners + agent_ask tool  │
└──────────────────────────────┬───────────────────────────────────────────────────────────┘
                               │ spawns
        claude · codex · gemini* · kimi · qwen(+rpc) · grok · crush · copilot · pi(+rpc) ·
        antigravity · opencode · cursor*          (* = new runners added in monomind)
```

Net effect: mono-agent **−~5k LOC AI code, +~800 LOC client**; monomind **+~1.5-2k LOC contract
layer**. One runner engine, one org engine, one place to fix agent-CLI wire drift.

## 5. Monomind-side changes

### M1 — machine contract (prerelease 2.10.0, Phase 0)

1. **`monomind agent exec`** (new command, e.g. `src/commands/agent-exec.ts`, added to the
   existing `agent` namespace): one-shot exposure of `AgentRunner` (resolved via existing
   `resolveRunner`, `daemon.ts:166`). Flags per `doc/agent-exec-protocol.md` rev 2: `--runtime`,
   `--prompt`/`--prompt-file`, `--system-file`, `--tools stdio|none`, `--tools-file`,
   `--tool-timeout`, `--model`, `--cwd`, `--resume`, `--max-turns` (default 25 — the orgrt
   unlimited default is NOT inherited), `--timeout` (overall; exit 124), `--env KEY=V`
   (repeatable), `--protocol`. Emits NDJSON events `v:1`: `start` (with `pid` + `child_pid`),
   `session`, `assistant`, `tool_call`, `tool_result`, `usage` (per-round delta), `result` (with
   machine-readable `stop_reason`), `error` (taxonomy: `auth`/`quota`/`missing-binary`/
   `runner-error`/`timeout`/`cancelled`/`bad-frame`), `done`. Adapts `--prompt` into the mailbox
   async-iterable; bridges `OrgToolDef` handler invocations to caller frames when `--tools-file`
   is given (native wiring on SDK runners, fence rendering otherwise).
2. **`monomind agent scan --json [--installed]`**: PATH probe for all runner binaries
   (+ `<NAME>_CLI_BIN` overrides), parallel `--version` probes with 5s per-binary timeout,
   per-agent `{id, installed, binary, version, install_hint}`. Auth status NOT probed — auth
   failures surface at exec time as `error {code:"auth", fatal:true}` with the runtime's login
   hint.
3. **`monomind --version --json`** handshake: `{version, min_caller, capabilities:
   ["agent-exec","agent-scan","org-json-v1"]}`.
4. **`--json` output** on existing org observe commands: `status, logs, report, costs, inbox,
   flow, questions, gates, decisions, memory, list` (+ action results for
   `answer/approve/deny/gate-approve/gate-reject`). ⓥ3 `list` moved here from "new commands" —
   it already exists (D12), only its `--json` flag is new. **Only genuinely new command**:
   `org events --ndjson [--follow] [--since]` (live tail of `bus.jsonl`). Org commands resolve the
   project by walking up from cwd (protocol §7.1).
5. Tests: scripted fake runner round-trip in both tool modes (extend `orgrt/test-loop.ts`
   pattern); `--json` snapshot tests; handshake test; **golden NDJSON transcripts** published at
   `doc/agent-exec-protocol/fixtures/*.ndjson` for caller-side contract tests.

### M2 — `agent_ask` org tool (Phase 4)

`agent_ask(runtime, prompt, model?)` in `orgrt/session.ts buildOrgTools`: sub-turn via
`resolveRunner` in-process, result text returns into the role's conversation; usage billed to the
calling role's budget; `tool` bus events for audit. Optionally add `gemini`/`cursor` runners here
(benefits both products; monomind work, never ported to Go).

### M3 — packaging (Phase 5)

Sidecar bundle `monomind-bundle-<os>-<arch>.tgz` = `dist/` + pruned prod `node_modules/` incl.
native addons + launcher. mono-agent prefers system Node ≥20; falls back to bundled pinned Node.
(True single-file builds are blocked by native addons: tree-sitter, better-sqlite3, LanceDB.)

## 6. Mono-agent-side changes

| Piece | Detail |
|---|---|
| `internal/monomind` | Discovery ladder + handshake + min-version pin; `Exec(ctx, args) (<-chan Event, stdin io.WriteCloser, error)`; process-group kill (setpgid / Job Object); overall timeout only (monomind owns 2h/45s ladders) |
| `monoagentcli chat` | `chat --runtime <r> [--model] [--resume] [--canvas <workflowID>] [--json]` wraps `agent exec`; `--canvas` ships the 8 CanvasTools as a `--tools-file` (JSON Schema generated from `internal/ai/chat/tools.go:82-227`), SQL execution stays in monoagentcli behind the stdio frame loop; history → `agent_chat_messages` (+`runtime`, `session_id`); NDJSON stdout contract is the UI interface |
| `monoagentcli agent` | `agent scan|test` = 1:1 proxies of monomind's (installed-only via `scan --installed`); UI Agents page replaces AIProviders |
| `agent.ask` node | `{runtime, model, prompt, output_key}` → synchronous `agent exec` inside the workflow engine; schema in `internal/workflow/schemas` |
| `org *` wrappers | Thin proxies to `monomind org … --json`, each spawned with **cwd = target project root** (D6); UI Orgs page (status/logs/costs/questions/gates/approve/deny) + live tail via `org events --ndjson --follow` |
| UI | Chat panel re-points to `chat` subprocess (same `ai:chunk`/`ai:tool`/`ai:error` events, cancel = group-kill); Agents page; Orgs page; onboarding card "AI engine: install/located ✓"; **auth-error surface**: `error {code:"auth"}` renders the runtime's login hint with a copyable command (ⓥ2) |
| Tier-3 selectors ⓥ2 | `generateConfig` (`manager.go:283-315`) calls `internal/monomind.Exec` with a selector-generation prompt instead of `apiv1.monoes.me`; cache consulted first, remote API deleted |
| DB migration | Drop `ai_providers`; rename `ai_chat_messages`→`agent_chat_messages`; export-then-purge vault `ai_provider` entries (backup written before delete); idempotent, in `initDB` ladder. ⓥ3 **No precedent**: mono-agent's entire migration ladder today is additive-only (`CREATE TABLE IF NOT EXISTS` / `ALTER TABLE ... ADD COLUMN`) — no `DROP TABLE` exists anywhere in the codebase. This migration is genuinely novel, not a proven pattern; see §9.13 |

## 7. Removal checklist (Phase 5, after parity proven)

- [ ] `internal/ai/{client,registry,openai,anthropic,google,bedrock,store,migrate}.go` + tests
- [ ] `internal/ai/nodes/*` (CanvasTools SQL salvaged first), `internal/ai/chat/*`
- [ ] `internal/noderegistry/registry.go:43-48` AI wiring → `agent.ask`
- [ ] `internal/nodes/service/openrouter.go` + schemas/templates referencing it
- [ ] `internal/config/apiclient.go` + `manager.go:283-315` remote call (replaced by `agent exec` delegation in Phase 2 ⓥ2)
- [ ] `cmd/monoagentcli/ai.go`; `root.go:151` migrate call; `secret_export.go:183-214`
- [ ] `wails-app/app_ai.go` rewrite (chat via subprocess only); `app.go:137-161` in-proc services
- [ ] Frontend: `AIProviders.jsx` → `Agents.jsx`; `api.js` AI section swap
- [ ] DB migration (§6); verify `rg -l 'internal/ai|openrouter|apiv1.monoes.me'` empty in code paths

## 8. Phases & verification gates

| Phase | Work | Gate |
|---|---|---|
| 0 | monomind M1 + public protocol spec + tests; tag 2.10.0 | `agent exec` round-trips with fake + 2 real runners in both tool modes; `--json` snapshots; handshake passes; **golden NDJSON fixtures published** ⓥ2 |
| 1 | mono-agent `internal/monomind` + `chat`/`agent` CLI + UI swap (parallel-run; providers present, unused) | **Contract tests consume Phase-0 golden fixtures** ⓥ2; UI chat works via ≥2 runtimes; canvas `tool_call` frames create/edit nodes via `--tools-file` (native path on claude, fence path on one non-native runner); group-kill leaves zero orphan processes |
| 2 | `agent.ask` node + `ai.*` deprecation + Tier-3 selector delegation ⓥ2 | `node run agent.ask` green; workflow referencing `ai.chat` **or `ai.embed`** ⓥ3 fails with migration hint; shipped templates audited for `ai.embed` usage; selector generation succeeds via `agent exec` with no network call to `apiv1.monoes.me`, and falls back to cache-only (no block) when no runtime is installed ⓥ3 |
| 3 | `org *` wrappers + Orgs UI | Live org visible in UI (status/logs/questions/approve); event tail streams; **org commands verified against orgs in ≥2 different project roots (cwd targeting)** ⓥ2 |
| 4 | monomind M2 (`agent_ask`) + release pinning | Role delegates via `agent_ask`; bus shows audit events |
| 5 | Rip-out (§7) + M3 packaging + onboarding | `go build ./... && go test ./...` green; zero provider refs; fresh-machine install works without preinstalled Node; ⓥ3 **DB migration tested against 3 fixtures** (fresh DB, populated `ai_providers`/`ai_chat_messages`, DB mid-Phase-1 parallel-run state) with restore-from-backup verified, as its own gate item — not implied by `go test` alone |

## 9. Risks & mitigations

1. **Agent-CLI wire-format drift** → contained entirely in monomind runners; spec'd formats were
   live-verified in monomind; fixes ship via `npm update monomind`, no mono-agent release.
2. **Bidirectional stdio protocol is new surface** → `v:1` frame field, `capabilities`
   handshake, public spec doc, golden fixtures + contract tests both sides (D8).
3. **Release coupling**: every mono-agent AI feature needs monomind ≥2.10 → pin + handshake fail
   with actionable install message.
4. **Doctrine erosion**: ~12 pre-existing UI bypasses (actions CRUD, HIL, connections, workflows)
   — out of scope here; tracked as separate workstream; **all new surfaces CLI-first** (D10).
5. **Process trees**: killing a chat/org must kill monomind + its agent-CLI grandchildren →
   process-group kill from Phase 1, gate-tested. (`start` event carries both `pid` and
   `child_pid` for diagnostics ⓥ2.)
6. **Windows**: orgrt/runners are unix-tested → chat/orgs marked best-effort until a dedicated
   pass; process-group kill uses Job Objects there.
7. **Bundle size**: ~120-250 MB sidecar with pinned Node → download-on-first-run, not embedded
   (D9).
8. **Per-runtime auth onboarding** ⓥ2: agent CLIs need their own logins; `scan` doesn't probe
   auth. Mitigation: `error {code:"auth", fatal:true}` carries the runtime's login hint, UI
   renders it as an actionable card; `agent test <id>` doubles as an auth smoke test.
9. **Native/fence capability split** ⓥ2: tool fidelity differs by runner (native wiring vs fence
   parsing, 10-round cap on fence runners). Mitigation: `--tools-file` gives both modes the same
   definitions and wire frames; canvas chats should default to a native runner when available
   (`scan --installed` ordering), fence runners remain fully functional for lighter flows.
10. **ⓥ3 No cost cap on bare `agent exec`**: `agent_ask` (M2) inherits the calling role's
    budget, but standalone `chat`/`agent.ask` (Phase 0/1/2 — the common case, no org involved) has
    only `--max-turns` as a limiter, not a spend cap. A verbose runner or a runaway tool loop can
    run up real cost with no ceiling. Mitigation: add an optional `--budget-usd` flag to
    `agent exec` that reuses orgrt's existing per-role budget/stop mechanism (`daemon.ts:770-851`)
    instead of mono-agent re-implementing cost tracking; monomind SIGTERMs the child and emits
    `error {code:"budget", fatal:true}` + `done` on breach.
11. **ⓥ3 Canvas tool args are untrusted input**: CanvasTools (`create_nodes`,
    `update_node_config`, `delete_nodes`, `connect_nodes`, `disconnect_nodes`, …) execute
    LLM-authored `tool_call` args as direct SQL mutations against the workflow DB. `--tools-file`
    gives the *runner* a JSON Schema, but the protocol doesn't require mono-agent to revalidate
    args against that same schema before executing SQL on its side of the wire. Mitigation:
    mono-agent's stdio frame loop must validate `tool_call.args` against its own copy of the
    schema before dispatch — treat the LLM output as untrusted, same as any other external input.
12. **ⓥ3 Process-group kill is new, not an upgrade**: zero existing `Setpgid`/job-object code in
    mono-agent today (verified by repo-wide search) — see §6 note. Size and gate Phase 1
    accordingly, especially the Windows Job Object path (already flagged best-effort in #6).
13. **ⓥ3 DB migration has no precedent**: mono-agent's migration ladder is additive-only today
    (no `DROP TABLE` anywhere). The `ai_providers` drop / `ai_chat_messages` rename is genuinely
    novel, not a proven pattern — needs its own 3-fixture test + verified restore-from-backup as
    a Phase 5 gate item (§8), independent of `go test ./...` passing.
14. **ⓥ3 `ai.embed` silent breakage**: D5 drops `ai.embed` with no chat-CLI equivalent, but the
    Phase 2 fail-fast/migration-hint gate (§8) was written for `ai.chat` only. Any workflow using
    `ai.embed` (e.g. RAG-style templates) would break with a bare node error instead of a
    migration hint unless explicitly included in the same gate — extended in §8 above.
15. **ⓥ3 Tier-3 selector delegation trade-off**: replacing a fast remote API call with a full
    local agent-CLI cold start (up to 45s startup grace per protocol §3) for a
    previously-invisible background feature (DOM selector generation) can visibly stall scraping
    on a cold cache, and now requires *some* local agent CLI installed+authenticated just for
    that path to work — a regression for users who haven't set one up yet. Mitigation: on
    first-call/no-runtime, fail fast to "cache only, no generation" rather than blocking on
    subprocess spin-up; document this as a known trade-off in Phase 2, not an unconditional win.

## 10. Reference index (research sources)

- monomind org runtime: `packages/@monomind/cli/src/orgrt/{types,daemon,session,agent-runner,
  mailbox,bus,task-dag,cross-org,decisions,approvals,questions,checkpoint,scheduler,policy}.ts`;
  commands `src/commands/org.ts` (~34 subcommands; ⓥ3 `org list` already existed pre-plan and
  gains `--json` in M1, `org events` is the only new command), `org-observe.ts`; existing
  `src/commands/agent.ts` swarm namespace; docs
  `doc/concepts/org-runtime.md`
- monomind runners: `src/orgrt/{codex,kimicode,antigravity,grok,qwen,qwen-rpc,crush,copilot,pi,
  pi-rpc,opencode,vercel}-runner.ts`, `tool-fence.ts` (`MAX_TOOL_ROUNDS=10`)
- mono-agent AI: `internal/ai/**`, `internal/ai/nodes/**`, `internal/ai/chat/**`,
  `wails-app/app_ai.go`, `cmd/monoagentcli/ai.go`
- mono-agent runner patterns: `wails-app/app_workflows.go` (RunWorkflow/CancelWorkflow,
  `runningCmds`), `app_vault.go` (doctrine-compliant CLI proxy), `app_nodes.go` (RunNode
  stdin/stdout)
- Protocol spec: `monomind:doc/agent-exec-protocol.md` (rev 2)
