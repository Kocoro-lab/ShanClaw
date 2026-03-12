package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Kocoro-lab/shan/internal/agent"
	"github.com/Kocoro-lab/shan/internal/agents"
	"github.com/Kocoro-lab/shan/internal/config"
	"github.com/Kocoro-lab/shan/internal/session"
	"github.com/Kocoro-lab/shan/internal/skills"
	"github.com/Kocoro-lab/shan/internal/tools"
	"gopkg.in/yaml.v3"
)

type Server struct {
	port     int
	client   *Client
	deps     *ServerDeps
	server   *http.Server
	listener net.Listener
	version  string
	cancel   context.CancelFunc
}

func NewServer(port int, client *Client, deps *ServerDeps, version string) *Server {
	return &Server{port: port, client: client, deps: deps, version: version}
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

func (s *Server) Start(ctx context.Context) error {
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
	mux.HandleFunc("GET /config", s.handleGetConfig)
	mux.HandleFunc("PATCH /config", s.handlePatchConfig)
	mux.HandleFunc("POST /config/reload", s.handleConfigReload)
	mux.HandleFunc("GET /instructions", s.handleGetInstructions)
	mux.HandleFunc("PUT /instructions", s.handlePutInstructions)
	mux.HandleFunc("GET /sessions", s.handleSessions)
	mux.HandleFunc("DELETE /sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("GET /sessions/search", s.handleSessionSearch)
	mux.HandleFunc("POST /message", s.handleMessage)
	mux.HandleFunc("POST /shutdown", s.handleShutdown)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
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
		skillFiles, _ := filepath.Glob(filepath.Join(dir, "skills", "*.yaml"))
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
	mgr := s.deps.SessionCache.GetOrCreate(agentName)
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
	mgr := s.deps.SessionCache.GetOrCreate(agentName)
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

	mgr := s.deps.SessionCache.GetOrCreate(agentName)
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
	if err := req.Validate(); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
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

	handler := &sseEventHandler{w: w, flusher: flusher}
	result, err := RunAgent(r.Context(), s.deps, req, handler)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(map[string]string{"error": err.Error()}))
		flusher.Flush()
		return
	}

	fmt.Fprintf(w, "event: done\ndata: %s\n\n", mustJSON(result))
	flusher.Flush()
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
func (h *httpEventHandler) OnApprovalNeeded(tool string, args string) bool {
	if DaemonDeniedTools[tool] {
		log.Printf("http: denied %s (not auto-approved in daemon mode)", tool)
		return false
	}
	return true
}

// sseEventHandler streams agent events as SSE to an HTTP response.
type sseEventHandler struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (h *sseEventHandler) OnToolCall(name string, args string) {
	data := mustJSON(map[string]interface{}{"tool": name, "status": "running"})
	fmt.Fprintf(h.w, "event: tool\ndata: %s\n\n", data)
	h.flusher.Flush()
}

func (h *sseEventHandler) OnToolResult(name string, args string, result agent.ToolResult, elapsed time.Duration) {
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

func (h *sseEventHandler) OnApprovalNeeded(tool string, args string) bool {
	if DaemonDeniedTools[tool] {
		return false
	}
	return true
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

const maxBodySize = 1 << 20 // 1 MB

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
	s.deps.SessionCache.Lock(req.Name)
	defer s.deps.SessionCache.Unlock(req.Name)

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
	s.deps.SessionCache.Lock(name)
	s.deps.SessionCache.Evict(name)
	s.deps.SessionCache.Unlock(name)
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
	if err := agents.ValidateCommandName(skillName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := agents.DeleteAgentSkill(s.deps.AgentsDir, agentName, skillName); err != nil && !os.IsNotExist(err) {
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

	cfg, _ := s.deps.Snapshot()
	effectiveJSON, _ := json.Marshal(cfg)
	var effectiveMap map[string]interface{}
	json.Unmarshal(effectiveJSON, &effectiveMap)

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

func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
	oldCfg, _ := s.deps.Snapshot()

	newCfg, err := config.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("config load failed: %v", err))
		return
	}

	newReg, _, newCleanup, regErr := tools.RegisterAll(s.deps.GW, newCfg)
	if regErr != nil {
		log.Printf("daemon: reload warning: %v", regErr)
	}
	tools.RegisterCloudDelegate(newReg, s.deps.GW, newCfg, nil, "", "")

	s.deps.mu.Lock()
	oldCleanup := s.deps.Cleanup
	s.deps.Config = newCfg
	s.deps.Registry = newReg
	s.deps.Cleanup = newCleanup
	s.deps.mu.Unlock()

	if oldCleanup != nil {
		oldCleanup()
	}

	resp := map[string]interface{}{"status": "reloaded"}
	if oldCfg != nil && (oldCfg.Endpoint != newCfg.Endpoint || oldCfg.APIKey != newCfg.APIKey) {
		resp["restart_required"] = true
		resp["restart_reason"] = "endpoint or api_key changed — restart daemon to apply"
	}

	// MCP server status for UI indicators
	if len(newCfg.MCPServers) > 0 {
		mcpStatus := make(map[string]string, len(newCfg.MCPServers))
		for name, srv := range newCfg.MCPServers {
			if srv.Disabled {
				mcpStatus[name] = "disabled"
			} else {
				mcpStatus[name] = "connected"
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
