package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kocoro-lab/shan/internal/client"
)

// nativeResponse builds a /v1/completions response for tests.
func nativeResponse(content string, finishReason string, fc *client.FunctionCall, inputTokens, outputTokens int) client.CompletionResponse {
	return client.CompletionResponse{
		Model:        "test-model",
		OutputText:   content,
		FinishReason: finishReason,
		FunctionCall: fc,
		Usage: client.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			TotalTokens:  inputTokens + outputTokens,
		},
		RequestID: "req-test",
	}
}

func toolCall(name string, args string) *client.FunctionCall {
	return &client.FunctionCall{
		Name:      name,
		Arguments: json.RawMessage(args),
	}
}

func TestAgentLoop_SimpleTextResponse(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(nativeResponse("The answer is 42.", "end_turn", nil, 10, 5))
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, usage, err := loop.Run(context.Background(), "What is the meaning of life?", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "The answer is 42." {
		t.Errorf("expected 'The answer is 42.', got %q", result)
	}
	if callCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", callCount)
	}
	if usage.TotalTokens != 15 {
		t.Errorf("expected 15 total tokens, got %d", usage.TotalTokens)
	}
	if usage.LLMCalls != 1 {
		t.Errorf("expected 1 LLM call in usage, got %d", usage.LLMCalls)
	}
}

// mockApprovalTool requires approval but implements SafeChecker.
type mockApprovalTool struct {
	name     string
	safeArgs func(string) bool
}

func (m *mockApprovalTool) Info() ToolInfo {
	return ToolInfo{
		Name:        m.name,
		Description: "mock tool requiring approval",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (m *mockApprovalTool) Run(ctx context.Context, args string) (ToolResult, error) {
	return ToolResult{Content: "executed"}, nil
}

func (m *mockApprovalTool) RequiresApproval() bool { return true }

func (m *mockApprovalTool) IsSafeArgs(argsJSON string) bool {
	if m.safeArgs != nil {
		return m.safeArgs(argsJSON)
	}
	return false
}

// mockHandler tracks whether approval was requested.
type mockHandler struct {
	approvalRequested bool
	approveResult     bool
	lastText          string
}

func (h *mockHandler) OnToolCall(name string, args string)        {}
func (h *mockHandler) OnToolResult(name string, args string, result ToolResult, elapsed time.Duration) {}
func (h *mockHandler) OnText(text string)                          { h.lastText = text }
func (h *mockHandler) OnStreamDelta(delta string)                  {}
func (h *mockHandler) OnUsage(usage TurnUsage)                     {}
func (h *mockHandler) OnApprovalNeeded(tool string, args string) bool {
	h.approvalRequested = true
	return h.approveResult
}

func TestAgentLoop_SafeCheckerSkipsApproval(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
				toolCall("guarded_tool", `{"command": "ls"}`), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("done", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockApprovalTool{
		name:     "guarded_tool",
		safeArgs: func(args string) bool { return true },
	})

	handler := &mockHandler{}
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)
	loop.SetHandler(handler)

	result, _, err := loop.Run(context.Background(), "run it", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "done" {
		t.Errorf("expected 'done', got %q", result)
	}
	if handler.approvalRequested {
		t.Error("expected approval to be skipped for safe command, but it was requested")
	}
}

func TestAgentLoop_UnsafeCheckerStillRequiresApproval(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
				toolCall("guarded_tool", `{"command": "rm -rf /"}`), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("denied", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockApprovalTool{
		name:     "guarded_tool",
		safeArgs: func(args string) bool { return false },
	})

	handler := &mockHandler{approveResult: false}
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)
	loop.SetHandler(handler)

	_, _, err := loop.Run(context.Background(), "run it", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handler.approvalRequested {
		t.Error("expected approval to be requested for unsafe command, but it was not")
	}
}

// mockImageTool returns a tool result with images.
type mockImageTool struct {
	name string
}

func (m *mockImageTool) Info() ToolInfo {
	return ToolInfo{
		Name:        m.name,
		Description: "mock tool with images",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (m *mockImageTool) Run(ctx context.Context, args string) (ToolResult, error) {
	return ToolResult{
		Content: "Screenshot captured",
		Images: []ImageBlock{
			{MediaType: "image/png", Data: "iVBORfakebase64data"},
		},
	}, nil
}

func (m *mockImageTool) RequiresApproval() bool { return false }

func TestAgentLoop_ImageToolResultIncludesBlocks(t *testing.T) {
	var lastMessages []client.Message
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req client.CompletionRequest
		json.NewDecoder(r.Body).Decode(&req)
		lastMessages = req.Messages

		if callCount == 1 {
			json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
				toolCall("image_tool", `{}`), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("I see a screenshot", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockImageTool{name: "image_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "take a screenshot", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "I see a screenshot" {
		t.Errorf("expected 'I see a screenshot', got %q", result)
	}

	// The messages sent to the LLM on the 2nd call should include content blocks
	found := false
	for _, msg := range lastMessages {
		if msg.Content.HasBlocks() {
			found = true
			blocks := msg.Content.Blocks()
			hasImage := false
			hasText := false
			for _, b := range blocks {
				if b.Type == "image" && b.Source != nil {
					hasImage = true
				}
				if b.Type == "text" {
					hasText = true
				}
			}
			if !hasImage {
				t.Error("expected image block in content")
			}
			if !hasText {
				t.Error("expected text block in content")
			}
			if msg.Role != "user" {
				t.Errorf("expected user role for image message, got %q", msg.Role)
			}
		}
	}
	if !found {
		t.Error("expected at least one message with content blocks containing image")
	}
}

func TestAgentLoop_ToolCallThenResponse(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
				toolCall("mock_tool", `{}`), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("Tool returned: mock result", "end_turn", nil, 20, 10))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, usage, err := loop.Run(context.Background(), "use the tool", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Tool returned: mock result" {
		t.Errorf("unexpected result: %q", result)
	}
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls, got %d", callCount)
	}
	if usage.TotalTokens != 45 {
		t.Errorf("expected 45 total tokens, got %d", usage.TotalTokens)
	}
	if usage.LLMCalls != 2 {
		t.Errorf("expected 2 LLM calls in usage, got %d", usage.LLMCalls)
	}
}

