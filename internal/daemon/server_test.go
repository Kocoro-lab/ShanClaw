package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/config"
	"gopkg.in/yaml.v3"
)

func TestServer_Health(t *testing.T) {
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, nil, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", srv.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("body = %v", body)
	}
	if body["version"] != "test" {
		t.Errorf("version = %q, want %q", body["version"], "test")
	}
}

func TestServer_Status(t *testing.T) {
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, nil, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/status", srv.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body struct {
		IsConnected bool   `json:"is_connected"`
		ActiveAgent string `json:"active_agent"`
		Uptime      int    `json:"uptime"`
		Version     string `json:"version"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if body.IsConnected {
		t.Error("should not be connected")
	}
	if body.Uptime < 0 {
		t.Error("uptime should be non-negative")
	}
	if body.Version != "test" {
		t.Errorf("version = %q, want %q", body.Version, "test")
	}
}

func TestServer_Shutdown(t *testing.T) {
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, nil, "test")
	ctx, cancel := context.WithCancel(context.Background())

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	cancel()
	time.Sleep(200 * time.Millisecond)

	_, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", srv.Port()))
	if err == nil {
		t.Error("expected connection refused after shutdown")
	}
}

func TestServer_Agents_Empty(t *testing.T) {
	agentsDir := t.TempDir()
	sessDir := t.TempDir()
	deps := &ServerDeps{
		AgentsDir:    agentsDir,
		SessionCache: NewSessionCache(sessDir),
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/agents", srv.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]json.RawMessage
	json.Unmarshal(body, &parsed)
	if string(parsed["agents"]) != "[]" {
		t.Errorf("expected empty agents array, got %s", string(body))
	}
}

func TestServer_Sessions_Empty(t *testing.T) {
	sessDir := t.TempDir()
	deps := &ServerDeps{
		SessionCache: NewSessionCache(sessDir),
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/sessions", srv.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]json.RawMessage
	json.Unmarshal(body, &parsed)
	if string(parsed["sessions"]) != "[]" {
		t.Errorf("expected empty sessions array, got %s", string(body))
	}
}

func TestServer_Message_MissingText(t *testing.T) {
	deps := &ServerDeps{}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/message", srv.Port()),
		"application/json",
		strings.NewReader(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestServer_Message_AgentNotFound(t *testing.T) {
	sessDir := t.TempDir()
	deps := &ServerDeps{
		Config:       &config.Config{},
		AgentsDir:    t.TempDir(),
		SessionCache: NewSessionCache(sessDir),
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/message", srv.Port()),
		"application/json",
		strings.NewReader(`{"text":"hello","agent":"nonexistent"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Agent falls back to default when not found, but RunAgent will fail
	// because deps are incomplete (no gateway, registry). 500 is expected.
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "error") {
		t.Errorf("expected error in body, got %s", string(body))
	}
}

// --- Issue 1: rollback on create failure ---

func TestServer_CreateAgent_Conflict(t *testing.T) {
	agentsDir := t.TempDir()
	sessDir := t.TempDir()
	deps := &ServerDeps{
		AgentsDir:    agentsDir,
		ShannonDir:   t.TempDir(),
		SessionCache: NewSessionCache(sessDir),
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	body := `{"name":"testbot","prompt":"hello world"}`
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/agents", srv.Port()),
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", resp.StatusCode)
	}

	// Duplicate create — should get 409
	resp2, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/agents", srv.Port()),
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("duplicate: expected 409, got %d", resp2.StatusCode)
	}
}

func TestServer_CreateAgent_RollbackOnWriteFailure(t *testing.T) {
	agentsDir := t.TempDir()
	sessDir := t.TempDir()
	deps := &ServerDeps{
		AgentsDir:    agentsDir,
		ShannonDir:   t.TempDir(),
		SessionCache: NewSessionCache(sessDir),
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// Make agents dir read-only so WriteAgentPrompt's MkdirAll fails
	os.Chmod(agentsDir, 0500)
	defer os.Chmod(agentsDir, 0700) // restore for cleanup

	body := `{"name":"failbot","prompt":"should fail"}`
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/agents", srv.Port()),
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}

	// Restore permissions and verify no orphaned directory
	os.Chmod(agentsDir, 0700)
	if _, err := os.Stat(filepath.Join(agentsDir, "failbot")); !os.IsNotExist(err) {
		t.Error("agent dir should not exist after rollback")
	}
}

func TestServer_CreateAgent_DoesNotCreateSessionManager(t *testing.T) {
	agentsDir := t.TempDir()
	sessDir := t.TempDir()
	sessionCache := NewSessionCache(sessDir)
	deps := &ServerDeps{
		AgentsDir:    agentsDir,
		ShannonDir:   t.TempDir(),
		SessionCache: sessionCache,
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	body := `{"name":"cache-test","prompt":"hello world"}`
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/agents", srv.Port()),
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", resp.StatusCode)
	}

	sessionCache.mu.Lock()
	route, ok := sessionCache.routes["agent:cache-test"]
	sessionCache.mu.Unlock()
	if !ok {
		t.Fatalf("expected route cache entry for agent:cache-test to exist")
	}
	if route.manager != nil {
		t.Fatalf("expected create path to avoid creating a route manager")
	}
}

// --- deepMerge unit tests ---

func TestDeepMerge(t *testing.T) {
	tests := []struct {
		name     string
		dst, src map[string]interface{}
		want     map[string]interface{}
	}{
		{
			name: "scalar replace",
			dst:  map[string]interface{}{"a": "old"},
			src:  map[string]interface{}{"a": "new"},
			want: map[string]interface{}{"a": "new"},
		},
		{
			name: "null deletes key",
			dst:  map[string]interface{}{"a": "val", "b": "keep"},
			src:  map[string]interface{}{"a": nil},
			want: map[string]interface{}{"b": "keep"},
		},
		{
			name: "nested merge preserves siblings",
			dst: map[string]interface{}{
				"agent": map[string]interface{}{"model": "old", "temp": 0.7},
			},
			src: map[string]interface{}{
				"agent": map[string]interface{}{"model": "new"},
			},
			want: map[string]interface{}{
				"agent": map[string]interface{}{"model": "new", "temp": 0.7},
			},
		},
		{
			name: "3-level deep merge",
			dst: map[string]interface{}{
				"a": map[string]interface{}{
					"b": map[string]interface{}{"c": 1, "d": 2},
				},
			},
			src: map[string]interface{}{
				"a": map[string]interface{}{
					"b": map[string]interface{}{"c": 99},
				},
			},
			want: map[string]interface{}{
				"a": map[string]interface{}{
					"b": map[string]interface{}{"c": 99, "d": 2},
				},
			},
		},
		{
			name: "src map replaces dst scalar",
			dst:  map[string]interface{}{"a": "scalar"},
			src:  map[string]interface{}{"a": map[string]interface{}{"nested": true}},
			want: map[string]interface{}{"a": map[string]interface{}{"nested": true}},
		},
		{
			name: "src scalar replaces dst map",
			dst:  map[string]interface{}{"a": map[string]interface{}{"nested": true}},
			src:  map[string]interface{}{"a": "scalar"},
			want: map[string]interface{}{"a": "scalar"},
		},
		{
			name: "new key added",
			dst:  map[string]interface{}{"a": 1},
			src:  map[string]interface{}{"b": 2},
			want: map[string]interface{}{"a": 1, "b": 2},
		},
		{
			name: "empty src is no-op",
			dst:  map[string]interface{}{"a": 1},
			src:  map[string]interface{}{},
			want: map[string]interface{}{"a": 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deepMerge(tc.dst, tc.src)
			gotJSON, _ := json.Marshal(tc.dst)
			wantJSON, _ := json.Marshal(tc.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

// --- Issue 2: PATCH config deep merge ---

func TestServer_PatchConfig_DeepMerge(t *testing.T) {
	shannonDir := t.TempDir()
	sessDir := t.TempDir()
	deps := &ServerDeps{
		ShannonDir:   shannonDir,
		SessionCache: NewSessionCache(sessDir),
		Config:       &config.Config{},
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	base := fmt.Sprintf("http://127.0.0.1:%d", srv.Port())

	// Step 1: Set initial config with nested agent block
	initial := map[string]interface{}{
		"agent": map[string]interface{}{
			"model":          "claude-3-5-sonnet",
			"max_iterations": 10,
			"temperature":    0.7,
		},
		"top_level_key": "keep_me",
	}
	initialYAML, _ := yaml.Marshal(initial)
	os.WriteFile(filepath.Join(shannonDir, "config.yaml"), initialYAML, 0600)

	// Step 2: PATCH only agent.model — should preserve max_iterations and temperature
	patch := `{"agent": {"model": "claude-4-opus"}}`
	req, _ := http.NewRequest("PATCH", base+"/config", strings.NewReader(patch))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH: expected 200, got %d", resp.StatusCode)
	}

	// Step 3: Read config back and verify deep merge
	data, err := os.ReadFile(filepath.Join(shannonDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]interface{}
	if err := yaml.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	agentBlock, ok := result["agent"].(map[string]interface{})
	if !ok {
		t.Fatalf("agent block not a map: %T", result["agent"])
	}

	// model should be updated
	if agentBlock["model"] != "claude-4-opus" {
		t.Errorf("model = %v, want claude-4-opus", agentBlock["model"])
	}

	// max_iterations and temperature should be preserved (deep merge)
	if agentBlock["max_iterations"] == nil {
		t.Error("max_iterations was lost during PATCH — shallow merge instead of deep merge")
	}
	if agentBlock["temperature"] == nil {
		t.Error("temperature was lost during PATCH — shallow merge instead of deep merge")
	}

	// top_level_key should still be there
	if result["top_level_key"] != "keep_me" {
		t.Errorf("top_level_key = %v, want keep_me", result["top_level_key"])
	}
}

func TestServer_PatchConfig_NullDeletes(t *testing.T) {
	shannonDir := t.TempDir()
	sessDir := t.TempDir()
	deps := &ServerDeps{
		ShannonDir:   shannonDir,
		SessionCache: NewSessionCache(sessDir),
		Config:       &config.Config{},
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	base := fmt.Sprintf("http://127.0.0.1:%d", srv.Port())

	// Set initial config
	initial := map[string]interface{}{
		"agent":    map[string]interface{}{"model": "gpt-4"},
		"to_delete": "bye",
	}
	initialYAML, _ := yaml.Marshal(initial)
	os.WriteFile(filepath.Join(shannonDir, "config.yaml"), initialYAML, 0600)

	// PATCH with null to delete a key
	patch := `{"to_delete": null}`
	req, _ := http.NewRequest("PATCH", base+"/config", strings.NewReader(patch))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH: expected 200, got %d", resp.StatusCode)
	}

	data, _ := os.ReadFile(filepath.Join(shannonDir, "config.yaml"))
	var result map[string]interface{}
	yaml.Unmarshal(data, &result)

	if _, exists := result["to_delete"]; exists {
		t.Error("to_delete should have been removed by null patch")
	}
	if result["agent"] == nil {
		t.Error("agent block should still exist")
	}
}

// --- Issue 3: request body size limit ---

func TestServer_BodySizeLimit(t *testing.T) {
	agentsDir := t.TempDir()
	sessDir := t.TempDir()
	deps := &ServerDeps{
		AgentsDir:    agentsDir,
		ShannonDir:   t.TempDir(),
		SessionCache: NewSessionCache(sessDir),
		Config:       &config.Config{},
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	base := fmt.Sprintf("http://127.0.0.1:%d", srv.Port())

	// Send a 2MB body to POST /agents — should be rejected
	bigBody := bytes.Repeat([]byte("x"), 2*1024*1024)
	payload := append([]byte(`{"name":"big","prompt":"`), bigBody...)
	payload = append(payload, '"', '}')

	resp, err := http.Post(base+"/agents", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Should get 413 or 400 (body too large), not 201
	if resp.StatusCode == http.StatusCreated {
		t.Error("expected rejection for 2MB body, got 201 Created")
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Logf("status = %d (acceptable if 400, ideal is 413)", resp.StatusCode)
	}
}

func TestEventsSSEEndpoint(t *testing.T) {
	bus := NewEventBus()
	s := &Server{eventBus: bus}

	handler := http.HandlerFunc(s.handleEvents)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %s", resp.Header.Get("Content-Type"))
	}

	// Wait for SSE handler to subscribe before emitting
	time.Sleep(50 * time.Millisecond)

	bus.Emit(Event{
		Type:    EventAgentReply,
		Payload: json.RawMessage(`{"agent":"test","text":"hello"}`),
	})

	scanner := bufio.NewScanner(resp.Body)
	var eventLine, dataLine string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			eventLine = line
		}
		if strings.HasPrefix(line, "data:") {
			dataLine = line
			break
		}
	}

	if eventLine != "event: agent_reply" {
		t.Fatalf("expected 'event: agent_reply', got %q", eventLine)
	}
	if !strings.Contains(dataLine, `"agent":"test"`) {
		t.Fatalf("expected agent in data, got %q", dataLine)
	}
}

func TestNormalizePatchKeys(t *testing.T) {
	tests := []struct {
		name       string
		input      map[string]interface{}
		want       map[string]interface{}
		applyTwice bool // set to verify idempotency
	}{
		{
			name:  "camelCase mcpServers renamed",
			input: map[string]interface{}{"mcpServers": map[string]interface{}{"x-twitter": map[string]interface{}{}}},
			want:  map[string]interface{}{"mcp_servers": map[string]interface{}{"x-twitter": map[string]interface{}{}}},
		},
		{
			name:  "PascalCase MCPServers renamed",
			input: map[string]interface{}{"MCPServers": map[string]interface{}{}},
			want:  map[string]interface{}{"mcp_servers": map[string]interface{}{}},
		},
		{
			name:  "apiKey renamed",
			input: map[string]interface{}{"apiKey": "sk_abc"},
			want:  map[string]interface{}{"api_key": "sk_abc"},
		},
		{
			name:  "canonical snake_case unchanged",
			input: map[string]interface{}{"mcp_servers": map[string]interface{}{}, "api_key": "sk_abc"},
			want:  map[string]interface{}{"mcp_servers": map[string]interface{}{}, "api_key": "sk_abc"},
		},
		{
			name:       "idempotent: applying twice gives same result",
			input:      map[string]interface{}{"mcpServers": map[string]interface{}{"s": map[string]interface{}{}}},
			want:       map[string]interface{}{"mcp_servers": map[string]interface{}{"s": map[string]interface{}{}}},
			applyTwice: true,
		},
		{
			name:  "alias + canonical both present: canonical wins, alias discarded",
			input: map[string]interface{}{"mcpServers": map[string]interface{}{"alias": map[string]interface{}{}}, "mcp_servers": map[string]interface{}{"canonical": map[string]interface{}{}}},
			want:  map[string]interface{}{"mcp_servers": map[string]interface{}{"canonical": map[string]interface{}{}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalizePatchKeys(tt.input)
			if tt.applyTwice {
				normalizePatchKeys(tt.input)
			}
			if len(tt.input) != len(tt.want) {
				t.Fatalf("key count mismatch: got %v, want %v", tt.input, tt.want)
			}
			for k := range tt.want {
				if _, ok := tt.input[k]; !ok {
					t.Errorf("missing expected key %q in result %v", k, tt.input)
				}
			}
			for k := range tt.input {
				if _, ok := tt.want[k]; !ok {
					t.Errorf("unexpected key %q in result %v", k, tt.input)
				}
			}
		})
	}
}

func TestStripRedactedSecrets(t *testing.T) {
	tests := []struct {
		name            string
		input           map[string]interface{}
		wantDeleted     []string // top-level keys that should be absent
		wantKept        []string // top-level keys that should still be present
		wantEnvDeleted  []string // mcp_servers.x-twitter.env keys that should be absent
		wantEnvKept     []string // mcp_servers.x-twitter.env keys that should still be present
	}{
		{
			name:        "api_key *** is dropped",
			input:       map[string]interface{}{"api_key": "***"},
			wantDeleted: []string{"api_key"},
		},
		{
			name:     "api_key real value is kept",
			input:    map[string]interface{}{"api_key": "sk_real"},
			wantKept: []string{"api_key"},
		},
		{
			name: "mcp env *** dropped, real kept",
			input: map[string]interface{}{
				"mcp_servers": map[string]interface{}{
					"x-twitter": map[string]interface{}{
						"env": map[string]interface{}{
							"ACCESS_TOKEN":  "***",
							"ACCESS_TOKEN2": "realvalue",
						},
					},
				},
			},
			wantEnvDeleted: []string{"ACCESS_TOKEN"},
			wantEnvKept:    []string{"ACCESS_TOKEN2"},
		},
		{
			name:     "literal *** in non-sensitive field is kept",
			input:    map[string]interface{}{"model_tier": "***"},
			wantKept: []string{"model_tier"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripRedactedSecrets(tt.input)
			for _, k := range tt.wantDeleted {
				if _, ok := tt.input[k]; ok {
					t.Errorf("expected key %q to be deleted, still present", k)
				}
			}
			for _, k := range tt.wantKept {
				if _, ok := tt.input[k]; !ok {
					t.Errorf("expected key %q to be kept, was deleted", k)
				}
			}
			if len(tt.wantEnvDeleted) > 0 || len(tt.wantEnvKept) > 0 {
				servers := tt.input["mcp_servers"].(map[string]interface{})
				env := servers["x-twitter"].(map[string]interface{})["env"].(map[string]interface{})
				for _, k := range tt.wantEnvDeleted {
					if _, ok := env[k]; ok {
						t.Errorf("expected env key %q to be dropped, still present", k)
					}
				}
				for _, k := range tt.wantEnvKept {
					if _, ok := env[k]; !ok {
						t.Errorf("expected env key %q to be kept, was deleted", k)
					}
				}
			}
		})
	}
}
