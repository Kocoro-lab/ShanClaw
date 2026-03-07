# Shan CLI — Project Guide

## What This Is

Go CLI tool (`shan`) — the command-line interface to the Shannon AI platform. Interactive TUI, one-shot mode, daemon mode for channel messaging, and local scheduled tasks.

## Tech Stack

- **Go 1.25.7** — `go.mod` is source of truth
- **Cobra** — CLI framework (`cmd/root.go`, `cmd/daemon.go`, `cmd/schedule.go`)
- **Bubbletea v1.3.10 + Bubbles v1.0.0** — TUI (`internal/tui/app.go`)
- **gorilla/websocket** — daemon WebSocket client
- **adhocore/gronx** — cron expression validation
- **chromedp** — browser automation (isolated Chrome profile)
- **mcp-go** — MCP client/server

## Project Structure

```
cmd/
  root.go              # entry, --agent flag, one-shot, mcp serve
  daemon.go            # shan daemon start/stop/status
  schedule.go          # shan schedule create/list/update/remove/enable/disable/sync
  update.go            # /update command

internal/
  agent/
    loop.go            # AgentLoop.Run() — core agentic loop, SetAgentOverride()
    tools.go           # Tool interface, ToolRegistry, Schemas()
    loopdetect.go      # 7 stuck-loop detectors
    readtracker.go     # read-before-edit enforcement
    approval_cache.go  # per-turn approval caching
  agents/
    loader.go          # LoadAgent, ListAgents, ParseAgentMention, ValidateAgentName
  client/
    gateway.go         # GatewayClient: Complete, CompleteStream, ListTools
    sse.go             # SSE event parsing
  config/
    config.go          # Config struct, Load(), multi-level merge (global/project/local)
    settings.go        # UI settings
    setup.go           # --setup wizard
  daemon/
    client.go          # WebSocket client with reconnect, bounded concurrency
    router.go          # SessionKey, SessionCache
  schedule/
    schedule.go        # Schedule CRUD, atomic writes, file locking, validation
    launchd_darwin.go  # plist generation, launchctl (darwin only)
    launchd_stub.go    # no-op stub for non-darwin
  permissions/
    permissions.go     # 5-layer: hard-block > denied > shell AST > allowed > ask
  audit/
    audit.go           # JSON-lines logger, RedactSecrets
  hooks/
    hooks.go           # PreToolUse/PostToolUse/SessionStart/Stop
  instructions/
    loader.go          # LoadInstructions, LoadMemory, LoadCustomCommands
  prompt/
    builder.go         # BuildSystemPrompt — 6 layers, token-budgeted
  session/
    store.go           # Session JSON persistence
    manager.go         # NewSession, Resume, Save, List
  mcp/
    client.go          # MCP client manager (stdio + HTTP transports)
    server.go          # MCP server (JSON-RPC 2.0 over stdio)
  tools/
    register.go        # RegisterLocalTools, RegisterAll (local > MCP > gateway)
    # 18 tool files: file_read, file_write, file_edit, glob, grep, bash,
    # directory_list, think, http, system_info, clipboard, notify, process,
    # applescript, accessibility, browser, screenshot, computer
    schedule.go        # schedule_create/list/update/remove tools
    mcp_tool.go        # MCPTool adapter
    server.go          # ServerTool adapter (gateway remote tools)
  tui/
    app.go             # Bubbletea Model — Init/Update/View, slash commands
  update/
    selfupdate.go      # GitHub release auto-update
```

## Key Conventions

### Agent Names
Must match `^[a-z0-9][a-z0-9_-]{0,63}$`. Validated before any path concatenation to prevent traversal.

### Tool Priority
Local tools > MCP tools > Gateway tools. Deduplication by name in registry.

### Permission Model
```
hard-block constants → denied_commands → shell AST parsing → allowed_commands → RequiresApproval + SafeChecker
```
Unknown tools → denied by default (fail-safe).

### Config Merge Order
1. `~/.shannon/config.yaml` (global)
2. `.shannon/config.yaml` (project)
3. `.shannon/config.local.yaml` (local, gitignored)

Scalars override, lists merge+dedup, structs field-level merge. MCP server env var casing preserved via direct YAML re-read.

### File Paths
- Agent definitions: `~/.shannon/agents/<name>/AGENT.md` + `MEMORY.md`
- Sessions: `~/.shannon/sessions/` (default) or `~/.shannon/agents/<name>/sessions/` (per-agent)
- Schedule index: `~/.shannon/schedules.json`
- Schedule plists: `~/Library/LaunchAgents/com.shannon.schedule.<id>.plist`
- Audit log: `~/.shannon/logs/audit.log`
- Schedule logs: `~/.shannon/logs/schedule-<id>.log`

### Atomic Writes
`schedules.json` uses write-to-temp + `os.Rename` + `syscall.Flock` on a persistent `.lock` file. Never delete the lock file (causes flock race on different inodes).

### Build Tags
`internal/schedule/launchd_darwin.go` uses `//go:build darwin`. `launchd_stub.go` provides no-op stubs for non-darwin. Tests that touch launchctl go in `_darwin_test.go`.

### Anti-Hallucination
XML `<tool_exec>` delimiters in conversation context with random hex call_id. Preamble text suppressed when response has tool calls. Fabricated tool calls detected and stripped.

## Testing

```bash
go test ./...                              # all tests
go test ./internal/agents/ -v              # agent loader tests
go test ./internal/schedule/ -v            # schedule CRUD + plist tests
go test ./internal/daemon/ -v              # WS client + router tests
go build ./...                             # build check
```

Schedule tests use `t.TempDir()` as `plistDir` — they never write to real `~/Library/LaunchAgents/`.

## Building & Releasing

- GoReleaser: `.goreleaser.yaml`
- Tap: `Kocoro-lab/homebrew-tap` → `brew install kocoro-lab/tap/shan`
- **Versioning: PATCH only (0.0.x)** — do NOT bump minor/major unless explicitly asked
- Release: `git tag -a vX.Y.Z` → `git push origin vX.Y.Z` → CI builds + publishes
- `docs/plans/` is gitignored — never commit plan files

## 22 Local Tools

**File ops:** file_read, file_write, file_edit, glob, grep, directory_list
**Shell/system:** bash, system_info, process, http, think
**macOS GUI:** accessibility (primary), applescript, screenshot, computer, clipboard, notify, browser
**Schedule:** schedule_create, schedule_list, schedule_update, schedule_remove
