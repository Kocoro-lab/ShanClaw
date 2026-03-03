package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Kocoro-lab/shan/internal/client"
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
	reg, cleanup, err := RegisterAll(gw, nil)
	defer cleanup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check local tools are registered
	for _, name := range []string{"file_read", "file_write", "file_edit", "glob", "grep", "bash", "think", "directory_list", "http", "system_info", "clipboard", "notify", "process", "applescript", "accessibility", "browser", "screenshot", "computer"} {
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

	// Total: 18 local + 2 server = 20
	schemas := reg.Schemas()
	if len(schemas) != 20 {
		t.Errorf("expected 20 tools, got %d", len(schemas))
	}
}

func TestRegisterAll_ServerUnavailable(t *testing.T) {
	// Point to a closed server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg, cleanup, err := RegisterAll(gw, nil)
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
	if len(schemas) != 18 {
		t.Errorf("expected 18 local tools, got %d", len(schemas))
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
	reg, cleanup, err := RegisterAll(gw, nil)
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

	// 18 local + 1 server (bash skipped) = 19
	schemas := reg.Schemas()
	if len(schemas) != 19 {
		t.Errorf("expected 19 tools, got %d", len(schemas))
	}
}
