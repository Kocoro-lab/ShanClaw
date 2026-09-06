# Kocoro (`kocoro` / `shan`)

**An AI cowork agent that lives on your Mac.**

See the [Kocoro product site](https://kocoro.ai/en/start/) for the current product demo.

Kocoro runs AI agents locally with full computer access — files, apps, browser, terminal, screen — and connects to your team's Slack / LINE / Feishu / Telegram channels via Shannon Cloud. Named agents with their own memory and tools, MCP-native, daemon-driven. The `shan` CLI is the runtime; **Kocoro Desktop** is the recommended way to use it.

> **What's open source here** — This repo is the Kocoro **engine + daemon**: the `shan` runtime that does the actual work (agent loop, local tools, permission engine, channel messaging, MCP, scheduling). It's fully usable on its own via the CLI, TUI, daemon HTTP API, and MCP. **Kocoro Desktop** — the native GUI app shown above — is a separate, closed-source product that runs *on top of* this daemon.

### Get Kocoro

- **[Download Kocoro Desktop (macOS)](https://kocoro.shannon.run/download/mac)** — DMG, the recommended way to use Kocoro
- **CLI only** — `npm install -g @kocoro/kocoro` (build-from-source and other options under [Installation](#installation))

> **Coming from Claude Code?** Kocoro Desktop can import your existing agents, skills, and instructions from `~/.claude/` in one click — preview-then-apply via the daemon's `/migrate/claude-code/*` endpoints.

In Kocoro Desktop you can also work *inside* a conversation: select any passage of a reply to annotate it with inline comments (they reach the agent alongside your next message and survive reload), open a **Side Chat** — a temporary aside with the same tools and approvals as the main conversation that vanishes when the app closes — or **branch** a finished turn into a new session, optionally under a different agent. All three are served by this daemon (`conversation_context_actions_v1`).

Built on **[Shannon](https://github.com/Kocoro-lab/Shannon)** — the open-source multi-agent framework that powers both the Shannon Cloud SaaS and the self-hosted Shannon Gateway.

## Contents

- [Installation](#installation) · [Updating](#updating) · [Setup](#setup) · [Requirements](#requirements)
- [Quick Start](#quick-start) · [One-Shot Examples](#one-shot-examples) · [Multi-step Cowork Recipes](#multi-step-cowork-recipes)
- [CLI Usage](#cli-usage) · [Voice Front Brain](#voice-front-brain) · [Commands](#commands)
- [Local Tools](#local-tools) · [Permission Engine](#permission-engine) · [Audit Logging](#audit-logging) · [Hooks](#hooks)
- [MCP Server](#mcp-server) · [MCP Client](#mcp-client)
- [Configuration](#configuration) · [Instructions & Memory](#instructions--memory) · [Sessions](#sessions) · [Context compaction](#context-compaction)
- [Named Agents](#named-agents)
- [Daemon Mode](#daemon-mode) · [Local HTTP API](#local-http-api-port-7533) · [Prompt Suggestion](#prompt-suggestion-ghost-text-in-input)
- [Memory (Kocoro Cloud)](#memory-kocoro-cloud-feature) · [Session sync to Cloud](#session-sync-to-cloud)
- [Scheduled Tasks](#scheduled-tasks) · [File System Watcher](#file-system-watcher) · [Heartbeat Mode](#heartbeat-mode)
- [SSE Event Handling](#sse-event-handling) · [UI Behavior](#ui-behavior) · [Keyboard](#keyboard)
- [Building & Testing](#building--testing) · [Known Limitations](#known-limitations) · [License](#license)

## Installation

> The CLI installs as two interchangeable commands — **`kocoro`** and **`shan`** (a built-in alias). Examples in this README use `shan`; `kocoro` works identically.

**npm (recommended)** — auto-updates on every launch:

```bash
npm install -g @kocoro/kocoro
```

**Install script** — downloads the latest binary to `/usr/local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/Kocoro-lab/Kocoro/main/install.sh | sh
```

**From source** — requires **Go 1.25+**:

```bash
git clone https://github.com/Kocoro-lab/Kocoro.git
cd Kocoro
go build -o "$(go env GOPATH)/bin/shan" .
ln -sf shan "$(go env GOPATH)/bin/kocoro"   # optional: kocoro alias, matching the npm/script installs
```

`go build -o` names the binary `shan` and drops it in `$GOPATH/bin` (default `~/go/bin`) — plain `go install .` would name it `ShanClaw` after the Go module path, so the `shan --help` check below wouldn't resolve. Add `export PATH="$HOME/go/bin:$PATH"` to your shell rc if it's not already on PATH.

Verify with `shan --help`.

## Updating

shan auto-updates on launch. To update explicitly:

```bash
shan update                       # manual update
npm update -g @kocoro/kocoro      # via npm (re-runs postinstall to fetch latest)
```

## Setup

Kocoro requires a Gateway API for LLM completions and remote tools.

**Shannon Cloud** — get an API key from [shannon.run](https://shannon.run):

```bash
shan --setup
# Enter endpoint: https://api-dev.shannon.run
# Enter API key: <your key>
```

**Self-hosted** — run the open-source [Shannon Gateway](https://github.com/Kocoro-lab/Shannon) locally, then `shan --setup` with `http://localhost:8080` and an empty API key.

**Ollama (local LLMs)** — set `provider: ollama` in `~/.shannon/config.yaml`. See [docs/config-reference.md](docs/config-reference.md#connection) for the full block.

## Quick Start

```bash
shan                                         # interactive TUI
shan "who was Ada Lovelace"                # one-shot
shan --agent ops-bot "check prod health"     # named agent
shan --setup                                 # configure endpoint + API key
```

In the TUI, type `/` to see built-in commands:

```
/research deep "latest advances in AI agents"
/swarm "build a marketing plan for our launch"
/model large
/sessions                       # browse and resume past sessions
/search websocket reconnect     # search session history
```

### One-Shot Examples

```bash
# Web research
shan "compare React vs Vue for a new project"

# File ops — file_read, file_write, file_edit, glob, grep, directory_list
shan "find all TODO comments in this project"
shan "replace all tabs with spaces in config.yaml"

# Archives — archive_inspect (no approval), archive_extract (approval)
shan "list what's inside ./backup.zip"
shan "extract backup.zip into ./restore — overwrite if it exists"

# Documents — pdf_to_text, docx_to_text, xlsx_to_text, pptx_to_text
shan "extract the text from quarterly-report.pdf, pages 3 to 8"
shan "what's in pricing.xlsx Sheet2?"

# Shell & system — bash, system_info, process
shan "run go test and fix any failures"
shan -y "kill the process on port 3000"

# macOS apps — applescript (use -y to auto-approve)
shan -y "open Safari and navigate to github.com"
shan -y "set my Mac volume to 50%"

# Unified native GUI control — delegate one complete desktop goal
shan -y "open Calendar and show me today's events"
shan -y "open TextEdit and type '你好世界 🌍'"

# Browser automation — Playwright MCP (preferred) or pinchtab/chromedp fallback
shan -y "open Chrome, go to x.com, and summarize my notifications"
shan -y "open https://news.ycombinator.com and get the top 5 stories"

# Ghostty terminal — requires Ghostty >= 1.3.0
shan ghostty workspace writer ops-bot      # open one window per agent

# MCP integrations (requires server config — see "MCP Client")
shan "list files on my Desktop"            # filesystem MCP
shan "show all tables in the database"     # sqlite MCP
```

#### Inbound file format support

| Format | Built-in fallback | Better with |
|---|---|---|
| PDF | macOS PDFKit through the installed Swift runtime; other hosts return an install/upload hint | `pdftotext` (`brew install poppler`) |
| DOCX | unzip + XML strip (raw text) | `pandoc` (`brew install pandoc`) |
| XLSX | unzip + raw XML | `xlsx2csv` (`pip install xlsx2csv`) |
| PPTX | unzip + XML strip | `pandoc` (`brew install pandoc`) |
| HEIC / AVIF | transcoded server-side by cloud | — |

### Multi-step Cowork Recipes

The daemon is meant to carry multi-step work that spans research, browser automation, and artifact generation in a single session. Pattern-focused recipes live in [`examples/cookbook/`](examples/cookbook/):

- **[Publish a Truth Social digest to note.com from Slack](examples/cookbook/slack-to-note-publish.md)** — `web_search` + `web_fetch` → Playwright MCP to publish in the user's authenticated browser.
- **[Scrape a Substack and generate a Word doc](examples/cookbook/substack-scrape-to-docx.md)** — attempted browser scraping, pivot to the site's JSON API, then `docx` skill → doc on the user's Desktop.

Add one when you find a task shape you keep coming back to; [`examples/cookbook/README.md`](examples/cookbook/README.md) has the format.

## Requirements

- **macOS** (clipboard, notifications, AppleScript, screencapture, accessibility)
- **Shannon Gateway** at configurable endpoint
- **Accessibility permission** granted in System Settings > Privacy & Security > Accessibility (for `computer_use`, `accessibility`, and `computer`); Screen Recording is needed only when those workflows request pixels
- **Chrome** (optional, for browser automation — Playwright MCP preferred)
- **[Ghostty](https://ghostty.org) >= 1.3.0** (optional, for `ghostty` tool)

## CLI Usage

```bash
shan                              # interactive TUI
shan "who was Ada Lovelace"     # one-shot (prompts for tool approval)
shan -y "query"                   # auto-approve all tools
shan --agent ops-bot "query"      # use a named agent
shan --setup                      # configure endpoint + API key
shan mcp serve                    # MCP server over stdio
shan daemon start                 # channel messaging daemon
shan schedule list                # local scheduled tasks
```

Flags: `-y/--yes` auto-approve; `--agent` named agent; `--dangerously-skip-permissions` skip checks in interactive mode; `--setup` interactive wizard.

### Voice Front Brain

`shan koe` is Kocoro's realtime voice interface. It speaks as one consistent assistant and handles computer tasks while you keep talking. Each request stays as one task by default and splits into parallel work only when you explicitly ask; results from tasks started by the same parallel response are held until the group is terminal and then spoken in one update.

The same front brain runs on iPhone. `shan koe` is the macOS entry point, and Kocoro for iOS embeds the identical Go brain through a gomobile bridge rather than a second implementation, so turn-taking, the tool authorities below, and delegated-task delivery behave the same on both. The phone contributes only what it owns — its microphone and speaker, and a relay that reaches your Mac's daemon through Shannon Cloud. Work you delegate mid-call keeps running and still lands as a report if you hang up.

Voice transport is WebRTC for every provider. `--provider auto` (the default) connects OpenAI first and falls back to Qwen only when a ready session cannot be established before media or user input begins; it never switches provider mid-call. `--provider openai` and `--provider qwen` force one route without fallback. Qwen's API key stays in Shannon Cloud: Koe sends only the SDP offer through the daemon and then carries media directly to Qwen. If an active provider session expires, queued task results survive the reconnect, while the replacement session explicitly treats earlier voice wording as unavailable instead of pretending conversational continuity.

Remote Koe front brains can set `KOE_DAEMON_TOKEN` to attach a bearer to daemon mint, SDP, task, cancellation, and usage requests. The receiving robot facade or reverse proxy validates it; use plaintext HTTP only on a trusted private LAN, and use TLS outside that boundary.

Qwen body carriers may attach a native H.264 video track before SDP negotiation. The frame source is read only while a call is active, at the carrier-selected cadence; idle and prewarmed sessions upload no images. If Qwen does not accept the offered video track, setup fails instead of silently presenting an audio-only call as vision-capable. Live vision is ambient context: ordinary replies stay on the user's topic and do not volunteer camera or video narration. Visible content is untrusted data, never instructions to follow. This is the Qwen live-vision transport seam, not an OpenAI `input_image` DataChannel event.

With `shan koe --barge-in` on the VPIO audio backend, Kocoro remains interruptible while it speaks. OpenAI can pause and resume around a backchannel with native truncation; Qwen accepts the overlapping user turn directly and protects only the short local playback tail after `response.done`, where speaker echo would otherwise make it interrupt itself. `KOE_QWEN_VAD_MODE=server_vad` remains available for controlled A/B testing. Stopping speech, cancelling work, and ending the call are separate actions; work already in progress survives a hang-up. Double-tap Option to talk again.

**Fast and Full execution.** Voice requests are dispatched at one of two levels. Bounded work ("what's on my calendar today?") runs **Fast** on a reserved profile tuned for latency; complex or high-risk work runs **Full** on your ordinary global/per-agent agent configuration, byte for byte. The choice is a soft semantic judgment, not a rigid classifier, and the daemon re-decides it on every request — an unrecognized or missing mode always falls back to Full, and nothing can upgrade a request the model marked Full. Fast resolves through Shannon Cloud with a 5-second bound and degrades to Full on any resolution failure, so a Cloud hiccup costs latency, never correctness. Fast is on by default; set `koe.fast_effort: false` to make every voice-triggered task run Full. It does not change the realtime voice model or override your normal agent's model and effort.

OpenAI defaults to `gpt-realtime-2.1`; Qwen defaults to `qwen3.5-omni-flash-realtime`. Compatible models can be selected independently in the developer panel, and the matching CLI override is used for each provider.

## Commands

Type `/` in the TUI for the interactive menu:

| Command | Description |
|---------|-------------|
| `/help` | Show help |
| `/research [quick\|standard\|deep] <query>` | Remote research via Gateway |
| `/swarm <query>` | Multi-agent swarm orchestration |
| `/copy` | Copy last response to clipboard |
| `/model [small\|medium\|large]` | Switch model tier |
| `/rename <title>` | Rename current session |
| `/config` | Show merged config with sources |
| `/status` | Show session status |
| `/sessions` | Interactive session picker |
| `/session new` | Start new session |
| `/session resume <n>` | Resume session by number or ID |
| `/search <query>` | Search session history (keyword, phrase, stemming) |
| `/clear` | New session + clear screen |
| `/reset` | Clear current session history in place (keeps ID, title, CWD) |
| `/compact [instructions]` | Compress context and keep a summary |
| `/doctor` | Run diagnostic checks |
| `/permissions` | Show or manage tool permissions |
| `/update` | Self-update from GitHub releases |
| `/setup` | Reconfigure endpoint & API key |
| `/quit` | Exit (alias: `/exit`) |
| `/<custom>` | Custom commands from global/project command dirs, plus agent commands and attached skills |

> `/research` and `/swarm` are also accepted via `POST /message` with `Accept: text/event-stream` (HTTP clients including Kocoro Desktop).

Subcommands: `shan mcp serve`, `shan daemon {start,stop,status}`, `shan schedule {create,list,update,remove,enable,disable}`, `shan sessions sync`, `shan koe` ([Voice Front Brain](#voice-front-brain)), `shan ghostty workspace`, `shan update`.

## Local Tools

Tools executed on your macOS machine. Detailed schemas live in each tool's `Info()` method in `internal/tools/`.

### File Operations

| Tool | Approval | Description |
|------|----------|-------------|
| `file_read` | CWD auto | Read files with line numbers (`offset`/`limit`). Repeat reads of the same unchanged range return a short "unchanged since last read" stub. Oversized text reads (~25K tokens estimated) return an error directing to use `offset+limit`. Images (png/jpg/gif/webp) returned as base64 vision blocks; auto-compresses large images. PDFs rendered page-by-page via Swift/PDFKit. |
| `file_write` | Yes | Write/create files, creates parent dirs. |
| `file_edit` | Yes | Find-and-replace. `old_string` must be unique unless `replace_all: true`. |
| `glob` | CWD auto | Find files by pattern (supports `**`). |
| `grep` | CWD auto | Search file contents (ripgrep, falls back to grep). `output_mode`: `files_with_matches` (default), `content`, `count`. Supports `glob`, `head_limit`, `offset`, `type`, `ignore_case`, `multiline`, `before_context`/`after_context`, `sort_by` (`mtime`). VCS dirs skipped; `--max-columns 500` keeps minified lines from dominating. |
| `directory_list` | CWD auto | List directory contents with sizes. |
| `archive_inspect` | No | List `.zip / .tar / .tar.gz / .tgz` contents without extracting. |
| `archive_extract` | Yes | Extract to `dest` (must not exist unless `overwrite=true`). Atomic via staging dir + rename. Rejects encrypted zips, symlink / absolute-path / setuid / device entries. Caps: 500 entries, 50 MB per entry, 200 MB total. Single-layer only. |

### Documents

| Tool | Approval | Description |
|------|----------|-------------|
| `pdf_to_text` | No | Extract plain text via poppler's `pdftotext -layout`. Optional `pages`: `"all"` (default), `"5"`, `"1-10"`. Install hint on missing binary. Output capped at 100K chars. |
| `docx_to_text` | No | Prefers `pandoc -t plain --wrap=preserve`; falls back to unzip + XML strip from `word/document.xml`. |
| `xlsx_to_text` | No | Prefers `xlsx2csv` (`-a` for every sheet); fallback unzip + `sharedStrings.xml`. `sheet` selector: `"all"` (default), name, or 1-based index. |
| `pptx_to_text` | No | Prefers `pandoc -t plain`; fallback unzip + XML strip per slide. |

### System & Shell

| Tool | Approval | Description |
|------|----------|-------------|
| `bash` | Auto for safe | Shell commands, 120s default timeout (per-call `timeout` arg clamped at `tools.bash_max_timeout`, default 600s). Output capped at 30K chars with head+tail truncation; pass `max_output_chars` to override. Process-group kill on timeout. |
| `system_info` | No | OS, arch, hostname, CPU, memory, disk. |
| `process` | Auto for list/ports | Process management: list, ports, kill. |
| `http` | Network allowlist | HTTP client, localhost auto-approved. |
| `think` | No | Scratchpad for reasoning. **Conditionally registered**: skipped on the default gateway + native-thinking path (Sonnet 4.6 / Opus 4.7 with `agent.thinking: true` cover this via interleaved thinking). Still registered when `agent.thinking: false`, `provider: ollama`, or `agent.force_think_tool: true`. |

### macOS Control

| Tool | Approval | Description |
|------|----------|-------------|
| `computer_use` | One task approval | **Primary native-GUI tool on daemon runs.** The Sonnet parent delegates one complete desktop goal; for one named app Kocoro binds its exact visible window in the background first, then activates it only when an action has no exact background primitive and foreground use is allowed. Multi-app tasks retain foreground app switching. A private OpenAI native Computer Use trajectory keeps screenshots, pointer actions, typing, re-observation, continuation, and `state_id`/ref steps inside the call. Exact CGWindow fallback supports apps with incomplete AX trees, and foreground pointer actions visibly move the real cursor. Unattended runs require the explicit persisted global Computer Use grant; blanket auto-approve is denied. |
| `accessibility` | Read: No; mutate: Yes | Legacy low-level AX tool retained for compatibility. Reads the macOS accessibility tree via persistent `ax_server`; refs are isolated per run. Mutations require a user-visible `description` and attended approval; read-only actions do not prompt. Actions: `read_tree`, `click`, `press`, `set_value`, `get_value`, `find`, `scroll`, `annotate`. |
| `wait_for` | No | Wait for UI conditions: `elementExists`, `elementGone`, `titleContains`, `urlContains`, `titleChanged`, `urlChanged`. Use instead of sleep after navigation or app launch. |
| `clipboard` | Yes | Read/write system clipboard. |
| `notify` | Yes | macOS desktop notifications. |
| `applescript` | Yes | Arbitrary AppleScript. Use for operations with no AX equivalent. **Not available on daemon runs** where `computer_use` is registered — it is removed from the model's toolset there (along with `accessibility`), and `bash` rejects `osascript`/`cliclick`, so a failed semantic path cannot bypass `computer_use` target binding. Still available in TUI / one-shot CLI / MCP. |
| `screenshot` | No | Screen capture (fullscreen/window/region). |
| `computer` | Yes | Mouse/keyboard via CGEvent (CJK/emoji safe). Click, type, hotkey, move, screenshot. No Python dependency. |
| `browser` | Yes | Playwright MCP (preferred), pinchtab, or chromedp fallback. When Playwright MCP is configured, the legacy browser tool is auto-disabled. Pinchtab connects to user's real browser for authenticated sessions; chromedp uses an isolated profile. |
| `ghostty` | Yes | Ghostty terminal control: open tabs, splits, send input. |

### Scheduling, Search, Memory & Skills

| Tool | Approval | Description |
|------|----------|-------------|
| `schedule_create` / `_update` / `_remove` | Yes | Manage launchd-backed scheduled tasks. |
| `schedule_list` / `_show` | No | List with sync status; show a schedule's last run. |
| `session_search` | No | FTS5 keyword search across past session messages. |
| `memory_append` | No | Append entries to agent MEMORY.md (flock-protected). |
| `set_work_plan` | No | Record a short progress checklist (2–8 steps) for genuinely multi-stage work the agent is executing. Daemon-served runs only; each update replaces the full list, the daemon persists it before broadcasting, and Kocoro Desktop shows it as a live progress card. It records progress only — it never executes work, and a checked step is not completion evidence. |
| `ask_user_question` | No | Ask the user to pick among a few explicit options (1-4 questions, 2-4 options each) when genuinely blocked or when they must choose between equivalent alternatives. You get the full chosen labels back. Rendered as a selection card in Kocoro Desktop; on channels/surfaces that can't prompt, it returns cleanly so the agent proceeds on its own judgment. |
| `use_skill` | No | Activate a skill by name — returns full SKILL.md body. Skill names and descriptions are always listed to the model, so it can call this unprompted. Additionally, an opt-in `model_tier: small` prefetch can semantically suggest a relevant skill on the first turn — set `agent.skill_discovery: true` to enable it (off by default, so ordinary turns carry no extra model call). |
| `present_deliverable` | No | Surface a finished file as a deliverable. The daemon validates the path locally and emits a vouched file reference to attached clients. **Kocoro Desktop renders it in the session's Deliverables sidebar; TUI, one-shot CLI, and MCP have no such surface**, so there the call succeeds without producing any UI. The path may sit outside the session working directory — the event is a user-visible file card, not proof the file was created inside a sandbox. |

### Calendar (registered only when daemon is a Kocoro Desktop subprocess)

Operates the user's iCloud / Google / Microsoft 365 / Exchange / Outlook calendars configured under **System Settings → Internet Accounts**. EventKit access lives in Kocoro Desktop (.app); daemon talks to Desktop over a local Unix domain socket. Not available in TUI / one-shot CLI / MCP / scheduled-task modes. In TUI / one-shot CLI / MCP, fall back to `applescript` driving Calendar.app; scheduled tasks run through the daemon, where `applescript` is removed whenever `computer_use` is registered, so use `computer_use` against Calendar.app there instead.

| Tool | Approval | Description |
|------|----------|-------------|
| `calendar_check_permission` | No | Returns TCC status: `not_determined` / `restricted` / `denied` / `granted` / `write_only`. |
| `calendar_request_permission` | Yes | Triggers the macOS TCC system dialog. Blocks up to 5 minutes for user decision. |
| `calendar_list_sources` | No | Enumerate all configured calendars (id, title, account_type, color, writable). |
| `calendar_list_events` | No | Query events in a time window. RFC 3339 timestamps with offset. Optional source / query / limit (max 2000). Returns `series_master_id` on recurring instances. |
| `calendar_get_event` | No | Full event detail including `recurrence_rule` and `alarms`. |
| `calendar_create_event` | Yes | Create event. `attendees` are written as metadata only — `invitations_sent` is always `false` in v1 (EventKit limitation; v1.x patch will route through AppleScript-Calendar.app fallback to send real invitations). |
| `calendar_update_event` | Yes | Update with `patch` semantics (missing/null = no change, empty string/array = clear, lists are replaced not merged). `scope`: `this` or `this_and_future` only (no `all` — use delete + create). |
| `calendar_delete_event` | Yes | Delete one instance / this-and-future / entire recurring series. |

### Desktop Skill Recommendation Tools

For signed-in Desktop requests that declare the `skill_install_recommendation_v1`
capability, the daemon exposes two Desktop-only tools:

| Tool | Approval | Description |
|------|----------|-------------|
| `discover_installable_skills` | No | Find reviewed, uninstalled catalog skills relevant to the current task. |
| `offer_skill_installation` | No | Show a localized Desktop installation card for IDs returned by discovery; it never installs by itself. |

These tools are absent from TUI, one-shot, MCP serve, schedules, IM channels,
heartbeat, and watcher runs. Accepting the card installs the pinned catalog
artifact and resumes the original Desktop session.

Task-time discovery is deliberately offline and narrow: the daemon consults
only its binary-pinned recommendation allowlist (`pptx`, `docx`, `xlsx`, `pdf`,
and `slack-gif-creator`). It never contacts GitHub or the community marketplace
while discovering candidates during a task. `slack-gif-creator` installs from
the embedded payload; after explicit acceptance, the four document skills use
an immutable, SHA-256-verified GitHub archive because their upstream license
prohibits redistributing them inside the daemon.

### Cloud Tools (gated on `cloud.enabled` + `api_key`)

| Tool | Approval | Description |
|------|----------|-------------|
| `cloud_delegate` | Yes | Delegate to Shannon Cloud for remote research/swarm execution. |
| `publish_to_web` | Yes ⚠️ | Upload to a **public** S3 URL on Shannon Cloud (50 MiB cap). Path blocklist (`.env`, `.ssh`, `credentials`, `*.pem`, …) and extension allowlist (html/md/txt/pdf/png/jpg/svg/csv/json/mp4/…). Extend allowlist via `cloud.publish_allowed_extensions`. Uploads are tagged `kind=other` server-side (Desktop UI's "All / Image / HTML / PDF / Other" filter sits alongside a separate "Session" bucket for daemon-side session shares). Files retractable via `retract_published_file`, but **anyone with the URL can read content until then** plus up to 5 minutes after via CDN edge cache. |
| `list_my_published_files` | No | List the user's still-active published files. Paginated (`limit` default 20, max 100). Optional `kind` filter (`session_share` / `report` / `landing_page` / `image` / `other`) — omit to list every category. |
| `retract_published_file` | Yes ⚠️ | Retract a published file by `id` (UUID from list, **not** the URL). Owner-only; cross-user calls return a friendly 404 (cloud conflates not-found/already-retracted/not-yours to prevent existence leaks). NOT on the high-risk auto-approval denylist — user can opt in to `always_allow_tools`. CDN edges may serve content for up to 5 min after success. |
| `generate_image` | Yes ⚠️ | Generate via `POST /api/v1/images/generations` (`gpt-image-2`); returns a **public permanent** CDN URL. Args: `prompt`, `size`, `quality`, `n` (1–10), `background`. Latency varies and each call consumes paid quota. For charts use `kocoro-generative-ui` instead. |
| `edit_image` | Yes ⚠️ | Edit via `POST /api/v1/images/edits`. Args: `prompt` + `image_urls` (1–4, must start with `https://static.kocoro.ai/` — external URLs rejected; pipe through `generate_image` / `publish_to_web` first). No mask field — describe the region in prose. Latency varies and each call consumes paid quota. |

### Tool Approval Flow

```
Tool call → Permission engine → RequiresApproval + SafeChecker → Pre-tool hook (can deny)
         → Execute → Post-tool hook → Audit log
```

- **Hard-blocked**: `rm -rf /`, `mkfs`, `dd if=`, `curl|sh` — cannot be overridden
- **CWD auto-approve**: read-only tools (`file_read`, `glob`, `grep`, `directory_list`) auto-approve under the session CWD
- **Auto-approve**: safe bash commands (`ls`, `git status`, `go test`), `process list/ports`, localhost HTTP
- **Prompt**: destructive tools show `[y/n]` in TUI or one-shot
- **Denied-call blocking**: denying a call suppresses the same tool+args for the rest of the turn
- **`-y` flag**: auto-approves everything in one-shot mode
- **No handler**: denied by default (fail-safe)

### Tool Result Sizing

Three layered caps protect context window pressure:

- **Per-result spill**: any tool result over ~50K characters is written to a temp file under `~/.shannon/tmp/` and replaced inline with a 2K preview plus the file path.
- **Per-turn aggregate cap**: when a turn returns more than 200K characters total, the largest results are spilled until the aggregate drops back under cap (counted in runes, multibyte-fair).
- **Bloat nudge**: surfaces a `tool_result_bloat` run-status hint when a single tool emits unusually large output, so the user/UI can see why the loop slowed down.

Per-tool overrides: `file_read` is unlimited at the budget layer (its own 25K-token guard), `grep` is tighter (~20K), unspecified tools use 50K.

## Permission Engine

Bash command resolution order:

1. **Hard-block** — built-in constants (rm -rf /, mkfs, dd, curl|sh), cannot be overridden
2. **Denied commands** — `permissions.denied_commands` in config
3. **Compound split** — `&&`, `||`, `;`, `|`, bare `&`, and `(...)` subshells split and checked per sub-command. Bare `&` is preserved so background launches still trigger always-ask.
4. **Always-ask high-risk gate** — runs BEFORE the allowlist. (a) fixed-prefix list (`python -c`, `bash -c`, `pip install`, `npx`, `rm -rf`, etc.); (b) dangerous-flag token scan for `git push` (`--force`, `-f`, `--force-with-lease`, `--mirror`, `--delete`, `--prune`, etc.). "Always Allow" on a high-risk command is honored once but NOT persisted.
5. **Allowed commands** — literal/glob match against the full command, then a token-prefix family fallback (depth N=2 for known CLIs like git/kubectl/docker/npm, N=3 for unknowns). So `terraform workspace list` covers `terraform workspace show` but not `terraform import`. The always-ask gate above prevents family expansion from silently widening scope to destructive variants.
6. **Default safe** — built-in safe list (ls, git status, go test, make).
7. **User approval** — interactive prompt or `-y`.

For compound commands, every sub-command must be explicitly allowed for auto-approval. Any denied sub-command denies the whole.

Additional checks: file paths use `filepath.EvalSymlinks` + sensitive patterns (`.env`, `*.pem`, `id_rsa`) + `allowed_dirs`; network egress uses allowlist (localhost always allowed); PreToolUse hook can deny with exit 2.

## Audit Logging

All tool calls logged to `~/.shannon/logs/audit.log`. JSON-lines, append-only. Each entry: timestamp, session ID, tool name, input/output summary, decision, approved, duration. **Auto-redaction**: AWS keys, JWT, `sk-`/`key-` prefixes, Bearer tokens, PEM markers, env var assignments.

## Hooks

Shell scripts triggered at lifecycle events:

| Hook | When | Can Deny |
|------|------|----------|
| `PreToolUse` | Before tool execution | Yes (exit 2) |
| `PostToolUse` | After tool execution | No |
| `SessionStart` | Session begins | No |
| `Stop` | Session ends | No |

```yaml
hooks:
  PostToolUse:
    - matcher: "file_edit|file_write"
      command: ".shannon/hooks/post-edit.sh"
```

Protocol: JSON on stdin (tool name, args, result), exit 0 = allow, exit 2 = deny (PreToolUse only), 10s timeout, 10KB output limit. Commands must use `./` prefix or absolute paths under `~/.shannon/`.

## MCP Server

Expose local tools to MCP clients via JSON-RPC 2.0 over stdio:

```bash
shan mcp serve
```

Same permission engine, hooks, and audit logging as the CLI. Tools requiring
approval fail closed unless the connected client negotiated form elicitation;
capable clients receive a structured one-call confirmation request.

Supported request methods: `initialize`, `tools/list`, `tools/call`, and
server-initiated `elicitation/create`. Long-running calls accept
`notifications/cancelled`, calls carrying `_meta.progressToken` receive
`notifications/progress`, and initialized clients receive
`notifications/tools/list_changed` when the registry changes.

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | shan mcp serve
echo '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"system_info","arguments":{}}}' | shan mcp serve
```

## MCP Client

Connect to external MCP servers in `~/.shannon/config.yaml` under `mcp_servers:`.

```yaml
mcp_servers:
  filesystem:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/Users/you/Desktop"]
    context: "Filesystem access to ~/Desktop. Use read_file, write_file, list_directory."

  sqlite:
    command: "npx"
    args: ["-y", "mcp-server-sqlite-npx", "/path/to/database.db"]
    context: "Connected to SQLite database. Use read_query for SELECT, write_query for writes."

  my-remote:
    type: http
    url: "https://mcp.example.com/sse"
    context: "Remote MCP server providing custom tools."
```

Per-server: `command`/`args` (stdio), `type: http` + `url` (HTTP), `env`, `context` (LLM guidance — critical), `disabled: true` (skip without removing), `tool_timeout_secs` (bound a single tool-call attempt — after a transport failure the daemon reconnects and retries once, so a call is at most two attempts plus reconnect; default from `mcp.tool_timeout_secs`, 300s — raise for long-running tools; cannot be disabled, `0` means the default), `workspace_base` (for file-producing servers: the directory their results render relative paths against, so the daemon can report absolute artifact paths).

- **On-demand discovery** — large MCP and integration catalogs are searched and loaded as needed; trusted Cloud `web_search`, `web_fetch`, and `x_search` openers plus core local tools remain immediately available.
- **Cloud integrations** — connected services such as X are exposed to the local agent after Desktop refreshes the catalog. Read-only calls can run concurrently when Cloud explicitly marks them observational; older schemas remain conservative. Integration tools and credential-capturing cloud/publish/image tools are credential/principal-generation scoped; old runtime clones fail locally before dispatch after auth changes. Auth/integration/MCP-health/reload registry transactions share one build-to-swap lock so cached overlays also fail closed. Reconnect prompts name the exact entry under Kocoro Settings → MCP Servers.
- **X automation guardrails** — built-in browser and computer automation are blocked from X composer and Post controls while ordinary X reading remains available. Canonical Playwright omits arbitrary-code tools; CDP-backed mutations serialize target verification with dispatch and remain read-only whenever an X target is open, covering the inline home composer without element-label guesses. Non-CDP ordinary mutations remain available without claiming target-state X protection. Posting on X happens only through the authorized X integration tools, never through browser automation.
- **Artifact scratch sweep** — files a file-producing MCP server saves without an explicit path (e.g. playwright screenshots in daemon-served sessions) land in a per-session scratch dir under `~/.shannon/tmp/sessions/<id>/`. The daemon removes whole session dirs older than `daemon.scratch_max_age_days` (default 14) once at startup; `0` disables the sweep.
- **`context` is critical** — tells the LLM what auth, capabilities, and queries to use. Without it, the LLM guesses wrong.
- **All MCP tools require approval.** Use `-y` for auto-approve in one-shot.
- **Local tools take priority** — same-name local tool wins over MCP.
- **Project-level overrides** — put server configs in `.shannon/config.yaml` (project) or `.shannon/config.local.yaml` (gitignored).
- **One-shot vs interactive** — each `shan "query"` starts fresh MCP connections. In `shan` (TUI), connections persist for the session.
- More servers: [MCP Server Registry](https://registry.modelcontextprotocol.io/) and [Awesome MCP Servers](https://github.com/punkpeye/awesome-mcp-servers).

## Configuration

Multi-level merge — later overrides earlier:

1. `~/.shannon/config.yaml` — global
2. `.shannon/config.yaml` — project
3. `.shannon/config.local.yaml` — local (gitignored)

Scalars override, lists merge + dedup, structs field-level merge.

Minimal `~/.shannon/config.yaml`:

```yaml
endpoint: https://api.shannon.run
api_key: <your key>
model_tier: medium

permissions:
  allowed_commands:
    - "git *"
    - "make *"
```

See [docs/config-reference.md](docs/config-reference.md) for the full key list including `agent.*`, `tools.*`, `mcp_servers`, `cloud`, `memory`, `sync`, `daemon`, `hooks`, UI settings, etc. Run `/config` in the TUI to see the merged config with sources.

Long browser/GUI sessions use `agent.observation_window_trigger.mode: hybrid`
by default: old page observations are cleared when the prompt cache is already
cold or when their aggregate text crosses a bounded budget. See
[Browser/GUI context trimming](docs/config-reference.md#agent-settings) for the
available trigger modes, idle gap, and aggregate-budget overrides.

Per-agent overrides live in `~/.shannon/agents/<name>/_attached.yaml` — including `agent.model_tier` so individual agents can opt into the Large (Opus) tier without changing the global default. `agent.effort_tier` (`low` / `high` / `xhigh` / `max`, empty = inherit) works the same way, overriding the unified reasoning-effort tier per agent. `agent.response_detail` (`concise` / `balanced` / `detailed`, global default `balanced`, empty per-agent value = inherit) controls final-answer style without changing reasoning effort. See [docs/agents-reference.md](docs/agents-reference.md) for the precedence chain.

## Instructions & Memory

AI behavior customization from markdown files (token-budgeted, deduplicated; `.md` links auto-expanded inline):

- `~/.shannon/instructions.md` — global
- `~/.shannon/rules/*.md` — global rules (alphabetical)
- `.shannon/instructions.md` — project
- `.shannon/rules/*.md` — project rules
- `.shannon/instructions.local.md` — project local override (gitignored)

Persistent memory: `~/.shannon/memory/MEMORY.md` (first 200 lines loaded on startup). The agent can write to this file to remember across sessions.

Custom slash commands: create `.shannon/commands/<name>.md` (or under `~/.shannon/`). `$ARGUMENTS` is replaced with the text after the command name in the TUI.

## Sessions

Conversations persisted as JSON in `~/.shannon/sessions/` (or `~/.shannon/agents/<name>/sessions/` for named agents).

- Each session is `<id>.json` (messages, metadata, remote task IDs)
- Saved after each agent turn and on exit
- Titles generated from the first user message (50-char cap)
- Sessions can be **pinned** and **favorited** via `PATCH /sessions/{id}` — Kocoro Desktop surfaces these as UI flags for quick access
- Search index `sessions.db` (SQLite FTS5) auto-created alongside JSON. Safe to delete — rebuilds on next launch.

```
/sessions                              # interactive picker
/session resume 1                      # by number
/session resume 2026-02-23-a1b2c3      # by full ID
/session new                           # start fresh
```

### Context compaction

Long sessions compact automatically — you do not need to run `/compact` to keep a session alive. When the prompt approaches the model's context window, the agent replaces the older middle of the conversation with a summary and keeps the recent turns intact.

Two things are worth knowing about how this works:

- **Your transcript is never rewritten.** The session JSON stays the lossless archive used by resume, search, share, and sync. Compaction writes a *separate* checkpoint (summary + retained tail) that only the model sees. Anything compaction drops from the model's view is still in the session file.
- **The checkpoint is durable.** It is persisted rather than recomputed per request, so the summary does not drift between turns and the prompt cache converges. It also survives a crash: on restart the daemon continues an interrupted turn from its checkpoint, provided `agent.interrupted_resume_enabled` is on (default) and the checkpoint is still inside the `agent.interrupted_resume_max_age_hours` window (default 4). An older checkpoint is abandoned rather than executed, since the intent behind it has gone stale. Recovered runs always execute unattended, so the unattended tool deny-list applies.

After a routine compaction the agent re-reads the files it had most recently read — bounded at 5 files and roughly 50K tokens, and skipped entirely when there is no headroom — so exact file content the summary only paraphrases comes back into context. Emergency compaction triggered by a rejected oversized prompt deliberately skips this and instead tells the model to re-read what it needs.

Useful knobs (all optional — the defaults are tuned for the 1M-context model families the default tiers route to):

```yaml
agent:
  context_window: 1000000              # seed; auto-corrected from the response model
  compact_timeout_secs: 300            # bound on one manual /compact pass
  compaction_snapshot_retention: 1     # pre-compaction snapshots kept per session
  compaction_snapshot_max_age_days: 14 # 0 disables the age sweep
```

`/compact [instructions]` forces a pass immediately. The TUI shows a transient "Tidying context" spinner between the daemon's `compaction_started` and `compaction_finished` run-status events; other clients can render the same indicator by gating on the `compaction_status_events_v1` capability token.

For the crash-recovery counterpart to this design, see [Mid-Turn Checkpointing in a Long-Running Agent Loop](https://waylandz.com/blog/mid-turn-checkpointing/), which explains why durable checkpoints are safer than replaying a partially completed agent turn.

## Named Agents

Create independent agents with their own instructions, memory, tools, MCP servers, and model settings:

```
~/.shannon/agents/
  ops-bot/
    AGENT.md          # instructions (replaces default system prompt)
    MEMORY.md         # agent-specific memory
    config.yaml       # optional: tool filtering, MCP scoping, model overrides
    commands/         # optional: agent-scoped slash commands
    _attached.yaml    # optional: attached installed skill names
```

Minimal agent — just `AGENT.md`:

```bash
mkdir -p ~/.shannon/agents/ops-bot
cat > ~/.shannon/agents/ops-bot/AGENT.md << 'EOF'
You are ops-bot, a production operations assistant.
- Monitor health metrics and error rates
- Summarize incidents concisely
- Always recommend next steps
EOF
```

Agents without `config.yaml` inherit all tools, global MCP servers, and default model settings.

Use:

```bash
shan --agent ops-bot "check error rate in prod"     # one-shot
shan --agent ops-bot                                 # TUI (with agent commands + attached skills)
# In daemon mode, @mention routes:
# "@ops-bot check prod"     → ops-bot agent
# "check prod"              → default Shannon agent
```

Names must match `^[a-z0-9][a-z0-9_-]{0,63}$`. Each agent gets its own session directory at `~/.shannon/agents/<name>/sessions/`.

Skills can be installed from **ClawHub** (the Kocoro skill marketplace) via the daemon HTTP API — `GET /skills/marketplace`, `POST /skills/marketplace/install/{slug}`, or upload a local ZIP with `POST /skills/upload`. Kocoro Desktop surfaces the marketplace as a browseable UI. A capable signed-in Desktop can also let the agent discover a missing reviewed skill during a task and present an installation card; installation occurs only after the user accepts it.

See [docs/agents-reference.md](docs/agents-reference.md) for the full `config.yaml` reference, `cwd` resolution, project-local config scope, tool filtering semantics, attached skills, builtin skills (`kocoro`, `kocoro-generative-ui`), skill secrets, and ZIP installs.

## Daemon Mode

The daemon connects to Shannon Cloud via WebSocket for channel messages (Slack, LINE, etc.) and exposes a local HTTP API on port 7533 for native apps and scripts.

```bash
shan daemon start           # foreground (logs to stdout)
shan daemon start -d        # background via launchd (macOS, survives reboots)
shan daemon stop            # stop daemon + remove launchd service if installed
shan daemon status          # show connection + launchd state
```

### Architecture

```
Slack/LINE ──webhook──▶ Shannon Cloud ──WebSocket──▶ shan daemon (macOS)
                                                      ├─ Agent loop + local tools
                                                      └─ HTTP :7533 (local API)
                                                           ▲
                                              curl / native apps / scripts
```

### Channel Messaging (via Shannon Cloud)

- **Envelope protocol** — typed messages with claim/ack (broadcast + first-to-claim)
- **Progress heartbeats** — 15s interval extends claim TTL during long agent runs
- **Channel routing** — agent name set per channel in cloud config, fallback to `@mention`
- **Session continuity** — per-agent history across messages
- **Up to 5 concurrent agents** — bounded worker pool
- **Auto-reconnect** with exponential backoff; graceful disconnect on shutdown
- **Schedule mutation tools** (`schedule_create/update/remove`) denied by default in daemon mode
- **HITL message injection** — `POST /message` while an agent is running injects mid-turn
- **File attachments** — Slack / LINE / Feishu / Telegram / webhook messages with files are surfaced to the agent. Three branches per file: `document_b64` (cloud-supplied PDF base64) → native Anthropic `document` block; `extracted_text` (cloud DOCX/XLSX/PPTX/CSV extraction or large-PDF fallback) → `text` block headed `[Attached: <name> (<mime>)]`; otherwise legacy URL download to `~/.shannon/tmp/attachments/` as `file_ref`. Caps: 20 files / message, 500 MB / file, 500K-char rune ceiling on `extracted_text`. SSRF-protected URL validation, scheme/IP allowlist, Authorization-header redirect preservation. Cleaned up on session close.

#### Interactive approval + always-allow

Tools requiring approval send requests to the client app (via WS relay through Shannon Cloud). General tools support two persisted tool-level scopes:

- **Global** (`~/.shannon/config.yaml permissions.always_allow_tools`) — every agent, including default
- **Per-agent** (`~/.shannon/agents/<name>/config.yaml permissions.always_allow_tools`) — single agent

For ordinary tools, clicking it writes the tool name to the appropriate scope (named agent → per-agent; default agent → global). `computer_use` is deliberately simpler: **Always Allow Computer Use is one global product permission**, regardless of which agent or app initiated the prompt. It applies to interactive and unattended tasks. Without that explicit grant, an attended user may Allow Once, while an unattended run fails closed.

**Safety gates remain regardless of what either list contains** — checked by separate code paths, hand-edited config cannot bypass:

- **High-risk bash commands** (`pip install`, `rm -rf`, `python -c`, `git push --force`, etc.) still prompt every call. Enforced by the runtime gate in `internal/agent/loop.go` against `permissions.alwaysAskPrefixes`.
- **Outbound integration writes prompt every call** — integration tools whose Cloud schema is marked `requires_approval` (e.g. `gmail_send_email`, X posting tools) cannot be persisted as Always Allow at any scope. The approval card hides the Always Allow button, the click (from an older client) is honored once but never saved, and an existing `always_allow_tools` entry for such a tool is ignored at runtime. Config self-heals too: agent config writes (API or cloud agent sync) drop such entries, and the next successful catalog rebuild — an integration refresh or a sign-in — prunes any that slipped in while the integration catalog was unavailable (e.g. during an API-key rotation). One click must never become a standing grant for unattended outbound sends; new provider write tools inherit this automatically from their Cloud schema.
- **Computer Use consent is explicit** — `computer_use` remains on the unattended auto-approval deny-list so blanket `daemon.auto_approve`, a missing approval UI, or a transient broker flag cannot silently grant desktop control. The agent loop overrides that default denial only for the persisted global `computer_use` grant. Legacy `computer`, `accessibility`, `applescript`, and `ghostty` wrappers cannot be persisted as Always Allow and remain denied for unattended execution. Built-in sensitive-app blocks, explicit Pause/Take Over/Stop, fresh re-observation after ambient physical interference, and separate point-of-risk confirmation remain authoritative even when the global grant is enabled.

#### Approval-card descriptions

Every approval-required tool (`bash`, `file_read`, `file_write`, `file_edit`, `glob`, `grep`, `directory_list`, `http`, `browser`, `applescript`, `process`, `clipboard`, `computer`, `ghostty`, `notify`, `cloud_delegate`, `generate_image`, `edit_image`, `schedule_*`) declares a required `description` field — a 5-15 word natural-language summary in the user's UI language (e.g. `"查看 ui-components 文件"`). Approval cards render the description prominently and fold raw args (paths, URLs, JSON, shell) behind a "View details" toggle, so non-technical users can review what an agent is about to do without reading syntax.

`publish_to_web` uses its existing required `purpose` field. UI clients fall back to displaying raw args when `description` is missing — the daemon passes args through unchanged for audit integrity.

### Local HTTP API (port 7533)

Localhost-only HTTP for native-app integration and scripting.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Liveness → `{"status":"ok","version":"..."}` |
| `/status` | GET | Connection state, active agent, uptime, version |
| `/agents` | GET | List named agents |
| `/sessions` | GET | List sessions, optional `?agent=` filter |
| `/sessions/{id}` | GET | Full session with messages, `?agent=<name>` |
| `/sessions/{id}` | PATCH | Update `title`, `pinned`, `favorite` (any subset) |
| `/sessions/{id}/edit` | POST | Truncate history at index, re-run with new content |
| `/sessions/{id}/reset` | POST | Clear session history in place (named agent only) |
| `/sessions/search` | GET | Search session history, `?q=<query>&agent=<name>` |
| `/projects` | GET, POST | List / create projects (session grouping with a name + theme color) |
| `/projects/{id}` | GET, PUT, DELETE | Read or rename/recolor a project. `DELETE` requires `?confirm=true` and **permanently deletes every session filed under it**, not just the project. |
| `/message` | POST | Send a message; supports HITL injection and idempotent retry |
| `/migrate/claude-code/preview` | POST | Scan `~/.claude/` and return what would be imported (dry-run) |
| `/migrate/claude-code/apply` | POST | Execute a previewed import — copies agents, skills, instructions from Claude Code |
| `/config/reload` | POST | Reload config, restart watchers and heartbeat managers |
| `/events` | GET | SSE stream of daemon events (`agent_reply`, `heartbeat_alert`, …) |
| `/shutdown` | POST | Graceful shutdown (used by `shan daemon stop`) |

Send a message:

```bash
# Synchronous
curl -X POST http://localhost:7533/message \
  -H "Content-Type: application/json" \
  -d '{"text":"what is 2+2?"}'

# SSE streaming — same body, add Accept header
curl -X POST http://localhost:7533/message \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{"text":"analyze this codebase","agent":"ops-bot"}'
```

Synchronous response:

```json
{
  "reply": "2+2 equals 4.",
  "session_id": "2026-03-08-a1b2c3d4e5f6",
  "agent": "",
  "usage": {"input_tokens": 150, "output_tokens": 20, "total_tokens": 170, "cost_usd": 0.002}
}
```

**Idempotent retry.** `POST /message` accepts an `idempotency_key` (8-128 chars, `[A-Za-z0-9._:-]`) alongside a client-minted `session_id`; it is rejected without one, and on ephemeral requests. Retrying a *completed* request returns the persisted result without re-invoking the LLM or any tools — which is what makes a file-producing run safe to retry after your client crashes. Interrupted or failed requests fail closed and need explicit recovery rather than silently re-running.

> The guarantee is **sequential-retry-safe, not concurrent-submission-safe**: two simultaneous requests carrying the same `session_id` + key can both clear the in-progress guard before either registers, so a naive concurrent client can still double-execute the side effect. Serialize retries of a key. Gate on the `message_idempotency_v1` capability token (`message_idempotency_receipt_v2` additionally persists validated `present_deliverable` receipts and returns stable error codes for failed/in-progress retries).

**Projects** group sessions under a name and theme color. Every session row carries a `project_id`, `GET /sessions` accepts a `project_id` filter, `PATCH /sessions/{id}` re-files a session, and project-scoped instructions/memory are injected into its runs. Gate on `project_entity_v1` — an older daemon 404s `/projects` and never emits `project_id`.

**Bridging a messaging platform (Discord, Matrix, custom webhook, etc.) to the daemon?** See the [Channel Integration Guide](examples/channel-integration-guide.md) for the full `POST /message` + SSE + interactive approval workflow, plus a community reference Discord bot. Official Slack/LINE/Feishu/Lark integrations go through Shannon Cloud for multi-tenant OAuth and audit — the local HTTP path here is for personal/dev deployments.

### Prompt Suggestion (ghost text in input)

When enabled, the daemon generates a 2-12 word suggestion for your next message after each agent turn. Desktop renders it as gray placeholder text — press → or Tab to accept.

Enable in `~/.shannon/config.yaml`:

```yaml
agent:
  prompt_suggestion:
    enabled: true
```

Or toggle from Desktop: Settings → Suggestions → Enable next-prompt suggestion.

**Cost** varies with the configured model, cache state, and
`agent.thinking`. Each suggestion is a potentially billed helper call; exact
product measurements and cost baselines are intentionally not published here.
Disabled by default. Skipped when the prompt cache is cold
(`cache_cold_threshold_tokens`).

## Memory (Kocoro Cloud feature)

`memory_recall` lets the agent look up facts learned from prior sessions before asking the user. Structured memory runs as a local sidecar over a Unix socket; the daemon manages spawn, readiness, restart, and bundle pull.

**Opt-in** — disabled by default; Kocoro Desktop's Episodic Memory toggle enables it. Three modes:

- `memory.provider: "disabled"` (default) — no sidecar; `memory_recall` falls back to session search + MEMORY.md
- `memory.provider: "cloud"` — daemon pulls fresh memory bundles from Kocoro Cloud every 24h. Requires `cloud.api_key` + `cloud.endpoint` (overridable via `memory.api_key` / `memory.endpoint`)
- `memory.provider: "local"` — daemon runs the sidecar against bundles you build locally; no Cloud calls

The daemon accepts bundle schema versions in `[0.4.0, 0.9.0)`. Version 0.8
carries the expanded W-prior manifest contract; 0.9.0 is the next
breaking-schema boundary.

### Quickstart (cloud mode)

1. Install the `tlm` binary somewhere on `$PATH` (or set `memory.tlm_path`).
2. Configure Cloud credentials:

   ```yaml
   cloud:
     endpoint: https://api.shannon.run
     api_key: <your key>
   memory:
     provider: cloud
   ```

3. Restart the daemon. First bundle download starts ~60s after boot, then every 24h.

### Model-driven episodic recall

The main model decides when the answer depends on the user's private past and calls `memory_recall` directly. Ordinary turns do not pay for a hidden classifier or pre-fetch request.

- Natural private-fact questions do not need to say “remember” or “recall”; the tool remains available whenever the model needs past context.
- `memory_recall` uses the structured sidecar when it is `Ready` and otherwise degrades to session search plus `MEMORY.md`.
- Groups may carry `temporal_status`: `current` is the latest value of an updating fact; `superseded_by_recency` is an older value kept for audit. Prefer current unless the user asked about the past.
- `memory_recall` can send `aggregator` (`count` / `sum`) on `direct_relation`. A withheld count is `reason: incomplete`, not a finished total.
- Unnamed references such as “my doctor” or “that plan” use `session_search` to recover the concrete name or wording instead of inventing a structured-memory anchor.
- A no-data result ends structured lookup for that target; the model does not retry relation-name or mode variants.

### Configuration

See the `memory:` block in [docs/config-reference.md](docs/config-reference.md#cloud-features) for all keys (`provider`, `endpoint`, `api_key`, `socket_path`, `bundle_root`, `tlm_path`, `bundle_pull_interval`, `sidecar_*` timeouts).

### Privacy

Memory bundles are local files. The daemon never sends recall queries or inferred candidates back to Cloud. Session sync defaults disabled and is flipped on alongside Episodic Memory by the Desktop toggle (or `sync.enabled: true` manually); when on, it uploads local session history so Kocoro Cloud can train fresh memory bundles. Switching the configured API key triggers a wipe + fresh bundle pull so cached recall from a previous tenant doesn't leak.

## Session sync to Cloud

Kocoro uploads local session JSON to Shannon Cloud once per day to power Cloud-side analytics, replay, and per-user memory training. **Opt-in** — disabled by default; the Kocoro Desktop Episodic Memory toggle flips this on, or set `sync.enabled: true` manually.

**What's uploaded:** full session JSON files under `~/.shannon/sessions/` and `~/.shannon/agents/*/sessions/`. Sessions are sent as-is — no built-in PII or secret redaction in v1. Skill secrets are never included (Keychain only, never in transcripts), but tool output, file contents, and bash results are uploaded verbatim.

Configure in `~/.shannon/config.yaml` — see [docs/config-reference.md](docs/config-reference.md#cloud-features) for the full `sync:` block (`enabled`, `dry_run`, `exclude_agents`, `exclude_sources`, batch caps, intervals).

**How it runs:**

1. **Daemon ticker** — when running, syncs once 60s after startup, then every 24h.
2. **Manual** — `shan sessions sync` any time. Useful for dry-run verification.
3. **System scheduler (recommended for daemon-off coverage)** — see [docs/session-sync-launchd.md](docs/session-sync-launchd.md) for the macOS launchd plist and Linux cron equivalent.

**State files:**

- `~/.shannon/sync_marker.json` — high-watermark + per-session retry bookkeeping. `cat` to triage.
- `~/.shannon/sync.lock` — flock for serialization across daemon + CLI calls. **Never delete.**
- `~/.shannon/sync_outbox/` — only in `dry_run` mode; contains JSON batches that would have been uploaded.

## Scheduled Tasks

Run agents on a cron schedule via macOS launchd. Schedules persist across reboots.

```bash
shan schedule create --agent ops-bot --cron "0 9 * * *" --prompt "check production health"
shan schedule create --cron "*/30 * * * *" --prompt "check disk usage"
shan schedule list
shan schedule update <id> --cron "0 8 * * 1-5" --prompt "weekday morning check"
shan schedule enable <id>
shan schedule disable <id>
shan schedule remove <id>
shan schedule sync            # retry failed launchd plists
```

Agents can also manage schedules via tools (`schedule_create`, `schedule_list`, etc.):

```bash
shan "schedule a daily health check at 9am using ops-bot"
shan "what schedules are running?"
shan "cancel the morning health check"
```

Cron supports the full 5-field syntax (via [gronx](https://github.com/adhocore/gronx)): ranges (`1-5`), steps (`*/5`), lists (`1,3,5`), and combinations. Impossible day/month combinations (e.g. `0 0 31 2 *` — Feb 31) are rejected at create time since they would never fire; use `L` (`0 0 L * *`) for the last day of the month rather than `31`.

**How it works:**

- Source of truth: `~/.shannon/schedules.json`
- Execution backend: `~/Library/LaunchAgents/com.shannon.schedule.<id>.plist`
- Each schedule runs `shan -y --agent <name> "<prompt>"` one-shot
- Logs: `~/.shannon/logs/schedule-<id>.log`
- Atomic file writes + file locking prevent corruption
- `SyncStatus`: `ok`, `pending`, or `failed`. `shan schedule sync` retries failures.

## File System Watcher

Agents can react to file changes. Configure in agent `config.yaml`:

```yaml
watch:
  - path: ~/Code/myproject
    glob: "*.go"              # optional — omit to watch all files
  - path: ~/Downloads
    glob: "*.csv"
```

On matching create / modify / delete / rename, the agent receives:

```
File changes detected:
- modified: internal/agent/loop.go
- created: internal/agent/loop_test.go
```

- **Debounce**: 2-second batching window
- **Recursive**: existing subdirs watched at startup; new ones auto-added
- **Routing**: events route to the agent's session (`agent:<name>` key), sharing context with other messages
- **Fan-out**: overlapping watches give each agent its own event batch
- **Reload**: `POST /config/reload` rebuilds watchers from fresh agent configs

## Heartbeat Mode

Agents can run periodic health checks. Define the checklist in `HEARTBEAT.md`:

```bash
cat > ~/.shannon/agents/ops-bot/HEARTBEAT.md << 'EOF'
- Check if any git repos in ~/Code have uncommitted changes
- Check if disk usage > 90%
- Check if any background processes are stuck
EOF
```

Configure in `config.yaml`:

```yaml
heartbeat:
  every: 30m                    # Go duration (required)
  active_hours: "09:00-22:00"   # optional (supports overnight: "22:00-02:00")
  model: small                  # optional — cheaper model for routine checks
  isolated_session: true        # default true — fresh session per heartbeat
```

**Silent-ack protocol**: if everything is fine, the agent replies `HEARTBEAT_OK` — silently dropped (no notification, no session persistence). If something needs attention, the reply is emitted as a `heartbeat_alert` event on the EventBus and logged.

**Cost controls**: isolated sessions (default) carry no history between heartbeats; model override allows cheaper-tier checks; empty `HEARTBEAT.md` skips entirely (no tokens spent); overlap prevention skips the next tick if the previous heartbeat is still running.

## SSE Event Handling

Remote workflows (`/research`, `/swarm`) stream events:

| Event | Display |
|-------|---------|
| `WORKFLOW_STARTED` | `> Starting workflow...` |
| `PROGRESS`, `STATUS_UPDATE` | `> Processing...` |
| `AGENT_STARTED` | `> Agent working...` |
| `TOOL_INVOKED`, `TOOL_STARTED` | `? Calling tool...` |
| `thread.message.delta` | Streaming text (incremental) |
| `thread.message.completed` | Final response |
| `WORKFLOW_FAILED`, `error` | `! Error: ...` |

## UI Behavior

- **Inline terminal rendering** (no alt screen) — allows normal mouse text selection
- **Visible activity updates** — attended sessions show a brief goal before tool work instead of appearing idle
- **Scrollable viewport** with Up/Down/PgUp/PgDn
- **Slash command menu**: appears on `/`, filters as you type, Tab/Enter to select
- **Session picker**: navigable list with Up/Down
- **Token usage**: `[tokens: N | cost: $X.XXXX]` after each response

## Keyboard

| Key | Context | Action |
|-----|---------|--------|
| Up/Down | Output | Scroll viewport |
| Up/Down | Command menu | Navigate items |
| Tab/Enter | Command menu | Insert selected command |
| Enter | Input | Submit message |
| Escape | Menu/picker | Close |
| y/n | Approval prompt | Approve/deny tool call |
| Ctrl+C | Any | Save session and exit |

## Building & Testing

```bash
go build -o shan .           # build
go test ./...                # run all tests
go vet ./...                 # lint
```

Koe voice tests link cgo audio deps on macOS; install them with `brew install opus opusfile pkg-config` and set `PKG_CONFIG_PATH=/opt/homebrew/lib/pkgconfig` if pkg-config cannot find the Homebrew files.

Paid live daemon and Koe tests use a hidden state-isolated child process so they
can run beside Kocoro Desktop. See [Live E2E testing](docs/live-e2e-testing.md)
for the containment boundary, credential warning, commands, and coverage gaps.

## Known Limitations

- **Vision**: screenshots are captured, resized, and sent as base64 image content blocks. On daemon runs, the Sonnet parent delegates one complete desktop goal to `computer_use`; only that call lazily starts a private OpenAI Responses trajectory with native Computer Use. Provider coordinates stay bound to the exact screenshot bytes and map into AppKit logical points. Anthropic-native execution and generic model-visible GUI fallbacks are not admitted on this path. Vision models may blend what they see with training knowledge — verify critical details.
- **Streaming**: one-shot mode does not stream; waits for the full LLM response before display.
- **Windows/Linux**: local tools (clipboard, notifications, AppleScript, screenshot, computer) and scheduled tasks (launchd) are macOS-only.
- **Account login**: email/password sign-in stores the api_key in a per-platform credential store — **macOS Keychain**, **Windows Credential Manager**, and on **Linux** a file store at `~/.shannon/credentials.json` (mode 0600). On Linux the sign-in key is moved out of `config.yaml` into that file; if you manage `config.yaml` with IaC (Ansible/Puppet) note the key now lives in `credentials.json`. On unsupported platforms, set `api_key` in `~/.shannon/config.yaml` instead.
- **Daemon background mode**: `shan daemon start -d` uses launchd (macOS only).
- **Scheduled tasks**: launchd-only. Complex cron expressions (ranges, steps) fall back to `StartInterval` instead of `StartCalendarInterval`.

## License

MIT
