package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/agents"
	"github.com/Kocoro-lab/ShanClaw/internal/config"
	"github.com/Kocoro-lab/ShanClaw/internal/mcp"
	"github.com/Kocoro-lab/ShanClaw/internal/permissions"
	"github.com/Kocoro-lab/ShanClaw/internal/schedule"
	"github.com/Kocoro-lab/ShanClaw/internal/session"
	"github.com/Kocoro-lab/ShanClaw/internal/skills"
	"github.com/Kocoro-lab/ShanClaw/internal/tools"
	"gopkg.in/yaml.v3"
)

type Server struct {
	port                   int
	client                 *Client
	deps                   *ServerDeps
	server                 *http.Server
	listener               net.Listener
	version                string
	ctx                    context.Context    // daemon lifecycle context, set on Start
	cancel                 context.CancelFunc
	approvalBroker         *ApprovalBroker
	eventBus               *EventBus
	notifyApprovalResolved func(p ApprovalResolvedPayload) error
	// pendingBrokers maps requestID → per-request ApprovalBroker.
	// SSE handlers register here so POST /approval can find the right broker.
	pendingBrokers sync.Map // map[string]*ApprovalBroker
	onReload       func()   // called after config reload to restart watchers/heartbeat
}

func NewServer(port int, client *Client, deps *ServerDeps, version string) *Server {
	return &Server{
		port:                   port,
		client:                 client,
		deps:                   deps,
		version:                version,
		approvalBroker:         NewApprovalBroker(func(req ApprovalRequest) error { return nil }),
		eventBus:               NewEventBus(),
		notifyApprovalResolved: func(p ApprovalResolvedPayload) error { return nil },
	}
}

// SetApprovalResolvedNotifier sets the function called to notify Cloud when
// Ptfrog resolves an approval before the external channel does.
func (s *Server) SetApprovalResolvedNotifier(fn func(ApprovalResolvedPayload) error) {
	s.notifyApprovalResolved = fn
}

func (s *Server) Port() int {
	if s.listener != nil {
		return s.listener.Addr().(*net.TCPAddr).Port
	}
	return s.port
}

// SetCancelFunc sets a cancel function that handleShutdown will call to stop the daemon.
func (s *Server) SetCancelFunc(cancel context.CancelFunc) {
	s.cancel = cancel
}

// SetOnReload sets a callback invoked after config reload to restart watchers/heartbeat.
func (s *Server) SetOnReload(fn func()) {
	s.onReload = fn
}

func (s *Server) Start(ctx context.Context) error {
	s.ctx = ctx
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("GET /agents", s.handleAgents)
	mux.HandleFunc("GET /agents/{name}", s.handleGetAgent)
	mux.HandleFunc("POST /agents", s.handleCreateAgent)
	mux.HandleFunc("PUT /agents/{name}", s.handleUpdateAgent)
	mux.HandleFunc("DELETE /agents/{name}", s.handleDeleteAgent)
	mux.HandleFunc("PUT /agents/{name}/config", s.handlePutAgentConfig)
	mux.HandleFunc("DELETE /agents/{name}/config", s.handleDeleteAgentConfig)
	mux.HandleFunc("PUT /agents/{name}/commands/{cmd}", s.handlePutCommand)
	mux.HandleFunc("DELETE /agents/{name}/commands/{cmd}", s.handleDeleteCommand)
	mux.HandleFunc("PUT /agents/{name}/skills/{skill}", s.handlePutSkill)
	mux.HandleFunc("DELETE /agents/{name}/skills/{skill}", s.handleDeleteSkill)
	mux.HandleFunc("GET /skills/downloadable", s.handleListDownloadableSkills)
	mux.HandleFunc("POST /skills/install/{name}", s.handleInstallSkill)
	mux.HandleFunc("GET /skills", s.handleListSkills)
	mux.HandleFunc("GET /skills/{name}", s.handleGetSkill)
	mux.HandleFunc("PUT /skills/{name}", s.handlePutGlobalSkill)
	mux.HandleFunc("DELETE /skills/{name}", s.handleDeleteGlobalSkill)
	mux.HandleFunc("GET /skills/{name}/scripts", s.handleListSkillScripts)
	mux.HandleFunc("PUT /skills/{name}/scripts/{filename}", s.handlePutSkillScripts)
	mux.HandleFunc("DELETE /skills/{name}/scripts/{filename}", s.handleDeleteSkillScripts)
	mux.HandleFunc("GET /skills/{name}/references", s.handleListSkillReferences)
	mux.HandleFunc("PUT /skills/{name}/references/{filename}", s.handlePutSkillReferences)
	mux.HandleFunc("DELETE /skills/{name}/references/{filename}", s.handleDeleteSkillReferences)
	mux.HandleFunc("GET /skills/{name}/assets", s.handleListSkillAssets)
	mux.HandleFunc("PUT /skills/{name}/assets/{filename}", s.handlePutSkillAssets)
	mux.HandleFunc("DELETE /skills/{name}/assets/{filename}", s.handleDeleteSkillAssets)
	mux.HandleFunc("GET /schedules", s.handleListSchedules)
	mux.HandleFunc("GET /schedules/{id}", s.handleGetSchedule)
	mux.HandleFunc("POST /schedules", s.handleCreateSchedule)
	mux.HandleFunc("PATCH /schedules/{id}", s.handlePatchSchedule)
	mux.HandleFunc("DELETE /schedules/{id}", s.handleDeleteSchedule)
	mux.HandleFunc("GET /config", s.handleGetConfig)
	mux.HandleFunc("GET /config/status", s.handleConfigStatus)
	mux.HandleFunc("PATCH /config", s.handlePatchConfig)
	mux.HandleFunc("POST /config/reload", s.handleConfigReload)
	mux.HandleFunc("GET /instructions", s.handleGetInstructions)
	mux.HandleFunc("PUT /instructions", s.handlePutInstructions)
	mux.HandleFunc("GET /sessions", s.handleSessions)
	mux.HandleFunc("DELETE /sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("GET /sessions/search", s.handleSessionSearch)
	mux.HandleFunc("GET /permissions", s.handlePermissions)
	mux.HandleFunc("POST /permissions/request", s.handlePermissionsRequest)
	mux.HandleFunc("POST /approval", s.handleApproval)
	mux.HandleFunc("POST /message", s.handleMessage)
	mux.HandleFunc("POST /cancel", s.handleCancel)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("POST /shutdown", s.handleShutdown)

	ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", s.port))
	if err != nil {
		return fmt.Errorf("daemon server listen: %w", err)
	}
	s.listener = ln
	s.server = &http.Server{Handler: mux}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.server.Shutdown(shutCtx)
	}()

	if err := s.server.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "shutting_down"})
	if s.cancel != nil {
		log.Println("daemon: shutdown requested via /shutdown")
		go s.cancel()
	}
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RouteKey  string `json:"route_key,omitempty"`
		SessionID string `json:"session_id,omitempty"`
		Agent     string `json:"agent,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	key := req.RouteKey
	if key == "" && req.SessionID != "" {
		key = "session:" + sanitizeRouteValue(req.SessionID)
	}
	if key == "" && req.Agent != "" {
		key = "agent:" + sanitizeRouteValue(req.Agent)
	}
	if key == "" {
		http.Error(w, `{"error":"route_key, session_id, or agent required"}`, http.StatusBadRequest)
		return
	}

	s.deps.SessionCache.CancelRoute(key)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "cancelled", "route": key})
}

func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	var req ApprovalResponse
	if !decodeBody(w, r, &req) {
		return
	}
	if req.RequestID == "" {
		http.Error(w, `{"error":"request_id required"}`, http.StatusBadRequest)
		return
	}
	switch req.Decision {
	case DecisionAllow, DecisionDeny, DecisionAlwaysAllow:
	default:
		http.Error(w, `{"error":"decision must be allow, deny, or always_allow"}`, http.StatusBadRequest)
		return
	}
	// Notify Cloud and emit event BEFORE unblocking the agent.
	// This ensures ShanClaw dismisses the approval card before seeing the agent reply.
	_ = s.notifyApprovalResolved(ApprovalResolvedPayload{
		RequestID:  req.RequestID,
		Decision:   req.Decision,
		ResolvedBy: "shanclaw",
	})

	if s.eventBus != nil {
		payload, _ := json.Marshal(map[string]string{
			"request_id":  req.RequestID,
			"decision":    string(req.Decision),
			"resolved_by": "shanclaw",
		})
		s.eventBus.Emit(Event{Type: EventApprovalResolved, Payload: payload})
	}

	// Look up the per-request broker (SSE path) or fall back to server broker (WS path).
	if b, ok := s.pendingBrokers.Load(req.RequestID); ok {
		b.(*ApprovalBroker).Resolve(req.RequestID, req.Decision)
	} else {
		s.approvalBroker.Resolve(req.RequestID, req.Decision)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ch := s.eventBus.Subscribe()
	defer s.eventBus.Unsubscribe(ch)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case evt := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, string(evt.Payload))
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// EventBus returns the server's EventBus for emitting events.
func (s *Server) EventBus() *EventBus {
	return s.eventBus
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": s.version})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"is_connected": s.client.IsConnected(),
		"active_agent": s.client.ActiveAgent(),
		"uptime":       int(s.client.Uptime().Seconds()),
		"version":      s.version,
	})
}

// handleAgents lists available agents with optional memory status.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.deps == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"agents": []interface{}{}})
		return
	}

	names, err := agents.ListAgents(s.deps.AgentsDir)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	type agentInfo struct {
		Name         string `json:"name"`
		HasMemory    bool   `json:"has_memory"`
		HasConfig    bool   `json:"has_config"`
		CommandCount int    `json:"command_count"`
		SkillCount   int    `json:"skill_count"`
	}
	result := make([]agentInfo, 0, len(names))
	for _, name := range names {
		dir := filepath.Join(s.deps.AgentsDir, name)
		_, memErr := os.Stat(filepath.Join(dir, "MEMORY.md"))
		_, cfgErr := os.Stat(filepath.Join(dir, "config.yaml"))
		cmdFiles, _ := filepath.Glob(filepath.Join(dir, "commands", "*.md"))
		skillFiles, _ := filepath.Glob(filepath.Join(dir, "skills", "*", "SKILL.md"))
		result = append(result, agentInfo{
			Name:         name,
			HasMemory:    memErr == nil,
			HasConfig:    cfgErr == nil,
			CommandCount: len(cmdFiles),
			SkillCount:   len(skillFiles),
		})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"agents": result})
}

// handleSessions lists sessions, optionally filtered by agent.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.deps == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"sessions": []interface{}{}})
		return
	}

	agentName := r.URL.Query().Get("agent")
	if agentName != "" {
		if err := agents.ValidateAgentName(agentName); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
	}
	mgr := s.deps.SessionCache.GetOrCreateManager(s.deps.SessionCache.SessionsDir(agentName))
	summaries, err := mgr.List()
	if err != nil {
		// If the directory doesn't exist, return empty list.
		if os.IsNotExist(err) {
			json.NewEncoder(w).Encode(map[string]interface{}{"sessions": []interface{}{}})
			return
		}
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	// Filter out empty sessions (created but never used).
	filtered := make([]session.SessionSummary, 0, len(summaries))
	for _, s := range summaries {
		if s.MsgCount > 0 {
			filtered = append(filtered, s)
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"sessions": filtered})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if s.deps == nil {
		writeError(w, http.StatusInternalServerError, "daemon deps not configured")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}
	// Prevent path traversal — session IDs must be safe filenames.
	if id != filepath.Base(id) || strings.ContainsAny(id, `/\`) {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	agentName := r.URL.Query().Get("agent")
	if agentName != "" {
		if err := agents.ValidateAgentName(agentName); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	mgr := s.deps.SessionCache.GetOrCreateManager(s.deps.SessionCache.SessionsDir(agentName))
	if err := mgr.Delete(id); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("session %q not found", id))
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleSessionSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.deps == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"results": []interface{}{}})
		return
	}

	agentName := r.URL.Query().Get("agent")
	if agentName != "" {
		if err := agents.ValidateAgentName(agentName); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, `{"error":"q parameter required"}`, http.StatusBadRequest)
		return
	}

	mgr := s.deps.SessionCache.GetOrCreateManager(s.deps.SessionCache.SessionsDir(agentName))
	results, err := mgr.Search(query, 20)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []session.SearchResult{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"results": results})
}

// handleMessage runs an agent turn via POST. Supports synchronous JSON and SSE streaming.
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if s.deps == nil {
		http.Error(w, `{"error":"daemon deps not configured"}`, http.StatusInternalServerError)
		return
	}

	var req RunAgentRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Source == "" {
		req.Source = "shanclaw"
	}
	// Normalize "default" → "" early so downstream guards are consistent.
	if req.Agent == "default" {
		req.Agent = ""
	}
	// Named agents always resume their single long-lived session.
	// Clear new_session so clients cannot fork a named agent's context.
	if req.Agent != "" {
		req.NewSession = false
	}
	req.EnsureRouteKey()
	if err := req.Validate(); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	// Try injecting into an in-flight run on the same route.
	if req.RouteKey != "" {
		switch s.deps.SessionCache.InjectMessage(req.RouteKey, req.Text) {
		case InjectOK:
			if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				fmt.Fprintf(w, "event: injected\ndata: %s\n\n", req.RouteKey)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"status": "injected",
				"route":  req.RouteKey,
			})
			return
		case InjectQueueFull:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "rejected",
				"reason": "queue_full",
				"route":  req.RouteKey,
			})
			return
		case InjectNoActiveRun:
			// Fall through to start a new RunAgent
		}
	}

	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		s.handleMessageSSE(w, r, req)
		return
	}

	handler := &httpEventHandler{}
	result, err := RunAgent(r.Context(), s.deps, req, handler)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleMessageSSE streams agent events as SSE.
func (s *Server) handleMessageSSE(w http.ResponseWriter, r *http.Request, req RunAgentRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	// Create a per-request broker to avoid racing with concurrent SSE requests.
	// Each SSE stream gets its own broker with its own sendFn and pending map.
	reqBroker := NewApprovalBroker(func(areq ApprovalRequest) error {
		data := mustJSON(areq)
		_, err := fmt.Fprintf(w, "event: approval\ndata: %s\n\n", data)
		flusher.Flush()
		return err
	})
	// Inherit onRequest callback from the server broker for EventBus emission.
	reqBroker.onRequest = s.approvalBroker.onRequest
	// Register pending requestIDs so POST /approval can find this broker.
	reqBroker.onRegister = func(requestID string) { s.pendingBrokers.Store(requestID, reqBroker) }
	reqBroker.onDeregister = func(requestID string) { s.pendingBrokers.Delete(requestID) }

	// Cancel only this request's pending approvals when the SSE stream ends.
	defer reqBroker.CancelAll()

	// Resolve auto_approve: per-agent overrides global
	cfg, _, _ := s.deps.Snapshot()
	autoApprove := cfg.Daemon.AutoApprove
	if req.Agent != "" {
		if a, err := agents.LoadAgent(s.deps.AgentsDir, req.Agent); err == nil && a.Config != nil && a.Config.AutoApprove != nil {
			autoApprove = *a.Config.AutoApprove
		}
	}

	handler := &sseEventHandler{w: w, flusher: flusher, broker: reqBroker, ctx: r.Context(), autoApprove: autoApprove, deps: s.deps}
	result, err := RunAgent(r.Context(), s.deps, req, handler)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(map[string]string{"error": err.Error()}))
		flusher.Flush()
		return
	}

	fmt.Fprintf(w, "event: done\ndata: %s\n\n", mustJSON(result))
	flusher.Flush()
}

// handlePermissions returns current macOS TCC permission status.
func (s *Server) handlePermissions(w http.ResponseWriter, r *http.Request) {
	result := probePermissions(r.Context())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handlePermissionsRequest triggers macOS permission dialogs for the requested permission.
func (s *Server) handlePermissionsRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Permission string `json:"permission"` // "screen_recording", "accessibility", or "automation"
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	switch req.Permission {
	case "screen_recording", "accessibility", "automation":
		// valid
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "unsupported permission: " + req.Permission})
		return
	}
	result := requestPermission(r.Context(), req.Permission)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// httpEventHandler is an EventHandler for synchronous HTTP responses.
type httpEventHandler struct{}

func (h *httpEventHandler) OnToolCall(name string, args string) {}
func (h *httpEventHandler) OnToolResult(name string, args string, result agent.ToolResult, elapsed time.Duration) {
	log.Printf("http: tool %s completed (%.1fs)", name, elapsed.Seconds())
}
func (h *httpEventHandler) OnText(text string)            {}
func (h *httpEventHandler) OnStreamDelta(delta string)    {}
func (h *httpEventHandler) OnUsage(usage agent.TurnUsage) {}

// OnApprovalNeeded auto-approves for local HTTP API calls.
// Threat model: localhost-only, unauthenticated but local-trusted.
// Permission engine (hard-blocks, denied_commands) runs before this.
// If daemon ever listens on non-localhost, this MUST require auth.
func (h *httpEventHandler) OnCloudAgent(agentID, status, message string) {}
func (h *httpEventHandler) OnCloudProgress(completed, total int)         {}
func (h *httpEventHandler) OnCloudPlan(planType, content string, needsReview bool) {}

func (h *httpEventHandler) OnApprovalNeeded(tool string, args string) bool {
	return true
}

// sseEventHandler streams agent events as SSE to an HTTP response.
type sseEventHandler struct {
	w           http.ResponseWriter
	flusher     http.Flusher
	broker      *ApprovalBroker
	ctx         context.Context
	autoApprove bool
	deps        *ServerDeps
}

func (h *sseEventHandler) OnToolCall(name string, args string) {
	// SSE is request-scoped (one tool stream per HTTP request), so session_id
	// is intentionally omitted here; session correlation is handled at the client
	// session boundary.
	data := mustJSON(map[string]interface{}{"tool": name, "status": "running"})
	fmt.Fprintf(h.w, "event: tool\ndata: %s\n\n", data)
	h.flusher.Flush()
}

func (h *sseEventHandler) OnToolResult(name string, args string, result agent.ToolResult, elapsed time.Duration) {
	// SSE is request-scoped (one tool stream per HTTP request), so session_id
	// is intentionally omitted here; session correlation is handled at the client
	// session boundary.
	data := mustJSON(map[string]interface{}{
		"tool":    name,
		"status":  "completed",
		"elapsed": elapsed.Seconds(),
	})
	fmt.Fprintf(h.w, "event: tool\ndata: %s\n\n", data)
	h.flusher.Flush()
}

func (h *sseEventHandler) OnText(text string) {}

func (h *sseEventHandler) OnStreamDelta(delta string) {
	data := mustJSON(map[string]string{"text": delta})
	fmt.Fprintf(h.w, "event: delta\ndata: %s\n\n", data)
	h.flusher.Flush()
}

func (h *sseEventHandler) OnUsage(usage agent.TurnUsage) {}

func (h *sseEventHandler) OnCloudAgent(agentID, status, message string) {
	data, _ := json.Marshal(map[string]interface{}{
		"agent_id": agentID,
		"status":   status,
		"message":  message,
	})
	fmt.Fprintf(h.w, "event: %s\ndata: %s\n\n", EventCloudAgent, data)
	h.flusher.Flush()
}

func (h *sseEventHandler) OnCloudProgress(completed, total int) {
	data, _ := json.Marshal(map[string]interface{}{
		"completed": completed,
		"total":     total,
	})
	fmt.Fprintf(h.w, "event: %s\ndata: %s\n\n", EventCloudProgress, data)
	h.flusher.Flush()
}

func (h *sseEventHandler) OnCloudPlan(planType, content string, needsReview bool) {
	data, _ := json.Marshal(map[string]interface{}{
		"type":         planType,
		"content":      content,
		"needs_review": needsReview,
	})
	fmt.Fprintf(h.w, "event: %s\ndata: %s\n\n", EventCloudPlan, data)
	h.flusher.Flush()
}

// OnApprovalNeeded sends an approval request over SSE and blocks until the
// client responds via POST /approval or the request context is cancelled.
func (h *sseEventHandler) OnApprovalNeeded(tool string, args string) bool {
	if h.autoApprove {
		log.Printf("sse: auto-approving %s (auto_approve=true)", tool)
		return true
	}
	decision := h.broker.Request(h.ctx, "", "", "", tool, args)
	if decision == DecisionAlwaysAllow {
		if tool == "bash" {
			cmd := permissions.ExtractField(args, "command")
			if cmd != "" {
				if err := config.AppendAllowedCommand(h.deps.ShannonDir, cmd); err != nil {
					log.Printf("sse: failed to persist always-allow: %v", err)
				} else {
					h.deps.WriteLock()
					perms := &h.deps.Config.Permissions
					found := false
					for _, c := range perms.AllowedCommands {
						if c == cmd {
							found = true
							break
						}
					}
					if !found {
						perms.AllowedCommands = append(perms.AllowedCommands, cmd)
					}
					h.deps.WriteUnlock()
					log.Printf("sse: always-allow persisted: %s", cmd)
				}
			}
		} else {
			h.broker.SetToolAutoApprove(tool)
			log.Printf("sse: always-allow (session): %s", tool)
		}
	}
	return decision == DecisionAllow || decision == DecisionAlwaysAllow
}

// mustJSON marshals v to JSON, returning "{}" on error.
func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// isJSONNull checks if a json.RawMessage represents a JSON null value.
func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

const (
	maxBodySize   = 1 << 20 // 1 MB
	maxUploadSize = 10 << 20
)

var skillSubresourceFileRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)

// decodeBody reads a JSON request body with a size limit. Returns false and
// writes an error response if decoding fails.
func decodeBody(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid request body")
		}
		return false
	}
	return true
}

func (s *Server) skillSources() ([]skills.SkillSource, error) {
	if s.deps == nil {
		return nil, fmt.Errorf("daemon deps not configured")
	}
	global := skills.SkillSource{
		Dir:    filepath.Join(s.deps.ShannonDir, "skills"),
		Source: skills.SourceGlobal,
	}
	bundled, err := skills.BundledSkillSource(s.deps.ShannonDir)
	if err != nil {
		return nil, err
	}
	return []skills.SkillSource{
		global,
		bundled,
	}, nil
}

func (s *Server) resolveSkillDir(name string) (string, string, bool, error) {
	if s.deps == nil {
		return "", "", false, fmt.Errorf("daemon deps not configured")
	}
	globalDir := filepath.Join(s.deps.ShannonDir, "skills", name)
	if _, err := os.Stat(filepath.Join(globalDir, "SKILL.md")); err == nil {
		return globalDir, skills.SourceGlobal, false, nil
	}
	bundled, err := skills.BundledSkillSource(s.deps.ShannonDir)
	if err != nil {
		return "", "", false, err
	}
	bundledDir := filepath.Join(bundled.Dir, name)
	if _, err := os.Stat(filepath.Join(bundledDir, "SKILL.md")); err == nil {
		return bundledDir, skills.SourceBundled, true, nil
	}
	return "", "", false, os.ErrNotExist
}

func isValidSkillFileName(name string) bool {
	if len(name) == 0 || len(name) > 255 {
		return false
	}
	return filepath.Base(name) == name && skillSubresourceFileRE.MatchString(name)
}

func modeForSubresource(subdir string) os.FileMode {
	switch subdir {
	case "scripts":
		return 0755
	default:
		return 0644
	}
}

// configKeyAliases maps known camelCase/PascalCase JSON field names that legacy
// clients may send back to their canonical snake_case YAML equivalents.
var configKeyAliases = map[string]string{
	"apiKey":          "api_key",
	"APIKey":          "api_key",
	"modelTier":       "model_tier",
	"ModelTier":       "model_tier",
	"autoUpdateCheck": "auto_update_check",
	"AutoUpdateCheck": "auto_update_check",
	"mcpServers":      "mcp_servers",
	"MCPServers":      "mcp_servers",
}

// normalizePatchKeys rewrites known camelCase aliases to snake_case at the
// top level of m only. All aliases in configKeyAliases are top-level config
// keys; nested maps are intentionally not traversed to avoid false-positive
// renames of unrelated fields that share an alias name.
// When both an alias and its canonical key are present, the canonical wins
// and the alias is discarded.
func normalizePatchKeys(m map[string]interface{}) {
	if m == nil {
		return
	}
	for k := range m {
		canonical, aliased := configKeyAliases[k]
		if !aliased {
			continue
		}
		if _, canonicalExists := m[canonical]; !canonicalExists {
			m[canonical] = m[k]
		}
		delete(m, k)
	}
}

// stripRedactedSecrets removes "***" placeholder values from the known sensitive
// paths only: top-level api_key and mcp_servers.<name>.env.<var>. This prevents
// a GET→PATCH round-trip from overwriting real credentials with redacted values,
// without globally blocking the literal string "***" as a config value elsewhere.
func stripRedactedSecrets(m map[string]interface{}) {
	if m == nil {
		return
	}
	if s, ok := m["api_key"].(string); ok && s == "***" {
		delete(m, "api_key")
	}
	servers, ok := m["mcp_servers"].(map[string]interface{})
	if !ok {
		return
	}
	for _, srv := range servers {
		srvMap, ok := srv.(map[string]interface{})
		if !ok {
			continue
		}
		env, ok := srvMap["env"].(map[string]interface{})
		if !ok {
			continue
		}
		for k, v := range env {
			if s, ok := v.(string); ok && s == "***" {
				delete(env, k)
			}
		}
	}
}

// redactConfigSecrets removes sensitive values from a config map before
// sending it over the API. Redacts api_key at top level and env vars
// inside mcp_servers entries.
func redactConfigSecrets(m map[string]interface{}) {
	if m == nil {
		return
	}
	if _, ok := m["api_key"]; ok {
		m["api_key"] = "***"
	}
	servers, ok := m["mcp_servers"].(map[string]interface{})
	if !ok {
		return
	}
	for _, srv := range servers {
		srvMap, ok := srv.(map[string]interface{})
		if !ok {
			continue
		}
		if env, ok := srvMap["env"].(map[string]interface{}); ok {
			for k := range env {
				env[k] = "***"
			}
		}
	}
}

// deepMerge merges src into dst recursively (RFC 7386 JSON Merge Patch).
// null values delete keys, nested maps merge, scalars replace.
func deepMerge(dst, src map[string]interface{}) {
	for key, srcVal := range src {
		if srcVal == nil {
			delete(dst, key)
			continue
		}
		srcMap, srcIsMap := srcVal.(map[string]interface{})
		if srcIsMap {
			if dstVal, ok := dst[key]; ok {
				if dstMap, dstIsMap := dstVal.(map[string]interface{}); dstIsMap {
					deepMerge(dstMap, srcMap)
					continue
				}
			}
		}
		dst[key] = srcVal
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// --- Agent CRUD handlers ---

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := agents.ValidateAgentName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentDir := filepath.Join(s.deps.AgentsDir, name)
	if _, err := os.Stat(filepath.Join(agentDir, "AGENT.md")); os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("agent not found: %s", name))
		return
	}
	a, err := agents.LoadAgent(s.deps.AgentsDir, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to load agent %s: %v", name, err))
		return
	}
	writeJSON(w, http.StatusOK, a.ToAPI())
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var req agents.AgentCreateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Serialize creates for the same agent name to prevent concurrent rollback races.
	routeKey := "agent:" + req.Name
	s.deps.SessionCache.LockRoute(routeKey)
	defer s.deps.SessionCache.UnlockRoute(routeKey)

	agentDir := filepath.Join(s.deps.AgentsDir, req.Name)
	if _, err := os.Stat(filepath.Join(agentDir, "AGENT.md")); err == nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("agent %q already exists", req.Name))
		return
	}
	// Write all agent files — rollback on any failure.
	rollback := func() { agents.DeleteAgentDir(s.deps.AgentsDir, req.Name) }
	if err := agents.WriteAgentPrompt(s.deps.AgentsDir, req.Name, req.Prompt); err != nil {
		rollback()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Memory != nil {
		if err := agents.WriteAgentMemory(s.deps.AgentsDir, req.Name, *req.Memory); err != nil {
			rollback()
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("write memory: %v", err))
			return
		}
	}
	if req.Config != nil {
		if err := agents.WriteAgentConfig(s.deps.AgentsDir, req.Name, req.Config); err != nil {
			rollback()
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("write config: %v", err))
			return
		}
	}
	for name, content := range req.Commands {
		if err := agents.WriteAgentCommand(s.deps.AgentsDir, req.Name, name, content); err != nil {
			rollback()
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("write command %s: %v", name, err))
			return
		}
	}
	for _, skill := range req.Skills {
		if skill == nil {
			rollback()
			writeError(w, http.StatusBadRequest, "skill entry cannot be null")
			return
		}
		if err := agents.WriteAgentSkill(s.deps.AgentsDir, req.Name, skill); err != nil {
			rollback()
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("write skill %s: %v", skill.Name, err))
			return
		}
	}
	a, err := agents.LoadAgent(s.deps.AgentsDir, req.Name)
	if err != nil {
		rollback()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, a.ToAPI())
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := agents.ValidateAgentName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentDir := filepath.Join(s.deps.AgentsDir, name)
	if _, err := os.Stat(filepath.Join(agentDir, "AGENT.md")); os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("agent %q not found", name))
		return
	}
	var req agents.AgentUpdateRequest
	if !decodeBody(w, r, &req) {
		return
	}

	// --- Pre-validate all fields before any mutations ---
	if req.Prompt != nil && *req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt cannot be empty")
		return
	}
	var parsedMemory *string
	if req.Memory != nil && !isJSONNull(req.Memory) {
		var mem string
		if err := json.Unmarshal(req.Memory, &mem); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid memory value: %v", err))
			return
		}
		parsedMemory = &mem
	}
	var parsedConfig *agents.AgentConfigAPI
	if req.Config != nil && !isJSONNull(req.Config) {
		var cfg agents.AgentConfigAPI
		if err := json.Unmarshal(req.Config, &cfg); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid config value: %v", err))
			return
		}
		if cfg.Tools != nil {
			if err := agents.ValidateToolsFilter(cfg.Tools); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		parsedConfig = &cfg
	}
	for cmdName := range req.Commands {
		if err := agents.ValidateCommandName(cmdName); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	for _, skill := range req.Skills {
		if skill == nil {
			writeError(w, http.StatusBadRequest, "skill entry cannot be null")
			return
		}
		if err := skills.ValidateSkillName(skill.Name); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if skill.Description == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("skill %q requires a description", skill.Name))
			return
		}
	}

	// --- Apply mutations (all inputs validated) ---
	if req.Prompt != nil {
		if err := agents.WriteAgentPrompt(s.deps.AgentsDir, name, *req.Prompt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.Memory != nil {
		if isJSONNull(req.Memory) {
			if err := os.Remove(filepath.Join(agentDir, "MEMORY.md")); err != nil && !os.IsNotExist(err) {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("delete memory: %v", err))
				return
			}
		} else {
			if err := agents.WriteAgentMemory(s.deps.AgentsDir, name, *parsedMemory); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if req.Config != nil {
		if isJSONNull(req.Config) {
			if err := os.Remove(filepath.Join(agentDir, "config.yaml")); err != nil && !os.IsNotExist(err) {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("delete config: %v", err))
				return
			}
		} else {
			if err := agents.WriteAgentConfig(s.deps.AgentsDir, name, parsedConfig); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	for cmdName, content := range req.Commands {
		if err := agents.WriteAgentCommand(s.deps.AgentsDir, name, cmdName, content); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("write command %s: %v", cmdName, err))
			return
		}
	}
	for _, skill := range req.Skills {
		if skill == nil {
			writeError(w, http.StatusBadRequest, "skill entry cannot be null")
			return
		}
		if err := agents.WriteAgentSkill(s.deps.AgentsDir, name, skill); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("write skill %s: %v", skill.Name, err))
			return
		}
	}
	a, err := agents.LoadAgent(s.deps.AgentsDir, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.ToAPI())
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := agents.ValidateAgentName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentDir := filepath.Join(s.deps.AgentsDir, name)
	if _, err := os.Stat(filepath.Join(agentDir, "AGENT.md")); os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("agent %q not found", name))
		return
	}
	// Evict handles its own per-route locking — do NOT wrap with Lock/Unlock
	// (that would self-deadlock since Evict calls evictRoute which acquires entry.mu).
	s.deps.SessionCache.Evict(name)
	if err := agents.DeleteAgentDir(s.deps.AgentsDir, name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Agent sub-resource handlers ---

// agentExists checks that the agent directory has AGENT.md. Returns false
// and writes a 404 error if the agent does not exist.
func (s *Server) agentExists(w http.ResponseWriter, name string) bool {
	agentDir := filepath.Join(s.deps.AgentsDir, name)
	if _, err := os.Stat(filepath.Join(agentDir, "AGENT.md")); os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("agent %q not found", name))
		return false
	}
	return true
}

func (s *Server) handlePutAgentConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := agents.ValidateAgentName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.agentExists(w, name) {
		return
	}
	var cfg agents.AgentConfigAPI
	if !decodeBody(w, r, &cfg) {
		return
	}
	if cfg.Tools != nil {
		if err := agents.ValidateToolsFilter(cfg.Tools); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := agents.WriteAgentConfig(s.deps.AgentsDir, name, &cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteAgentConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := agents.ValidateAgentName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.agentExists(w, name) {
		return
	}
	path := filepath.Join(s.deps.AgentsDir, name, "config.yaml")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handlePutCommand(w http.ResponseWriter, r *http.Request) {
	agentName := r.PathValue("name")
	cmdName := r.PathValue("cmd")
	if err := agents.ValidateAgentName(agentName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.agentExists(w, agentName) {
		return
	}
	if err := agents.ValidateCommandName(cmdName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if err := agents.WriteAgentCommand(s.deps.AgentsDir, agentName, cmdName, body.Content); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteCommand(w http.ResponseWriter, r *http.Request) {
	agentName := r.PathValue("name")
	cmdName := r.PathValue("cmd")
	if err := agents.ValidateAgentName(agentName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.agentExists(w, agentName) {
		return
	}
	if err := agents.ValidateCommandName(cmdName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := agents.DeleteAgentCommand(s.deps.AgentsDir, agentName, cmdName); err != nil && !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handlePutSkill(w http.ResponseWriter, r *http.Request) {
	agentName := r.PathValue("name")
	skillName := r.PathValue("skill")
	if err := agents.ValidateAgentName(agentName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.agentExists(w, agentName) {
		return
	}
	if err := skills.ValidateSkillName(skillName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var skill skills.Skill
	if !decodeBody(w, r, &skill) {
		return
	}
	skill.Name = skillName // URL takes precedence
	if skill.Description == "" {
		writeError(w, http.StatusBadRequest, "description is required")
		return
	}
	if err := agents.WriteAgentSkill(s.deps.AgentsDir, agentName, &skill); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	agentName := r.PathValue("name")
	skillName := r.PathValue("skill")
	if err := agents.ValidateAgentName(agentName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.agentExists(w, agentName) {
		return
	}
	if err := skills.ValidateSkillName(skillName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := agents.DeleteAgentSkill(s.deps.AgentsDir, agentName, skillName); err != nil && !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Global skills handlers ---

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	sources, err := s.skillSources()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	list, err := skills.LoadSkills(sources...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	metas := make([]skills.SkillMeta, 0, len(list))
	for _, skill := range list {
		metas = append(metas, skill.ToMeta())
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"skills": metas})
}

func (s *Server) handleListDownloadableSkills(w http.ResponseWriter, r *http.Request) {
	globalDir := filepath.Join(s.deps.ShannonDir, "skills")
	result := make([]skills.DownloadableSkill, 0, len(skills.DownloadableSkills))
	for _, ds := range skills.DownloadableSkills {
		installed := false
		if _, err := os.Stat(filepath.Join(globalDir, ds.Name, "SKILL.md")); err == nil {
			installed = true
		}
		result = append(result, skills.DownloadableSkill{
			Name:        ds.Name,
			Description: ds.Description,
			Installed:   installed,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"skills": result})
}

func (s *Server) handleInstallSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !skills.IsDownloadable(name) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("skill %q is not available for download", name))
		return
	}

	if err := skills.InstallSkillFromRepo(s.deps.ShannonDir, name); err != nil {
		if strings.Contains(err.Error(), "already installed") {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Load the installed skill to return its metadata
	sources, _ := s.skillSources()
	list, _ := skills.LoadSkills(sources...)
	for _, skill := range list {
		if skill.Name == name {
			writeJSON(w, http.StatusCreated, skill.ToMeta())
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "installed", "name": name})
}

func (s *Server) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := skills.ValidateSkillName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	sources, err := s.skillSources()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	list, err := skills.LoadSkills(sources...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, skill := range list {
		if skill.Name == name {
			detail := skills.SkillDetail{
				Name:          skill.Name,
				Description:   skill.Description,
				Prompt:        skill.Prompt,
				Source:        skill.Source,
				License:       skill.License,
				Compatibility: skill.Compatibility,
				Metadata:      skill.Metadata,
			}
			if len(skill.AllowedTools) > 0 {
				detail.AllowedTools = skill.AllowedTools
			}
			writeJSON(w, http.StatusOK, detail)
			return
		}
	}
	writeError(w, http.StatusNotFound, fmt.Sprintf("skill %q not found", name))
}

func (s *Server) handlePutGlobalSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := skills.ValidateSkillName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
		License     string `json:"license"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Description == "" {
		writeError(w, http.StatusBadRequest, "description is required")
		return
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	if s.deps == nil {
		writeError(w, http.StatusInternalServerError, "daemon deps not configured")
		return
	}
	if err := skills.WriteGlobalSkill(s.deps.ShannonDir, &skills.Skill{
		Name:        name,
		Description: req.Description,
		Prompt:      req.Prompt,
		License:     req.License,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteGlobalSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := skills.ValidateSkillName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.deps == nil {
		writeError(w, http.StatusInternalServerError, "daemon deps not configured")
		return
	}
	globalDir := filepath.Join(s.deps.ShannonDir, "skills", name)
	skillFile := filepath.Join(globalDir, "SKILL.md")
	if _, err := os.Stat(skillFile); err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("skill %q not found in global directory", name))
		return
	}
	if err := skills.DeleteGlobalSkill(s.deps.ShannonDir, name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleListSkillScripts(w http.ResponseWriter, r *http.Request) {
	s.handleListSkillSubresource(w, r, "scripts")
}

func (s *Server) handlePutSkillScripts(w http.ResponseWriter, r *http.Request) {
	s.handlePutSkillSubresource(w, r, "scripts")
}

func (s *Server) handleDeleteSkillScripts(w http.ResponseWriter, r *http.Request) {
	s.handleDeleteSkillSubresource(w, r, "scripts")
}

func (s *Server) handleListSkillReferences(w http.ResponseWriter, r *http.Request) {
	s.handleListSkillSubresource(w, r, "references")
}

func (s *Server) handlePutSkillReferences(w http.ResponseWriter, r *http.Request) {
	s.handlePutSkillSubresource(w, r, "references")
}

func (s *Server) handleDeleteSkillReferences(w http.ResponseWriter, r *http.Request) {
	s.handleDeleteSkillSubresource(w, r, "references")
}

func (s *Server) handleListSkillAssets(w http.ResponseWriter, r *http.Request) {
	s.handleListSkillSubresource(w, r, "assets")
}

func (s *Server) handlePutSkillAssets(w http.ResponseWriter, r *http.Request) {
	s.handlePutSkillSubresource(w, r, "assets")
}

func (s *Server) handleDeleteSkillAssets(w http.ResponseWriter, r *http.Request) {
	s.handleDeleteSkillSubresource(w, r, "assets")
}

func (s *Server) handleListSkillSubresource(w http.ResponseWriter, r *http.Request, subdir string) {
	name := r.PathValue("name")
	if err := skills.ValidateSkillName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, _, _, err := s.resolveSkillDir(name)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("skill %q not found", name))
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	target := filepath.Join(dir, subdir)
	entries, err := os.ReadDir(target)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string][]string{"files": []string{}})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	writeJSON(w, http.StatusOK, map[string][]string{"files": files})
}

func (s *Server) handlePutSkillSubresource(w http.ResponseWriter, r *http.Request, subdir string) {
	name := r.PathValue("name")
	if err := skills.ValidateSkillName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, _, readOnly, err := s.resolveSkillDir(name)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("skill %q not found", name))
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if readOnly {
		writeError(w, http.StatusBadRequest, "bundled skill is read-only; create a global override first via PUT /skills/{name}")
		return
	}
	filename := r.PathValue("filename")
	if !isValidSkillFileName(filename) {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	targetDir := filepath.Join(dir, subdir)
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fileMode := modeForSubresource(subdir)
	tmp, err := os.CreateTemp(targetDir, ".skill-file-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Chmod(tmpPath, fileMode); err != nil {
		os.Remove(tmpPath)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dest := filepath.Join(targetDir, filename)
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteSkillSubresource(w http.ResponseWriter, r *http.Request, subdir string) {
	name := r.PathValue("name")
	if err := skills.ValidateSkillName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, _, readOnly, err := s.resolveSkillDir(name)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("skill %q not found", name))
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if readOnly {
		writeError(w, http.StatusBadRequest, "bundled skill is read-only; create a global override first via PUT /skills/{name}")
		return
	}
	filename := r.PathValue("filename")
	if !isValidSkillFileName(filename) {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	if err := os.Remove(filepath.Join(dir, subdir, filename)); err != nil && !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Schedule handlers ---

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	if s.deps == nil || s.deps.ScheduleManager == nil {
		writeError(w, http.StatusInternalServerError, "daemon deps not configured")
		return
	}
	list, err := s.deps.ScheduleManager.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []schedule.Schedule{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"schedules": list})
}

func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	if s.deps == nil || s.deps.ScheduleManager == nil {
		writeError(w, http.StatusInternalServerError, "daemon deps not configured")
		return
	}
	id := r.PathValue("id")
	sched, err := s.deps.ScheduleManager.Get(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sched)
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	if s.deps == nil || s.deps.ScheduleManager == nil {
		writeError(w, http.StatusInternalServerError, "daemon deps not configured")
		return
	}
	var req struct {
		Agent  string `json:"agent"`
		Cron   string `json:"cron"`
		Prompt string `json:"prompt"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	id, err := s.deps.ScheduleManager.Create(req.Agent, req.Cron, req.Prompt)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
		} else if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "prompt cannot be empty") {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	sched, err := s.deps.ScheduleManager.Get(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sched)
}

func (s *Server) handlePatchSchedule(w http.ResponseWriter, r *http.Request) {
	if s.deps == nil || s.deps.ScheduleManager == nil {
		writeError(w, http.StatusInternalServerError, "daemon deps not configured")
		return
	}
	id := r.PathValue("id")
	var patch struct {
		Cron    *string `json:"cron"`
		Prompt  *string `json:"prompt"`
		Enabled *bool   `json:"enabled"`
	}
	if !decodeBody(w, r, &patch) {
		return
	}
	if patch.Cron == nil && patch.Prompt == nil && patch.Enabled == nil {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}
	update := &schedule.UpdateOpts{
		Cron:    patch.Cron,
		Prompt:  patch.Prompt,
		Enabled: patch.Enabled,
	}
	if err := s.deps.ScheduleManager.Update(id, update); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if strings.Contains(err.Error(), "no fields to update") ||
			strings.Contains(err.Error(), "invalid") ||
			strings.Contains(err.Error(), "prompt cannot be empty") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sched, err := s.deps.ScheduleManager.Get(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sched)
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if s.deps == nil || s.deps.ScheduleManager == nil {
		writeError(w, http.StatusInternalServerError, "daemon deps not configured")
		return
	}
	id := r.PathValue("id")
	if err := s.deps.ScheduleManager.Remove(id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Global config + instructions handlers ---

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	globalPath := filepath.Join(s.deps.ShannonDir, "config.yaml")
	globalData, err := os.ReadFile(globalPath)
	var globalMap map[string]interface{}
	if err == nil {
		if yamlErr := yaml.Unmarshal(globalData, &globalMap); yamlErr != nil {
			log.Printf("daemon: GET /config: global config parse error: %v", yamlErr)
		}
	}

	cfg, _, _ := s.deps.Snapshot()
	effectiveJSON, _ := json.Marshal(cfg)
	var effectiveMap map[string]interface{}
	json.Unmarshal(effectiveJSON, &effectiveMap)

	// Redact secrets from both maps before responding
	redactConfigSecrets(globalMap)
	redactConfigSecrets(effectiveMap)

	// Collect unique source files from config merge
	var sources []string
	if cfg != nil && cfg.Sources != nil {
		seen := make(map[string]bool)
		for _, src := range cfg.Sources {
			if src.File != "" && !seen[src.File] {
				seen[src.File] = true
				sources = append(sources, src.File)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"global":    globalMap,
		"effective": effectiveMap,
		"sources":   sources,
	})
}

func (s *Server) handlePatchConfig(w http.ResponseWriter, r *http.Request) {
	var patch map[string]interface{}
	if !decodeBody(w, r, &patch) {
		return
	}

	globalPath := filepath.Join(s.deps.ShannonDir, "config.yaml")
	globalData, _ := os.ReadFile(globalPath)
	var current map[string]interface{}
	if len(globalData) > 0 {
		if err := yaml.Unmarshal(globalData, &current); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("existing config is corrupt: %v", err))
			return
		}
	}
	if current == nil {
		current = make(map[string]interface{})
	}

	normalizePatchKeys(patch)
	stripRedactedSecrets(patch)
	deepMerge(current, patch)

	data, err := yaml.Marshal(current)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := agents.AtomicWrite(globalPath, data); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleConfigStatus returns current MCP server status without restarting processes.
func (s *Server) handleConfigStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _, _ := s.deps.Snapshot()
	resp := make(map[string]interface{})

	if cfg != nil && len(cfg.MCPServers) > 0 {
		// Build set of live-connected server names from MCPManager.
		s.deps.mu.RLock()
		mgr := s.deps.MCPManager
		s.deps.mu.RUnlock()
		connected := make(map[string]bool)
		if mgr != nil {
			for _, name := range mgr.ConnectedServers() {
				connected[name] = true
			}
		}

		mcpStatus := make(map[string]string, len(cfg.MCPServers))
		for name, srv := range cfg.MCPServers {
			if srv.Disabled {
				mcpStatus[name] = "disabled"
			} else if mgr != nil && mgr.NeedsSetup(name) {
				mcpStatus[name] = "needs_setup"
			} else if connected[name] {
				mcpStatus[name] = "connected"
			} else {
				mcpStatus[name] = "enabled"
			}
		}
		resp["mcp_servers"] = mcpStatus
	}

	_, _, sup := s.deps.Snapshot()
	if sup != nil {
		healthData := make(map[string]interface{})
		for name, h := range sup.HealthStates() {
			entry := map[string]interface{}{
				"state":                h.State.String(),
				"since":                h.Since.Format(time.RFC3339),
				"consecutive_failures": h.ConsecutiveFailures,
			}
			if !h.LastTransportOK.IsZero() {
				entry["last_transport_ok"] = h.LastTransportOK.Format(time.RFC3339)
			}
			if !h.LastCapabilityOK.IsZero() {
				entry["last_capability_ok"] = h.LastCapabilityOK.Format(time.RFC3339)
			}
			if h.LastTransportError != "" {
				entry["last_transport_error"] = h.LastTransportError
			}
			if h.LastCapabilityError != "" {
				entry["last_capability_error"] = h.LastCapabilityError
			}
			healthData[name] = entry
		}
		if len(healthData) > 0 {
			resp["mcp_health"] = healthData
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// mcpConfigChanged returns true if MCP server configuration differs between old and new config.
func mcpConfigChanged(oldCfg, newCfg *config.Config) bool {
	if oldCfg == nil {
		return len(newCfg.MCPServers) > 0
	}
	if len(oldCfg.MCPServers) != len(newCfg.MCPServers) {
		return true
	}
	for name, oldSrv := range oldCfg.MCPServers {
		newSrv, ok := newCfg.MCPServers[name]
		if !ok {
			return true
		}
		if oldSrv.Command != newSrv.Command || oldSrv.Type != newSrv.Type ||
			oldSrv.URL != newSrv.URL || oldSrv.Disabled != newSrv.Disabled ||
			oldSrv.Context != newSrv.Context || !slices.Equal(oldSrv.Args, newSrv.Args) ||
			!maps.Equal(oldSrv.Env, newSrv.Env) {
			return true
		}
	}
	return false
}

func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
	oldCfg, _, _ := s.deps.Snapshot()

	newCfg, err := config.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("config load failed: %v", err))
		return
	}

	// Only re-register MCP servers if MCP config actually changed.
	// RegisterAll tears down and restarts all MCP processes (including
	// playwright-mcp which opens Chrome), so skip it when unnecessary.
	mcpChanged := mcpConfigChanged(oldCfg, newCfg)

	var regErr error
	if mcpChanged {
		var newReg *agent.ToolRegistry
		var newMCPMgr *mcp.ClientManager
		var newCleanup func()
		var newBaseline *agent.ToolRegistry
		newBaseline, newReg, _, newMCPMgr, newCleanup, regErr = tools.RegisterAllWithBaseline(s.deps.GW, newCfg)
		if regErr != nil {
			log.Printf("daemon: reload warning: %v", regErr)
		}
		tools.RegisterCloudDelegate(newReg, s.deps.GW, newCfg, nil, "", "")

		newGatewayOverlay := tools.ExtractGatewayTools(newReg)
		newPostOverlays := tools.ExtractPostOverlays(newReg, newBaseline)

		newSupervisor := mcp.NewSupervisor(newMCPMgr)
		newSupervisor.RegisterCapabilityProbe("playwright", &mcp.PlaywrightProbe{})
		newSupervisor.SetOnReconnect(func(ctx context.Context, serverName string) {
			if serverName == "playwright" {
				tools.CleanupPlaywrightReconnect(ctx, newMCPMgr)
			}
		})
		newSupervisor.SetOnChange(func(server string, oldState, newState mcp.HealthState) {
			_, _, depsSup := s.deps.Snapshot()
			if depsSup != newSupervisor {
				return
			}
			bl, gwOv, po, mgr := s.deps.RebuildLayers()
			rebuilt := tools.RebuildRegistryForHealth(bl, gwOv, po, newSupervisor.HealthStates(), mgr, newSupervisor)
			s.deps.WriteLock()
			s.deps.Registry = rebuilt
			s.deps.WriteUnlock()
			log.Printf("MCP registry rebuilt (reload): %d tools", len(rebuilt.All()))
		})

		s.deps.mu.Lock()
		oldCleanup := s.deps.Cleanup
		oldSupervisor := s.deps.Supervisor
		s.deps.Config = newCfg
		s.deps.Registry = newReg
		s.deps.MCPManager = newMCPMgr
		s.deps.Supervisor = newSupervisor
		s.deps.Cleanup = newCleanup
		s.deps.BaselineReg = newBaseline
		s.deps.GatewayOverlay = newGatewayOverlay
		s.deps.PostOverlays = newPostOverlays
		s.deps.mu.Unlock()

		if oldSupervisor != nil {
			oldSupervisor.Stop()
		}
		if oldCleanup != nil {
			oldCleanup()
		}

		newSupervisor.Start(s.ctx)

		// Force registry rebuild to attach supervisor to MCPTools (same
		// reason as initial startup — CompleteRegistration creates tools
		// before the supervisor exists).
		{
			bl, gwOv, po, mgr := s.deps.RebuildLayers()
			initReg := tools.RebuildRegistryForHealth(bl, gwOv, po, newSupervisor.HealthStates(), mgr, newSupervisor)
			s.deps.WriteLock()
			s.deps.Registry = initReg
			s.deps.WriteUnlock()
			log.Printf("MCP registry initialized with supervisor (reload): %d tools", len(initReg.All()))
		}
	} else {
		// Config changed but MCP servers didn't — update config and refresh
		// cached rebuild layers so health-driven rebuilds use current settings.
		newBaseline, _, newBaseCleanup := tools.RegisterLocalTools(newCfg)
		// Re-register gateway tools on top of fresh baseline clone.
		// Use a short timeout — if the gateway is unavailable, keep existing overlay.
		freshReg := newBaseline.Clone()
		gwCtx, gwCancel := context.WithTimeout(r.Context(), 5*time.Second)
		gwErr := tools.RegisterServerTools(gwCtx, s.deps.GW, freshReg)
		gwCancel()
		tools.RegisterCloudDelegate(freshReg, s.deps.GW, newCfg, nil, "", "")
		var newGatewayOverlay []agent.Tool
		if gwErr != nil {
			log.Printf("daemon: reload: gateway refresh failed, keeping existing overlay: %v", gwErr)
			s.deps.mu.RLock()
			newGatewayOverlay = s.deps.GatewayOverlay
			s.deps.mu.RUnlock()
		} else {
			newGatewayOverlay = tools.ExtractGatewayTools(freshReg)
		}
		newPostOverlays := tools.ExtractPostOverlays(freshReg, newBaseline)

		s.deps.mu.Lock()
		oldCleanup := s.deps.Cleanup
		s.deps.Config = newCfg
		s.deps.BaselineReg = newBaseline
		s.deps.GatewayOverlay = newGatewayOverlay
		s.deps.PostOverlays = newPostOverlays
		s.deps.Cleanup = func() { newBaseCleanup(); oldCleanup() }
		s.deps.mu.Unlock()
	}

	if s.onReload != nil {
		go s.onReload()
	}

	resp := map[string]interface{}{"status": "reloaded"}
	if oldCfg != nil && (oldCfg.Endpoint != newCfg.Endpoint || oldCfg.APIKey != newCfg.APIKey) {
		resp["restart_required"] = true
		resp["restart_reason"] = "endpoint or api_key changed — restart daemon to apply"
	}

	// MCP server status for UI indicators
	if len(newCfg.MCPServers) > 0 {
		// Build set of live-connected server names from MCPManager.
		s.deps.mu.RLock()
		mgr := s.deps.MCPManager
		s.deps.mu.RUnlock()
		connected := make(map[string]bool)
		if mgr != nil {
			for _, name := range mgr.ConnectedServers() {
				connected[name] = true
			}
		}

		mcpStatus := make(map[string]string, len(newCfg.MCPServers))
		for name, srv := range newCfg.MCPServers {
			if srv.Disabled {
				mcpStatus[name] = "disabled"
			} else if mgr != nil && mgr.NeedsSetup(name) {
				mcpStatus[name] = "needs_setup"
			} else if connected[name] {
				mcpStatus[name] = "connected"
			} else {
				mcpStatus[name] = "enabled"
			}
		}
		// Mark failed servers from registration error
		if regErr != nil {
			errMsg := regErr.Error()
			for name := range newCfg.MCPServers {
				if newCfg.MCPServers[name].Disabled {
					continue
				}
				if strings.Contains(errMsg, name+":") {
					mcpStatus[name] = "error"
				}
			}
		}
		resp["mcp_servers"] = mcpStatus
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetInstructions(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.deps.ShannonDir, "instructions.md")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"content": nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": string(data)})
}

func (s *Server) handlePutInstructions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content *string `json:"content"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	path := filepath.Join(s.deps.ShannonDir, "instructions.md")
	if body.Content == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if err := agents.AtomicWrite(path, []byte(*body.Content)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
