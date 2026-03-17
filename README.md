# Shannon CLI (`shan`)

Interactive AI agent CLI powered by Shannon. Named agents with independent instructions/memory, local tools for computer control, MCP client for third-party integrations (GitHub, Slack, databases, etc.), daemon mode for channel messaging (Slack, Telegram, LINE), local scheduled tasks via launchd, and remote research/swarm orchestration via the Gateway API. macOS focused.

## Installation

### Option A: npm (Recommended)

```bash
npm install -g @kocoro/shanclaw
```

Auto-updates on every launch — no manual upgrading needed.

### Option B: Install Script

Downloads the latest release binary to `/usr/local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/Kocoro-lab/ShanClaw/main/install.sh | sh
```

### Option C: Build from Source

Requires **Go 1.25+**:

```bash
git clone https://github.com/Kocoro-lab/ShanClaw.git
cd shan
go install .
```

> **Note:** `go install` places the binary in `$GOPATH/bin` (default: `~/go/bin`).
> Make sure this directory is in your PATH:
> ```bash
> # Add to your ~/.zshrc or ~/.bashrc if not already present:
> export PATH="$HOME/go/bin:$PATH"
> ```
> Then restart your shell or run `source ~/.zshrc`.

### Verify Installation

```bash
shan --help
```

## Updating

shan auto-updates when you launch it. You can also update explicitly:

```bash
shan update              # manual update
npm update -g @kocoro/shanclaw  # if installed via npm (re-runs postinstall to fetch latest)
```

## Setup

Shannon CLI requires a Gateway API for LLM completions and remote tools.

**Option A: Shannon Cloud** — get an API key from [shannon.run](https://shannon.run):

```bash
shan --setup
# Enter endpoint: https://api-dev.shannon.run
# Enter API key: <your key from shannon.run>
```

**Option B: Self-hosted** — run the open-source [Shannon Gateway](https://github.com/Kocoro-lab/Shannon) locally:

```bash
# Clone and run Shannon Gateway (see repo for full instructions)
git clone https://github.com/Kocoro-lab/Shannon.git
cd Shannon && docker compose up -d

# Point shan to your local instance
shan --setup
# Enter endpoint: http://localhost:8080
# Enter API key: (leave empty for local)
```

## Quick Start

```bash
# Interactive mode — chat with Shannon in your terminal
shan

# One-shot mode — ask a question and get an answer
shan "who is wayland zhang"

# Use a named agent
shan --agent ops-bot "check production health"

# Configure your endpoint and API key
shan --setup
```

### Interactive Commands

In the TUI (`shan`), type `/` to access built-in commands:

```bash
/research deep "latest advances in AI agents"    # deep research via Gateway
/swarm "build a marketing plan for our launch"   # multi-agent orchestration
/model large                                      # switch to large model
/copy                                             # copy last response to clipboard
/sessions                                         # browse and resume past sessions
/session new                                      # start a fresh session
/search websocket reconnect                       # search session history
```

### One-Shot Examples

**Web Search & Research**
```bash
shan "who is wayland zhang"
shan "what happened in the news today"
shan "compare React vs Vue for a new project"
```

**File Operations** — `file_read`, `file_write`, `file_edit`, `glob`, `grep`, `directory_list`
```bash
shan "find all TODO comments in this project"
shan "read the main.go file and explain what it does"
shan "list all files in the current directory"
shan "create a .gitignore for a Go project"
shan "replace all tabs with spaces in config.yaml"
shan "search for any hardcoded passwords in the codebase"
```

**Shell & System** — `bash`, `system_info`, `process`
```bash
shan "run go test and fix any failures"
shan "what's using port 8080"
shan "show my system info — CPU, memory, disk"
shan "list all running node processes"
shan -y "kill the process on port 3000"
```

**macOS App Control** — `applescript` (use `-y` to auto-approve)
```bash
shan -y "open Calculator"
shan -y "use applescript to open Safari and navigate to github.com"
shan -y "use applescript to open my Downloads folder in Finder"
shan -y "set my Mac volume to 50%"
shan -y "get the name of the frontmost application"
```

**Notifications & Clipboard** — `notify`, `clipboard`
```bash
shan -y "send me a notification saying 'Build complete!'"
shan -y "copy the current date to my clipboard"
shan -y "read my clipboard and summarize the content"
```

**GUI Interaction via Accessibility Tree** — `accessibility`, `wait_for` (annotate → click by ref)
```bash
shan -y "annotate the Finder window and tell me what you see"
shan -y "open Calendar and show me today's events"
shan -y "open System Settings and check my display resolution"
shan -y "open Finder to Downloads and list the files"
shan -y "open TextEdit, create a new document, and type 'hello world'"
shan -y "open Notes and type '你好世界 🌍'"
shan -y "find the search field in Safari and type 'shannon ai'"
```

**Screenshot & Computer Use** — `screenshot`, `computer` (vision + CGEvent, CJK/emoji safe)
```bash
shan -y "take a screenshot and tell me what's on my screen"
shan -y "open LINE app and bring it to the front"
shan -y "open Chrome, go to x.com, and post a tweet"
shan -y "click on the Finder icon in the dock"
```

**Browser Automation** — `browser` (pinchtab for real browser, chromedp fallback)
```bash
shan -y "open https://news.ycombinator.com and get the top 5 stories"
shan -y "navigate to waylandz.com and take a browser screenshot"
shan -y "go to wikipedia.org, search for 'Shannon entropy', and summarize the page"
```

**Ghostty Terminal** — `ghostty` (requires [Ghostty](https://ghostty.org) >= 1.3.0)
```bash
# In TUI — agent opens and controls Ghostty terminals
"open a new terminal and run top"
"open a new split to the right running go test ./... -v and tell me the results"
"set up my dev environment: open a new terminal running the server, and a new split tailing logs"

# CLI — open one Ghostty window per agent
shan ghostty workspace                    # all agents
shan ghostty workspace writer ops-bot     # specific agents
```

**HTTP Requests** — `http`
```bash
shan "check if https://api.github.com is responding"
shan "fetch https://httpbin.org/ip and show my public IP"
```

**MCP Integrations** (requires MCP server config in `~/.shannon/config.yaml`)
```bash
shan "list my github repos"
shan "create an issue in myrepo titled 'Bug: login fails'"
shan "search slack for messages about deployment"
shan "show all tables in the database"
```

## Requirements

- **macOS** (clipboard, notifications, AppleScript, screencapture, accessibility)
- **Shannon Gateway** at configurable endpoint (for LLM completions + remote tools)
- **Accessibility permission** granted in System Settings > Privacy & Security > Accessibility (for `accessibility` and `computer` tools)
- **Chrome** (optional, for `browser` tool — chromedp with isolated profile)
- **[Ghostty](https://ghostty.org) >= 1.3.0** (optional, for `ghostty` tool — terminal tabs, splits, input)

## CLI Usage

```bash
shan                              # interactive TUI
shan "who is wayland zhang"       # one-shot mode (prompts for tool approval)
shan -y "query"                   # one-shot, auto-approve all tools
shan --agent ops-bot "query"      # use a named agent
shan --setup                      # configure endpoint + API key
shan mcp serve                    # start MCP server over stdio
shan daemon start                 # start channel messaging daemon
shan schedule list                # manage local scheduled tasks
```

### Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--yes` | `-y` | Auto-approve all tool calls in one-shot mode |
| `--agent` | | Named agent to use (from `~/.shannon/agents/`) |
| `--dangerously-skip-permissions` | | Skip all permission checks in interactive mode |
| `--setup` | | Run interactive setup wizard |

## Commands

Type `/` in the TUI to see the interactive command menu:

| Command | Description |
|---------|-------------|
| `/help` | Show help |
| `/research [quick\|standard\|deep] <query>` | Remote research via Gateway |
| `/swarm <query>` | Multi-agent swarm orchestration |
| `/copy` | Copy last response to clipboard |
| `/model [small\|medium\|large]` | Switch model tier |
| `/config` | Show merged config with sources |
| `/sessions` | Interactive session picker |
| `/session new` | Start new session |
| `/session resume <n>` | Resume session by number or ID |
| `/search <query>` | Search session history (keyword, phrase, stemming) |
| `/clear` | Clear screen |
| `/update` | Self-update from GitHub releases |
| `/setup` | Reconfigure endpoint & API key |
| `/quit` | Exit (alias: `/exit`) |
| `/<custom>` | Custom commands from `.shannon/commands/*.md` or agent `commands/` and `skills/` |

### Subcommands

| Command | Description |
|---------|-------------|
| `shan mcp serve` | Start MCP server over stdio |
| `shan daemon start` | Start channel messaging daemon |
| `shan daemon stop` | Stop background daemon |
| `shan daemon status` | Show daemon connection status |
| `shan schedule create` | Create a scheduled task |
| `shan schedule list` | List scheduled tasks |
| `shan schedule update <id>` | Update a scheduled task |
| `shan schedule remove <id>` | Remove a scheduled task |
| `shan schedule enable <id>` | Enable a scheduled task |
| `shan schedule disable <id>` | Disable a scheduled task |
| `shan schedule sync` | Re-sync failed launchd plists |

## Local Tools

Local tools executed on your macOS machine:

### File Operations

| Tool | Approval | Description |
|------|----------|-------------|
| `file_read` | CWD auto | Read files with line numbers, supports offset/limit |
| `file_write` | Yes | Write/create files, creates parent dirs |
| `file_edit` | Yes | Find-and-replace (old_string must be unique) |
| `glob` | CWD auto | Find files by pattern (supports `**` recursive) |
| `grep` | CWD auto | Search file contents (ripgrep, falls back to grep) |
| `directory_list` | CWD auto | List directory contents with sizes |

### System & Shell

| Tool | Approval | Description |
|------|----------|-------------|
| `bash` | Auto for safe | Shell commands, 120s timeout, safe commands auto-approved |
| `system_info` | No | OS, arch, hostname, CPU, memory, disk |
| `process` | Auto for list/ports | Process management: list, ports, kill |
| `http` | Network allowlist | HTTP client, localhost auto-approved |

### macOS Control

| Tool | Approval | Description |
|------|----------|-------------|
| `accessibility` | Yes | **Primary GUI tool.** Reads macOS accessibility tree via persistent `ax_server` (compiled Swift sidecar). Actions: `read_tree`, `click`, `press`, `set_value`, `get_value`, `find`, `scroll`, `annotate`. Semantic depth traversal (layout containers cost 0), click auto-fallback (AXPress → synthetic coordinate click). Works with Finder, Safari, Chrome, TextEdit, Calendar, System Settings, etc. |
| `wait_for` | Yes | Wait for UI conditions: `elementExists`, `elementGone`, `titleContains`, `urlContains`, `titleChanged`, `urlChanged`. Use instead of sleep after navigation or app launch. |
| `clipboard` | Yes | Read/write system clipboard (pbcopy/pbpaste) |
| `notify` | Yes | macOS desktop notifications via osascript |
| `applescript` | Yes | Execute arbitrary AppleScript. Use for operations with no AX equivalent (e.g., "tell Finder to empty trash") |
| `screenshot` | Yes | Screen capture (fullscreen/window/region). Visual fallback when accessibility tree is insufficient |
| `computer` | Yes | Mouse/keyboard via CGEvent (CJK/emoji safe). Click, type, hotkey, move, screenshot. Fallback when accessibility refs don't work or for drag operations. No Python dependency. |
| `browser` | Yes | Browser automation via pinchtab (preferred) or chromedp fallback. Isolated profile for web scraping; pinchtab connects to user's real browser for authenticated sessions. |

### Scheduling

| Tool | Approval | Description |
|------|----------|-------------|
| `schedule_create` | Yes | Create a launchd-backed scheduled task |
| `schedule_list` | No | List all scheduled tasks with sync status |
| `schedule_update` | Yes | Update cron, prompt, or enabled state |
| `schedule_remove` | Yes | Remove a scheduled task and unload plist |

### Session Search

| Tool | Approval | Description |
|------|----------|-------------|
| `session_search` | No | FTS5 keyword search across past session messages |

### Tool Approval Flow

```
Tool call from LLM
  → Permission engine (hard-block → denied_commands → allowed_commands)
  → RequiresApproval + SafeChecker
  → Pre-tool hook (can deny)
  → Execute tool
  → Post-tool hook
  → Audit log
```

- **Hard-blocked**: `rm -rf /`, `mkfs`, `dd if=`, `curl|sh`, etc. — always denied, cannot be overridden
- **CWD auto-approve**: Read-only tools (`file_read`, `glob`, `grep`, `directory_list`) auto-approve paths under the current working directory
- **Auto-approve**: Safe bash commands (`ls`, `git status`, `go test`, `make`, etc.), `process list/ports`, localhost HTTP
- **Prompt**: Destructive tools show `[y/n]` in TUI or one-shot mode
- **Denied-call blocking**: If you deny a tool call, the same tool+args won't be re-prompted for the rest of the turn
- **`-y` flag**: Auto-approves everything in one-shot mode
- **No handler**: Denied by default (security fail-safe)

## Permission Engine

5-layer command checking:

1. **Hard-block** — built-in constants (rm -rf /, mkfs, dd, curl|sh, etc.), cannot be overridden
2. **Denied commands** — `permissions.denied_commands` in config
3. **Compound command splitting** — commands split on `&&`, `||`, `;`, `|`, each sub-command checked independently
4. **Allowed commands** — `permissions.allowed_commands` in config (glob patterns)
5. **User approval** — interactive prompt or `-y` flag

Additional checks:
- **File paths**: symlink protection (`filepath.EvalSymlinks`), sensitive file patterns (`.env`, `*.pem`, `id_rsa`), allowed_dirs
- **Network egress**: allowlist-based, localhost always allowed
- **Hooks**: PreToolUse can deny with exit code 2

## Audit Logging

All tool calls are logged to `~/.shannon/logs/audit.log`:

- JSON-lines format, append-only
- Each entry: timestamp, session ID, tool name, input/output summary, decision, approved, duration
- **Auto-redaction**: AWS keys, JWT, `sk-`/`key-` prefixes, Bearer tokens, PEM markers, env var assignments

## Hooks

Shell scripts triggered at lifecycle events:

| Hook | When | Can Deny |
|------|------|----------|
| `PreToolUse` | Before tool execution | Yes (exit 2) |
| `PostToolUse` | After tool execution | No |
| `SessionStart` | Session begins | No |
| `Stop` | Session ends | No |

Configure in `~/.shannon/config.yaml`:

```yaml
hooks:
  PostToolUse:
    - matcher: "file_edit|file_write"
      command: ".shannon/hooks/post-edit.sh"
```

Hook protocol:
- Receives JSON on stdin with tool name, arguments, result
- Exit 0 = allow, exit 2 = deny (PreToolUse only)
- 10s timeout, 10KB output limit
- Hook commands must use `./` prefix (relative) or absolute paths under `~/.shannon/` (bare command names and absolute paths outside `~/.shannon/` are rejected for security)

## MCP Server

Expose local tools to MCP clients via JSON-RPC 2.0 over stdio:

```bash
shan mcp serve
```

The MCP server enforces the same permission engine, hooks, and audit logging as the interactive CLI. Tools requiring approval are denied in MCP mode (no interactive TTY).

Supported methods:
- `initialize` — handshake with protocol version
- `tools/list` — returns all tool schemas
- `tools/call` — execute a tool by name with arguments

Example:
```bash
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | shan mcp serve
echo '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"system_info","arguments":{}}}' | shan mcp serve
```

## MCP Client

Connect to external MCP servers to extend Shannon with third-party tools. Configure in `~/.shannon/config.yaml` under `mcp_servers:`.

Each server supports:
- `command` / `args` — stdio transport (default)
- `type: http` + `url` — HTTP transport
- `env` — environment variables passed to the server process
- `context` — guidance injected into the LLM system prompt (critical for correct tool usage)
- `disabled: true` — skip without removing config

### GitHub

```yaml
mcp_servers:
  github:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-github"]
    env:
      GITHUB_PERSONAL_ACCESS_TOKEN: "ghp_xxxxx"
    context: "Authenticated as GitHub user 'yourname'. Use search_repositories with query 'user:yourname'."
```

```bash
shan "list my github repos"
shan "create an issue in myrepo titled 'Bug: login fails'"
shan "show open PRs in shan"
```

### Slack

```yaml
mcp_servers:
  slack:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-slack"]
    env:
      SLACK_BOT_TOKEN: "xoxb-xxxxx"
      SLACK_TEAM_ID: "T01234567"
    context: "Connected to Slack workspace 'MyTeam'. Use list_channels to find channels."
```

```bash
shan "list slack channels"
shan "search slack for messages about deployment"
```

### Filesystem

```yaml
mcp_servers:
  filesystem:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/Users/you/Desktop", "/Users/you/Documents"]
    context: "Filesystem access to ~/Desktop and ~/Documents. Use read_file, write_file, list_directory, search_files."
```

```bash
shan "list files on my Desktop"
shan "search for .md files in my Documents"
shan "create a notes.txt file on my Desktop"
```

### Puppeteer (Browser Automation)

```yaml
mcp_servers:
  puppeteer:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-puppeteer"]
    context: "Browser automation via Puppeteer. Use puppeteer_navigate, puppeteer_screenshot, puppeteer_click, puppeteer_fill, puppeteer_evaluate."
```

```bash
shan -y "navigate to https://example.com and take a screenshot"
shan -y "navigate to https://news.ycombinator.com and get the top 5 story titles"
```

### PostgreSQL

```yaml
mcp_servers:
  postgres:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-postgres", "postgresql://user:pass@localhost:5432/mydb"]
    context: "Connected to mydb PostgreSQL database. Use query tool for SELECT."
```

```bash
shan "show all tables in the database"
shan "how many users signed up this week?"
```

### SQLite

```yaml
mcp_servers:
  sqlite:
    command: "npx"
    args: ["-y", "mcp-server-sqlite-npx", "/path/to/database.db"]
    context: "Connected to SQLite database. Use read_query for SELECT, write_query for writes."
```

### Brave Search

```yaml
mcp_servers:
  brave-search:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-brave-search"]
    env:
      BRAVE_API_KEY: "BSAxxxxx"
    context: "Use brave_web_search for web queries. Use brave_local_search for local businesses."
```

### Google Maps

```yaml
mcp_servers:
  google-maps:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-google-maps"]
    env:
      GOOGLE_MAPS_API_KEY: "AIzaxxxxx"
    context: "Use maps_search_places to find places. Use maps_directions for routing."
```

### Sentry (Error Tracking)

```yaml
mcp_servers:
  sentry:
    command: "npx"
    args: ["@sentry/mcp-server"]
    env:
      SENTRY_ACCESS_TOKEN: "sntrys_xxxxx"
    context: "Connected to Sentry org. Use to query issues, error events, and stack traces."
```

### Linear (Project Management)

```yaml
mcp_servers:
  linear:
    command: "npx"
    args: ["-y", "@linear/mcp-server"]
    env:
      LINEAR_API_KEY: "lin_api_xxxxx"
    context: "Connected to Linear workspace. Use to list/create issues, search projects."
```

### Git (Repo Analysis)

```yaml
mcp_servers:
  git:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-git"]
    context: "Use git_log, git_diff, git_show for repository analysis."
```

### HTTP Transport (Remote MCP Server)

```yaml
mcp_servers:
  my-remote-server:
    type: http
    url: "https://mcp.example.com/sse"
    context: "Remote MCP server providing custom tools."
```

### Multiple Servers

You can run multiple MCP servers simultaneously:

```yaml
mcp_servers:
  github:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-github"]
    env:
      GITHUB_PERSONAL_ACCESS_TOKEN: "ghp_xxxxx"
    context: "GitHub user 'yourname'. query 'user:yourname' for repos."
  slack:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-slack"]
    env:
      SLACK_BOT_TOKEN: "xoxb-xxxxx"
      SLACK_TEAM_ID: "T01234567"
    context: "Slack workspace 'MyTeam'."
  postgres:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-postgres", "postgresql://localhost/mydb"]
    context: "PostgreSQL mydb."
```

### MCP Client Notes

- **`context` is critical** — tells the LLM who's authenticated and what queries to use. Without it, the LLM guesses wrong.
- **All MCP tools require approval**. Use `shan -y` for auto-approve in one-shot mode.
- **Local tools take priority** — if an MCP tool has the same name as a local tool, the local one wins.
- **Project-level overrides** — put server configs in `.shannon/config.yaml` (project) or `.shannon/config.local.yaml` (gitignored).
- **One-shot vs interactive** — each `shan "query"` starts fresh MCP connections. In interactive TUI mode (`shan`), connections persist for the session.
- Find more servers at [MCP Server Registry](https://registry.modelcontextprotocol.io/) and [Awesome MCP Servers](https://github.com/punkpeye/awesome-mcp-servers).

## Configuration

### Multi-level Config

Config files are merged in order (later overrides earlier):

1. `~/.shannon/config.yaml` — global
2. `.shannon/config.yaml` — project
3. `.shannon/config.local.yaml` — local override (gitignored)

Merge behavior: scalars override, lists merge + dedup, structs field-level merge.

### Full Config Structure

```yaml
# Connection
endpoint: http://localhost:8080    # Shannon Gateway URL
api_key: ""                        # Gateway API key
model_tier: medium                 # small, medium, large (default: medium)

# Permissions
permissions:
  allowed_dirs:
    - ~/Documents/notes
    - ./docs
  allowed_commands:
    - "git *"
    - "go test *"
    - "make *"
  denied_commands:
    - "rm -rf *"
  network_allowlist:
    - "localhost"
    - "127.0.0.1"
    - "api.example.com"

# MCP servers (external tool sources)
mcp_servers:
  github:
    command: "npx"
    args: ["-y", "@modelcontextprotocol/server-github"]
    env:
      GITHUB_PERSONAL_ACCESS_TOKEN: "ghp_xxxxx"
    context: "GitHub user 'yourname'."

# Agent behavior
agent:
  max_iterations: 25               # max tool calls per turn (default: 25)
  temperature: 0                   # LLM temperature (default: 0)
  max_tokens: 32000                # max output tokens (default: 32000)
  thinking: true                   # enable extended thinking (default: true)
  thinking_mode: adaptive          # "adaptive" or "enabled" (default: adaptive)
  thinking_budget: 10000           # thinking token budget (default: 10000)
  model: ""                        # specific model override (empty = use model_tier)
  context_window: 128000           # context window in tokens (default: 128000)

# Tool settings
tools:
  bash_timeout: 120                # seconds (default: 120)
  bash_max_output: 30000           # max chars in bash output (default: 30000)
  result_truncation: 2000          # max chars in tool result (default: 2000)
  args_truncation: 200             # max chars in displayed args (default: 200)
  server_tool_timeout: 5           # gateway tool timeout in seconds (default: 5)
  grep_max_results: 100            # max grep results (default: 100)

# Hooks
hooks:
  PreToolUse:
    - matcher: "bash"
      command: ".shannon/hooks/check-bash.sh"
  PostToolUse:
    - matcher: "file_edit|file_write"
      command: ".shannon/hooks/post-edit.sh"
  SessionStart:
    - command: ".shannon/hooks/on-start.sh"
  Stop:
    - command: ".shannon/hooks/on-stop.sh"
```

Use `/config` in the TUI to see the merged config with sources showing which file each value came from.

### UI Settings — `~/.shannon/settings.json`

```json
{
  "spinner_texts": [
    "Thinking deeply...",
    "Exploring possibilities...",
    "Connecting the dots..."
  ]
}
```

## Instructions & Memory

### Instructions

AI behavior customization loaded from markdown files:

- `~/.shannon/instructions.md` — global instructions
- `.shannon/instructions.md` — project instructions

Both are loaded into the system prompt (token-budgeted, deduplicated).

### Persistent Memory

- `~/.shannon/memory/MEMORY.md` — first 200 lines loaded on startup
- The agent can write to this file to remember information across sessions

### Custom Slash Commands

Create `.shannon/commands/<name>.md` or `~/.shannon/commands/<name>.md`:

```markdown
Review the following code for bugs and security issues.
Focus on: $ARGUMENTS
```

This creates a `/name` command in the TUI. `$ARGUMENTS` is replaced with whatever follows the command.

## Sessions

Conversations are persisted as JSON files in `~/.shannon/sessions/` (or `~/.shannon/agents/<name>/sessions/` for named agents).

- Each session is a `<id>.json` file containing messages, metadata, and remote task IDs
- Saved after each agent turn and on exit
- Titles generated from the first user message (truncated to 50 chars)
- **Search index**: a `sessions.db` (SQLite FTS5) is auto-created alongside JSON files for fast keyword search. Safe to delete — rebuilds automatically on next launch

```
/sessions                              # interactive picker
/session resume 1                      # by number
/session resume 2026-02-23-a1b2c3      # by full ID
/session new                           # start fresh
```

## Named Agents

Create independent agents with their own instructions, memory, tools, MCP servers, and model settings:

```
~/.shannon/agents/
  ops-bot/
    AGENT.md          # agent instructions (replaces default system prompt)
    MEMORY.md         # agent-specific memory (persists across sessions)
    config.yaml       # optional: tool filtering, MCP scoping, model overrides
    commands/          # optional: agent-scoped slash commands (*.md)
    skills/            # optional: agent-scoped skills (skill-name/SKILL.md)
```

### Creating an Agent

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

### Agent Config

Create `config.yaml` to scope an agent's capabilities:

```yaml
# Tool allow list — agent can ONLY use these tools
tools:
  allow:
    - file_read
    - grep
    - glob
    - bash
    - think
    - directory_list

# Per-agent MCP servers
# _inherit: false → only these servers (ignore global config)
# _inherit: true  → merge on top of global servers
mcp_servers:
  _inherit: false
  github:
    command: mcp-server-github
    env:
      GITHUB_TOKEN: "${GITHUB_TOKEN}"

# Model and behavior overrides
agent:
  model: "claude-sonnet-4-6"
  max_iterations: 10
  temperature: 0.2
  max_tokens: 16000
  context_window: 64000

# File system watcher — trigger agent on file changes
watch:
  - path: ~/Code/myproject
    glob: "*.go"
  - path: ~/Downloads
    glob: "*.csv"

# Heartbeat — periodic "anything need attention?" checks
heartbeat:
  every: 30m                    # Go duration (required)
  active_hours: "09:00-22:00"   # optional time window (supports overnight e.g. "22:00-02:00")
  model: small                  # optional cheaper model for heartbeat runs
  isolated_session: true        # default true — fresh session per heartbeat
```

**Tool filtering options:**

```yaml
# Allow list — only these tools available
tools:
  allow: [file_read, grep, glob, bash]

# OR deny list — all tools EXCEPT these
tools:
  deny: [computer, browser, screenshot, applescript]

# Omit tools section entirely → all tools available
```

The filter applies to all tool sources (local, MCP, gateway). If both `allow` and `deny` are set, `allow` takes precedence.

### Agent Commands

Create `.md` files in the `commands/` directory to add agent-scoped slash commands:

```bash
mkdir -p ~/.shannon/agents/reviewer/commands

cat > ~/.shannon/agents/reviewer/commands/review.md << 'EOF'
Review the code in $ARGUMENTS for:
- Correctness and logic errors
- Security vulnerabilities
- Performance issues
Focus on bugs that matter, skip nitpicks.
EOF
```

Use in the TUI: `/review src/auth/login.go`

`$ARGUMENTS` is replaced with everything after the command name. Agent commands cannot overwrite built-in commands (`/help`, `/quit`, etc.).

### Agent Skills

Skills use the [Anthropic SKILL.md spec](https://agentskills.io/specification). Create a directory per skill with a `SKILL.md` file:

```bash
mkdir -p ~/.shannon/agents/reviewer/skills/summarize

cat > ~/.shannon/agents/reviewer/skills/summarize/SKILL.md << 'EOF'
---
name: summarize
description: Summarize codebase architecture
---

Provide a concise summary of the architecture and key decisions.
Focus on: entry points, data flow, error handling patterns.
EOF
```

Skills are listed in the system prompt (name + description only). The LLM activates a skill by calling the `use_skill` tool, which returns the full SKILL.md body — progressive disclosure that keeps prompt size small.

**Skill sources (priority order):** agent-scoped (`skills/`) > global (`~/.shannon/skills/`) > bundled.

Skills also appear as `/summarize` slash commands in the TUI.

### Using Agents

```bash
# One-shot mode
shan --agent ops-bot "check error rate in prod"

# Interactive TUI (with agent commands and skills as /slash commands)
shan --agent ops-bot

# In daemon mode, @mention routes to agents:
# "@ops-bot check prod" → ops-bot agent
# "@reviewer look at PR" → reviewer agent
# "check prod" → default Shannon agent
```

### Agent Name Rules

Names must match `^[a-z0-9][a-z0-9_-]{0,63}$` (lowercase alphanumeric, hyphens, underscores).

### Agent-Scoped Sessions

Each agent gets its own session directory at `~/.shannon/agents/<name>/sessions/`, keeping conversation histories separate.

## Daemon Mode

The daemon serves two roles: it connects to Shannon Cloud via WebSocket to receive channel messages (Slack, LINE, etc.), and it exposes a local HTTP API on port 7533 for native apps and scripts.

```bash
shan daemon start           # foreground (logs to stdout)
shan daemon start -d        # background via launchd (macOS, survives reboots)
shan daemon stop            # stop daemon + remove launchd service if installed
shan daemon status          # show connection status + launchd state
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

- **Envelope protocol** — typed messages with claim/ack handshake (broadcast + first-to-claim)
- **Progress heartbeats** — 15s interval extends claim TTL during long agent runs
- **Channel routing** — agent name set per channel in cloud config, fallback to `@mention` parsing
- **Session continuity** — conversation history maintained per agent across messages
- **Up to 5 concurrent agents** — bounded worker pool prevents resource exhaustion
- **Auto-reconnect** with exponential backoff on connection loss
- **Graceful disconnect** — sends disconnect message on shutdown
- **Schedule mutation tools** (`schedule_create/update/remove`) are denied by default in daemon mode
- **Interactive approval** — tools requiring approval send requests to the client app (via WS relay through Shannon Cloud); supports "always allow" persistence for bash commands
- **HITL message injection** — send follow-up messages to a running agent via `POST /message`; messages are injected mid-turn and incorporated into the conversation

### Local HTTP API (port 7533)

The daemon exposes a localhost-only HTTP server for native app integration and scripting.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Liveness check → `{"status":"ok","version":"..."}` |
| `/status` | GET | Connection state, active agent, uptime, version |
| `/agents` | GET | List named agents from `~/.shannon/agents/` |
| `/sessions` | GET | List sessions, optional `?agent=` filter |
| `/sessions/search` | GET | Search session history, `?q=<query>&agent=<name>` |
| `/message` | POST | Send a message to an agent, get reply (supports HITL injection) |
| `/config/reload` | POST | Reload config, restart watchers and heartbeat managers |
| `/events` | GET | SSE stream of daemon events (`agent_reply`, `heartbeat_alert`, etc.) |
| `/shutdown` | POST | Graceful daemon shutdown (used by `shan daemon stop`) |

**Send a message:**

```bash
# Synchronous (blocks until agent completes)
curl -X POST http://localhost:7533/message \
  -d '{"text":"what is 2+2?"}' \
  -H "Content-Type: application/json"

# With a named agent and session resumption
curl -X POST http://localhost:7533/message \
  -d '{"text":"check disk usage","agent":"ops-bot","session_id":"2026-03-08-abc123"}' \
  -H "Content-Type: application/json"

# SSE streaming (tool progress + text deltas)
curl -X POST http://localhost:7533/message \
  -d '{"text":"analyze this codebase"}' \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream"
```

**Response (synchronous):**

```json
{
  "reply": "2+2 equals 4.",
  "session_id": "2026-03-08-a1b2c3d4e5f6",
  "agent": "",
  "usage": {"input_tokens": 150, "output_tokens": 20, "total_tokens": 170, "cost_usd": 0.002}
}
```

## Scheduled Tasks

Run agents on a cron schedule using macOS launchd. Schedules persist across reboots.

### CLI Management

```bash
shan schedule create --agent ops-bot --cron "0 9 * * *" --prompt "check production health"
shan schedule create --cron "*/30 * * * *" --prompt "check disk usage"
shan schedule list
shan schedule update <id> --cron "0 8 * * 1-5" --prompt "weekday morning check"
shan schedule enable <id>
shan schedule disable <id>
shan schedule remove <id>
shan schedule sync            # re-sync failed/pending launchd plists
```

### Agent-Accessible Tools

Agents can also manage schedules via tools:

```bash
shan "schedule a daily health check at 9am using ops-bot"
# Agent calls schedule_create tool → generates launchd plist
shan "what schedules are running?"
# Agent calls schedule_list tool
shan "cancel the morning health check"
# Agent calls schedule_remove tool
```

| Tool | Approval | Description |
|------|----------|-------------|
| `schedule_create` | Yes | Create a new scheduled task |
| `schedule_list` | No | List all scheduled tasks |
| `schedule_update` | Yes | Update cron, prompt, or enabled state |
| `schedule_remove` | Yes | Remove a scheduled task |

### Cron Syntax

Full 5-field cron expressions supported (via [gronx](https://github.com/adhocore/gronx)):

```
┌───── minute (0-59)
│ ┌───── hour (0-23)
│ │ ┌───── day of month (1-31)
│ │ │ ┌───── month (1-12)
│ │ │ │ ┌───── day of week (0-6, Sun=0)
│ │ │ │ │
* * * * *
```

Supports ranges (`1-5`), steps (`*/5`), lists (`1,3,5`), and combinations.

### How It Works

- **Source of truth:** `~/.shannon/schedules.json` (JSON index with sync status)
- **Execution backend:** `~/Library/LaunchAgents/com.shannon.schedule.<id>.plist`
- Each schedule runs `shan -y --agent <name> "<prompt>"` in one-shot mode
- Logs written to `~/.shannon/logs/schedule-<id>.log`
- Atomic file writes (temp + rename) and file locking prevent corruption
- `SyncStatus` tracks whether launchd is in sync: `ok`, `pending`, or `failed`
- `shan schedule sync` retries any failed plist operations

## File System Watcher

Agents can react to file changes in real-time. Configure watched paths in the agent's `config.yaml`:

```yaml
watch:
  - path: ~/Code/myproject
    glob: "*.go"              # optional — omit to watch all files
  - path: ~/Downloads
    glob: "*.csv"
```

When files matching the glob are created, modified, deleted, or renamed, the agent receives a prompt like:

```
File changes detected:
- modified: internal/agent/loop.go
- created: internal/agent/loop_test.go
```

- **Debounce**: Changes are batched over a 2-second window to avoid flooding from rapid saves
- **Recursive**: Existing subdirectories are watched at startup; new subdirectories are auto-added
- **Routing**: Events route to the agent's session (`agent:<name>` key), sharing context with other messages
- **Fan-out**: If multiple agents watch overlapping directories, each gets its own event batch
- **Reload**: `POST /config/reload` rebuilds all watchers from fresh agent configs

## Heartbeat Mode

Agents can run periodic health checks using a configurable heartbeat. Define the checklist in `HEARTBEAT.md`:

```bash
cat > ~/.shannon/agents/ops-bot/HEARTBEAT.md << 'EOF'
- Check if any git repos in ~/Code have uncommitted changes
- Check if disk usage > 90%
- Check if any background processes are stuck
EOF
```

Configure the interval in `config.yaml`:

```yaml
heartbeat:
  every: 30m                    # required — Go duration
  active_hours: "09:00-22:00"   # optional (supports overnight e.g. "22:00-02:00")
  model: small                  # optional — cheaper model for cost control
  isolated_session: true        # default true — fresh session per heartbeat
```

### Silent-Ack Protocol

If everything is fine, the agent replies `HEARTBEAT_OK` — this is silently dropped (no notification, no session persistence). If something needs attention, the reply is emitted as a `heartbeat_alert` event on the EventBus and logged.

### Cost Controls

- **Isolated sessions** (default): No conversation history carried between heartbeats
- **Model override**: Use a cheaper model tier for routine checks
- **Empty checklist**: If `HEARTBEAT.md` is missing or empty, the heartbeat is skipped entirely (no tokens spent)
- **Overlap prevention**: If a previous heartbeat is still running, the next tick is skipped

## SSE Event Handling

Remote workflows (`/research`, `/swarm`) stream events via SSE:

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

## Known Limitations

- **Vision**: Screenshots are captured, resized (1200px max), and sent as base64 image content blocks to the LLM. The computer tool uses Anthropic's native `computer_20251124` schema with coordinate scaling for retina displays. Vision models may blend what they see with training knowledge — verify critical details.
- **Streaming**: One-shot mode does not stream responses; it waits for the full LLM response before displaying.
- **Windows/Linux**: Local tools (clipboard, notifications, AppleScript, screenshot, computer) and scheduled tasks (launchd) are macOS-only.
- **Daemon background mode**: `shan daemon start -d` uses launchd (macOS only).
- **Scheduled tasks**: launchd-only (macOS). Complex cron expressions (ranges, steps) fall back to `StartInterval` instead of `StartCalendarInterval`.

## License

MIT
