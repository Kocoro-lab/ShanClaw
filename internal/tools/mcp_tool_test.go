package tools

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/mcp"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// --- Test 1: Disconnected → first call fails → on-demand reconnect → retry succeeds ---

func TestMCPTool_Run_ReconnectOnDisconnected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up a manager with config but NO client initially.
	mgr := mcp.NewClientManager()
	mgr.SeedConfig("playwright", mcp.MCPServerConfig{Command: "dummy"})

	// Set up supervisor and start it — initial probe will fail (no client)
	// → server enters StateDisconnected.
	sup := mcp.NewSupervisor(mgr)
	sup.Start(ctx)
	defer sup.Stop()

	// Let the initial probe cycle run and mark the server disconnected.
	time.Sleep(100 * time.Millisecond)

	h := sup.HealthFor("playwright")
	if h.State != mcp.StateDisconnected {
		t.Fatalf("expected disconnected after initial probe, got %v", h.State)
	}

	// Now inject a controllable client: CallTool fails once (io.EOF), then
	// succeeds. ListTools always succeeds (so the transport probe works).
	fake := &controllableCallToolClient{}
	mgr.SeedClient("playwright", fake)

	// Create MCPTool with supervisor for on-demand reconnect.
	tool := mcpgo.Tool{Name: "browser_navigate"}
	mt := NewMCPTool("playwright", tool, mgr)
	mt.SetSupervisor(sup)

	result, err := mt.Run(ctx, `{"url":"https://example.com"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", result.Content)
	}

	// Verify: first call failed (EOF), ProbeNow reconnected, second call succeeded.
	calls := int(fake.callToolCount.Load())
	if calls != 2 {
		t.Errorf("expected 2 CallTool calls (fail + retry), got %d", calls)
	}
}

// --- Test 2: No cache → disconnected server tools NOT injected ---

func TestRebuildRegistryForHealth_DisconnectedNoCache(t *testing.T) {
	baseline := agent.NewToolRegistry()
	baseline.Register(&ThinkTool{})
	baseline.Register(&BrowserTool{})

	healthStates := map[string]mcp.ServerHealth{
		"playwright": {State: mcp.StateDisconnected},
	}

	// Manager with no cached tools for the disconnected server.
	mgr := mcp.NewClientManager()
	// Deliberately NOT calling mgr.SeedToolCache("playwright", ...)

	reg := RebuildRegistryForHealth(baseline, nil, nil, healthStates, mgr, nil)
	if _, ok := reg.Get("browser_navigate"); ok {
		t.Error("browser_navigate should NOT be in registry when cache is empty")
	}
	// Legacy browser should remain when no Playwright tools are present.
	if _, ok := reg.Get("browser"); !ok {
		t.Error("legacy browser should remain when no Playwright tools are present")
	}
}

// --- Test 3: No supervisor → no reconnect, error returned directly ---

func TestMCPTool_Run_NoSupervisor_NoReconnect(t *testing.T) {
	mgr := mcp.NewClientManager()
	// No client → CallTool will fail.

	tool := mcpgo.Tool{Name: "browser_navigate"}
	mt := NewMCPTool("playwright", tool, mgr)
	// Deliberately NOT calling mt.SetSupervisor(...)

	result, err := mt.Run(context.Background(), `{"url":"https://example.com"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result when server not connected and no supervisor")
	}
}

// --- Fake MCP client with controllable CallTool ---

// controllableCallToolClient is a minimal MCPClient where CallTool fails on the
// first call (io.EOF) and succeeds on subsequent calls. ListTools always succeeds
// so the supervisor's transport probe can mark the server healthy.
type controllableCallToolClient struct {
	callToolCount atomic.Int32
}

func (c *controllableCallToolClient) Initialize(context.Context, mcpgo.InitializeRequest) (*mcpgo.InitializeResult, error) {
	return &mcpgo.InitializeResult{}, nil
}
func (c *controllableCallToolClient) Ping(context.Context) error { return nil }
func (c *controllableCallToolClient) ListResourcesByPage(context.Context, mcpgo.ListResourcesRequest) (*mcpgo.ListResourcesResult, error) {
	return &mcpgo.ListResourcesResult{}, nil
}
func (c *controllableCallToolClient) ListResources(context.Context, mcpgo.ListResourcesRequest) (*mcpgo.ListResourcesResult, error) {
	return &mcpgo.ListResourcesResult{}, nil
}
func (c *controllableCallToolClient) ListResourceTemplatesByPage(context.Context, mcpgo.ListResourceTemplatesRequest) (*mcpgo.ListResourceTemplatesResult, error) {
	return &mcpgo.ListResourceTemplatesResult{}, nil
}
func (c *controllableCallToolClient) ListResourceTemplates(context.Context, mcpgo.ListResourceTemplatesRequest) (*mcpgo.ListResourceTemplatesResult, error) {
	return &mcpgo.ListResourceTemplatesResult{}, nil
}
func (c *controllableCallToolClient) ReadResource(context.Context, mcpgo.ReadResourceRequest) (*mcpgo.ReadResourceResult, error) {
	return &mcpgo.ReadResourceResult{}, nil
}
func (c *controllableCallToolClient) Subscribe(context.Context, mcpgo.SubscribeRequest) error {
	return nil
}
func (c *controllableCallToolClient) Unsubscribe(context.Context, mcpgo.UnsubscribeRequest) error {
	return nil
}
func (c *controllableCallToolClient) ListPromptsByPage(context.Context, mcpgo.ListPromptsRequest) (*mcpgo.ListPromptsResult, error) {
	return &mcpgo.ListPromptsResult{}, nil
}
func (c *controllableCallToolClient) ListPrompts(context.Context, mcpgo.ListPromptsRequest) (*mcpgo.ListPromptsResult, error) {
	return &mcpgo.ListPromptsResult{}, nil
}
func (c *controllableCallToolClient) GetPrompt(context.Context, mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
	return &mcpgo.GetPromptResult{}, nil
}
func (c *controllableCallToolClient) ListToolsByPage(context.Context, mcpgo.ListToolsRequest) (*mcpgo.ListToolsResult, error) {
	return &mcpgo.ListToolsResult{}, nil
}
func (c *controllableCallToolClient) ListTools(context.Context, mcpgo.ListToolsRequest) (*mcpgo.ListToolsResult, error) {
	return &mcpgo.ListToolsResult{}, nil
}
func (c *controllableCallToolClient) CallTool(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	n := c.callToolCount.Add(1)
	if n == 1 {
		return nil, io.EOF // transport error → triggers reconnect path
	}
	return mcpgo.NewToolResultText("ok"), nil
}
func (c *controllableCallToolClient) SetLevel(context.Context, mcpgo.SetLevelRequest) error {
	return nil
}
func (c *controllableCallToolClient) Complete(context.Context, mcpgo.CompleteRequest) (*mcpgo.CompleteResult, error) {
	return &mcpgo.CompleteResult{}, nil
}
func (c *controllableCallToolClient) Close() error                                         { return nil }
func (c *controllableCallToolClient) OnNotification(func(mcpgo.JSONRPCNotification)) {}
