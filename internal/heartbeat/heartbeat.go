package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/agents"
	"github.com/Kocoro-lab/ShanClaw/internal/daemon"
	"github.com/Kocoro-lab/ShanClaw/internal/watcher"
)

const maxChecklistChars = 4000

// IsHeartbeatOK returns true if the agent reply is the silent ack token.
func IsHeartbeatOK(reply string) bool {
	return strings.EqualFold(strings.TrimSpace(reply), "HEARTBEAT_OK")
}

// FormatPrompt builds the heartbeat prompt from a checklist body.
func FormatPrompt(checklist string) string {
	return fmt.Sprintf(`This is a periodic heartbeat check. Review the checklist below and check each item using your available tools. If everything is fine, reply with exactly "HEARTBEAT_OK" and nothing else. If something needs attention, describe the issue concisely.

Checklist:
%s`, checklist)
}

// ReadChecklist reads HEARTBEAT.md at the given path.
// Missing file returns ("", nil) — this is the expected "disabled" state.
// Other read errors return ("", error) so callers can detect degraded monitoring.
// Content exceeding maxChecklistChars is truncated with a warning.
func ReadChecklist(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // missing = heartbeat disabled for this agent
		}
		return "", fmt.Errorf("read HEARTBEAT.md: %w", err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", nil
	}
	if len(content) > maxChecklistChars {
		log.Printf("heartbeat: HEARTBEAT.md at %s exceeds %d chars, truncating", path, maxChecklistChars)
		content = content[:maxChecklistChars]
	}
	return content, nil
}

// agentHeartbeat holds per-agent heartbeat state.
type agentHeartbeat struct {
	name            string
	interval        time.Duration
	activeHours     string
	model           string
	isolatedSession bool
	agentDir        string
	mu              sync.Mutex // overlap prevention
}

// Manager runs periodic heartbeat checks for all configured agents.
type Manager struct {
	agents []*agentHeartbeat
	deps   *daemon.ServerDeps
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a heartbeat Manager by scanning agents for heartbeat config.
// Returns an empty (but valid) Manager if no agents have heartbeat configured.
func New(agentsDir string, deps *daemon.ServerDeps) (*Manager, error) {
	names, err := agents.ListAgents(agentsDir)
	if err != nil {
		return nil, fmt.Errorf("heartbeat: list agents: %w", err)
	}

	var entries []*agentHeartbeat
	for _, name := range names {
		ag, err := agents.LoadAgent(agentsDir, name)
		if err != nil {
			log.Printf("heartbeat: skip agent %q: %v", name, err)
			continue
		}
		if ag.Config == nil || ag.Config.Heartbeat == nil || ag.Config.Heartbeat.Every == "" {
			continue
		}
		hb := ag.Config.Heartbeat

		interval, err := time.ParseDuration(hb.Every)
		if err != nil {
			log.Printf("heartbeat: skip agent %q: invalid interval %q: %v", name, hb.Every, err)
			continue
		}
		if interval < 1*time.Minute {
			log.Printf("heartbeat: skip agent %q: interval %s too short (min 1m)", name, interval)
			continue
		}

		entries = append(entries, &agentHeartbeat{
			name:            name,
			interval:        interval,
			activeHours:     hb.ActiveHours,
			model:           hb.Model,
			isolatedSession: hb.IsIsolatedSession(),
			agentDir:        filepath.Join(agentsDir, name),
		})
	}

	return &Manager{
		agents: entries,
		deps:   deps,
		done:   make(chan struct{}),
	}, nil
}

// Start launches per-agent ticker goroutines. Blocks until ctx is cancelled or Close is called.
func (m *Manager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)

	var wg sync.WaitGroup
	for _, ah := range m.agents {
		wg.Add(1)
		go func(ah *agentHeartbeat) {
			defer wg.Done()
			m.runTicker(ctx, ah)
		}(ah)
	}

	go func() {
		wg.Wait()
		close(m.done)
	}()
}

// runTicker runs the heartbeat ticker for a single agent.
func (m *Manager) runTicker(ctx context.Context, ah *agentHeartbeat) {
	ticker := time.NewTicker(ah.interval)
	defer ticker.Stop()

	log.Printf("heartbeat: started for agent %q every %s", ah.name, ah.interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx, ah)
		}
	}
}

// tick executes a single heartbeat check for an agent.
func (m *Manager) tick(ctx context.Context, ah *agentHeartbeat) {
	// Non-blocking overlap check.
	if !ah.mu.TryLock() {
		log.Printf("heartbeat: skip %q (previous tick still running)", ah.name)
		return
	}
	defer ah.mu.Unlock()

	if !watcher.InActiveHours(ah.activeHours, time.Now()) {
		log.Printf("heartbeat: skip %q (outside active hours)", ah.name)
		return
	}

	checklistPath := filepath.Join(ah.agentDir, "HEARTBEAT.md")
	checklist, err := ReadChecklist(checklistPath)
	if err != nil {
		log.Printf("heartbeat: skip %q: read checklist: %v", ah.name, err)
		return
	}
	if checklist == "" {
		log.Printf("heartbeat: skip %q (no checklist)", ah.name)
		return
	}

	prompt := FormatPrompt(checklist)
	req := daemon.RunAgentRequest{
		Agent:         ah.name,
		Source:        "heartbeat",
		Text:          prompt,
		NewSession:    ah.isolatedSession,
		Ephemeral:     true,
		ModelOverride: ah.model,
	}

	handler := &heartbeatHandler{}
	result, err := daemon.RunAgent(ctx, m.deps, req, handler)
	if err != nil {
		log.Printf("heartbeat: agent %q error: %v", ah.name, err)
		return
	}

	if IsHeartbeatOK(result.Reply) {
		log.Printf("heartbeat: agent %q OK", ah.name)
		return
	}

	log.Printf("heartbeat: agent %q alert: %s", ah.name, result.Reply)
	if m.deps.EventBus != nil {
		payload, _ := json.Marshal(map[string]string{
			"agent":      ah.name,
			"text":       result.Reply,
			"session_id": result.SessionID,
		})
		m.deps.EventBus.Emit(daemon.Event{
			Type:    daemon.EventHeartbeatAlert,
			Payload: payload,
		})
	}
}

// Close cancels all tickers and waits for goroutines to finish.
func (m *Manager) Close() {
	if m.cancel != nil {
		m.cancel()
	}
	<-m.done
}

// heartbeatHandler is a silent EventHandler for heartbeat agent runs.
type heartbeatHandler struct{}

func (h *heartbeatHandler) OnToolCall(name string, args string)                                      {}
func (h *heartbeatHandler) OnToolResult(name string, args string, result agent.ToolResult, elapsed time.Duration) {
}
func (h *heartbeatHandler) OnText(text string)                  {}
func (h *heartbeatHandler) OnStreamDelta(delta string)          {}
func (h *heartbeatHandler) OnUsage(usage agent.TurnUsage)       {}
func (h *heartbeatHandler) OnApprovalNeeded(tool string, args string) bool { return true }
