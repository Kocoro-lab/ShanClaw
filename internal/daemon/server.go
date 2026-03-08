package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Kocoro-lab/shan/internal/agent"
	"github.com/Kocoro-lab/shan/internal/agents"
	"github.com/Kocoro-lab/shan/internal/session"
)

type Server struct {
	port     int
	client   *Client
	deps     *ServerDeps
	server   *http.Server
	listener net.Listener
}

func NewServer(port int, client *Client, deps *ServerDeps) *Server {
	return &Server{port: port, client: client, deps: deps}
}

func (s *Server) Port() int {
	if s.listener != nil {
		return s.listener.Addr().(*net.TCPAddr).Port
	}
	return s.port
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("GET /agents", s.handleAgents)
	mux.HandleFunc("GET /sessions", s.handleSessions)
	mux.HandleFunc("POST /message", s.handleMessage)

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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"is_connected": s.client.IsConnected(),
		"active_agent": s.client.ActiveAgent(),
		"uptime":       int(s.client.Uptime().Seconds()),
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
		Name      string `json:"name"`
		HasMemory bool   `json:"has_memory"`
	}
	result := make([]agentInfo, 0, len(names))
	for _, name := range names {
		memPath := filepath.Join(s.deps.AgentsDir, name, "MEMORY.md")
		_, statErr := os.Stat(memPath)
		result = append(result, agentInfo{Name: name, HasMemory: statErr == nil})
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
	sessDir := s.deps.SessionCache.SessionsDir(agentName)
	mgr := session.NewManager(sessDir)
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
	if summaries == nil {
		summaries = []session.SessionSummary{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"sessions": summaries})
}

// handleMessage runs an agent turn via POST. Supports synchronous JSON and SSE streaming.
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if s.deps == nil {
		http.Error(w, `{"error":"daemon deps not configured"}`, http.StatusInternalServerError)
		return
	}

	var req RunAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if err := req.Validate(); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	if r.Header.Get("Accept") == "text/event-stream" {
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
