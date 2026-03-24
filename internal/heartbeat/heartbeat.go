package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/agents"
	"github.com/Kocoro-lab/ShanClaw/internal/client"
	"github.com/Kocoro-lab/ShanClaw/internal/daemon"
	"github.com/Kocoro-lab/ShanClaw/internal/watcher"
)

const maxChecklistChars = 4000

// IsHeartbeatOK returns true if the agent reply is the silent ack token.
func IsHeartbeatOK(reply string) bool {
	return strings.EqualFold(strings.TrimSpace(reply), "HEARTBEAT_OK")
}

// IsHeartbeatOKFromMessages checks the last assistant message in a transcript.
func IsHeartbeatOKFromMessages(messages []client.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return strings.EqualFold(strings.TrimSpace(messages[i].Content.Text()), "HEARTBEAT_OK")
		}
	}
	return false
}

// FormatPrompt builds the heartbeat prompt from a checklist body.
func FormatPrompt(checklist string) string {
	return fmt.Sprintf(`This is a periodic heartbeat check. Review the checklist below and check each item using your available tools. If everything is fine, reply with exactly "HEARTBEAT_OK" and nothing else. If something needs attention, describe the issue concisely.

Checklist:
%s`, checklist)
}

// FormatGoalPrompt builds a goal-driven heartbeat prompt.
func FormatGoalPrompt(goals string) string {
	return fmt.Sprintf(`This is a periodic check-in. Review your goals below and your current conversation context. If something needs your attention, take action using your available tools. If nothing needs doing, reply with exactly "HEARTBEAT_OK" and nothing else.

Goals:
%s`, goals)
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
	goalDriven      bool
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

		ah := &agentHeartbeat{
			name:            name,
			interval:        interval,
			activeHours:     hb.ActiveHours,
			model:           hb.Model,
			isolatedSession: hb.IsIsolatedSession(),
			goalDriven:      true,
			agentDir:        filepath.Join(agentsDir, name),
		}
		if ah.goalDriven {
			ah.isolatedSession = false
		}
		entries = append(entries, ah)
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
	if err != nil || checklist == "" {
		log.Printf("heartbeat: skip %q (no goals/checklist)", ah.name)
		return
	}

	start := time.Now()

	if ah.goalDriven {
		m.tickGoalDriven(ctx, ah, checklist, start)
	} else {
		m.tickChecklist(ctx, ah, checklist, start)
	}
}

// tickGoalDriven runs a goal-driven heartbeat with session context.
func (m *Manager) tickGoalDriven(ctx context.Context, ah *agentHeartbeat, goals string, start time.Time) {
	routeKey := "agent:" + ah.name
	sessionsDir := filepath.Join(ah.agentDir, "sessions")

	sessionID, history, err := m.deps.SessionCache.ResolveLatestSession(routeKey, sessionsDir)
	if err != nil {
		log.Printf("heartbeat: %q skipped_no_session: %v", ah.name, err)
		return
	}

	prompt := FormatGoalPrompt(goals)
	collector := &TranscriptCollector{}

	req := daemon.RunAgentRequest{
		Agent:          ah.name,
		Source:         "heartbeat",
		Text:           prompt,
		Ephemeral:      true,
		BypassRouting:  true,
		ModelOverride:  ah.model,
		SessionHistory: history,
	}

	result, err := daemon.RunAgent(ctx, m.deps, req, collector)
	elapsed := time.Since(start).Milliseconds()

	if err != nil {
		log.Printf("heartbeat: %q error (session=%s, duration=%dms): %v", ah.name, sessionID, elapsed, err)
		m.emitAlert(ah.name, fmt.Sprintf("Heartbeat error: %v", err), "")
		return
	}

	if IsHeartbeatOKFromMessages(collector.Messages) {
		log.Printf("heartbeat: %q ok (session=%s, duration=%dms)", ah.name, sessionID, elapsed)
		return
	}

	// Prepend the heartbeat prompt as a user message so the full turn is persisted
	turn := append([]client.Message{{Role: "user", Content: client.NewTextContent(prompt)}}, collector.Messages...)
	appendErr := m.deps.SessionCache.AppendToSession(routeKey, sessionsDir, sessionID, turn)
	if appendErr != nil {
		if errors.Is(appendErr, daemon.ErrSessionChanged) {
			log.Printf("heartbeat: %q session_changed (session=%s, duration=%dms)", ah.name, sessionID, elapsed)
			m.emitAlert(ah.name, "Heartbeat completed but session changed — turn dropped", "")
		} else {
			log.Printf("heartbeat: %q append error (session=%s, duration=%dms): %v", ah.name, sessionID, elapsed, appendErr)
		}
		return
	}

	log.Printf("heartbeat: %q action (session=%s, duration=%dms): %s", ah.name, sessionID, elapsed, result.Reply)
	m.emitAlert(ah.name, result.Reply, sessionID)
}

// tickChecklist runs a legacy checklist-based heartbeat.
func (m *Manager) tickChecklist(ctx context.Context, ah *agentHeartbeat, checklist string, start time.Time) {
	prompt := FormatPrompt(checklist)
	req := daemon.RunAgentRequest{
		Agent:         ah.name,
		Source:        "heartbeat",
		Text:          prompt,
		NewSession:    ah.isolatedSession,
		Ephemeral:     true,
		ModelOverride: ah.model,
	}
	collector := &TranscriptCollector{}
	result, err := daemon.RunAgent(ctx, m.deps, req, collector)
	if err != nil {
		log.Printf("heartbeat: agent %q error: %v", ah.name, err)
		return
	}
	if IsHeartbeatOK(result.Reply) {
		log.Printf("heartbeat: agent %q OK", ah.name)
		return
	}
	log.Printf("heartbeat: agent %q alert: %s", ah.name, result.Reply)
	m.emitAlert(ah.name, result.Reply, result.SessionID)
}

// emitAlert sends a heartbeat alert event via the event bus.
func (m *Manager) emitAlert(agent, text, sessionID string) {
	if m.deps.EventBus == nil {
		return
	}
	payload, _ := json.Marshal(map[string]string{
		"agent":      agent,
		"text":       text,
		"session_id": sessionID,
	})
	m.deps.EventBus.Emit(daemon.Event{
		Type:    daemon.EventHeartbeatAlert,
		Payload: payload,
	})
}

// Close cancels all tickers and waits for goroutines to finish.
func (m *Manager) Close() {
	if m.cancel != nil {
		m.cancel()
	}
	<-m.done
}

// TranscriptCollector captures all messages during a run for post-run inspection.
type TranscriptCollector struct {
	Messages []client.Message
}

func (tc *TranscriptCollector) OnToolCall(name string, args string) {
	tc.Messages = append(tc.Messages, client.Message{Role: "assistant", Content: client.NewTextContent(fmt.Sprintf("[tool_call: %s]", name))})
}
func (tc *TranscriptCollector) OnToolResult(name string, args string, result agent.ToolResult, elapsed time.Duration) {
	tc.Messages = append(tc.Messages, client.Message{Role: "tool", Content: client.NewTextContent(result.Content)})
}
func (tc *TranscriptCollector) OnText(text string) {
	tc.Messages = append(tc.Messages, client.Message{Role: "assistant", Content: client.NewTextContent(text)})
}
func (tc *TranscriptCollector) OnStreamDelta(delta string)                            {}
func (tc *TranscriptCollector) OnUsage(usage agent.TurnUsage)                         {}
func (tc *TranscriptCollector) OnCloudAgent(agentID, status, message string)          {}
func (tc *TranscriptCollector) OnCloudProgress(completed, total int)                  {}
func (tc *TranscriptCollector) OnCloudPlan(planType, content string, needsReview bool) {}
func (tc *TranscriptCollector) OnApprovalNeeded(tool string, args string) bool { return true }
