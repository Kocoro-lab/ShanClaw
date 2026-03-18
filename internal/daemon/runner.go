package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/agents"
	"github.com/Kocoro-lab/ShanClaw/internal/audit"
	"github.com/Kocoro-lab/ShanClaw/internal/client"
	"github.com/Kocoro-lab/ShanClaw/internal/config"
	"github.com/Kocoro-lab/ShanClaw/internal/hooks"
	"github.com/Kocoro-lab/ShanClaw/internal/schedule"
	"github.com/Kocoro-lab/ShanClaw/internal/session"
	"github.com/Kocoro-lab/ShanClaw/internal/skills"
	"github.com/Kocoro-lab/ShanClaw/internal/tools"
)

// RunAgentRequest is the input for RunAgent.
type RunAgentRequest struct {
	Text       string `json:"text"`
	Agent      string `json:"agent,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	NewSession bool   `json:"new_session,omitempty"`
	Source     string `json:"source,omitempty"`    // "slack", "line", "shanclaw", "webhook"
	Sender     string `json:"sender,omitempty"`    // user identifier from channel
	Channel    string `json:"channel,omitempty"`   // channel/thread source context
	ThreadID   string `json:"thread_id,omitempty"` // thread context for messaging platforms
	RouteKey      string `json:"-"`                   // internal routing key
	Ephemeral     bool   `json:"-"`                   // caller owns persistence + events
	ModelOverride string `json:"-"`                   // overrides agent model tier
}

// Validate checks that the request has the minimum required fields.
func (r *RunAgentRequest) Validate() error {
	if strings.TrimSpace(r.Text) == "" {
		return fmt.Errorf("text is required")
	}
	if r.Agent != "" {
		if err := agents.ValidateAgentName(r.Agent); err != nil {
			return err
		}
	}
	return nil
}

// ComputeRouteKey builds the route key for session cache/locking decisions.
func ComputeRouteKey(req RunAgentRequest) string {
	if req.Agent != "" {
		return "agent:" + req.Agent
	}
	if req.SessionID != "" {
		return "session:" + sanitizeRouteValue(req.SessionID)
	}
	if req.NewSession || shouldBypassRouteCache(req.Source) {
		return ""
	}
	if req.Source != "" && req.Channel != "" {
		return "default:" + sanitizeRouteValue(req.Source) + ":" + sanitizeRouteValue(req.Channel)
	}
	return ""
}

func shouldBypassRouteCache(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "", ChannelWeb, "webhook", "cron", ChannelSchedule, ChannelSystem:
		return true
	default:
		return false
	}
}

func sanitizeRouteValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return url.PathEscape(trimmed)
}

// EnsureRouteKey computes and sets the route key if not already set.
func (req *RunAgentRequest) EnsureRouteKey() {
	if req == nil {
		return
	}
	if req.RouteKey == "" {
		req.RouteKey = ComputeRouteKey(*req)
	}
}

func routeTitle(source, channel string) string {
	if source == "" || channel == "" {
		return ""
	}
	s := strings.ToLower(strings.TrimSpace(source))
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:] + " " + channel
}

// RunAgentResult is the output from RunAgent.
type RunAgentResult struct {
	Reply     string        `json:"reply"`
	SessionID string        `json:"session_id"`
	Agent     string        `json:"agent"`
	Usage     RunAgentUsage `json:"usage"`
}

// RunAgentUsage tracks token and cost information for a single agent run.
type RunAgentUsage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// ServerDeps holds shared dependencies required by both the WS callback
// and the HTTP server for running agent loops.
type ServerDeps struct {
	mu              sync.RWMutex // guards Config, Registry, Cleanup during reload
	Config          *config.Config
	GW              *client.GatewayClient
	Registry        *agent.ToolRegistry
	Cleanup         func() // closes MCP connections; swapped on reload
	ShannonDir      string
	AgentsDir       string
	Auditor         *audit.AuditLogger
	HookRunner      *hooks.HookRunner
	SessionCache    *SessionCache
	EventBus        *EventBus
	ScheduleManager *schedule.Manager
}

// Snapshot returns current Config and Registry under read lock.
// Callers use the returned values without holding the lock.
func (d *ServerDeps) Snapshot() (*config.Config, *agent.ToolRegistry) {
	d.mu.RLock()
	cfg, reg := d.Config, d.Registry
	d.mu.RUnlock()
	return cfg, reg
}

// ShutdownCleanup captures and calls the current Cleanup function under lock,
// preventing races with concurrent reload swaps.
func (d *ServerDeps) ShutdownCleanup() {
	d.mu.Lock()
	cleanup := d.Cleanup
	d.Cleanup = nil
	d.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}

// WriteLock acquires the write lock on ServerDeps. Used by daemon event
// handler to update in-memory config (e.g., always-allow persistence).
func (d *ServerDeps) WriteLock()   { d.mu.Lock() }
func (d *ServerDeps) WriteUnlock() { d.mu.Unlock() }

// RunAgent executes a single agent turn using the shared dependencies.
// The caller provides an EventHandler to control streaming, approval, and
// event reporting (WS uses daemonEventHandler, HTTP uses httpEventHandler).
func RunAgent(ctx context.Context, deps *ServerDeps, req RunAgentRequest, handler agent.EventHandler) (*RunAgentResult, error) {
	cfg, baseReg := deps.Snapshot()
	if cfg == nil || deps.GW == nil || baseReg == nil || deps.SessionCache == nil {
		return nil, fmt.Errorf("daemon not fully configured")
	}
	agentName := req.Agent
	prompt := req.Text
	// "default" is not a real agent — it means "use base agent, no --agent flag".
	if agentName == "default" {
		agentName = ""
	}
	req.Agent = agentName
	explicitAgent := agentName != "" // explicitly requested, not parsed from @mention

	// Parse @mention if no explicit agent was provided.
	if agentName == "" {
		agentName, prompt = agents.ParseAgentMention(req.Text)
	}
	if prompt == "" {
		prompt = req.Text
	}

	var agentOverride *agents.Agent
	if agentName != "" {
		a, loadErr := agents.LoadAgent(deps.AgentsDir, agentName)
		if loadErr != nil {
			if explicitAgent {
				return nil, fmt.Errorf("agent not found: %s", agentName)
			}
			// @mention fallback: use default agent
			log.Printf("daemon: agent %q not found: %v, using default", agentName, loadErr)
			agentName = ""
			prompt = req.Text
		} else {
			agentOverride = a
		}
	}
	// Resolve agent-scoped slash command: "/cmd-name args" → command content.
	if agentOverride != nil && strings.HasPrefix(prompt, "/") {
		parts := strings.Fields(prompt)
		cmdName := strings.TrimPrefix(parts[0], "/")
		if content, ok := agentOverride.Commands[cmdName]; ok {
			args := ""
			if len(parts) > 1 {
				args = strings.Join(parts[1:], " ")
			}
			prompt = strings.ReplaceAll(content, "$ARGUMENTS", args)
		}
	}
	req.Text = prompt
	// Recompute route key after final agent resolution.
	// Callers may precompute a default/source-channel key before @mention parsing.
	// Recomputing here avoids cross-route contamination.
	req.RouteKey = ComputeRouteKey(req)

	sessionsDir := deps.SessionCache.SessionsDir(agentName)
	var sessMgr *session.Manager

	var route *routeEntry
	// Empty route key = no cache entry for routing, always start a fresh local session.
	if req.RouteKey != "" {
		route = deps.SessionCache.LockRouteWithManager(req.RouteKey, sessionsDir)
		sessMgr = route.manager
		reqCtx, cancel := context.WithCancel(ctx)
		route.done = make(chan struct{})
		route.injectCh = make(chan string, 10) // buffered for async sends
		ctx = reqCtx
		// Register cancel under sc.mu so CancelRoute sees it immediately.
		// Also fires cancel right away if CancelRoute already set cancelPending.
		deps.SessionCache.SetRouteCancel(req.RouteKey, cancel)
	defer func() {
			// route.mu is already held from LockRouteWithManager — do NOT
			// re-acquire it (sync.Mutex is not reentrant; that deadlocks).
			// Clean up under the existing lock, then release via UnlockRoute.
			if route.done != nil {
				closeRouteDone(route.done)
			}
			route.done = nil
			route.cancel = nil
			route.injectCh = nil
			// Set sessionID directly — do NOT call SetRouteSessionID which
			// would try to acquire route.mu again (same deadlock).
			if current := sessMgr.Current(); current != nil {
				route.sessionID = current.ID
			}
			deps.SessionCache.UnlockRoute(req.RouteKey)
		}()
	} else {
		sessMgr = session.NewManager(sessionsDir)
		defer func() {
			if err := sessMgr.Close(); err != nil {
				log.Printf("daemon: failed to close ephemeral session manager for %q: %v", sessionsDir, err)
			}
		}()
	}

	switch {
	case req.SessionID != "":
		// Resume a specific session by ID (reuses cached manager to avoid DB handle leak).
		if _, err := sessMgr.Resume(req.SessionID); err != nil {
			return nil, fmt.Errorf("session not found: %s", req.SessionID)
		}
	case req.NewSession || req.RouteKey == "":
		sessMgr.NewSession()
	case route != nil && route.sessionID != "":
		if _, err := sessMgr.Resume(route.sessionID); err != nil {
			log.Printf("daemon: failed to resume routed session %q for %q: %v", route.sessionID, req.RouteKey, err)
			sessMgr.NewSession()
		}
	case strings.HasPrefix(req.RouteKey, "agent:"):
		// Named-agent routes preserve manager-loaded current session across restarts.
		if sessMgr.Current() == nil || sessMgr.Current().ID == "" {
			sessMgr.NewSession()
		}
	default:
		sessMgr.NewSession()
	}
	sess := sessMgr.Current()

	// Persist session to disk before loop.Run() so there's a record even if
	// the daemon crashes mid-execution. The final save after completion is
	// still needed to capture the assistant's reply.
	// Ephemeral requests skip persistence — the caller owns session lifecycle.
	if !req.Ephemeral {
		if req.Source != "" && req.Channel != "" {
			sess.Source = req.Source
			sess.Channel = req.Channel
		}
		if sess.Title == "New session" && req.RouteKey != "" {
			title := routeTitle(req.Source, req.Channel)
			if title != "" {
				sess.Title = title
			}
		}
		if err := sessMgr.Save(); err != nil {
			log.Printf("daemon: failed to pre-save session: %v", err)
		}
	}

	// Snapshot history BEFORE appending the user message so loop.Run(prompt, history)
	// does not receive the user message twice (once as prompt, once in history).
	history := sess.Messages

	// For externally-sourced messages (Slack, LINE, etc.), persist the user message
	// before the agent loop so the UI can display it immediately on notification.
	// preLoopUserAppended tracks the in-memory append (not save success) to prevent
	// double-appending in the post-loop persist block.
	userMsgTime := time.Now()
	var preLoopUserAppended bool
	if !req.Ephemeral && req.Source != "" {
		source := req.Source
		if source == "" {
			source = "unknown"
		}
		msgID := generateMessageID()
		sess.Messages = append(sess.Messages,
			client.Message{Role: "user", Content: client.NewTextContent(prompt)},
		)
		sess.MessageMeta = append(sess.MessageMeta,
			session.MessageMeta{Source: source, MessageID: msgID, Timestamp: session.TimePtr(userMsgTime)},
		)
		preLoopUserAppended = true
		if err := sessMgr.Save(); err != nil {
			log.Printf("daemon: failed to pre-save user message: %v", err)
		} else if deps.EventBus != nil {
			payload, _ := json.Marshal(map[string]any{
				"agent":      agentName,
				"source":     req.Source,
				"sender":     req.Sender,
				"session_id": sess.ID,
				"message_id": msgID,
				"text":       prompt,
			})
			deps.EventBus.Emit(Event{Type: EventMessageReceived, Payload: payload})
		}
	}

	// Clone and apply per-agent tool filter
	reg := baseReg.Clone()
	if agentOverride != nil {
		reg = tools.ApplyToolFilter(reg, agentOverride)
	}

	// Load skills (agent-scoped or global) and wire to registry
	var loadedSkills []*skills.Skill
	if agentOverride != nil {
		loadedSkills = agentOverride.Skills
	} else {
		var err error
		loadedSkills, err = agents.LoadGlobalSkills(deps.ShannonDir)
		if err != nil {
			log.Printf("WARNING: failed to load global skills: %v", err)
		}
	}
	tools.SetRegistrySkills(reg, loadedSkills)

	// Always expose local session search for daemon-served agents.
	// Use the per-agent manager so searches are scoped to that agent's sessions.
	tools.RegisterSessionSearch(reg, sessMgr)

	loop := agent.NewAgentLoop(deps.GW, reg, cfg.ModelTier, deps.ShannonDir,
		cfg.Agent.MaxIterations, cfg.Tools.ResultTruncation, cfg.Tools.ArgsTruncation,
		&cfg.Permissions, deps.Auditor, deps.HookRunner)
	loop.SetMaxTokens(cfg.Agent.MaxTokens)
	loop.SetTemperature(cfg.Agent.Temperature)
	loop.SetContextWindow(cfg.Agent.ContextWindow)
	loop.SetEnableStreaming(false)
	if agentOverride != nil {
		scopedMCPCtx := tools.ResolveMCPContext(cfg, agentOverride)
		agentDir := filepath.Join(deps.ShannonDir, "agents", agentName)
		loop.SwitchAgent(agentOverride.Prompt, agentDir, nil, scopedMCPCtx, loadedSkills)
	} else {
		loop.SetMemoryDir(filepath.Join(deps.ShannonDir, "memory"))
		if loadedSkills != nil {
			loop.SetSkills(loadedSkills)
		}
		scopedMCPCtx := tools.ResolveMCPContext(cfg)
		if scopedMCPCtx != "" {
			loop.SetMCPContext(scopedMCPCtx)
		}
	}
	if cfg.Agent.Model != "" {
		loop.SetSpecificModel(cfg.Agent.Model)
	}
	if cfg.Agent.Thinking {
		if cfg.Agent.ThinkingMode == "enabled" {
			loop.SetThinking(&client.ThinkingConfig{Type: "enabled", BudgetTokens: cfg.Agent.ThinkingBudget})
		} else {
			loop.SetThinking(&client.ThinkingConfig{Type: "adaptive"})
		}
	}
	if cfg.Agent.ReasoningEffort != "" {
		loop.SetReasoningEffort(cfg.Agent.ReasoningEffort)
	}
	// Per-agent model config overrides
	if agentOverride != nil && agentOverride.Config != nil && agentOverride.Config.Agent != nil {
		ac := agentOverride.Config.Agent
		if ac.Model != nil {
			loop.SetSpecificModel(*ac.Model)
		}
		if ac.MaxIterations != nil {
			loop.SetMaxIterations(*ac.MaxIterations)
		}
		if ac.Temperature != nil {
			loop.SetTemperature(*ac.Temperature)
		}
		if ac.MaxTokens != nil {
			loop.SetMaxTokens(*ac.MaxTokens)
		}
		if ac.ContextWindow != nil {
			loop.SetContextWindow(*ac.ContextWindow)
		}
	}
	if req.ModelOverride != "" {
		loop.SetModelTier(req.ModelOverride)
	}
	// Inject session metadata as sticky context so it survives compaction.
	if req.Source != "" || req.Channel != "" || req.Sender != "" {
		var parts []string
		if req.Source != "" {
			parts = append(parts, "Source: "+req.Source)
		}
		if req.Channel != "" {
			parts = append(parts, "Channel: "+req.Channel)
		}
		if req.Sender != "" {
			parts = append(parts, "Sender: "+req.Sender)
		}
		if agentName != "" {
			parts = append(parts, "Agent: "+agentName)
		}
		loop.SetStickyContext(strings.Join(parts, "\n"))
	}

	loop.SetHandler(handler)

	// Wire handler and agent context to cloud_delegate tool.
	if ct, ok := baseReg.Get("cloud_delegate"); ok {
		if cdt, ok := ct.(*tools.CloudDelegateTool); ok {
			cdt.SetHandler(handler)
			if agentOverride != nil {
				cdt.SetAgentContext(agentName, agentOverride.Prompt)
			} else {
				cdt.SetAgentContext("", "")
			}
		}
	}

	if route != nil && route.injectCh != nil {
		loop.SetInjectCh(route.injectCh)
	}

	result, usage, runErr := loop.Run(ctx, prompt, history)
	if runErr != nil {
		return nil, fmt.Errorf("agent error for %s: %w", agentName, runErr)
	}

	// Ephemeral requests skip post-run persistence — the caller owns session lifecycle.
	if !req.Ephemeral {
		// Set title from first user message (named agents get a fixed title).
		if sess.Title == "New session" {
			if agentName != "" {
				sess.Title = session.AgentTitle(agentName)
			} else {
				sess.Title = session.Title(prompt)
			}
		}

		// Append the turn to the session and persist.
		source := req.Source
		if source == "" {
			source = "unknown"
		}
		// Skip user message append if already in sess.Messages from pre-loop path.
		if !preLoopUserAppended {
			sess.Messages = append(sess.Messages,
				client.Message{Role: "user", Content: client.NewTextContent(prompt)},
			)
			sess.MessageMeta = append(sess.MessageMeta,
				session.MessageMeta{Source: source, Timestamp: session.TimePtr(userMsgTime)},
			)
		}
		// Persist any messages injected mid-run (HITL follow-ups).
		for _, injMsg := range loop.InjectedMessages() {
			sess.Messages = append(sess.Messages,
				client.Message{Role: "user", Content: client.NewTextContent(injMsg)},
			)
			sess.MessageMeta = append(sess.MessageMeta,
				session.MessageMeta{Source: source, Timestamp: session.TimePtr(time.Now())},
			)
		}
		replyTime := time.Now()
		sess.Messages = append(sess.Messages,
			client.Message{Role: "assistant", Content: client.NewTextContent(result)},
		)
		sess.MessageMeta = append(sess.MessageMeta,
			session.MessageMeta{Source: source, Timestamp: session.TimePtr(replyTime)},
		)
		if err := sessMgr.Save(); err != nil {
			log.Printf("daemon: failed to save session: %v", err)
			if deps.EventBus != nil {
				payload, _ := json.Marshal(map[string]string{
					"agent": agentName,
					"error": fmt.Sprintf("session save failed: %v", err),
				})
				deps.EventBus.Emit(Event{Type: EventAgentError, Payload: payload})
			}
		}

		if deps.EventBus != nil {
			payload, _ := json.Marshal(map[string]any{
				"agent":      agentName,
				"source":     req.Source,
				"session_id": sess.ID,
				"text":       result,
			})
			deps.EventBus.Emit(Event{Type: EventAgentReply, Payload: payload})
		}
	}

	log.Printf("daemon: reply to %s (%d tokens, $%.4f)", agentName, usage.TotalTokens, usage.CostUSD)

	return &RunAgentResult{
		Reply:     result,
		SessionID: sess.ID,
		Agent:     agentName,
		Usage: RunAgentUsage{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			TotalTokens:  usage.TotalTokens,
			CostUSD:      usage.CostUSD,
		},
	}, nil
}

func generateMessageID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "msg-" + hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func closeRouteDone(done chan struct{}) {
	if done == nil {
		return
	}
	defer func() {
		if recover() != nil {
			// Best effort cleanup; callers may close defensively in multiple paths.
			// Avoid panic if the channel was already closed externally.
		}
	}()
	close(done)
}
