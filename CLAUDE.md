# Kocoro — Project Guide

## What This Is

Go CLI tool (`shan`) — the runtime for Shannon AI agents. Production stack is **daemon + Kocoro Desktop + Shannon Cloud**: the daemon connects to Cloud via WebSocket, receives channel messages (Slack, LINE, Feishu, Telegram, webhook), runs the agent loop locally with full tool access, and streams results back. Also supports interactive TUI, one-shot CLI, MCP server, and local scheduled tasks.

## Tech Stack

- **Go 1.25.7** — `go.mod` is source of truth
- **Cobra** — CLI (`cmd/`)
- **gorilla/websocket** — daemon WS client (primary production path)
- **Bubbletea + Bubbles** — TUI (`internal/tui/`)
- **adhocore/gronx** — cron validation
- **modernc.org/sqlite** — pure-Go SQLite, FTS5 session index
- **chromedp** — browser automation (isolated profile)
- **mcp-go** — MCP client/server
- **adrg/frontmatter** — YAML frontmatter for SKILL.md

## Project Structure

Use Glob/Grep — the layout is `cmd/` (Cobra entry points) plus `internal/<pkg>/`.
A file-by-file tree used to live here; it was only a projection of the code and
is not maintained.

Primary production path is `internal/daemon/` (HTTP API, WS client, routing,
approvals/questions, scheduler) driving `internal/agent/` (the loop, tool
batching, compaction, deferred loading). Cross-package invariants that reading
one file will NOT tell you are written down below under Key Conventions —
notably Voice Front-Brain Authority (`internal/koe`), Provider Architecture
(`internal/executionprofile`), and Cross-Platform Support (`internal/keychain`,
`internal/fslock`).

## Key Conventions

### Doc Co-Maintenance

Feature changes update README.md (user-facing), CLAUDE.md (this file, developer-facing), and AGENTS.md (external-agent-facing: the rules and the greppable symbols, not a second copy of this file). AGENTS.md is capped at 24576 bytes (CI asserts — harnesses truncate it from the tail silently), and within 1 KiB of that ceiling any addition must cut at least as many bytes from AGENTS.md in the same PR (also CI-asserted).

**Kocoro skill is the AI's source of truth for the daemon HTTP API** — `references/*.md` are injected into the **kocoro agent's** context, so the rule covers only endpoints the agent calls or must understand: every such `mux.HandleFunc(...)` in `internal/daemon/server.go` needs a matching `references/*.md` entry in the same PR. Maps:
- agents/skills/schedules/config endpoints → `references/{agents,skills,schedules,config}.md`
- MCP / permissions / project-init / instructions / recipes / session-sync / memory → matching `references/*.md`
- `/local/auth/*` endpoints → `references/auth.md`
- `calendar_*` tools (8) + protocol → `internal/skills/bundled/skills/kocoro/references/calendar.md` + `.../desktop-rpc.md` (these skill refs are the public protocol reference); the full design doc `docs/desktop-calendar-rpc.md` is local-only / untracked (rationale + closed-app internals, not shipped)
- Protected config fields, tool filter → `SKILL.md` security section
- Desktop-only transport endpoints the agent never calls → NOT in references; their Desktop↔daemon wire contract lives in `docs/desktop-wire-fixtures/`

### Hardcoded Limit Policy

When introducing `const max[A-Z]\w+ = <small_int>` (count caps, retention, retries, concurrency), the comment MUST name (1) the user workload that justifies the value, (2) the symptom when it binds, (3) the override path. Prefer `viper.SetDefault(...)` over `const` for caps a power user might need to lift. Re-check small-int caps whenever the model family upgrades (200K-era defaults often bind silently on 1M-context families).

### Auto-installed Builtin Skills

Skills listed in `builtinSkills` (`internal/skills/api.go`) are sha256-walk synced from `embed.FS` to `~/.shannon/skills/<name>/` on every startup. User edits to a builtin SKILL.md are wiped on next startup — fork under a different name. Current: `kocoro` (daemon API assistant), `kocoro-generative-ui` (`hidden: true`, html-artifact visualizations).

### Agent Names

Must match `^[a-z0-9][a-z0-9_-]{0,63}$`. Validated before any path concatenation to prevent traversal.

### Voice Front-Brain Authority

`internal/koe` exposes 7 OpenAI-Realtime tools (`tools.go`). Three of them are
SEPARATE authorities and must not be collapsed into each other:

- `stop_speaking` — silences only the CURRENT output; the call stays active.
- `cancel` — targets delegated work by `task_id`, or `all_running=true` for one
  atomic stop-everything. Per-task partial failures stay explicit and a failed
  task stays running.
- `end_call` — the terminal whole-conversation dismiss/hang-up.

Both `stop_speaking` and `end_call` say nothing and send no
`function_call_output` on the regular response path.

- `do_task` defaults to exactly ONE call per response. Multiple calls in one
  response require an explicit user request for parallel execution, and each
  call must carry one disjoint work scope.
- Results from `do_task` calls created by the same Realtime response form one
  delivery group. Do not make a partial group speakable: wait until every
  registered call is terminal, submit all provider acknowledgements together,
  and request exactly one spoken continuation. OpenAI seals on `response.done`;
  Qwen also seals after a provider-specific tool-call quiet window because it
  can withhold `response.done` until function outputs arrive.
- `do_task` does not expose execution routing to Realtime — see Provider
  Architecture for the daemon-side Fast/Full decision.
- `MapDoTaskOutcome` maps a partial run (soft idle/deadline timeout,
  `iteration_limit`, force-stop — but NOT `user_cancelled`, which stays silent)
  to a canned per-language `incomplete` line and seeds no digest, so a cut run's
  progress tail is NEVER voiced as the result.
- ASR transcripts never admit barge-in or ordinary turns. When native floor
  control is unavailable, a terminal-only fixed vocabulary (exit/goodbye,
  never stop-speaking phrases) provides the default lifecycle backstop. Set
  `KOE_ASR_DISMISS_BACKSTOP=0` to disable it. The backstop converges on
  `realtime.go`'s handler-local terminal rather than firing teardown
  independently; model-judged `end_call` remains authoritative elsewhere.
- An accepted interruption truncates the paused assistant item server-side
  (`conversation.item.truncate` to the audio actually heard) so the model never
  treats unspoken text as said.

**One brain, two hosts.** `internal/koe` is the ONLY voice brain. macOS drives it
in-process from `cmd/koe.go`; iOS drives the same code through `koe.Session` and
the `mobile/koebind` gomobile bridge. Nothing in this list may be reimplemented
host-side — a Swift copy of the turn/tool/delivery rules is how the two platforms
start disagreeing about what a call even is. The host supplies only what it
physically owns, through four seams: `Backend` (delegation; `*DaemonClient` on
macOS, `ExternalTaskBackend` relayed to Cloud's remote-run channel on iOS),
`ExternalAudio`/`AudioController` (capture + playout gating), `Send` (one client
event onto the transport), and `ControlApp`. Outcome parsing is deliberately
shared (`parseMessageResponse`, `normalizeCancelReason`) so delegation semantics
cannot drift between platforms.

- **`mobile/` is a SEPARATE Go module** (its own `go.mod`), so the root module's
  vet/build/test/tidy steps do not see it — CI repeats them under
  `working-directory: mobile`. The bound surface obeys gobind's rules: no maps,
  no struct slices, no `context.Context`, strings and JSON strings in and out.
  `gobind` SILENTLY SKIPS an unbindable exported func and exits 0 (measured,
  x/mobile v0.0.0-20260813), so the CI step is a codegen smoke check, not proof
  the whole surface bound. Reviewing it against `bind.go`'s header stays human work.
- **`Session.HandleEvent` / `Bridge.HandleEvent` must not be called
  concurrently.** The event path writes plain non-atomic handler state and is
  safe only because transports deliver serially (pion's `OnMessage`; one serial
  queue on iOS). A concurrent dispatch queue corrupts it with no error and no crash.
- **`do_task` runs on `context.WithoutCancel`, on BOTH platforms**, and the
  macOS HTTP client is `Timeout: 0`. That is the contract, not an oversight: a
  hang-up must not kill the back-brain run, whose report persists in the session.
  It is bounded by the daemon's idle watchdog. Do not "fix" it with a timeout;
  hosts release only their own observation chain (iOS: `VoiceTaskBackendAdapter.close()`).
- **Release ownership: two epochs, and a path that supersedes another must retire
  it.** `speakingEpoch` gates the speaking release; `playbackDrainEpoch` gates
  ONLY the missing-stop-event watchdog (`releaseSpeakingAfterOutputBufferWait`).
  `output_audio_buffer.stopped` arrives after `response.done` by protocol, so the
  watchdog is armed on every spoken turn and the stopped path must bump BOTH —
  bumping one left two pollers live and released each turn twice (2026-08-28).
  Drain tuning: `KOE_STOPPED_DRAIN_HOLD_MS` (500) / `KOE_STOPPED_DRAIN_CAP_MS`
  (8000) for the stopped path, `KOE_PLAYBACK_IDLE_HOLD_MS` (1500) for the watchdog.
- **The spoken persona lives in `internal/koe/persona.go`**, not in any host.
  `SpokenPersona(lang, host)` + `AppendTaskLedgerPersona` compose it; `Host`
  selects the host-specific slots. Golden tests pin the text, so intentional
  wording changes must re-dump the goldens.
- **`call_state` carries an additive `reason`.** The state stays `ended` —
  Desktop's decoder DROPS a `call_state` whose state it does not know, so a new
  state value would leave an older Desktop's call UI open forever. `EmitCallFailed`
  therefore takes only the reason. The `CallFailure*` set is closed and Desktop
  localizes it; adding a code is a coordinated release.
- **Idle pre-warm retry is one lane.** `WarmRetryPolicy` decides the delay and
  whether to retry at all; `WarmRetryLane` guarantees exactly one live timer,
  its generation counter guarded by the caller's `sessMu` so "am I current" and
  the caller's session-state check are one decision. Auto's provider choice
  consults BOTH negative caches (`OpenAICircuit` and `FallbackHealth`) —
  the circuit alone would pin a cooldown onto a fallback already known unusable.

### Provider Architecture

`provider` config key selects the LLM backend: default → `GatewayClient` (Cloud); `ollama` → `OllamaClient` (OpenAI-compatible). Both implement `Complete` / `CompleteStream`.

**`agent.service_tier`** (`""`/`default`/`fast`, validated in `validateConfig` + `PATCH /config`) is a PROCESS-GLOBAL OpenAI processing lane sent on ordinary requests. It never rides an isolated route: `applyAgentModelOverlayToLoop` clears it whenever a named agent selects either its own model tier or an exact model, and `requestServiceTier` returns "" whenever a sealed execution profile (kfp1 Koe fast / ep1 computer) owns the request. Capability token `agent_service_tier_v1`.

**Koe Fast/Full execution** (`internal/executionprofile` + daemon `resolveKoeExecutionRun` / `authorizeKoeExecutionLineage`): Realtime's `do_task` schema contains no routing fields; each new `source=koe` request asks for Fast. The daemon remains authoritative: `koe.fast_effort=false`, a failed/missing Cloud profile, or a validated inherited Full lineage keeps the normal global/per-agent configuration byte-for-byte. Fast resolves the opaque Luna kfp1 profile from Cloud (`ResolveKoeExecutionProfile`, 5s bound). The validated `Run` (lineage ids + profile + digest-only evidence) is persisted in `Session.ExecutionRuns` and returned on `done` as `execution_run` for Koe's call ledger; interrupted recovery restores the pinned profile from the checkpoint and validates it against the ledger, abandoning on `ErrInvalidPersistedRun` (legacy zero-run checkpoints instead mint a fresh Full run under the route lock). The wire still carries admitted mode fields for compatibility and recovery. Capability token `koe_fast_profile_v1`; wire fixtures `message_koe_execution_{fast,full}_request.json` + `sse_event.done.with_execution_run.json`.

**Koe Realtime provider routing** (`internal/koe/provider.go`, `webrtc.go`, `cmd/koe.go`): every provider uses WebRTC. `auto` tries the pinned OpenAI route and changes to Qwen only for a classified pre-media bootstrap failure (network error/timeout, 5xx bootstrap response, ICE/DataChannel/session-ready timeout). Cloud's mint endpoint folds every upstream OpenAI failure — including upstream credential and configuration rejections — into 502, so those Cloud-side failures ARE fallback-eligible by design (a broken Cloud-held OpenAI credential degrades voice to Qwen instead of killing it). What stays terminal: gateway-authored 4xx (client auth, quota denial), locally validated invalid model/voice, and rejected `session.update`. The OpenAI negative-health cache only affects later calls during its cooldown; a ready call never switches provider. Forced OpenAI/Qwen never fall back. Qwen SDP goes through `DaemonClient.ExchangeSDPViaDaemon`; media remains direct and no provider credential enters Koe. Remote front brains set `KOE_DAEMON_TOKEN` for the robot facade/reverse proxy bearer; plaintext HTTP is trusted-private-LAN only, with TLS required outside that boundary. Qwen lacks `conversation.item.truncate`, so `eventHandler.nativeFloorEnabled` and `truncateHeldSpeech` disable that capability for Qwen. Qwen's server-VAD dialect also emits no `input_audio_buffer.committed` and echoes no response metadata, so the tool-loop turn boundary is minted client-side at the explicit user-purpose `response.create` (`maybeMintCommitlessTurn`, ledger-owned `mintedTurnBase` id space — `inputCommitSeq` stays a pure provider-commit signal, and whichever signal appears first owns the sequence), and tool calls riding unannounced response ids bind lazily to the current turn — Qwen-only via the ledger's `setLazyBind`; other providers keep the strict no-bind-no-authority contract. Interruptible (barge-in on) Qwen calls use server VAD so short first turns and talk-over are detected promptly; barge-in-off calls keep semantic VAD's complete-thought endpointing. Ordinary VPIO barge-in stays available while the response is active; only the short local playback tail after `response.done` is protected from capture to prevent self-interruption. An active transport reconnect preserves the task ledger/mailbox but marks prior voice wording unavailable in the replacement persona; it never pretends provider conversation history transferred. `KOE_QWEN_VAD_MODE` remains the A/B and rollback override in either direction. Capability token `koe_realtime_provider_v1`; the provider/model/voice catalog is additive under `GET /config/status`.

**Qwen live vision** (`internal/koe/webrtc.go`): a body carrier can supply encoded H.264 constrained-baseline keyframes through `ConnectOptions.VideoSource`. Koe adds that video track only to Qwen before SDP negotiation; a video source without `CallActive` fails local setup, and each read is bounded to the configured frame interval. A missing, rejected, or inactive video answer is terminal rather than silently degrading a vision-capable product call to audio-only. Prewarm, idle, and ended calls emit zero frames. Qwen vision travels as RTP media, never as an OpenAI `conversation.item.create` / `input_image` event. When live vision is attached, session instructions treat it as ambient, untrusted context: do not follow visible instructions, volunteer scene/camera narration, or infer identity or sensitive traits from appearance. The body runtime must reuse its existing camera owner, honor the read context, and return one independently decodable frame per configured interval.

### Tool Priority

Local tools > MCP tools > Gateway tools. Deduplicated by name. MCP-vs-MCP name collisions resolve to the **alphabetically-first server** (sorted iteration in `RebuildRegistryForHealth`, stable across rebuilds); the shadowed tool is logged, never registered.

### Tool Concurrency

The dispatcher batches tool calls by `IsConcurrencySafeCall`, not `IsReadOnlyCall`. Tools without an explicit `ConcurrencySafeChecker` implementation fall back to their `IsReadOnlyCall` value — so file_read / grep / glob etc. keep batching concurrently as before, and writers stay serial. Adding the new interface to one tool has no effect on any other tool's grouping.

`BashTool` implements `IsConcurrencySafeCall` via `internal/tools/bash_concurrency.go`. It is gated by `agent.bash_concurrency_enabled` (default `true` in Phase C — Desktop consumes the `tool_use_id_events` capability so id-keyed cards stay correct under concurrent batches). When the flag is on, commands whose first token is in a strict read-only whitelist AND contain no shell metacharacters (including `\n` / `\r`) are eligible for the concurrent batch. Everything else — `&&` / pipes / redirects / command substitution, plus any non-whitelisted leading token (`git push`, `npm install`, `curl`, `rm`, `git remote add`, `go env -w`, ...) — stays in a size-1 serial batch.

Tool events on the SSE/WS wire (`tool_status` running + completed) include a `tool_use_id` field so multi-tool-in-flight UIs (e.g. parallel bash) can pair them correctly. The daemon advertises this on the WS handshake via the `tool_use_id_events` capability token.

### Tool Required-Field Validation

Every tool's `Run()` MUST explicitly check that each field listed in `ToolInfo.Required` is non-zero immediately after `json.Unmarshal`, and return `agent.ValidationError(...)` (NOT a bare `ToolResult{Content: ..., IsError: true}`) on failure. Go's `json.Unmarshal` cannot distinguish "field missing" from "field present with zero value" on a strongly-typed struct, so a missing string `required` field arrives as `""` — which `os.WriteFile`, `exec.Command`, etc. happily accept. The 2026-05-13 production stuck loop was a `file_write` call with no `content` field that wrote 0 bytes, returned `IsError=false`, truncated the user's existing file, and trapped the model into a 16-call retry spin.

The `[validation error]` prefix that `ValidationError` injects is load-bearing: `LoopDetector.isValidationErrorSig` short-circuits a same-tool + same-args + 3-consecutive `[validation error]` run to `LoopForceStop`, well below the all-errors 2x ConsecutiveDup budget at call #7. Returning a hand-rolled `IsError=true` result without the prefix loses this early-stop and falls back to the slower flaky-retry path. Examples: `internal/tools/file_write.go` (content), `internal/tools/file_edit.go` (old_string), `internal/tools/archive.go` (path/dest), `internal/tools/cloud_delegate.go` (task), `internal/tools/edit_image.go` (prompt).

### Skill Discovery

Three layers triggering `use_skill`:
1. **Listing** — full descriptions (4000-char budget) in scaffolded user message on first turn. Not in system prompt (cache stability).
2. **Semantic** — opt-in small-tier prefetch on iter 0; the main path waits up to 2s for a hint while the helper has a 5s timeout. Disabled by default and gated by `agent.skill_discovery`.
3. **Catalog fallback** — `use_skill` description includes all skill names.

**Per-request channel suppression** (`internal/daemon/skill_filter.go`): `desktopOnlySkills` is filtered out of `loadedSkills` (use_skill registry + listing + semantic discovery) when `isCloudSource(req.Source)`. A single producer-side filter in `runner.go` keeps all three exposure layers consistent. Only entry today: `kocoro-generative-ui` (its `html-artifact` fences render only in Desktop's WKWebView; on cloud channels they'd surface as a code block). Drift test (`skill_filter_test.go`) walks `desktopOnlySkills × cloudSourceSet`. The divergence lives in the scaffolded user message, not the cached system prompt — revisit this filter if listing ever moves into the system prompt.

**Allowed-tools enforcement** is execution-time denial (`loop.go`), NOT schema filtering — tools array stays full for cache stability. Blocked tools return `[skill restriction]` error.

**Skill exemption** (`agent.SkillExempt` interface): `think`, `tool_search`, `use_skill` always run. Reserved for pure-infrastructure zero-I/O tools. **Do NOT add to tools with side effects** — those must stay skill-restrictable.

### Permission Model

```
hard-block → denied_commands → split compounds (incl. & and (...)) → always-ask (prefix + git-push dangerous-flag scan) → allowed (literal/glob + token-prefix family) → default safe → RequiresApproval + SafeChecker
```

Unknown tools → denied (fail-safe). Always-ask gate runs BEFORE the allowlist, so adding a high-risk command to `allowed_commands` is a no-op; "Always Allow" on always-ask commands is honored once, never persisted (enforced at write-time in `cmd/daemon.go` + `server.go` AND at runtime in `loop.go checkPermissionAndApproval`). Token-prefix family matching (depth 2 for known CLIs, 3 for unknowns) cannot widen scope past the always-ask gate.

### Daemon Architecture

One `####` per subsystem. Each names its code home first, then the invariant.

#### Desktop RPC channel

`internal/daemon/desktop_rpc/` + `cmd/daemon.go`. Unix domain socket reverse-RPC to Kocoro Desktop's EventKit, spawned by Desktop's `DaemonManager` with `--rpc-socket` + `--rpc-pidfile` (the daemon NEVER derives one path from the other). Length-prefixed JSON framing, 4-byte BE uint32, body <= 4 MiB. `DesktopRPCBroker` mirrors `ApprovalBroker` race-safety; single-instance accept. Sock disconnect -> `CancelAll` (`desktop_disconnected`); sock listen failure is fatal. ProtocolVersion `1.0.0`; both responder sides MUST return `ProtocolMethods` byte-identically. Lifecycle is semi-bound: the daemon outlives a Desktop UI quit, so IM channels keep working. `launchd_darwin.go` is the npm-CLI standalone path, NOT used by Desktop-bundled deployments.

#### Desktop skill recommendations

`internal/daemon/skill_recommendation.go` + `internal/skills/catalog.go`. Signed-in Desktop requests declaring `skill_install_recommendation_v1` get Direct tools `discover_installable_skills` + `offer_skill_installation`.

- Task-time discovery is binary-pinned and OFFLINE: production uses only the embedded eligible catalog (`official_catalog.json`), never the GitHub/static/ClawHub registries.
- Only after explicit acceptance do the document entries fetch their immutable SHA-256-verified GitHub archive — their upstream license prohibits bundling.
#### Desktop skill recommendations: principal and sink

- The verified principal (`AuthManager.VerifiedPrincipal`) is read ONCE at the `/message` admission boundary and carried on the request; the runner MUST NOT re-read the AuthManager.
- Ordering invariant: the bus subscription is established BEFORE the initial `/events` sink bind. Bind-first leaves a window only the keepalive ticker recovers.
- Delivery resolves the live sink at emit time but is pinned to the admitting epoch. Any principal transition (sign-out, account switch, same-account re-login) fails delivery closed — an offer expires and never resurrects.

#### Conditional Desktop skill tools

`daemon/runner.go` + `daemon/skill_recommendation.go`. The two tools above are Direct only for capable signed-in Desktop/Kocoro requests with the feature enabled; absent from TUI, one-shot, MCP, schedule, IM, heartbeat, and watcher runs. A disconnected card-event sink does NOT change schemas — `offer_skill_installation` fails closed when invoked.

#### Conversation context actions (Branch / Side Chat / reply annotations)

`internal/daemon/conversation_context.go` + `conversation_reply.go`, capability `conversation_context_actions_v1`, Desktop-only transport (the agent never calls these → NOT in kocoro skill references; wire contract in `docs/desktop-wire-fixtures/` + `docs/cloud-contract-surface.md`). `POST /sessions/{id}/fork` copies history through a complete assistant turn into a normal persisted session (optional `target_agent`, validated to exist); `POST /sessions/{id}/side-chat` runs an ephemeral turn over that bounded history. `message_index` on both is a RAW-archive boundary (system-injected entries counted) — the same index space Desktop's `rawIndex` uses. Both honor a covering `CompactionCheckpoint` (fork deep-copies it; side-chat feeds checkpoint+tail via `Session.HistoryThrough`). Side chats run the NORMAL tool registry + permission engine with SSE approvals, but stay ephemeral: no session persisted, no bus events, and no question asker (`shouldInjectQuestionAsker` — the panel has no question UI, so an asker would block then report a phantom decline). Desktop text replies ride a transient head-only `<kocoro_replies>` envelope: stripped from the archive ONLY when it parses (malformed stays verbatim — lossless), decoded into `message_meta[].conversation_annotations`, limits (100 replies / 8K quote / 2K comment runes) enforced with stable 400 codes at `/message` + `/queue`.

#### WS handshake

`client.go`. Sends `User-Agent: kocoro/<ver>` + `X-Kocoro-Daemon-Version` + `X-Kocoro-Capabilities`.

#### `delivery_ack` capability

`client.go:SendDeliveryAck`. After `SendReply` succeeds for `MsgTypeMessage`, emit the ack so Cloud drops the replay-buffer entry. Reply-failure paths MUST skip the ack so replay stays correct.

#### Per-inbound reply addressing

A run that absorbed mid-run injected follow-ups completes + acks EACH inbound message under its OWN cloud id — superseded turns via `OnIntermediateAnswer(text, cloudMessageID)` (a real `SendReply`+ack, not a timeline segment); the final answer + co-acks via `RunAgentResult.{ReplyToMessageID,PendingAckMessageIDs}` -> `Client.SetReplyPlan`. Ack-after-delivery: `handleMessage` acks every absorbed id ONLY after the final reply lands. The injected follow-up's own handler suppresses BOTH its reply and ack via `Client.SuppressReply`; the owning run is solely responsible, so a crash replays it instead of losing the answer. Without this, Cloud collapses two logically-distinct replies (group-chat messages from different senders) into one channel message.

#### Agent sync ownership

`internal/agents/owner.go` + `internal/daemon/agentsync.go`. Agents carry a
device-local `_owner` sidecar (verified Cloud user id; never synced, excluded
from the LWW definition-file set). Push (`buildSyncItems`) excludes
foreign-owned agents and grandfathers unstamped ones to the current verified
principal on first push contact; the principal resync stamps everything it
materializes/overwrites, and a pulled tombstone deletes only own/unstamped
agents (account B's cloud delete cannot remove account A's same-key local
agent). Create stamps the creator; definition edits (PUT agent / PUT+DELETE
config) re-stamp to the editing principal (ownership follows content, same as
the pull's LWW overwrite); delete removes the sidecar. The RUNTIME
(listing/routing/execution) deliberately stays cross-account shared — only the
sync boundary is principal-scoped, closing the cross-account upload of the
previous account's local agents after a switch. No verified principal (legacy
yaml-key platforms) = unstamped, unfiltered, historical device-shared behavior;
an AuthManager with NO verified principal (optimistic sign-in) skips the push
entirely instead — the gateway key is live there and an unfiltered push would
leak. A push whose excluded foreign key COLLIDES with the account's cloud
mirror (live-key snapshot taken by the resync pull) degrades to upsert-only —
Cloud's full_sync SoftDeleteMissing would tombstone the account's own same-key
row; disjoint foreign keys keep full_sync, since a blanket degrade would stop
local deletes from ever reaching Cloud (deleted agents would resurrect on
every multi-account device). Owner writes are atomic (temp+rename) so
lock-free reads never see a torn value. Accepted residuals: an agent edited
under A whose stamp never landed (push failed / daemon killed inside the
debounce / pre-upgrade population) is grandfathered to whoever is signed in at
first push contact; per-agent always-allow add/remove deliberately does NOT
re-stamp yet still bumps the LWW clock (config.yaml is a definition file), so
B's approval click on A's agent rides A's next push — small, known.

#### Config revision state

`internal/config/revision.go` + `internal/daemon/server.go`. The daemon records the exact global `~/.shannon/config.yaml` revision reflected in memory. GET `/config` and `/config/status` report a newer external revision as `reload_required`. Internal read-modify-write mutations preserve unknown external edits and MUST NOT mark bytes they did not load as applied. Project/local overlays are NOT watched by this signal. Capability `config_reload_state_v1`.

#### Built-in MCP catalog

`internal/mcp/builtins.go` + `config.go mergeBuiltinMCPServers`. `BuiltinMCPServers` ships pre-bundled servers (e.g. `intercom`) disabled by default. `config.Load` field-merges the Go catalog onto user yaml: command/args/url come from the binary (auto-upgrade); disabled/env/keep_alive plus `connect_timeout_secs`/`tool_timeout_secs`/`workspace_base` persist from yaml. `PATCH /config` refuses daemon-owned fields on a builtin (`builtin_mcp_immutable`, 409). `GET /config/status` exposes `mcp_server_info` so Desktop renders a toggle + OAuth modal without hard-coding the catalog.

#### MCP async startup

`internal/mcp/client.go StartConnectAll` + `register.go RegisterAllWithBaselineAsync`. Startup and `/config/reload` do NOT block on MCP handshakes — the registry builds with local+gateway tools synchronously, then per-server connect goroutines fire. A per-server `inFlight` set dedups concurrent attempts. Timeout: `ConnectTimeoutSeconds` > `DefaultConnectTimeoutSecs` > 60s. Success -> `Supervisor.ProbeNow` -> registry rebuild.

#### MCP reconnect after a failed connect

`internal/mcp/reconnect.go` + `internal/daemon/mcp_reconnect.go`. A failed async connect is NOT terminal. `ReconnectScheduler` retries that one server with exponential backoff (5s -> 5min cap, `reconnectMaxAttempts` 6, ~5m15s total), one pending retry per server, streak reset on success. Both entry points — daemon startup (`cmd/daemon.go startDaemonMCPServices`) and the reload rebuild branch (`server.go handleConfigReload`) — MUST share the one `buildMCPConnectResult` callback so recovery behaves identically.

#### MCP reconnect: generation ownership

Retries are OWNED BY THE MANAGER GENERATION: `ServerDeps.SwapMCPReconnectScheduler` stops the superseded ladder when a reload rebuilds the manager, and `ShutdownCleanup` stops it before closing connections — a timer must never respawn a subprocess the cleanup is reaping. A retry re-reads `mgr.ConfigFor` and skips servers deleted or disabled while it was pending. Explicit user action re-arms an exhausted streak via `ForgetMCPReconnect`.

**Why this exists**: rotating an MCP credential rebuilds the manager and respawns the stdio child; when that single attempt lost the race with the SIGTERM'd process group, the server stayed enabled-but-dead until a daemon restart.

#### MCP subprocess reaping

`internal/mcp/processgroup_unix.go` + `client.go cancellers`. Stdio MCP subprocesses spawn in their own process group (Setpgid), killed via `-pgid` SIGTERM + 3s SIGKILL backstop. Needed because npx-bridged servers are a process chain (npx -> npm exec -> node): killing only the direct child orphans a grandchild holding the OAuth loopback port (EADDRINUSE on re-toggle). `Disconnect`/`Close`/`Reconnect` cancel the group before `c.Close()`.

#### Reload as explicit retry

`server.go retryDisconnectedEnabledMCPServers`. When `/config/reload` runs without an MCP config delta (`mcpChanged=false`), the reload tail fires a fresh `StartConnectAll` for every `disabled: false` server not in `mgr.ConnectedServers()` — otherwise the no-auto-reconnect policy leaves previously-failed servers stuck "enabled" forever. Desktop's "Retry" button maps here.

#### MCP call resilience

`tools/mcp_tool.go` + `mcp/client.go`. Pre-dispatch gate: a supervisor-known-disconnected server is ProbeNow'd BEFORE CallTool instead of discovering the corpse mid-call (2026-07-29: 6.5 min on a dead google-workspace pipe vs a 12s reconnect). Every tools/call attempt is bounded: per-server `tool_timeout_secs` > `mcp.tool_timeout_secs` (default 300s, `mcp.DefaultToolCallTimeout`); an earlier caller deadline still wins.

#### MCP call resilience: replay gating

On failure the tool probes FRESH health (never trusts the cached state at error time), but **post-dispatch re-dispatch is annotation-gated** (`mcp.ToolReplaySafe`): a transport error after dispatch does NOT prove the server never acted — a stdio server can execute its write and die before responding. Only `readOnlyHint`/`idempotentHint` tools are replayed; everything else returns `mcp.OutcomeUnknownError` steering the model to verify before retrying. Timeouts and protocol errors are never retried. Live repro pinned in `tools/mcp_tool_live_test.go`.

#### MCP artifact paths

`tools/mcp_result_paths.go` + `mcp_tool.go` + daemon `runner.go`. Two-sided fix for server-relative artifact paths (playwright renders links relative to the FIRST advertised root, not the session CWD, which once sent the model into a 242s `find /`).

- Result side: relative markdown links from servers with known path semantics (built-in table: playwright; user MCPs opt in via `workspace_base`) get a "Saved to: <abs>" annotation ONLY when the joined path exists under the base. Unknown servers stay opaque.
- Input side: `browser_take_screenshot` without a filename gets one injected into the per-session artifact scratch (`~/.shannon/tmp/sessions/<id>/`, `cwdctx.WithArtifactDir`, daemon-served runs only; swept at startup by `daemon.scratch_max_age_days`, default 14).
#### MCP artifact paths: filename rules

- `browser_snapshot` NEVER gets a default filename — an omitted filename means the INLINE accessibility snapshot, the model's primary page-reading channel.
- **Model-supplied absolute paths always win** — that is how the user addresses a deliverable. File-producing tool descriptions append `fileOutputArgHint` steering the model to bare relative filenames for its own intermediates.

#### Generic integrations broker

`integrations_handler.go` + `client/gateway.go`. `POST /integrations/{provider}/connect`, `GET /integrations`, `GET`/`DELETE /integrations/{id}` forward to Cloud `/api/v1/integrations/*` with the user's API key attached server-side. Cloud owns the per-provider OAuth exchange; the daemon has no public callback URL. Every provider authorizes in the browser: Cloud's Klavis -> Composio vendor migration (2026-08) retired token mode, so `connect` returns `{connection_id, oauth_url, status:"pending"}` and the renderer opens the URL. `connect` forwards the client's JSON body verbatim (64 KiB cap — Cloud decodes at 4 KiB; caller-supplied, **never log or persist it**), which is how a provider's declared connect params reach the flow: Shopify / Jira / Confluence / Salesforce require `{params:{subdomain}}`, everything else sends no body. Capability `integration_connect_body_v1` (an old daemon drops the body and Cloud rejects the missing param; the token name predates the migration, its wire meaning is unchanged). **The kocoro agent never calls these** (OAuth needs a browser), so they are NOT in the kocoro skill references.

#### Integration tools (local agent)

`tools/register.go RegisterIntegrationTools` + `tools/server.go NewIntegrationTool` + `client/gateway.go`. The local agent loop does NOT go through Cloud's orchestrator, so Cloud's request-time tool injection never reaches it — the daemon MUST register the tools itself. `RegisterIntegrationTools` fetches active integration tool schemas from Cloud `GET /api/v1/integrations/tools` (X-API-Key, **no local allowlist** — Cloud already filters; local tool names still win on collision) and registers each as a `ServerTool` variant (`SourceIntegration`; `RequiresApproval()` reads the schema's optional `requires_approval` flag — absent means false, Cloud's own access control only). Each schema and tool is bound to the exact credential and verified-principal generation that listed it. Dispatch atomically rechecks that generation while capturing the credential, so a tool retained by an old agent loop or clone fails known-no-effect before reaching Cloud after any key/account mutation. Execution proxies to `POST /api/v1/integrations/tools/{name}/execute`.

Cloud may add `material_side_effect` to a schema. Its presence is trusted
policy: `false` keeps observational tools such as X identity reads out of the
durable mutation journal and permits concurrent batching; absence stays
fail-closed for older Cloud versions. Cloud may also add `requires_approval`
(trusted policy too): `true` routes the tool through the normal local approval
flow — first-use approval card and `daemon.auto_approve` behave exactly as for
local approval-requiring tools — EXCEPT that "Always Allow" persistence is
refused at every layer: the approval card carries `always_allow_disabled`,
`HandleAlwaysAllowDecision` / `PersistAgentAlwaysAllow` reject the write, the
broker cache refuses the name, and the runtime gate in `loop.go` ignores an
existing `always_allow_tools` entry (hand-edited or pre-policy). Two self-heal
layers keep config from accumulating grants the runtime will never honor:
full-replace agent config writes (create / `PUT /agents/{name}` / `PUT
/agents/{name}/config` / the cloud agent-sync pull, `server.go
dropRegistryDeniedAlwaysAllow`) DROP registry-denied entries — drop, not
reject, for the same reason as the legacy GUI list (config writes are
full-replace; rejecting would brick agents carrying a stale entry) — and a
successful catalog rebuild prunes global + per-agent entries the rebuilt
catalog denies (`integrations_handler.go pruneDeniedAlwaysAllowGrants`, run by
both `RefreshIntegrationTools` and the verified-principal transition's
`resetIntegrationToolsForPrincipal`; sign-out only clears the catalog and
never prunes), covering grants persisted while the catalog was empty
(key-rotation window, where the registry miss judges false). Both self-heals
converge upstream: a pull-side sanitize drop (dynamic registry denial, or the
pre-write static `SanitizeAgentPermissionsConfig` pass in
`materializeAgentFromItem` — legacy GUI names AND per-agent `computer_use`,
which `WriteAgentConfig`'s validator would otherwise reject into a permanent
pull-retry loop) skips the agent-sync LWW mirror stamp (local clock stays
"now"), and the REFRESH-path prune fires
`triggerAgentSync` when a removal actually wrote bytes, so the sanitized
config passes Cloud's strict-newer upsert instead of leaving a stale row that
reseeds other devices. The principal-transition prune now also fires
`triggerAgentSync` on write, same as the REFRESH path — safe because every
verified-principal transition FIRST resets `agentPullClean` to false
(`beginAgentSyncPrincipalTransition` — the full-sync license was earned under
the previous account, so post-switch pushes degrade to upsert-only, never
destructive) and only the async pull-then-push resync for the new principal
(`resyncAgentsAfterPrincipalChange`, serialized + principal-epoch-guarded on
both sides of fetch AND apply, so a superseded transition's mirror never
touches disk) restores the flag after a clean pull; the startup pull's restore
is guarded the same way (`principalUnchangedGuard`).
A registry miss never drops or prunes —
fail-safe against mass deletion; the runtime gate backstops. The per-turn
`ApprovalCache` also refuses these tools — a byte-identical repeated tool_use
carries a fresh request id the idempotency journal cannot dedupe, so every
repeat re-prompts. **BREAKING**: users who previously clicked Always Allow on
such a tool are re-prompted on every call from now on; their persisted entry
is silently ignored. Deliberately retained carve-outs (the refusal covers the
persistence pathway, not every approval bypass): `daemon.auto_approve` and the
no-approval-UI auto-approve on non-interactive channels
(`IsNonInteractiveApprovalChannel`: WeChat/WeCom/Discord/Telegram/voice) still
approve these tools per call — a user's real-time request on those channels
must keep working, and Cloud retains its own access control. Cloud defines
the requires_approval set as material outbound writes (gmail_send_email, X
posts) needing per-call human approval, so one click must never become a
standing unattended grant. The dynamic denial is schema-derived
(`ServerTool.DisallowsAlwaysAllowPersistence` via
`agent.AlwaysAllowPersistenceDenier` + `ServerDeps.ToolDisallowsAlwaysAllowPersistence`)
— new provider write tools inherit it with no daemon change. The list
request advertises `integration_requires_approval` on `X-Kocoro-Capabilities`;
Cloud fails closed and withholds `requires_approval:true` schemas from daemons
without the token, because an older daemon would register them approval-free.
The token is also on the WS handshake; its string constant lives in
`internal/client` (`CapIntegrationRequiresApproval`), aliased into the daemon
`Capabilities` slice. Every execute body carries a stable
`request_id` when the agent dispatcher supplies a tool-use identity. Material
calls additionally send the durable journal's `Idempotency-Key`; read-only
calls never claim provider idempotency. Structured Cloud error codes preserve
known outcomes: reconnectable auth errors point to the provider's Settings
entry, and explicit pre-dispatch `provider_unavailable` is retryable with
known no effect. Billing/provider-error/unknown post-dispatch failures on
material tools remain `outcome_unknown`. Integration usage preserves provider, model,
unit type/count, and cost through the tool result and usage emitter.
`call_in_progress` is a separate known state: read-only calls may retry after
waiting. Material calls poll only with the same durable request identity; if
that bounded polling is exhausted, the result is journaled `outcome_unknown`
and never committed. An outcome-unknown material result returns to the model
as an ORDINARY narratable tool error (the run continues — no synthetic
terminal assistant message), while the same-turn retry latch
(`agent/unknown_outcome_gate.go`) locally rejects any byte-identical
tool+arguments repeat for the rest of the user turn — before the approval
flow, with zero network. The next user message (fresh run or committed
mid-run injected follow-up) clears the latch; different arguments pass
through. The journal-unavailable sibling (call definitively never executed)
also continues as an ordinary error.

#### Integration tools: refresh triggers

Registered on startup + `/config/reload`, refreshed on a verified-principal
epoch and — the immediate path — **`POST /integrations/refresh`**
(`Server.RefreshIntegrationTools`; lightweight, does NOT restart MCP). Before
the potentially blocking catalog clear, an API-key swap synchronously updates
the Gateway key and invalidates every captured generation, then releases the
dispatch writer. The daemon removes every `SourceIntegration` tool without that
writer held; the new verified principal repopulates the catalog, and a failed
list leaves it empty. All key and principal mutations are serialized. Auth,
integration, MCP-health, and
reload registry build-to-swap transactions share one lock, so a queued old
catalog cannot land after a credential or manager transition. Auth rebuilds
refresh both cached overlay layers: credential-capturing cloud/publish/image
tools are removed on key loss while calendar and other non-auth post overlays
survive. The concrete `cloud_delegate`, publish/list/retract, and generate/edit
tools each carry the verified credential/principal generation that built their
client. Their generation lease spans the complete `Run`, including client
retries, so pointers retained by an old runtime clone, deferred load, or
approval wait fail known-no-effect before network dispatch after any key or
principal epoch change. An
ordinary same-identity refresh failure retains the current catalog. First-time
activation is async (a connection goes active only after the browser OAuth
completes, out of band), so `connect`/`delete` fire a best-effort refresh but the
reliable trigger is Desktop calling `POST /integrations/refresh`. Capability
`integration_tools_v1`.

X automation guardrails (independent of the X integration tools — publishing
happens ONLY through Cloud's authorized X API tools, never through browser or
GUI automation): the local `browser` tool rejects direct
X composer navigation and publish-capable mutation on an observed/cached X
composer; URL-observation failure also rejects explicit composer controls. The
`computer_use` guard rejects composer controls and coordinate clicks whose exact
target is unknowable on an observed X page; the OpenAI native computer adapter
projects every action back through this guarded tool. The built-in Playwright
MCP adapter does not expose `browser_run_code` or `browser_evaluate`, because
either can navigate and publish in one opaque call. It rejects composer
navigation before dispatch and explicit composer controls in every transport.
In CDP mode, its target check and tools/call execute under the same per-server
lock; an empty HTTP target list falls back to the WebSocket target source, and
an ultimately empty/unavailable target set is reported as browser-not-ready
rather than evidence of an X composer. X home and timeline pages embed a full
composer, and CDP cannot reveal Playwright MCP's private current-page choice, so
any observed X target blocks publish-capable mutation without relying on element
labels. Non-CDP Playwright keeps ordinary mutation available but makes no
target-state X-protection claim. Observations, ordinary X links/read navigation,
and mutation when no X target is present remain available. These runtime guards do
not police arbitrary shell commands or custom MCP servers. Browser-side, only
the user's own click on X's Post button may publish. (The former
`x_prepare_post` Web Intent tool was removed — superseded by the Cloud
`x_create_post` integration tool.)

#### Attachments

`attachment.go`. Priority `document_b64` -> `extracted_text` -> URL download. Caps: 500 MB/file, 20/msg, inline doc <= 25 MB raw. Capabilities `inline_document_b64`/`inline_extracted_text` gate these fields. DOCX/XLSX/PPTX/CSV extraction is daemon-local (`doc_extract.go`); Cloud fills PDF `DocumentB64` + transcodes HEIC/AVIF.

#### Session routing

`router.go`. `ComputeRouteKey` precedence: `PinnedRouteKey` (sticky schedule) -> `session:<id>` -> thread -> sender -> plain `agent:<name>` (only when NOT `new_session`) -> channel. Web/webhook/cron bypass (always fresh).

#### Session routing: multi-session and schedules

**Named agents are multi-session** (they honor `session_id`/`new_session` like the default agent); the plain `agent:<name>` lane resolves to the latest `kind=interactive` session via `Manager.ResumeLatestMatching(isInteractiveSource)`, never a schedule/IM session. **Schedule `Stateful`** is the single remember-across-runs switch (`schedule.IsSticky`): `false`/legacy-nil -> fresh session each run; `true` -> dedicated accumulating session pinned via `PinnedRouteKey`. **Heartbeat** reads/appends the latest `kind=interactive` session; a mid-run session switch hits `ErrSessionChanged` and is dropped silently.

#### IM connection awareness

`connection_state_cache.go` + `message_origin.go`. Per-platform connection state from Cloud `channel_state_event`s, rendered as a `Connection:` Session-Facts line + new-session `Preamble()`. **Binding axis** (install/token revoked — actionable) is stored SEPARATELY from **transport axis** (transient disconnect) so a blip cannot mask a revocation; binding wins at render, and `Preamble()` is sorted for byte-stable prompts.

#### Work plans

`internal/daemon/work_plan.go` + `internal/session/work_plan.go`. Optional durable progress checklist for one daemon run: `set_work_plan` (Direct by local default, run-scoped, registered in `RunAgent` for every non-ephemeral run) submits a FULL 2–8-step snapshot; `runPlanController` owns the runtime fields (`plan_id`/`run_id`/`revision`/`lifecycle`/`close_reason` — never model arguments; ownership binds to RunID, not AttemptID). Invariants:

- **Persist before emit**: a changed snapshot sets `ToolResult.CheckpointNow`, the loop runs `checkpointNow` (debounce bypassed; save failure fails the run), the checkpoint's `StageForSave` copies the snapshot into `Session.WorkPlan`, and only after `sessMgr.Save` succeeds does `work_plan.updated` (dotted family, fixture-pinned, capability `work_plan_v1`) leave `TakePendingEvent`. SSE can never show a revision a crash could erase.
- **Closure is runtime evidence, never a model claim**: at run end `CloseForRun(LastRunStatus, runErr)` maps clean+all-complete → `completed/run_completed`, clean+pending → `stopped/run_completed_with_pending_steps` (outer result NOT downgraded), partial/cancel/hard-error → `stopped` with the stable reason — and **bumps the revision**, because consumers drop lower-or-equal revisions. A recovery-eligible hard error (session still `InProgress`) keeps the plan ACTIVE for the same RunID.
- Identical normalized steps (content+status; explanation excluded) are a no-op: no revision, no event, no forced save. `set_work_plan` is deliberately NOT dup-exempt in the loop detector, IS `SkillExempt` (pure zero-I/O harness metadata), and never enters the side-effect journal.
- Resume injects the active plan into VolatileContext after `cache_break` (`renderWorkPlanForPrompt`); prompt guidance is the byte-exact strip section `workPlanBulletSection` (same pattern as think/skills), so TUI/CLI/MCP prompts are unchanged. Koe `ToolDefs()` never sees it (tripwire test).
- Registration deliberately covers EVERY persistent daemon run (IM/schedule/heartbeat included), unlike `wantsPromptSuggestion`'s consumer allow-list: the plan's durable value (session record, recovery, later inspection) exists without a live watcher, and per-source gating would fork the provider-visible schema bytes. History mutations (`Manager.Reset`, `TruncateMessages`, `Session.TruncateAt`) DROP `WorkPlan` — the transcript that justified it is gone. Step caps: `agent.work_plan_max_steps` (default 8); content/explanation rune caps are wire-sanity consts in `work_plan.go`.

#### Smart session titles

`runner.go fireTitleAfterRun` + `internal/context/title_gen.go`. Async small-tier title upgrade at completed turns {1,3} on `TitleAuto` sessions. **Skipped for autonomous local sources** (watcher/heartbeat/mcp via `isAutonomousLocalSource`) so they never relabel the user's interactive session. `DecorateTitle`/`SourceLabel` prefix the brand (`Slack - ...`).

#### Session share uploads

`daemon/share_handler.go` + `share_async.go`. Render HTML -> `POST /api/v1/uploads` with `kind=session_share` (post-upload LIST filters by that kind so concurrent uploads cannot bump our row off page 1). publish_to_web uses `kind=other`. Tool runs are stripped from the page (prose + images only); `html-artifact` fences render in a sandboxed iframe (`internal/share/artifact.go`, **assistant-role messages only** — user/third-party text stays inert escaped markdown to avoid stored XSS).

#### Session share: artifact host mirror

**The artifact host CSS (`internal/share/templates/artifact_host.css`), the CSP, and the resize bridge are VERBATIM mirrors of `ARTIFACT_HOST_CSS` / `ARTIFACT_CSP` / `buildArtifactSrcdoc` in Kocoro Desktop's `message-list.js` — re-sync when Desktop's artifact design system changes. There is no cross-repo automated check.**

#### Output format

`runner.go outputFormatForSource`. `plain` for cloud-distributed channels; `markdown` default. **Feishu/Lark/Teams are cloud sources but use `markdown`** (`markdownCloudSources`) — their cards render standard markdown, and GFM re-enables Cloud's `[name](url)` -> file-attachment conversion. **WeChat (iLink) stays `plain`** — Cloud's iLink outbound extracts raw CDN URLs from plain text; do NOT move it into `markdownCloudSources`.

#### Tool result sizing

`spill.go` + `toolresult_budget.go` + `context_bloat.go`. Per-result spill at policy threshold (`DefaultMaxToolResultSizeChars` 50K, grep 20K) -> tmp file + 2K preview. `file_read` is `UnlimitedToolResultSizeChars` (no spill); it self-bounds via `fileReadHardCapRunes = 500_000` with a truncation marker. Per-turn 200K-rune aggregate cap (`aggregateCapThreshold`) skips Unlimited tools. `ToolResultReplacements` + `ToolResultSeen` persist across checkpoints AND terminal saves.

#### file_read dedup

`agent/readtracker.go` + `daemon/readtracker_cache.go`. Records `(path, offset, limit, mtime, size)`; re-reads return a stub. Per-session, released via `SessionManager.OnSessionClose`.

#### Image size guard

`imaging_compress.go` + `oversize_image.go`. Three layers: source-time compression (`EncodeImage` decode -> 2000x2000 -> JPEG ladder), wire-time sanitizer (`filterOversizeImages` in `messagesForLLM`), persist-time guard (`SanitizedRunMessages`). Any new image path MUST pass through all three before reaching the LLM or session JSON.

#### Skill secrets

`skills/secrets.go`. Keychain `com.shannon.skill.<name>` + a plaintext index of key NAMES only. Env-var-only injection, scoped to skills activated by `use_skill` in the current run.

#### Skill marketplace sources

`daemon/server.go` (`s.marketplace` / `s.clawhub`) + `config.MarketplaceConfig`. TWO independent API surfaces that MUST NEVER share a response shape:

- `/skills/marketplace/*` = static registry (`registry_url`), integer `page` pagination, `{total,page,size,skills}`. **This is the frozen macOS Desktop contract — do not add source-conditional branches here.**
- `/skills/clawhub/*` = ClawHub live catalog (`clawhub_url`, default `https://clawhub.ai`), opaque `cursor` pagination (`{skills,size,next_cursor}`), plus per-version `/files` + `/file` browsing and `/install/{slug}`.

Both back the same `MarketplaceClient` (mode set by constructor) and install to the same on-disk location.

#### Skill marketplace: retry policy

**Transient resilience**: every catalog GET and the zip install download go through `doGETWithRetry` (`marketplace_retry.go`) — retries 429/5xx + network errors with exponential backoff + jitter (honors a numeric `Retry-After`), tuned by `skills.marketplace.max_attempts` / `.retry_base_backoff_secs`. **4xx is never retried.** The helper returns the final response on exhaustion so each caller's `status %d` error is preserved. The install zip download is single-attempt — retrying would multiply a hang.

#### Skill marketplace: ClawHub caching

**ClawHub caching**: short-TTL per-URL response cache (`marketplace_cache.go`, `skills.marketplace.clawhub_cache_ttl_secs` default 60), serve-stale-on-error. `warmClawHubOnce` warms the canonical default browse page ONCE at startup (`clawhub_warm_on_startup`, default true) — deliberately one-shot, so an air-gapped daemon makes at most one clawhub.ai request. A view-agnostic last-good first page (`clawhubFirstPageMaxAge` 30min) is served when a fresh fetch fails **for a transient reason only**: `isTransientListErr` mirrors `isRetryableStatus`, so 429/5xx + network/parse degrade to a stale page while 400/401/403/404/409/410/422 surface immediately. Deep pages and searches have no fallback.

#### Skill marketplace: exclude-installed

**Exclude-installed** (`GET /skills/clawhub?exclude_installed=true`, capability `clawhub_exclude_installed`): opt-in, ClawHub-only, NEVER the frozen `/skills/marketplace/*`. "Installed" is local-only, so `FetchClawHubPageExcludingInstalled` fetches normally then drops installed slugs, refilling from later pages bounded by `skills.marketplace.clawhub_exclude_fill_max_pages` (default 5). Cursor is page-granular, so a returned page may exceed `size` and `next_cursor` stays page-aligned; **if the fill cap binds the page is short or empty with a non-empty cursor and the client MUST keep paging.**

#### Turn phase tracker

`agent/phase.go`. Only `PhaseAwaitingLLM` and `PhaseForceStop` are idle-counted. Fail-closed: panics under `testing.Testing()` or `SHANNON_PHASE_STRICT=1`.

#### Idle watchdog

`agent/watchdog.go` + `client/gateway.go`. Two layers. Turn-elapsed: `OnRunStatus("idle_soft")` at `agent.idle_soft_timeout_secs` (default 90), `ctx.Cancel(ErrHardIdleTimeout)` at `agent.idle_hard_timeout_secs` (default 540; opt out via `0` + startup WARN). Streaming chunk-gap: `CompleteStream` returns `ErrStreamIdleTimeout` if no SSE chunk arrives within `agent.stream_idle_timeout_secs` (default 90). The loop short-circuits the streaming->Complete fallback on `ErrStreamIdleTimeout` and `isRetryableLLMError` refuses to retry it.

#### Mid-turn checkpoint

runner `applyTurn*` helpers. Fires at three phase-exit boundaries; 2s debounce. The same helpers run from checkpoint, final save, and hard-error save. `session.InProgress` non-zero on reload = crash recovery.

#### Interrupted-turn auto-resume

`daemon/interrupted_recovery.go` + `session/store.go` markers. Daemon start scans the durable `.in-progress/*.marker` index and serially continues checkpoints, newest first. **Policy gates, all load-bearing**:

- `agent.interrupted_resume_enabled` (default true).
- Staleness window `agent.interrupted_resume_max_age_hours` (default 4) — an older checkpoint carries a user intent whose context is gone; it is abandoned, marker cleared, NEVER executed.
- Attempt cap `agent.interrupted_resume_max_attempts` (default 3), persisted BEFORE the LLM call.

#### Interrupted-turn resume: run invariants

Recovered runs are ALWAYS unattended (`IsUnattendedRun()==true` regardless of the session's original source) so the unattended deny-list applies. The resume request pins the session's original RouteKey so it takes the same route lock as concurrent inbound traffic. Every persistence path — `Store.Save` AND all `Patch*` RMW paths — re-asserts the marker invariant (`syncInterruptedMarker`: on-disk InProgress=true <=> marker exists).

#### Schedule proactive push

`scheduler.go broadcastReply` + `broadcast_gate.go shouldBroadcast` / `resolveThread`. After a successful run, push is gated by `shouldBroadcast`: explicit `Schedule.Broadcast *bool` wins, else a smart default by `CreatedFromSource` (IM sources -> push, else silent).

#### Schedule proactive push: origin-only delivery

**Origin-only delivery**: the push target is ALWAYS the schedule's snapshotted `IMStatusContext` blob. A schedule with no blob (Desktop/TUI/CLI/webhook-created) NEVER pushes, even with `broadcast=on` — wrong-audience delivery beats no delivery, and results stay in the session. Cloud mirrors this by dropping a non-empty blob it cannot honor.

#### Schedule proactive push: thread mode

**Thread three-state** (`Schedule.Thread *bool`, `ParseThreadEnum`): `auto` follows session state (stateful -> one thread; stateless -> fresh top-level each run); `on`/`off` are verbatim. `resolveThread(thread, isSticky, hasBlob)` -> `ProactivePayload.UseThread *bool`; `nil` = anchored thread (current behavior) and only `&false` goes top-level. Threadless platforms (LINE/WeCom/Telegram) ignore it. Capabilities `schedule_broadcast_gate`, `proactive_thread_mode` (observability only).

#### Playwright file:// bridge

`tools/filepreview.go`. Loopback HTTP rewrites `browser_navigate(file://...)`. Fail-closed: symlink-resolved allowlist, loopback-only `r.RemoteAddr` check, random tokens, no directory listing, teardown on session close.

#### Session sync

`internal/sync/`. Daily upload (opt-in `sync.enabled`). flock + atomic marker. Permanent failure reasons (`size_limit_exceeded`, `load_error`) self-heal on session edit.

#### Memory client

`internal/memory/`. The daemon owns sidecar lifecycle + the 24h bundle pull. `memory_recall` -> `Service.Query` over UDS; falls back to `session_search` + MEMORY.md when not `Ready`. The puller accepts bundle versions `[0.4.0, 0.9.0)`; 0.8 carries the expanded W-prior manifest contract and 0.9 is the next breaking boundary. **The API key never hits disk** — only a `sha256[:16]` fingerprint. Schema-mismatch lockout surfaces as `memory.reason=tlm_binary_too_old` on `GET /status` and triggers a one-shot self-heal pull before degrading. `Sidecar.Shutdown` is idempotent so failed children do not accumulate as orphans.

#### Episodic recall routing

`prompt/builder.go` + `tools/memory.go`. Production CLI, TUI, and daemon loops expose `memory_recall` directly to the main model; no implicit small-model preflight is installed. Unnamed references route to `session_search`; no-data stops relation/mode retries. Evidence guidance (`MemoryEvidenceGuidance`) lives with the tool that produces `evidence_tier` — the `memory_recall` description — NOT in the system prompt. `memory_recall` also keeps per-group `temporal_status` (`current` vs `superseded_by_recency`) from the sidecar; prefer current unless the user asked about the past. Unknown sidecar group fields (`aggregation`, `measure`, …) survive the Go round-trip. `aggregator` (`count`/`sum`) may be sent on `direct_relation`. `agent/preflight.go` + `tools/memory_preflight.go` remain evaluation-only hooks; the injected `<private_memory>` block now tags `[status=current|superseded_by_recency]`.

#### Loop detector

`agent/loopdetect.go`. 9 detectors. `dupExemptTools` skip dup detection; all-errors 2x budget; rolling nudge window (max 3 within trailing 5). Rule "0a" empty-think force-stop: two consecutive `think({})` -> `LoopForceStop`, defending against ritual empty think after native interleaved thinking.

#### Thinking blocks

`client.ContentBlock` + `agent.buildAssistantMessage`. Cloud relays full ordered `content_blocks` incl. `thinking`/`redacted_thinking`. Persisted verbatim; `internal/sync/strip_thinking.go` removes them from the upload-side copy before the size check. Sanitizers in `messagesForLLM` / time-based / micro-compact / `BuildForkedRequest` MUST preserve them.

#### Conditional `think` tool

`tools/register.go shouldRegisterThinkTool`. Not registered on the default gateway+thinking path. Still registered when thinking is disabled, on the Ollama provider, or with `ForceThinkTool=true`. `operationalRules()` strips the `### Planning` bullet only when think is absent, keeping the prompt byte-equal otherwise.

#### Prompt suggestion

`agent/suggestion.go` + `daemon/runner.go`. Forked LLM call after each main turn. **CACHE SAFETY**: byte-equal to the main request except 2 appended messages + `SkipCacheWrite: true`. Any other divergence fragments the cache. **Source-gated** (`wantsPromptSuggestion` / `promptSuggestionSources`): only foreground sources with a UI consumer — `desktop`, `kocoro`, `shanclaw` (legacy Desktop alias), `web`. IM channels, schedule/cron, and autonomous local sources are skipped (no consumer = dead work + a billed call). It is an ALLOW-list, so new background sources default to skipped.

#### Email/password auth (macOS + Windows + Linux)

`internal/daemon/auth.go` + `auth_handlers.go` + `ws_controller.go` + `internal/keychain/`. `/local/auth/*` proxy to Cloud `/api/v1/auth/*`. The AuthManager state machine drives WS lifecycle — WS runs only in `signed_in`, and `WSController.Start`/`Stop` are the ONLY allowed call sites for spinning the reconnect loop. api_key is the source-of-truth credential in the credential store (`ai.kocoro.daemon.api_key/<user_id>`); the yaml field is migrated away on first launch, with a read-back verify before stripping yaml. The supported-platform set is `keychain.Supported()` and MUST stay in sync with the backend build tags (enforced by `TestSupportedMatchesBuildTag`). On unsupported platforms: AuthManager nil, endpoints 503 `platform_unsupported`, legacy `cfg.APIKey` path.

### Daemon Approval Protocol

- **Interactive** (default): approval round-trips over WS to Ptfrog — the historical codename for the Cloud-relayed first-party approval surface, as opposed to an external channel. **The name survives only in prose and one field comment; it is NOT on the wire.** `ApprovalResolvedPayload.ResolvedBy` (`internal/daemon/types.go`) is written as `"kocoro"` by the HTTP resolve path (`server.go`) and `"daemon"` by broker-side cleanup (`approval.go`, `question_broker.go`), which is what the pinned fixtures carry. Match a fixture or a writer, never the comment.
- **Auto-approve** (`daemon.auto_approve` or per-agent): skips the WS round-trip except for unattended-deny-listed tools such as `computer_use` and `screenshot`; the permission engine remains enforced.
- Synchronous HTTP API handlers auto-approve (localhost-only) except for unattended-deny-listed tools.

"Always Allow" goes through `alwaysallow.go HandleAlwaysAllowDecision` — single entry point shared by SSE and WS so transports can't drift. Persistence matrix:

| Tool | Agent | Persistence | Notes |
|---|---|---|---|
| bash, always-ask command | any | none | One-time allow + `EventApprovalNotice` warning. Runtime gate in `loop.go` enforces denylist even if hand-written into config. |
| bash, safe command | named | per-agent `permissions.always_allow_tools` | Future bash from this agent skips approval. |
| bash, safe command | default (`req.Agent==""`) | GLOBAL `permissions.always_allow_tools` | Affects all agents. PR 6 fix for non-technical users on default agent. |
| non-bash | named | per-agent tool-level | `agent.DisallowsAutoApproval` refuses persistence + emits warn notice. See `internal/agent/tools.go` for trade-off rationale. |
| non-bash | default | global tool-level | Same path bash takes. SSE handler creates fresh broker per request, so broker-only persistence evaporates. |

Global and per-agent always-allow lists are **unioned at injection** in `SetAlwaysAllowTools` (called from runner.go / tui/app.go / cmd/root.go after `SwitchAgent`). `SwitchAgent` resets the field so reuse can't leak.

**Two auto-approval deny-lists** (both rewritten by computer-use v1): `agent.DisallowsAutoApproval` refuses "always allow" persistence and now contains the four legacy GUI wrappers `computer` / `accessibility` / `applescript` / `ghostty`. A DYNAMIC layer extends the persistence refusal beyond that static name list: integration tools whose Cloud schema carries `requires_approval` (see Integration tools above) — enforced via `agent.AlwaysAllowPersistenceDenier`, not by growing the name list. `computer_use` is deliberately ABSENT from it, because its explicit persisted grant IS the product's single global Computer Use permission. `agent.DisallowsUnattendedAutoApproval` refuses unattended auto-approval and contains `computer_use`, the standalone `screenshot` tool, plus those same four legacy names.

**The one exception**: `loop.go checkPermissionAndApproval` honors a persisted GLOBAL `computer_use` grant even on unattended runs — that grant is what lets schedules and background tasks drive the Mac. Without it, unattended execution fails closed rather than inferring consent from `daemon.auto_approve` or the absence of an approval UI. Legacy GUI names can never use the global grant. The exception is scoped to `computer_use` BY NAME (`unattendedGrantHonored` in loop.go) — not to "any persisted always-allow" — because a blanket rule would silently re-open unattended desktop capture for `screenshot`.

The unattended gate is enforced twice: at every unattended handler's `OnApprovalNeeded` AND in `loop.go checkPermissionAndApproval`, where an unattended run (`SetUnattendedRun`, fed by `runner.go isUnattendedRun` from source classification or a no-broker transport such as synchronous HTTP) skips BOTH the persisted always-allow bypass AND the SafeChecker observation exemption for deny-listed tools so the request actually reaches the handler gate — without the latter, approval-free observation actions (e.g. `computer_use` screenshot) would run unattended without the deny-list ever being consulted. `approval_request.flags` carries `always_allow_disabled` for tools on the first list (now the four legacy GUI names).

**BREAKING (computer-use v1)**: existing unattended schedules that used `applescript` / `accessibility` / `computer` / `ghostty` now fail closed — this reverses the earlier deliberate decision to keep them working. Separately, on any daemon run where `computer_use` is registered, `disableLegacyGUIFallbacksForComputerUseRun` (`tools/register.go`) REMOVES `accessibility` and `applescript` from the model-visible registry entirely and sets `BashTool.LegacyGUIAutomationDisabled` so `osascript` / `cliclick` are rejected. TUI / one-shot CLI / MCP do not go through the daemon runner and keep those tools.

**Per-agent always-allow scope**: `computer_use` is rejected at per-agent scope (`agents.ValidateAgentPermissionsConfig`) because the grant is global-only — `HandleAlwaysAllowDecision` routes it to `persistGlobalToolAlwaysAllow` from any agent. Legacy GUI names are NOT rejected on write; `agents.SanitizeAgentPermissionsConfig` drops them instead, because they were legitimately persistable before joining `highRiskTools` and config writes are full-replace — rejecting would make every affected agent permanently uneditable through the API.

**`EventApprovalNotice`** payload is `{severity, code, tool, message}`. `code` is a stable i18n key (`high_risk_not_persistable` / `bash_always_ask_not_persisted` / `persist_failed`); daemon NEVER ships translated text. `message` is English fallback.

**Approval-card `description` field**: every tool whose `RequiresApproval()` returns true requires a `description` field (5-15 words, user-facing intent, model writes it, daemon passes through). UI clients render it prominently; raw args behind a toggle. Spec in `internal/agent/approval_description.go`. Exemptions: `bash` keeps its bespoke schema (cache-stability), `computer` is a native tool (Parameters not wire-transmitted — UI synthesizes from action/x/y), `publish_to_web` declares both `description` and `purpose`. Daemon does NOT block on missing/empty `description`; UI must fall back to tool-specific args using `description?.trim() || fallback` (NOT nullish coalescing).

### Wire Contract Discipline (daemon ↔ UI clients)

- **The surface list is `docs/cloud-contract-surface.md`** (tracked). It enumerates what this repo owes Shannon Cloud and Kocoro Desktop — both call paths, `ProactivePayload`, `cache_source`, capability tokens, the upload `kind` enum, usage fields, the 429 sub-shape, and every daemon-side surface Desktop binds to — each with the counterpart symbol on the other side. Read it before changing anything that crosses a process boundary; the cloud-side counterpart is `shannon-cloud/contracts/shanclaw-surface.md`.
- **Wire fixtures**: canonical JSON for every payload UI clients decode lives in `docs/desktop-wire-fixtures/` (bus event payloads, per-request SSE payloads, HTTP response bodies — see its README for surface framing). `internal/daemon/wire_fixtures_test.go` emits each through the REAL producer path (event emitters / full `Handler()` router), semantic-compares against the fixture (never byte-equal), and decodes the produced bytes into consumer-shaped structs. Any payload-shape change updates fixture + test in the same PR; the Desktop side mirrors with decode tests over the same fixture files.
- **Capability token minting**: every cross-version contract change a UI client must detect mints a token in `Capabilities` (`internal/daemon/client.go`), surfaced on BOTH the WS handshake (`X-Kocoro-Capabilities`) and `GET /status` (`capabilities` array). Clients gate features on tokens — never version sniffing or decode-failure probing. Historical trap this kills: `display_name` / `model_tier` shipped as HTTP contract changes with no token, so partially-deployed Desktop/daemon pairs half-rendered. The `/status` fixture pins the full token list, so minting is enforced mechanically by the fixture test.
- **New event families**: session-scoped events stay flat (`tool_status`, `approval_request`, …). A new domain (e.g. a hypothetical hardware-device integration) uses dotted types (`device.status`, `device.action_request`) with a common envelope — `type`, `ts`, plus `session_id` (session-scoped) or `target_id` (device-scoped) — and the domain payload nested under its own keys. Never repurpose an existing type's fields; additive only. Full rules in the kocoro skill `references/events.md`.
- **Request/resolve interactions**: two kinds today — `ApprovalBroker` (tool approvals) and `QuestionBroker` (`ask_user_question`). Both are thin wire faces over ONE shared generic core, `pendingCore[D]` in `pending.go` (register / emit-guard / Resolve / CancelAll + at-most-one-terminal-event + bus-ID ordering invariants). A third interaction kind (device action requests, …) must build on `pendingCore` too — do NOT copy a broker. Question specifics: `question.request`/`question.resolved` bus events (dotted family) + per-request SSE `question` frame + `POST /question` ingress (Desktop-only, agent never calls it → not in kocoro skill refs, but pinned by wire fixtures); **every source without a question UI DECLINES** (a question has no safe auto-answer, unlike approval's auto-approve); capability token `question_v1`. There is no Cloud question transport yet (Desktop-local), so the gate is `CanPresentQuestionUI` — an ALLOW-list (`questionUISources`), NOT the approval predicate: Slack/Feishu/Lark/Teams/LINE can render an Allow/Deny card but have no question channel, and gating on that similarity once left those runs blocked for the whole resolution window before reporting a decline the user never made. Both the asker injection (`server.go`) and `QuestionBroker.Request` gate on it, the broker deliberately backstopping the call site.

### Config Merge Order

1. `~/.shannon/config.yaml` (global)
2. `.shannon/config.yaml` (project)
3. `.shannon/config.local.yaml` (local, gitignored)

Scalars override, lists merge+dedup, structs field-level merge. MCP server env-var casing preserved via direct YAML re-read.

### File Paths

- Agent: `~/.shannon/agents/<name>/{AGENT.md, MEMORY.md, config.yaml, commands/*.md, _attached.yaml, _owner}` (`_owner` = device-local sync-ownership sidecar, never synced)
- Global skills: `~/.shannon/skills/<name>/SKILL.md`
- Sessions: `~/.shannon/sessions/` (default) or `~/.shannon/agents/<name>/sessions/` (per-agent); SQLite FTS5 index at `<sessions-dir>/sessions.db` (auto-rebuilt)
- Spill: `~/.shannon/tmp/tool_result_<session>_<call_id>.txt`
- Attachments: `~/.shannon/tmp/attachments/<nonce>/`
- Schedules: `~/.shannon/schedules.json` + `~/Library/LaunchAgents/com.shannon.schedule.<id>.plist`
- Notification history: `~/.shannon/notifications.jsonl` (JSONL append-only, capped at 500 entries; trimmed + atomically rewritten on daemon startup, survives restarts)
- Skill secrets index: `~/.shannon/secrets-index.json` (chmod 600, flock-protected, names only); values in macOS Keychain (service `com.shannon.skill.<name>`)
- Skill recommendations: `~/.shannon/skill-recommendations.json` (atomic JSON store for account/device-directed offer state, continuation tokens, immutable install snapshots, and receipts; terminal records are pruned after TTL)
- Daemon api_key (macOS + Windows + Linux): credential store service `ai.kocoro.daemon.api_key`, account = Cloud user_id (UUID). Backend is macOS Keychain / Windows Credential Manager / Linux file store (`~/.shannon/credentials.json`, 0600, fslock-protected). Active user pointer at service `ai.kocoro.daemon.state`, account `current_user_id`. `cfg.APIKey` (yaml) is empty after the v1 migration; Bootstrap reads the credential store instead. On other platforms the credential store is unavailable and `cfg.APIKey` stays in yaml (legacy path). **Linux IaC caveat**: the migration moves `api_key` out of `config.yaml` into `credentials.json`; if `config.yaml` is config-managed (Ansible/Puppet) and re-adds `api_key`, the daemon's self-heal re-strips it each launch — document the key now lives in `credentials.json`
- Sync: marker `~/.shannon/sync_marker.json`, lock `~/.shannon/sync.lock` (never delete), dry-run outbox `~/.shannon/sync_outbox/`
- Logs: `~/.shannon/logs/audit.log`, `~/.shannon/logs/schedule-<id>.log`; `~/.shannon/logs/preflight_dump.jsonl` (only when `SHANNON_PREFLIGHT_DUMP=1`; private memory content, 0600, delete after debugging)
- Memory: socket `~/.shannon/memory.sock`, bundle root `~/.shannon/memory/`
- Desktop RPC (when daemon spawned by Kocoro Desktop): sock `~/Library/Application Support/run.shannon.shanclaw/daemon.sock` (0600) + pidfile `daemon.pid` in same dir (0700). Paths passed via `--rpc-socket` + `--rpc-pidfile` CLI flags; daemon never derives one from the other.

### Atomic Writes

`schedules.json` and `secrets-index.json` use write-to-temp + `os.Rename` + an exclusive lock (via `internal/fslock`, NOT raw `syscall.Flock` — see Cross-Platform Support) on a persistent `.lock` file. **Never delete the lock file** (causes lock race on different inodes). Atomic-rename targets are read lock-free (the rename is atomic, so readers always see a complete file); never hold a lock on the destination file itself — on Windows a mandatory `LockFileEx` would block the rename-over-open.

### Cross-Platform Support

The daemon cross-compiles to macOS / Linux / Windows (`CGO_ENABLED=0`). POSIX-only syscalls are confined behind build tags so the Windows build stays green:

- **File locking** → `internal/fslock` (`Lock`/`RLock`/`TryLock`/`Unlock`/`IsWouldBlock`): `lock_unix.go` wraps `flock(2)`, `lock_windows.go` wraps `LockFileEx`/`UnlockFileEx` (the only `golang.org/x/sys/windows` consumer). All lock call sites go through this — do NOT reintroduce raw `syscall.Flock` (breaks Windows).
- **Process-group kill** → per-package `*_proc_{unix,windows}.go` helpers (`internal/hooks`, `internal/tools` for bash, `internal/memory` for the sidecar; `internal/mcp/processgroup_{unix,windows}.go` is the original): POSIX `Setpgid` + `Kill(-pid)` vs Windows `CREATE_NEW_PROCESS_GROUP` + `taskkill /T /F`. Windows has no usable graceful step for console children (graceful `taskkill` no-ops), so the sidecar force-kills directly.
- **`shan daemon stop`** → `cmd/proc_signal_{unix,windows}.go` (`terminateDaemon`): POSIX SIGTERM vs Windows `taskkill`. HTTP `/shutdown` remains the cross-platform graceful primary; signal/taskkill is the PID-file fallback.
- **macOS-only GUI tools** (`computer_use`/`accessibility`/`applescript`/`clipboard`/`computer`/`screenshot`/`ghostty`) gate on `runtime.GOOS != "darwin"` and return a clean "only available on macOS" error elsewhere. `notify` is NOT gated — it has a cross-platform Desktop route; only its osascript fallback is darwin-gated.
- **Memory bundle `current` pointer** → `internal/memory/bundle_link_{unix,windows}.go` (`swapCurrent`): a symlink (atomic tmp+rename) on POSIX vs an unprivileged directory junction (`mklink /J`, remove+recreate) on Windows — `os.Symlink` would fail with ERROR_PRIVILEGE_NOT_HELD off Developer Mode. Both keep `current/<file>` transparently traversable by the `tlm` sidecar and resolvable by `os.Readlink` (`currentTs`).
- **Credential store (daemon api_key)** → three mutually-exclusive backends partitioning GOOS exactly: `backend_keyring.go` (`//go:build darwin || windows`, go-keyring → Keychain / Credential Manager), `backend_linux.go` (`//go:build linux`, file store via `backend_file.go`), `backend_other.go` (`//go:build !darwin && !windows && !linux`, `NewOSStoreAt` returns `ErrUnsupportedPlatform`). The constructor is `NewOSStoreAt(dir, logger)` — `dir` is the shannon dir (used by the Linux file backend, ignored by Keychain/wincred); callers pass `config.ShannonDir()` rather than letting keychain re-derive it (keychain importing config is a cycle). Runtime callers (config hydrate/save/migrate/setup, auth gating) MUST gate on `keychain.Supported()` (darwin\|\|windows\|\|linux — single source of truth, kept in sync with the build tags, enforced by `TestSupportedMatchesBuildTag`), never a raw `runtime.GOOS == "darwin"`. **Why Linux is a file store, not Secret Service**: go-keyring's SS/dbus backend returns success at construction then fails every read/write on headless hosts (no D-Bus), which would let the yaml→store migration strip the key then fail to persist it; the 0600 file is deterministic on desktop/Docker/SSH alike and is security-parity with the legacy yaml plaintext path (NOT GNOME-Keyring encryption — acceptable trade for headless support). The Linux build links no go-keyring/dbus at all. **Test-coverage note**: CI cross-compiles Windows (`go build`/`go vet`) but its unit suite runs on macOS/Linux — the live wincred round-trip is covered only by manual Windows E2E; the Linux file backend IS unit-tested (`backend_file_test.go`, tagless) and the invariant test is the automated backstop for the gate.
- **Known Windows gaps (not yet ported)**: `bash` runs `sh -c` and requires Git Bash/WSL on PATH (returns a clean error otherwise).

### Prompt Cache

See `docs/cache-strategy.md` for the public Kocoro-side contract and
`docs/cache-debug.md` for local diagnostic log fields. Production baselines,
release thresholds, and Cloud deployment controls are not tracked here.
Invariants:

- Every LLM call tags `cache_source` for attribution. Cloud owns TTL policy;
  Kocoro never treats the label as a TTL selector.
- `normalizeToolInput` (`gateway.go`) canonicalizes nested JSON key ordering for byte-stability.
- Skill allowed-tools = execution-time denial, not schema filtering (tools array stays byte-stable).
- Skill listing lives in the scaffolded user message, not system prompt.
- `agent.response_detail` renders provider-neutral final-answer guidance in BP3 StableContext: global missing/empty resolves to `balanced`, named-agent missing/empty inherits global, and provider request effort is unchanged. Strict machine-readable internal loops suppress it explicitly; normal Fast/Full profiles retain it.
- All in-place `messages[idx].Content` rewrites MUST call `client.LogCacheCompactEvent` — uninstrumented rewrites silently break drift attribution.
- **A rewrite pass MUST NOT be scheduled per iteration.** Instrumentation is not enough: a sliding window evaluated every turn is fully compliant with the rule above and still bleeds. Each new observation pushes one older observation into the stub set, so the first divergent byte advances one slot per turn and the provider re-writes the whole suffix behind it — cache WRITE (Luna 1.25x) for bytes that would otherwise have been cache READS (0.1x). Schedule the pass on a signal where clearing is free instead: a prefix already proven cold (`timebasedcompact.go`), an aggregate budget (`toolresult_budget.go`), or context pressure (compaction). `internal/agent/observation_trigger.go` (`agent.observation_window_trigger`, default `hybrid`) is the browser/GUI observation window's implementation of this; `every_turn` preserves the legacy behavior for measurement. Measured on a real 9-call browser session: `every_turn` broke the prefix on 4 of 9 turns and re-sent 41.5 KB, `hybrid` broke 1 of 9 and re-sent 23.3 KB.

### Context Management

- **Context window**: `agent.context_window` (default 1_000_000 — matches the 1M-context families that medium/large tiers route to; the authoritative list is `modelcontext.go`, currently Sonnet 4.6 / Sonnet 5 / Opus 4.6 / Opus 4.7 / mythos-preview / Gemini 3 Pro) is a seed; `maybeAutoAdjustContextWindow` resets from `response.model` via `modelcontext.go` (Anthropic/OpenAI/Google/xAI; 1M and 200K families). Catches Cloud tier-failover (e.g. Haiku 200K) in either direction. Per-agent override calls `SetContextWindowExplicit` (lock); auto-detect skips locked loops. For Ollama (model names absent from `LookupModelContextWindow`), callers wrap the fallback with `agent.ContextWindowFloorForProvider` which clamps to 200K so a local 128K model is not seeded at 1M.
- **Estimator calibration**: `AgentLoop.estOverheadTokens` = (real prompt tokens of the last main response) − (`EstimateTokens` of the request that produced them), re-derived per response — captures tools[] schema mass and the chars/3.5 error (~25% on code). Every estimate-based compaction decision (ShapeHistory gates, preflight, user truncation) adds it so estimates and the real-usage trigger share one scale; reset on `SwitchAgent`/session change, sample skipped when an error retry rebuilt the request. The sample persists in the session (`Session.CompactionCalibration`: overhead + response model + tool-registry fingerprint) and is restored onto the daemon's fresh per-request loop via `SetEstOverheadState`, which discards it on model-pin mismatch, registry-fingerprint change, or the >window sanity clamp — so resumed daemon Runs are no longer blind at iteration 0.
- **Archive vs live context**: `Session.Messages` + `MessageMeta` is the lossless transcript used by resume/search/share/sync and is never rewritten by compaction. `Session.CompactionCheckpoint` is the durable model-live state (`Messages` = summary + retained tail, no system prompt/injected messages; `ArchiveThroughIndex` = exclusive index in the RAW transcript). `HistoryForLoop` returns checkpoint messages plus the filtered raw tail. Daemon auto-compaction and TUI `/compact` both update this checkpoint; later non-compacting turns append to the archive without regenerating the summary. Rewind/truncate/reset invalidates the checkpoint. Current-run archival messages are frozen before every in-memory reshape so mid-turn compaction cannot delete tool evidence from the transcript.
- **Proactive compaction** triggers at max(90% of window, window − `compactAbsoluteBufferTokens` 60K) real usage — on the 1M families the absolute buffer governs and reclaims the ~40K the fractional line forfeited; on small windows the 90% floor is unchanged. `PersistLearnings` → `GenerateSummary` (two-phase analysis→summary) → `ShapeHistory`. Shaped history must LAND under the matching landing line (max(80% of window, window − 3×buffer)) — the trigger/landing gap is the hysteresis band that stops back-to-back compactions (two buffers wide when the absolute trigger governs, sized so the ~50K restoration payload plus a large turn pair still fit; pinned by `TestRestoreCapFitsAbsoluteHysteresisBand`). The preflight backstop complements the same way: max(95% of window, window − buffer/2). A shaping no-op (nothing droppable) does not latch `compactionApplied` and keeps the summary cached for a free retry.
- **Pre-flight compaction** at max(95% of window, window − max(buffer/2, `defaultMaxOutputTokens`)) calibrated estimate (`shouldPreflightCompact`): backup gate before each main LLM call + force-stop turn. Emits `OnRunStatus("preflight_compaction")`; reuses the proactive path's cached summary when present.
- **Reactive evidence floor**: a context-length 400 proves the prompt exceeds the window, so the reactive path floors its calibration at `window − estimate + 1` — shaping engages even with no usage sample this Run.
- **Summary quality validation**: `GenerateSummary` audits its candidate at compaction scale (history has a droppable middle): labeled section structure must be present and identifier-shaped tokens scanned from the droppable middle (long hex, URLs, unix paths, host:port, long numbers; ≤12 unique) must appear verbatim. One retry carries the failure reasons; whatever it still misses is appended mechanically under `## Exact identifiers (auto-preserved)`. Short histories keep single-call prose behavior.
- **Post-compaction file restoration**: after an applied PROACTIVE or PREFLIGHT compaction, the loop re-reads the most recently read files (session `ReadTracker`) and appends them as one system-reminder user message before the task reanchor — 5 files / ~5K tokens each / ~50K total, and the payload must keep the calibrated estimate under the 90% trigger line (it may consume part of the hysteresis band). MEMORY.md/AGENT.md excluded; reads surviving in the kept tail (including prior `## [restored]` blocks) skipped. The REACTIVE path deliberately skips restoration — its 400-evidence floor is only a lower bound and `reactiveCompacted` makes a second overflow terminal; its reanchor variant instead tells the model to re-read what it needs.
- **Pre-compaction snapshot**: every applied compaction (proactive/preflight/reactive/force-stop, plus TUI `/compact`) hands the exact pre-replacement model-live state to a snapshotter before the durable checkpoint changes. Two MUSTs on the way out: `<private_memory>` is stripped inside `snapshotBeforeCompaction`, and `session.Store.SaveCompactionSnapshot` replaces every top-level or tool-result-nested image with a text marker so screenshot base64 never reaches rollback files and the active vision trajectory is not mutated. Stored at `<sessions-dir>/.compaction-snapshots/<session-id>/`; `agent.compaction_snapshot_retention` (default 1, 0 disables; >=2 pins the OLDEST and evicts from the second-oldest up), `agent.compaction_snapshot_max_age_days` (default 14, 0 disables; age expiry overrides the oldest pin). Cleanup has three paths and one deliberate omission: `Store.Delete` removes the session's snapshot dir, store startup removes orphan dirs, and the age sweep removes expired files — but **empty per-session directories are left behind on purpose, to avoid racing a concurrent writer**. Do not "tidy" them away. Best-effort — snapshot errors are logged and NEVER fail a compaction. No read/restore tooling ships yet, so recovery is manual.
- **Reactive compaction** on context-length error: emergency compress + single retry; `reactiveCompacted` prevents loops. Summarize input is budgeted in TOKENS, not bytes: `summarizeInputCapTokens=150_000` against the small tier's 200K window, converted to a byte ceiling via `conservativeBytesPerToken=2.5` (a measured floor — live small tier bills JSON at 2.60 bytes/token, CJK 2.71, Go source 3.43, English prose 4.78). Do NOT gate this on `EstimateTokens`: its 3.5 chars/token is representative, not conservative, and the previous byte cap (540_000, documented as "≈180K tokens") actually billed 213,719 against a 200K window, so every compaction 400'd and the session grew ~2K tokens per failed turn instead of shrinking (2026-08-05 production incident). `warnIfDenserThanSafetyFloor` logs when billed tokens fall below the floor so the next drift is diagnosable; a transcript over the cap is no longer head+tail-truncated — it is split into tool-pair-preserving chunks and sequentially folded into a running summary (max `maxSummaryFoldChunks`, oldest elided beyond that), with the final chunk going through the normal structured two-phase prompt. Any fold failure degrades to the old rune-safe head+tail single call.
- **Failure telemetry**: `recordCompactionFailure` emits `OnRunStatus("compaction_failed")` + audit row. 10 phase tags cover force-stop, proactive, preflight, reactive, and emergency paths.
- **Tiered result compression**: newest 8 tool-result messages stay full; distance 8-19 uses Tier 2 (up to two semantic summaries per pass for eligible results >2,000 chars, otherwise 300-char head+tail); distance >=20 normally uses Tier 1 metadata. Already-compressed blocks stay byte-stable, and Tier 2 floor tools (`file_read`, `grep`, `glob`, `directory_list`, `browser_*`) never degrade to Tier 1. Tier-2 semantic summaries carry a head-clipped current-task line (`latestUserText`, ≤240 runes) so the summarizer prioritizes task-relevant details.
- **Memory staleness**: `annotateStaleness()` appends `[N days ago]` to memory headings.
- **Deferred tool loading**: each tool resolves independently from an explicit `ToolExposure` override, then its source default. Local tools default Direct; MCP/gateway/integration tools default Deferred. `ask_user_question` is an explicit Direct opener. `web_search` / `web_fetch` / `x_search` are Direct **only when `ToolSource()==SourceGateway`** (`tools/exposure.go` `ServerTool.ToolExposure`) — a same-named MCP or integration tool keeps its Deferred source default, so a third-party catalog cannot widen the base schema surface by shadowing a trusted name. GUI/process automation plus calendar/schedule mutations are explicit Deferred, while calendar/schedule reads remain Direct. `tool_search` ranks automatically derived name/description/schema/namespace metadata with BM25 (maximum 8 ranked seeds before family expansion). The session `WorkingSet` caches warmed schemas and the deterministic search index. The 16K Direct-schema estimate is diagnostic only and never reclassifies tools.
- **System reminders**: short `<system-reminder>` hints appended to `file_read`/`file_write`/`file_edit`/`bash` results; skipped for `cloud_delegate`.

### Anti-Hallucination

XML `<tool_exec>` delimiters use random hex call_id. Model-authored preambles are preserved. In attended runs, a silent first tool batch may fall back to a local tool's required user-facing `description`; external tool descriptions and generic `purpose` fields remain silent. Unattended runs remain silent. Fabricated tool calls are detected and stripped.

## Testing

```bash
go test ./...                              # all
go test ./internal/daemon/ -v              # daemon: WS, router, E2E routing, launchd
go test ./internal/agent/ -v               # loop, partitioning, spill, deferred
go test ./internal/agents/ -v              # agent loader
go test ./internal/schedule/ -v            # schedule CRUD
go test ./test/ -v                         # E2E: vision pipeline, persist learnings
go test ./test/e2e/ -v                     # E2E offline (CI)
SHANNON_E2E_LIVE=1 go test -tags=live ./test/e2e/ -v  # E2E live (run before each release)
go build ./...
```

Koe tests link cgo audio deps. On macOS, install them with `brew install opus opusfile pkg-config` and set `PKG_CONFIG_PATH=/opt/homebrew/lib/pkgconfig` if pkg-config cannot find the Homebrew files.

Schedule tests use temp dirs — never write to real `~/Library/LaunchAgents/`. Launchd plist coverage lives with daemon tests.

Koe live E2E uses the hidden, foreground-only daemon isolation flags documented in `docs/live-e2e-testing.md`. It isolates filesystem state, port ownership, AND credential-store access (`--isolated` calls `config.DisableCredentialStoreForProcess`, so config load skips OS credential reads and the yaml→keychain migration; the authorized key is piped in via `--isolated-api-key-stdin`). It suppresses Cloud/background automation and leaves agent tool capabilities shared with the user account. MCP is disabled unless `--isolated-mcp` passes an explicit startup allowlist.

## Building & Releasing

- GoReleaser: `.goreleaser.yaml`
- npm: `@kocoro/kocoro` (previously `@kocoro/shanclaw`, deprecated post-v0.1.7)
- **Versioning: PATCH-only by default** — do NOT bump minor/major unless explicitly asked
- Release: `git tag -a vX.Y.Z` → `git push origin vX.Y.Z` → CI publishes
- `docs/` is gitignored by default — tracked documents are explicitly allowlisted in `.gitignore`; add new docs there before committing

## Local Tools

Always registered (`internal/tools/register.go RegisterLocalTools`):

- **File**: file_read (auto-compresses images >3.75 MB raw, see `imaging_compress.go`), file_write, file_edit, glob, grep, directory_list
- **Archive**: archive_inspect (read-only), archive_extract (approval). Zip/tar/tar.gz via stdlib. Atomic staging+rename; rejects encrypted/absolute/symlink/device/setuid; zipbomb caps (50 MB/entry, 200 MB total, 500 entries). See `archive.go`.
- **Documents**: pdf_to_text, docx_to_text, xlsx_to_text, pptx_to_text. Prefer poppler/pandoc/xlsx2csv; fall back to unzip+XML strip (no fallback for PDF — surfaces `brew install poppler` hint + suggests upload for native Anthropic document block). Fixed-argv, 60s timeout, 100K-rune output cap. See `doc_extract.go`.
- **Shell/system**: bash, system_info, process, http, think
- **macOS GUI**: computer_use (primary native-GUI workflow), accessibility (legacy low-level AX), applescript, screenshot, computer, clipboard, notify, browser, wait_for, ghostty. On daemon runs, the parent exposes one high-level `computer_use` task function (explicit Deferred — discovered via tool_search like the rest of the GUI family) and keeps its configured model (Sonnet 5 by default). Only an actual call lazily resolves `openai.computer.v1` and starts a private OpenAI Responses trajectory. A single exact task app binds background-first; semantic press/scroll and ordinary target-bound input stay in that lane, while `foreground_allowed` may activate the target only when an action lacks an exact background primitive. Multi-app tasks retain foreground switching. Screenshots, pointer actions, typing, re-observation, continuation, `state_id`, refs, and coordinate frames stay internal. NSWorkspace + CGWindow provides a coordinate-capable target when AX is incomplete, while foreground OpenAI pointer actions use the visible CGEvent path. Ambient physical interference requires one exact fresh observation before another mutation; explicit Pause/Take Over/Stop retains user-owned quiescence. The whole call uses the shared GUI-operation lock. The standalone `screenshot` tool remains separate and approval-gated. Unattended `computer_use` still requires the explicit persisted global grant.
- **Schedule**: schedule_create / _list / _update / _remove / _show
- **Memory**: memory_append (flock-protected MEMORY.md append)
- **User interaction**: ask_user_question — closed-choice escalation (1-4 questions, 2-4 options each; model receives full option labels, not bare tokens). `RequiresApproval()==false` — its own request/resolve interaction, NOT an approval. Reaches the daemon `QuestionBroker` through an `agent.QuestionAsker` injected on the tool-call context (`internal/tools` can't import `internal/daemon`); no asker on ctx (unattended, ANY messaging channel — including Slack/Feishu/Lark/Teams/LINE despite their approval UI, and koe — sync HTTP, TUI) → clean "can't ask here, use best judgment" result. `CanPresentQuestionUI`/`questionUISources` is the single gate; it is an allow-list, so an unlisted source degrades to prose rather than wedging the run. Its explicit Direct exposure keeps it in the first-turn schema set; the volatile `Structured question UI: available` capability line gates calls on surfaces with a live asker. See Wire Contract Discipline + `internal/daemon/question.go` / `question_broker.go` / `pending.go`. Over-asking is suppressed by the "## Structured Questions" prompt gate (`internal/prompt/builder.go`) — material-unresolved-fork threshold, the same-response MUST rule when the UI line is present, and the Custom/Other placeholder-option ban — not the tool description.
- **Skills**: use_skill

Conditional:

- `session_search` — when session manager available
- `cloud_delegate` — `cloud.enabled: true`
- `publish_to_web` — `cloud.enabled` + `cfg.APIKey`. Always approval. Path-segment + basename blocklist (`.env`/`.pem`/…); extension allowlist (`cloud.publish_allowed_extensions`). All uploads tagged `kind=other` server-side; the kind enum (`session_share`/`report`/`landing_page`/`image`/`other` — see `internal/uploads/client.go`) is NOT exposed to the model.
- `list_my_published_files` — same gating. Read-only, no approval. `limit` (≤100), `offset`, optional `kind` filter (same enum). Returns paged `UploadEntry` rows keyed by id; rendering surfaces a `kind=…` badge per row so the LLM can answer "which of these are session shares".
- `retract_published_file` — same gating. Destructive, requires approval. Args: `id` (UUID from list) + `description`. 404 conflates not-found/already-retracted/not-yours to avoid existence leak.
- `generate_image` / `edit_image` — same gating. Always approval (paid quota + permanent CDN). Edit requires `image_urls` 1-4 entries starting with `https://static.kocoro.ai/`.
- `x_upload_media` — same gating. Always approval; explicit Deferred. Uploads one local image (jpg/jpeg/png/gif/webp; 5 MB, GIF 15 MB — X's own caps) for the X posting tools: local guards (publish_to_web's path blocklist + narrow media allowlist) → CDN staging upload (`kind=image`, metadata `{"purpose":"x_media"}`) → Cloud integration execute `x_upload_media` with `media_url` → best-effort delete of the staging upload (skipped when the Upload response has no `id`; failure logged, never overrides the result) → returns `media_id` + expiry hint. Cloud defines a same-named integration schema for execute-route authz; the local tool wins by the standard local-priority collision rules. `extractToolPath` recognizes the conventional `file_path` argument generically, so user-attached files ride the attachment auto-approve.
- `tool_search` — registered Direct whenever the effective registry contains cold Deferred tools; keyword retrieval uses the internal deterministic BM25 index in `agent/toolsearch_index.go`
- **`calendar_*` family (8 tools)** — registered only when daemon is a Kocoro Desktop subprocess (`tools.RegisterCalendarTools` no-ops when the `DesktopRPCBroker` is nil; TUI/one-shot/MCP/scheduled paths fall back to `applescript` + Calendar.app). Tools: `calendar_check_permission`, `calendar_request_permission` (approval, 5-min TCC-dialog timeout), `calendar_list_sources`, `calendar_list_events`, `calendar_get_event`, `calendar_create_event` / `_update_event` / `_delete_event` (approval). Backed by Calendar RPC v1 (Unix socket reverse RPC to Desktop's EventKit); protocol reference in the kocoro skill `references/calendar.md` + `references/desktop-rpc.md`. `attendees` is metadata-only — `invitations_sent` always `false` in v1. `update_event` rejects `scope=all`; use delete + create.
