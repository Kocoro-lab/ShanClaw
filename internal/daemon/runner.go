package daemon

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/Kocoro-lab/shan/internal/agent"
	"github.com/Kocoro-lab/shan/internal/agents"
	"github.com/Kocoro-lab/shan/internal/audit"
	"github.com/Kocoro-lab/shan/internal/client"
	"github.com/Kocoro-lab/shan/internal/config"
	"github.com/Kocoro-lab/shan/internal/hooks"
	"github.com/Kocoro-lab/shan/internal/session"
	"github.com/Kocoro-lab/shan/internal/skills"
	"github.com/Kocoro-lab/shan/internal/tools"
)

// RunAgentRequest is the input for RunAgent.
type RunAgentRequest struct {
	Text      string `json:"text"`
	Agent     string `json:"agent,omitempty"`
	SessionID string `json:"session_id,omitempty"`
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
	mu           sync.RWMutex // guards Config, Registry, Cleanup during reload
	Config       *config.Config
	GW           *client.GatewayClient
	Registry     *agent.ToolRegistry
	Cleanup      func() // closes MCP connections; swapped on reload
	ShannonDir   string
	AgentsDir    string
	Auditor      *audit.AuditLogger
	HookRunner   *hooks.HookRunner
	SessionCache *SessionCache
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

// DaemonDeniedTools are tools that are not auto-approved in daemon mode.
// Currently empty — all tools are auto-approved since users explicitly
// request actions via chat. Schedule tools were previously denied here
// but that prevented legitimate use from Ptfrog.
var DaemonDeniedTools = map[string]bool{}

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
	explicitAgent := req.Agent != "" // explicitly requested, not parsed from @mention

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

	// Per-agent lock serializes concurrent messages to the same agent.
	deps.SessionCache.Lock(agentName)
	defer deps.SessionCache.Unlock(agentName)

	sessMgr := deps.SessionCache.GetOrCreate(agentName)
	if req.SessionID != "" {
		// Resume a specific session by ID (reuses cached manager to avoid DB handle leak).
		if _, err := sessMgr.Resume(req.SessionID); err != nil {
			return nil, fmt.Errorf("session not found: %s", req.SessionID)
		}
	}
	sess := sessMgr.Current()
	history := sess.Messages

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
		loadedSkills, _ = agents.LoadGlobalSkills(deps.ShannonDir)
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
		loop.SwitchAgent(agentOverride.Prompt, agentOverride.Memory, nil, scopedMCPCtx, loadedSkills)
	} else {
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

	result, usage, runErr := loop.Run(ctx, prompt, history)
	if runErr != nil {
		return nil, fmt.Errorf("agent error for %s: %w", agentName, runErr)
	}

	// Set title from first user message (named agents get a fixed title).
	if sess.Title == "New session" {
		if agentName != "" {
			sess.Title = session.AgentTitle(agentName)
		} else {
			sess.Title = session.Title(prompt)
		}
	}

	// Append the turn to the session and persist.
	sess.Messages = append(sess.Messages,
		client.Message{Role: "user", Content: client.NewTextContent(prompt)},
		client.Message{Role: "assistant", Content: client.NewTextContent(result)},
	)
	if err := sessMgr.Save(); err != nil {
		log.Printf("daemon: failed to save session: %v", err)
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
