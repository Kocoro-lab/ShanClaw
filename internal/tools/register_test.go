package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/client"
	"github.com/Kocoro-lab/ShanClaw/internal/mcp"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
)

func TestRegisterAll_WithServerTools(t *testing.T) {
	serverTools := []client.ServerToolSchema{
		{Name: "web_search", Description: "Search the web"},
		{Name: "getStockBars", Description: "Get stock price bars"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(serverTools)
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg, _, _, cleanup, err := RegisterAll(gw, nil)
	defer cleanup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check local tools are registered
	for _, name := range []string{"use_skill", "file_read", "file_write", "file_edit", "glob", "grep", "bash", "think", "directory_list", "http", "system_info", "clipboard", "notify", "process", "applescript", "accessibility", "ghostty", "browser", "screenshot", "computer", "wait_for", "schedule_create", "schedule_list", "schedule_update", "schedule_remove"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("local tool %q not registered", name)
		}
	}

	// Check server tools are registered
	for _, name := range []string{"web_search", "getStockBars"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("server tool %q not registered", name)
		}
	}

	// Total: 26 local + 2 server = 28
	schemas := reg.Schemas()
	if len(schemas) != 28 {
		t.Errorf("expected 28 tools, got %d", len(schemas))
	}
}

func TestRegisterAll_ServerUnavailable(t *testing.T) {
	// Point to a closed server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg, _, _, cleanup, err := RegisterAll(gw, nil)
	defer cleanup()
	if err == nil {
		t.Error("expected warning error when server is unavailable")
	}

	// Local tools should still be registered
	for _, name := range []string{"file_read", "bash", "glob"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("local tool %q should still be registered", name)
		}
	}

	schemas := reg.Schemas()
	if len(schemas) != 26 {
		t.Errorf("expected 26 local tools, got %d", len(schemas))
	}
}

func TestRegisterAll_LocalPriority(t *testing.T) {
	// Server returns a tool named "bash" — should be skipped
	serverTools := []client.ServerToolSchema{
		{Name: "bash", Description: "Server bash (should be skipped)"},
		{Name: "web_search", Description: "Search the web"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(serverTools)
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg, _, _, cleanup, err := RegisterAll(gw, nil)
	defer cleanup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "bash" should be the local BashTool, not the server one
	tool, ok := reg.Get("bash")
	if !ok {
		t.Fatal("bash tool not found")
	}
	if _, isServer := tool.(*ServerTool); isServer {
		t.Error("bash should be local tool, not server tool")
	}

	// web_search should be server tool
	tool, ok = reg.Get("web_search")
	if !ok {
		t.Fatal("web_search tool not found")
	}
	if _, isServer := tool.(*ServerTool); !isServer {
		t.Error("web_search should be a server tool")
	}

	// 26 local + 1 server (bash skipped) = 27
	schemas := reg.Schemas()
	if len(schemas) != 27 {
		t.Errorf("expected 27 tools, got %d", len(schemas))
	}
}

func TestRegisterServerTools_AllowlistFiltering(t *testing.T) {
	serverTools := []client.ServerToolSchema{
		{Name: "web_search", Description: "Search the web"},
		{Name: "python_executor", Description: "Run Python in sandbox"},
		{Name: "calculator", Description: "Basic calculator"},
		{Name: "getStockBars", Description: "Get stock price bars"},
		{Name: "session_file_write", Description: "Write session file"},
		{Name: "some_future_tool", Description: "Unknown new tool"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(serverTools)
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg, _, _, cleanup, err := RegisterAll(gw, nil)
	defer cleanup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Allowlisted tools should be registered
	for _, name := range []string{"web_search", "getStockBars"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("allowlisted tool %q should be registered", name)
		}
	}

	// Non-allowlisted tools should be filtered out
	for _, name := range []string{"python_executor", "calculator", "session_file_write", "some_future_tool"} {
		if _, ok := reg.Get(name); ok {
			t.Errorf("non-allowlisted tool %q should NOT be registered", name)
		}
	}
}

func TestExtractGatewayTools(t *testing.T) {
	reg := agent.NewToolRegistry()
	gw := client.NewGatewayClient("http://test", "key")
	reg.Register(NewServerTool(client.ServerToolSchema{Name: "web_search", Description: "search"}, gw))
	reg.Register(&ThinkTool{})
	tools := ExtractGatewayTools(reg)
	if len(tools) != 1 {
		t.Fatalf("expected 1 gateway tool, got %d", len(tools))
	}
	if tools[0].Info().Name != "web_search" {
		t.Errorf("expected web_search, got %s", tools[0].Info().Name)
	}
}

func TestExtractPostOverlays(t *testing.T) {
	baseline := agent.NewToolRegistry()
	baseline.Register(&ThinkTool{})

	full := baseline.Clone()
	gw := client.NewGatewayClient("http://test", "key")
	full.Register(NewServerTool(client.ServerToolSchema{Name: "web_search", Description: "search"}, gw))
	mgr := mcp.NewClientManager()
	full.Register(NewMCPTool("playwright", mcpproto.Tool{Name: "browser_navigate"}, mgr))
	full.Register(&NotifyTool{}) // a local overlay

	overlays := ExtractPostOverlays(full, baseline)
	if len(overlays) != 1 {
		t.Fatalf("expected 1 overlay, got %d", len(overlays))
	}
	if overlays[0].Info().Name != "notify" {
		t.Errorf("expected notify, got %s", overlays[0].Info().Name)
	}
}

func TestRebuildRegistryForHealth_PlaywrightHealthy(t *testing.T) {
	baseline := agent.NewToolRegistry()
	baseline.Register(&ThinkTool{})
	baseline.Register(&BrowserTool{})

	healthStates := map[string]mcp.ServerHealth{
		"playwright": {State: mcp.StateHealthy},
	}

	mgr := mcp.NewClientManager()
	mgr.SeedToolCache("playwright", []mcp.RemoteTool{
		{ServerName: "playwright", Tool: mcpproto.Tool{Name: "browser_navigate"}},
	})

	reg := RebuildRegistryForHealth(baseline, nil, nil, healthStates, mgr, nil)
	if _, ok := reg.Get("browser"); ok {
		t.Error("legacy browser should be removed when Playwright is healthy")
	}
	if _, ok := reg.Get("browser_navigate"); !ok {
		t.Error("browser_navigate should be registered from healthy Playwright")
	}
}

func TestRebuildRegistryForHealth_PlaywrightDisconnected(t *testing.T) {
	baseline := agent.NewToolRegistry()
	baseline.Register(&ThinkTool{})
	baseline.Register(&BrowserTool{})

	healthStates := map[string]mcp.ServerHealth{
		"playwright": {State: mcp.StateDisconnected},
	}

	mgr := mcp.NewClientManager()
	mgr.SeedToolCache("playwright", []mcp.RemoteTool{
		{ServerName: "playwright", Tool: mcpproto.Tool{Name: "browser_navigate"}},
	})

	reg := RebuildRegistryForHealth(baseline, nil, nil, healthStates, mgr, nil)
	// Disconnected Playwright tools are included from cache for on-demand reconnect.
	if _, ok := reg.Get("browser_navigate"); !ok {
		t.Error("browser_navigate should be present from cache even when disconnected")
	}
	// Legacy browser is removed when Playwright tools are present (even disconnected).
	if _, ok := reg.Get("browser"); ok {
		t.Error("legacy browser should be removed when Playwright tools are present")
	}
}

func TestRebuildRegistryForHealth_GatewayAndPostOverlays(t *testing.T) {
	baseline := agent.NewToolRegistry()
	baseline.Register(&ThinkTool{})

	gw := client.NewGatewayClient("http://test", "key")
	gatewayOverlay := []agent.Tool{
		NewServerTool(client.ServerToolSchema{Name: "web_search", Description: "search"}, gw),
	}
	postOverlays := []agent.Tool{
		&NotifyTool{},
	}

	reg := RebuildRegistryForHealth(baseline, gatewayOverlay, postOverlays, nil, nil, nil)
	if _, ok := reg.Get("think"); !ok {
		t.Error("baseline tool 'think' should be present")
	}
	if _, ok := reg.Get("web_search"); !ok {
		t.Error("gateway overlay 'web_search' should be present")
	}
	if _, ok := reg.Get("notify"); !ok {
		t.Error("post overlay 'notify' should be present")
	}
}
