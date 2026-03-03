# Shannon CLI (`shan`)

Interactive AI agent CLI powered by Shannon. Local tools for computer control, MCP client for third-party integrations (GitHub, Slack, databases, etc.), and remote research/swarm orchestration via the Gateway API. macOS focused.

## Installation

### Option A: Homebrew (Recommended)

```bash
brew install Kocoro-lab/tap/shan
```

### Option B: Install Script

Downloads the latest release binary to `/usr/local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/Kocoro-lab/shan/main/install.sh | sh
```

### Option C: Build from Source

Requires **Go 1.25+**:

```bash
git clone https://github.com/Kocoro-lab/shan.git
cd shan
go install -ldflags "-X github.com/Kocoro-lab/shan/cmd.Version=0.1.0" .
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

**GUI Interaction via Accessibility Tree** — `accessibility` (primary: read UI elements, click/type by ref)
```bash
shan -y "open Calendar and show me today's events"
shan -y "open System Settings and check my display resolution"
shan -y "open Finder to Downloads and list the files"
shan -y "open TextEdit, create a new document, and type 'hello world'"
shan -y "open Reminders and mark 'Buy groceries' as done"
```

**Screenshot & Computer Use** — `screenshot`, `computer` (vision fallback: act → screenshot → observe → decide)
```bash
shan -y "take a screenshot and tell me what's on my screen"
shan -y "open LINE app and bring it to the front"
shan -y "open Chrome, search for 'vivaia', and list the first 5 results"
shan -y "click on the Finder icon in the dock"
```

**Browser Automation** — `browser` (isolated Chrome via chromedp)
```bash
shan -y "open https://news.ycombinator.com and get the top 5 stories"
shan -y "navigate to waylandz.com and take a browser screenshot"
shan -y "go to wikipedia.org, search for 'Shannon entropy', and summarize the page"
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

- **macOS** (clipboard, notifications, AppleScript, screencapture, Quartz mouse control)
- **Shannon Gateway** at configurable endpoint (for LLM completions + remote tools)
- **Swift toolchain** (Xcode CLI tools, for `accessibility` tool — present on all standard macOS installs)
- **Accessibility permission** granted in System Settings > Privacy & Security > Accessibility (for `accessibility` tool)
- **Python 3 + pyobjc-framework-Quartz** (optional, for `computer` tool mouse/click control)
- **Chrome** (optional, for `browser` tool — chromedp with isolated profile)

## CLI Usage

```bash
shan                              # interactive TUI
shan "who is wayland zhang"       # one-shot mode (prompts for tool approval)
shan -y "query"                   # one-shot, auto-approve all tools
shan --setup                      # configure endpoint + API key
shan mcp serve                    # start MCP server over stdio
```

### Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--yes` | `-y` | Auto-approve all tool calls in one-shot mode |
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
| `/clear` | Clear screen |
| `/update` | Self-update from GitHub releases |
| `/quit` | Exit |
| `/<custom>` | Custom commands from `.shannon/commands/*.md` |

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
| `accessibility` | Yes | **Primary GUI tool.** Reads macOS accessibility tree (AXUIElement), interact by ref: `read_tree`, `click`, `press`, `set_value`, `get_value`. Works with Finder, Safari, TextEdit, Calendar, System Settings, etc. |
| `clipboard` | Yes | Read/write system clipboard (pbcopy/pbpaste) |
| `notify` | Yes | macOS desktop notifications via osascript |
| `applescript` | Yes | Execute arbitrary AppleScript. Use for operations with no AX equivalent (e.g., "tell Finder to empty trash") |
| `screenshot` | Yes | Screen capture (fullscreen/window/region). Visual fallback when accessibility tree is insufficient |
| `computer` | Yes | Coordinate-based mouse/keyboard. Fallback when accessibility refs don't work or for drag operations |
| `browser` | Yes | Chromedp with isolated Chrome profile (navigate/click/type/screenshot/read_page/execute_js/wait/close) |

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
- **`-y` flag**: Auto-approves everything in one-shot mode
- **No handler**: Denied by default (security fail-safe)

## Permission Engine

5-layer command checking:

1. **Hard-block** — built-in constants (rm -rf /, mkfs, dd, curl|sh, etc.), cannot be overridden
2. **Denied commands** — `permissions.denied_commands` in config
3. **Shell AST parsing** — compound commands split on `&&`, `||`, `;`, `|`, each sub-command checked independently
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
- Hook commands must use absolute paths or `./` prefix (bare command names are rejected for security)

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
model_tier: medium                 # small, medium, large

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
  max_iterations: 25               # max tool calls per turn

# Tool settings
tools:
  bash_timeout: 120                # seconds
  result_truncation: 2000          # max chars in tool result
  args_truncation: 200             # max chars in displayed args

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

Conversations are persisted as JSON files in `~/.shannon/sessions/`.

- Each session is a `<id>.json` file containing messages, metadata, and remote task IDs
- Saved after each agent turn and on exit
- Titles generated from the first user message (truncated to 50 chars)

```
/sessions                              # interactive picker
/session resume 1                      # by number
/session resume 2026-02-23-a1b2c3      # by full ID
/session new                           # start fresh
```

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
- **Windows/Linux**: Local tools (clipboard, notifications, AppleScript, screenshot, computer) are macOS-only.

## License

MIT
