package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func toolCallWithID(name, args, id string) *client.FunctionCall {
	return &client.FunctionCall{
		ID:        id,
		Name:      name,
		Arguments: json.RawMessage(args),
	}
}

// nativeResponseWithID builds a response with a tool call that has an ID.
func nativeResponseWithID(content string, finishReason string, fc *client.FunctionCall, inputTokens, outputTokens int) client.CompletionResponse {
	resp := nativeResponse(content, finishReason, nil, inputTokens, outputTokens)
	if fc != nil {
		resp.ToolCalls = []client.FunctionCall{*fc}
	}
	return resp
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
func (h *mockHandler) OnToolResult(name string, args string, result ToolResult, elapsed time.Duration) {
}
func (h *mockHandler) OnText(text string)         { h.lastText = text }
func (h *mockHandler) OnStreamDelta(delta string) {}
func (h *mockHandler) OnUsage(usage TurnUsage)    {}
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

// TestAgentLoop_ThinkThenExecute verifies the think tool provides an explicit
// continuation signal — the model calls think to plan, then executes with tools.
func TestAgentLoop_ThinkThenExecute(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1:
			// Model uses think tool to plan — triggers continuation via tool_use
			json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
				toolCall("think", `{"thought":"Plan:\n1. Read the file\n2. Edit config\n3. Verify"}`), 10, 5))
		case 2:
			// After think, model executes the plan with actual tools
			json.NewEncoder(w).Encode(nativeResponse("Reading...", "tool_use",
				toolCall("mock_tool", `{"action":"read"}`), 10, 5))
		case 3:
			// Final summary after tool use
			json.NewEncoder(w).Encode(nativeResponse("Done. File updated.", "end_turn", nil, 10, 5))
		default:
			json.NewEncoder(w).Encode(nativeResponse("unexpected", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "think"}) // mock think tool
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "update the config file", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Done. File updated." {
		t.Errorf("unexpected result: %q", result)
	}
	// think (1) → tool call (2) → text summary (3) = 3 LLM calls
	if callCount != 3 {
		t.Errorf("expected 3 LLM calls (think + tool + summary), got %d", callCount)
	}
}

// TestAgentLoop_TextOnlyAlwaysStops verifies that text-only responses always
// terminate the loop now that isPlanningResponse is removed.
func TestAgentLoop_TextOnlyAlwaysStops(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Even bulleted text should stop immediately — no plan heuristic.
		json.NewEncoder(w).Encode(nativeResponse(
			"React vs Vue:\n• React has larger ecosystem\n• Vue is easier to learn\n• Both are great choices",
			"end_turn", nil, 10, 5))
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "compare React vs Vue", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "React vs Vue") {
		t.Errorf("unexpected result: %q", result)
	}
	// Text-only = done immediately, 1 LLM call
	if callCount != 1 {
		t.Errorf("expected 1 LLM call (text-only stops immediately), got %d", callCount)
	}
}

// TestAgentLoop_RepeatableToolsExempt verifies GUI tools don't trigger same-tool limit.
func TestAgentLoop_RepeatableToolsExempt(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 5 {
			json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
				toolCall("screenshot", fmt.Sprintf(`{"delay":%d}`, callCount)), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("Captured 5 screenshots.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "screenshot"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "take 5 screenshots", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Captured 5 screenshots." {
		t.Errorf("unexpected result: %q", result)
	}
}

// TestAgentLoop_GracefulMaxIterExit verifies graceful degradation on iteration limit.
func TestAgentLoop_GracefulMaxIterExit(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(nativeResponse(
			fmt.Sprintf("Step %d done.", callCount), "tool_use",
			toolCall("mock_tool", fmt.Sprintf(`{"step":%d}`, callCount)), 10, 5))
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 3, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "complex task", nil)
	// Should return ErrMaxIterReached, not a generic error
	if !errors.Is(err, ErrMaxIterReached) {
		t.Fatalf("expected ErrMaxIterReached, got: %v", err)
	}
	if result != "Step 3 done." {
		t.Errorf("expected last text from graceful exit, got %q", result)
	}
}

func TestEffectiveMaxIter(t *testing.T) {
	a := &AgentLoop{maxIter: 25}

	// No GUI tools: use default
	if got := a.effectiveMaxIter(map[string]int{"bash": 3}); got != 25 {
		t.Errorf("coding tasks: expected 25, got %d", got)
	}

	// GUI tool present: bump to 75
	if got := a.effectiveMaxIter(map[string]int{"screenshot": 1, "bash": 2}); got != 75 {
		t.Errorf("GUI tasks: expected 75, got %d", got)
	}

	// User set high limit: keep it
	a.maxIter = 100
	if got := a.effectiveMaxIter(map[string]int{"screenshot": 1}); got != 100 {
		t.Errorf("high user limit: expected 100, got %d", got)
	}

	// Empty toolsUsed: use default
	a.maxIter = 25
	if got := a.effectiveMaxIter(map[string]int{}); got != 25 {
		t.Errorf("empty tools: expected 25, got %d", got)
	}
}

func TestFilterOldImages(t *testing.T) {
	messages := []client.Message{
		{Role: "system", Content: client.NewTextContent("system prompt")},
		{Role: "user", Content: client.NewTextContent("take screenshots")},
	}

	// Add 7 image messages
	for i := range 7 {
		messages = append(messages, client.Message{
			Role: "user",
			Content: client.NewBlockContent([]client.ContentBlock{
				{Type: "text", Text: fmt.Sprintf("Screenshot %d", i)},
				{Type: "image", Source: &client.ImageSource{Type: "base64", MediaType: "image/png", Data: "fake"}},
			}),
		})
	}

	filterOldImages(messages, 5)

	// Count remaining image blocks
	imageCount := 0
	for _, msg := range messages {
		if !msg.Content.HasBlocks() {
			continue
		}
		for _, b := range msg.Content.Blocks() {
			if b.Type == "image" {
				imageCount++
			}
		}
	}

	if imageCount != 5 {
		t.Errorf("expected 5 images after filtering, got %d", imageCount)
	}

	// Verify the 2 oldest (index 2, 3) were replaced with text placeholders
	for i := 2; i < 4; i++ {
		for _, b := range messages[i].Content.Blocks() {
			if b.Type == "image" {
				t.Errorf("message %d should not have image blocks after filtering", i)
			}
		}
	}

	// Verify the 5 newest (index 4-8) still have images
	for i := 4; i < 9; i++ {
		hasImage := false
		for _, b := range messages[i].Content.Blocks() {
			if b.Type == "image" {
				hasImage = true
			}
		}
		if !hasImage {
			t.Errorf("message %d should still have image block", i)
		}
	}
}

func TestFilterOldImages_NoOpWhenUnderLimit(t *testing.T) {
	messages := []client.Message{
		{Role: "user", Content: client.NewBlockContent([]client.ContentBlock{
			{Type: "text", Text: "Screenshot"},
			{Type: "image", Source: &client.ImageSource{Type: "base64", MediaType: "image/png", Data: "fake"}},
		})},
	}

	filterOldImages(messages, 5)

	// Should not modify anything
	imageCount := 0
	for _, b := range messages[0].Content.Blocks() {
		if b.Type == "image" {
			imageCount++
		}
	}
	if imageCount != 1 {
		t.Errorf("expected 1 image (no filtering needed), got %d", imageCount)
	}
}

// TestAgentLoop_ConsecutiveDupForceStop verifies the consecutive duplicate detector
// forces a stop after back-to-back identical tool calls (2→nudge, 4→force stop).
func TestAgentLoop_ConsecutiveDupForceStop(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 4 {
			// 4 consecutive identical calls: nudge at 2,3 → force stop at 4
			json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
				toolCall("mock_tool", `{"cmd":"same"}`), 10, 5))
		} else {
			// Final forced response (no tools)
			json.NewEncoder(w).Encode(nativeResponse("Stopped due to loop.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "do something", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Stopped due to loop." {
		t.Errorf("expected force-stop response, got %q", result)
	}
	// 4 tool iterations + 1 forced final = 5 LLM calls
	if callCount != 5 {
		t.Errorf("expected 5 LLM calls (4 tool + 1 forced), got %d", callCount)
	}
}

// mockErrorTool always returns an error.
type mockErrorTool struct {
	name string
}

func (m *mockErrorTool) Info() ToolInfo {
	return ToolInfo{
		Name:        m.name,
		Description: "mock tool that always fails",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (m *mockErrorTool) Run(ctx context.Context, args string) (ToolResult, error) {
	return ToolResult{Content: "permission denied: /etc/shadow", IsError: true}, nil
}

func (m *mockErrorTool) RequiresApproval() bool { return false }

// TestAgentLoop_ErrorAwareBreaking verifies the detector catches repeated errors.
// SameToolError threshold=4, nudge at 4,5,6 → force stop via nudge cap → final call.
func TestAgentLoop_ErrorAwareBreaking(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 6 {
			// 6 calls to a failing tool: error nudge at 4,5,6 → force stop via cap
			json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
				toolCall("failing_tool", fmt.Sprintf(`{"attempt":%d}`, callCount)), 10, 5))
		} else {
			// Final forced response (no tools)
			json.NewEncoder(w).Encode(nativeResponse("Gave up.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockErrorTool{name: "failing_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "try something", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Gave up." {
		t.Errorf("expected error-stop response, got %q", result)
	}
	// 6 tool iterations + 1 forced final = 7 LLM calls
	if callCount != 7 {
		t.Errorf("expected 7 LLM calls (6 tool + 1 forced), got %d", callCount)
	}
}

func TestAgentLoop_ContextCancellation(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Small delay per request so cancellation fires before maxIter
		time.Sleep(20 * time.Millisecond)
		// Always return tool calls to keep the loop running
		json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
			toolCall("mock_tool", fmt.Sprintf(`{"step":%d}`, callCount)), 10, 5))
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay to let a few iterations run
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, _, err := loop.Run(ctx, "long task", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	// Should have stopped well before maxIter=25
	if callCount >= 25 {
		t.Errorf("expected loop to exit early due to cancellation, but made %d calls", callCount)
	}
}

func TestGenerateCallID(t *testing.T) {
	id := generateCallID()
	if len(id) != 6 {
		t.Errorf("expected 6 chars, got %d: %q", len(id), id)
	}
	id2 := generateCallID()
	if id == id2 {
		t.Errorf("two consecutive calls returned same ID: %s", id)
	}
}

func TestFormatToolExec(t *testing.T) {
	result := formatToolExec("screenshot", `{"target":"fullscreen"}`, "a1b2c3", "screenshot saved to /tmp/s.png", false)
	if !strings.Contains(result, `<tool_exec tool="screenshot" call_id="a1b2c3">`) {
		t.Errorf("missing opening tag: %s", result)
	}
	if !strings.Contains(result, `<output status="ok">`) {
		t.Errorf("missing ok status: %s", result)
	}
	if !strings.Contains(result, `</tool_exec>`) {
		t.Errorf("missing closing tag: %s", result)
	}

	errResult := formatToolExec("bash", `{"cmd":"ls"}`, "d4e5f6", "permission denied", true)
	if !strings.Contains(errResult, `<output status="error">`) {
		t.Errorf("missing error status: %s", errResult)
	}

	// Verify XML escaping: output containing tag-like content must not break parsing
	nasty := formatToolExec("bash", `echo "</input>"`, "aabbcc", "line with </output> and </tool_exec> in it", false)
	if strings.Contains(nasty, "</input>\"") || strings.Count(nasty, "</output>") != 1 || strings.Count(nasty, "</tool_exec>") != 1 {
		t.Errorf("XML escaping failed — raw delimiters leaked through: %s", nasty)
	}
	// Escaped output should still be parseable by toolResultPattern
	if !toolResultPattern.MatchString(nasty) {
		t.Errorf("escaped output should still match toolResultPattern: %s", nasty)
	}
}

func TestToolResultPatternMatchesXML(t *testing.T) {
	text := formatToolExec("bash", `{"cmd":"ls"}`, "abc123", "file1.go\nfile2.go", false)
	if !toolResultPattern.MatchString(text) {
		t.Errorf("toolResultPattern should match XML format: %s", text)
	}
}

func TestFabricatedToolCallDetection(t *testing.T) {
	// Old format (backward compat)
	old := "I called screenshot({\"target\":\"fullscreen\"}).\n\nResult:\nscreenshot saved"
	if !looksLikeFabricatedToolCalls(old) {
		t.Error("should detect old format")
	}
	// New XML format in text output
	xml := `<tool_exec tool="bash" call_id="aaa111">
<input>{"cmd":"ls"}</input>
<output status="ok">done</output>
</tool_exec>`
	if !looksLikeFabricatedToolCalls(xml) {
		t.Error("should detect XML format in text output")
	}
	// Normal text
	if looksLikeFabricatedToolCalls("Here is the answer.") {
		t.Error("should not flag normal text")
	}
}

func TestPreambleSuppressedWithToolCalls(t *testing.T) {
	var lastMessages []client.Message
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req client.CompletionRequest
		json.NewDecoder(r.Body).Decode(&req)
		lastMessages = req.Messages
		if callCount == 1 {
			json.NewEncoder(w).Encode(nativeResponse("Let me check that file for you.", "tool_use",
				toolCall("mock_tool", `{}`), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("Done.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	_, _, err := loop.Run(context.Background(), "check the file", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the preamble is NOT in context
	for _, msg := range lastMessages {
		text := msg.Content.Text()
		if strings.Contains(text, "Let me check that file for you") {
			t.Errorf("preamble should be suppressed from context, but found: %s", text)
		}
	}
}

func TestContextUsesXMLFormat(t *testing.T) {
	var lastMessages []client.Message
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req client.CompletionRequest
		json.NewDecoder(r.Body).Decode(&req)
		lastMessages = req.Messages
		if callCount == 1 {
			json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
				toolCall("mock_tool", `{}`), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("done", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	_, _, err := loop.Run(context.Background(), "use the tool", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Context should contain XML format, not "I called" format
	for _, msg := range lastMessages {
		text := msg.Content.Text()
		if strings.Contains(text, "I called ") {
			t.Errorf("context should use XML format, not 'I called': %s", text)
		}
		if strings.Contains(text, "<tool_exec ") {
			if !strings.Contains(text, "call_id=") {
				t.Error("tool_exec should have call_id attribute")
			}
		}
	}
}

func TestCompressOldToolResultsXML(t *testing.T) {
	messages := []client.Message{
		{Role: "system", Content: client.NewTextContent("system prompt")},
		{Role: "user", Content: client.NewTextContent("do stuff")},
	}
	// Add 5 assistant messages with XML-format tool results
	for i := range 5 {
		text := formatToolExec("bash", fmt.Sprintf(`{"step":%d}`, i), generateCallID(),
			strings.Repeat("x", 500), false)
		messages = append(messages, client.Message{
			Role:    "assistant",
			Content: client.NewTextContent(text),
		})
	}

	compressOldToolResults(messages, 3, 100)

	// First 2 assistant messages (indices 2,3) should be compressed
	for _, idx := range []int{2, 3} {
		text := messages[idx].Content.Text()
		if !strings.Contains(text, "[compressed]") {
			t.Errorf("message %d should be compressed", idx)
		}
	}
	// Last 3 (indices 4,5,6) should be uncompressed
	for _, idx := range []int{4, 5, 6} {
		text := messages[idx].Content.Text()
		if strings.Contains(text, "[compressed]") {
			t.Errorf("message %d should NOT be compressed", idx)
		}
	}
}

// --- Phase 3: Native tool_use/tool_result block tests ---

func TestAgentLoop_NativeToolUseBlocks(t *testing.T) {
	var lastMessages []client.Message
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req client.CompletionRequest
		json.NewDecoder(r.Body).Decode(&req)
		lastMessages = req.Messages
		if callCount == 1 {
			json.NewEncoder(w).Encode(nativeResponseWithID("Let me check.", "tool_use",
				toolCallWithID("mock_tool", `{}`, "toolu_abc123"), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("Done.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "check something", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Done." {
		t.Errorf("unexpected result: %q", result)
	}

	// Verify native blocks in context (second LLM call)
	hasToolUse := false
	hasToolResult := false
	for _, msg := range lastMessages {
		if !msg.Content.HasBlocks() {
			// Should NOT contain "I called" or "<tool_exec" in text
			text := msg.Content.Text()
			if strings.Contains(text, "I called ") || strings.Contains(text, "<tool_exec ") {
				t.Errorf("native path should not use text format: %s", text)
			}
			continue
		}
		for _, b := range msg.Content.Blocks() {
			if b.Type == "tool_use" {
				hasToolUse = true
				if b.ID != "toolu_abc123" {
					t.Errorf("expected tool_use ID=toolu_abc123, got %q", b.ID)
				}
				if b.Name != "mock_tool" {
					t.Errorf("expected tool_use Name=mock_tool, got %q", b.Name)
				}
			}
			if b.Type == "tool_result" {
				hasToolResult = true
				if b.ToolUseID != "toolu_abc123" {
					t.Errorf("expected tool_result tool_use_id=toolu_abc123, got %q", b.ToolUseID)
				}
			}
		}
	}
	if !hasToolUse {
		t.Error("expected tool_use block in context")
	}
	if !hasToolResult {
		t.Error("expected tool_result block in context")
	}
}

func TestAgentLoop_NativeBlocks_IncludesPreamble(t *testing.T) {
	var lastMessages []client.Message
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req client.CompletionRequest
		json.NewDecoder(r.Body).Decode(&req)
		lastMessages = req.Messages
		if callCount == 1 {
			json.NewEncoder(w).Encode(nativeResponseWithID("Let me check that file.", "tool_use",
				toolCallWithID("mock_tool", `{}`, "toolu_preamble"), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("Done.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	_, _, err := loop.Run(context.Background(), "check file", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Native path INCLUDES preamble text in assistant message (unlike Phase 2 suppression)
	for _, msg := range lastMessages {
		if msg.Role == "assistant" && msg.Content.HasBlocks() {
			for _, b := range msg.Content.Blocks() {
				if b.Type == "text" && b.Text == "Let me check that file." {
					return // found it
				}
			}
		}
	}
	t.Error("native path should include preamble text in assistant message")
}

func TestAgentLoop_FallbackToXML_NoID(t *testing.T) {
	var lastMessages []client.Message
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req client.CompletionRequest
		json.NewDecoder(r.Body).Decode(&req)
		lastMessages = req.Messages
		if callCount == 1 {
			// No ID on the tool call — should use XML fallback
			json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
				toolCall("mock_tool", `{}`), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("Done.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	_, _, err := loop.Run(context.Background(), "use tool", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should use XML format (no tool_use/tool_result blocks)
	for _, msg := range lastMessages {
		if msg.Content.HasBlocks() {
			for _, b := range msg.Content.Blocks() {
				if b.Type == "tool_use" || b.Type == "tool_result" {
					t.Error("fallback path should not produce native blocks")
				}
			}
		}
		text := msg.Content.Text()
		if strings.Contains(text, "<tool_exec ") {
			return // found XML format — correct
		}
	}
	t.Error("fallback path should use XML format")
}

func TestAgentLoop_NativeBlocks_DeniedTool(t *testing.T) {
	var lastMessages []client.Message
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req client.CompletionRequest
		json.NewDecoder(r.Body).Decode(&req)
		lastMessages = req.Messages
		if callCount == 1 {
			json.NewEncoder(w).Encode(nativeResponseWithID("", "tool_use",
				toolCallWithID("guarded_tool", `{"cmd":"rm -rf /"}`, "toolu_denied"), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("Denied.", "end_turn", nil, 10, 5))
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

	_, _, err := loop.Run(context.Background(), "run dangerous", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify tool_result with is_error for denied tool
	for _, msg := range lastMessages {
		if !msg.Content.HasBlocks() {
			continue
		}
		for _, b := range msg.Content.Blocks() {
			if b.Type == "tool_result" && b.ToolUseID == "toolu_denied" {
				if !b.IsError {
					t.Error("denied tool should have is_error=true")
				}
				return
			}
		}
	}
	t.Error("expected tool_result block for denied tool")
}

func TestAgentLoop_NativeBlocks_ImageResult(t *testing.T) {
	var lastMessages []client.Message
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req client.CompletionRequest
		json.NewDecoder(r.Body).Decode(&req)
		lastMessages = req.Messages
		if callCount == 1 {
			json.NewEncoder(w).Encode(nativeResponseWithID("", "tool_use",
				toolCallWithID("image_tool", `{}`, "toolu_img"), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("I see it", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockImageTool{name: "image_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	_, _, err := loop.Run(context.Background(), "take screenshot", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify image is nested inside tool_result (not as separate message)
	for _, msg := range lastMessages {
		if !msg.Content.HasBlocks() {
			continue
		}
		for _, b := range msg.Content.Blocks() {
			if b.Type == "tool_result" && b.ToolUseID == "toolu_img" {
				nested, ok := b.ToolContent.([]client.ContentBlock)
				if !ok {
					t.Fatalf("expected nested blocks, got %T", b.ToolContent)
				}
				hasImage := false
				for _, nb := range nested {
					if nb.Type == "image" {
						hasImage = true
					}
				}
				if !hasImage {
					t.Error("expected image block nested inside tool_result")
				}
				return
			}
		}
	}
	t.Error("expected tool_result block with image for image_tool")
}

// --- Parallel tool execution tests ---

// mockSlowTool sleeps for a configurable duration and tracks concurrent executions.
type mockSlowTool struct {
	name     string
	delay    time.Duration
	maxConc  *atomic.Int32 // tracks peak concurrency
	curConc  *atomic.Int32
}

func newMockSlowTool(name string, delay time.Duration) *mockSlowTool {
	return &mockSlowTool{
		name:    name,
		delay:   delay,
		maxConc: &atomic.Int32{},
		curConc: &atomic.Int32{},
	}
}

func (m *mockSlowTool) Info() ToolInfo {
	return ToolInfo{
		Name:        m.name,
		Description: "slow mock tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (m *mockSlowTool) Run(ctx context.Context, args string) (ToolResult, error) {
	cur := m.curConc.Add(1)
	// Update max concurrency if current is higher
	for {
		old := m.maxConc.Load()
		if cur <= old || m.maxConc.CompareAndSwap(old, cur) {
			break
		}
	}
	time.Sleep(m.delay)
	m.curConc.Add(-1)
	return ToolResult{Content: fmt.Sprintf("result from %s", m.name)}, nil
}

func (m *mockSlowTool) RequiresApproval() bool { return false }

// mockPanicTool panics during Run.
type mockPanicTool struct {
	name string
}

func (m *mockPanicTool) Info() ToolInfo {
	return ToolInfo{
		Name:        m.name,
		Description: "panicking mock tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (m *mockPanicTool) Run(ctx context.Context, args string) (ToolResult, error) {
	panic("intentional test panic")
}

func (m *mockPanicTool) RequiresApproval() bool { return false }

// multiToolResponse builds a response with multiple tool calls (all with IDs for native path).
func multiToolResponse(content string, calls []client.FunctionCall, inputTokens, outputTokens int) client.CompletionResponse {
	return client.CompletionResponse{
		Model:        "test-model",
		OutputText:   content,
		FinishReason: "tool_use",
		ToolCalls:    calls,
		Usage: client.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			TotalTokens:  inputTokens + outputTokens,
		},
		RequestID: "req-test",
	}
}

func TestAgentLoop_ParallelToolExecution(t *testing.T) {
	toolA := newMockSlowTool("tool_a", 100*time.Millisecond)
	toolB := newMockSlowTool("tool_b", 100*time.Millisecond)
	toolC := newMockSlowTool("tool_c", 100*time.Millisecond)

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// Return 3 tool calls in a single response
			json.NewEncoder(w).Encode(multiToolResponse("", []client.FunctionCall{
				{ID: "id_a", Name: "tool_a", Arguments: json.RawMessage(`{"key":"a"}`)},
				{ID: "id_b", Name: "tool_b", Arguments: json.RawMessage(`{"key":"b"}`)},
				{ID: "id_c", Name: "tool_c", Arguments: json.RawMessage(`{"key":"c"}`)},
			}, 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("All done.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(toolA)
	reg.Register(toolB)
	reg.Register(toolC)
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	start := time.Now()
	result, _, err := loop.Run(context.Background(), "run all tools", nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "All done." {
		t.Errorf("expected 'All done.', got %q", result)
	}

	// If sequential, 3 * 100ms = ~300ms. If parallel, ~100ms.
	// Use 250ms as threshold with margin for CI slowness.
	if elapsed > 250*time.Millisecond {
		t.Errorf("parallel execution took %v, expected < 250ms (3 x 100ms tools)", elapsed)
	}
}

func TestAgentLoop_ParallelToolExecution_ResultOrdering(t *testing.T) {
	var lastMessages []client.Message
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req client.CompletionRequest
		json.NewDecoder(r.Body).Decode(&req)
		lastMessages = req.Messages
		if callCount == 1 {
			json.NewEncoder(w).Encode(multiToolResponse("", []client.FunctionCall{
				{ID: "id_1", Name: "tool_a", Arguments: json.RawMessage(`{"order":"first"}`)},
				{ID: "id_2", Name: "tool_b", Arguments: json.RawMessage(`{"order":"second"}`)},
				{ID: "id_3", Name: "tool_c", Arguments: json.RawMessage(`{"order":"third"}`)},
			}, 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("Done.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	// Tools with different delays — results should still be in original order
	reg.Register(newMockSlowTool("tool_a", 80*time.Millisecond))
	reg.Register(newMockSlowTool("tool_b", 10*time.Millisecond))
	reg.Register(newMockSlowTool("tool_c", 50*time.Millisecond))
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	_, _, err := loop.Run(context.Background(), "run ordered tools", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify tool_result blocks are in order: id_1, id_2, id_3
	var resultIDs []string
	for _, msg := range lastMessages {
		if !msg.Content.HasBlocks() {
			continue
		}
		for _, b := range msg.Content.Blocks() {
			if b.Type == "tool_result" {
				resultIDs = append(resultIDs, b.ToolUseID)
			}
		}
	}
	expectedOrder := []string{"id_1", "id_2", "id_3"}
	if len(resultIDs) != len(expectedOrder) {
		t.Fatalf("expected %d tool_result blocks, got %d: %v", len(expectedOrder), len(resultIDs), resultIDs)
	}
	for i, id := range expectedOrder {
		if resultIDs[i] != id {
			t.Errorf("result[%d]: expected tool_use_id=%q, got %q", i, id, resultIDs[i])
		}
	}
}

func TestAgentLoop_ParallelToolExecution_PanicRecovery(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(multiToolResponse("", []client.FunctionCall{
				{ID: "id_ok", Name: "tool_ok", Arguments: json.RawMessage(`{}`)},
				{ID: "id_panic", Name: "tool_panic", Arguments: json.RawMessage(`{}`)},
			}, 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("Handled panic.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "tool_ok"})
	reg.Register(&mockPanicTool{name: "tool_panic"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "run with panic", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Handled panic." {
		t.Errorf("expected 'Handled panic.', got %q", result)
	}
}

func TestAgentLoop_SingleToolCall_NoGoroutine(t *testing.T) {
	// Verify single tool call works correctly (no goroutine overhead path)
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(nativeResponseWithID("", "tool_use",
				toolCallWithID("mock_tool", `{"single":true}`, "toolu_single"), 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("Single tool done.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "single tool", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Single tool done." {
		t.Errorf("expected 'Single tool done.', got %q", result)
	}
}

func TestAgentLoop_ParallelToolExecution_MixedDeniedAndApproved(t *testing.T) {
	var lastMessages []client.Message
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req client.CompletionRequest
		json.NewDecoder(r.Body).Decode(&req)
		lastMessages = req.Messages
		if callCount == 1 {
			// Mix of: known tool, unknown tool, tool requiring approval (denied)
			json.NewEncoder(w).Encode(multiToolResponse("", []client.FunctionCall{
				{ID: "id_ok", Name: "mock_tool", Arguments: json.RawMessage(`{}`)},
				{ID: "id_unknown", Name: "nonexistent_tool", Arguments: json.RawMessage(`{}`)},
				{ID: "id_denied", Name: "guarded_tool", Arguments: json.RawMessage(`{"cmd":"rm -rf /"}`)},
			}, 10, 5))
		} else {
			json.NewEncoder(w).Encode(nativeResponse("Mixed results.", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	reg.Register(&mockApprovalTool{
		name:     "guarded_tool",
		safeArgs: func(args string) bool { return false },
	})
	handler := &mockHandler{approveResult: false}
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)
	loop.SetHandler(handler)

	result, _, err := loop.Run(context.Background(), "mixed tools", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Mixed results." {
		t.Errorf("expected 'Mixed results.', got %q", result)
	}

	// Verify all 3 tool_result blocks exist with correct error states
	var results []struct {
		id      string
		isError bool
	}
	for _, msg := range lastMessages {
		if !msg.Content.HasBlocks() {
			continue
		}
		for _, b := range msg.Content.Blocks() {
			if b.Type == "tool_result" {
				results = append(results, struct {
					id      string
					isError bool
				}{b.ToolUseID, b.IsError})
			}
		}
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 tool_result blocks, got %d", len(results))
	}
	// id_ok should succeed
	if results[0].id != "id_ok" || results[0].isError {
		t.Errorf("expected id_ok to succeed, got id=%q isError=%v", results[0].id, results[0].isError)
	}
	// id_unknown should be error
	if results[1].id != "id_unknown" || !results[1].isError {
		t.Errorf("expected id_unknown to be error, got id=%q isError=%v", results[1].id, results[1].isError)
	}
	// id_denied should be error
	if results[2].id != "id_denied" || !results[2].isError {
		t.Errorf("expected id_denied to be error, got id=%q isError=%v", results[2].id, results[2].isError)
	}
}

func TestToolExecResult_Struct(t *testing.T) {
	// Verify the toolExecResult struct can hold results correctly
	results := make([]toolExecResult, 3)

	results[0] = toolExecResult{
		result:  ToolResult{Content: "file contents", IsError: false},
		elapsed: 50 * time.Millisecond,
	}
	results[1] = toolExecResult{
		result:  ToolResult{Content: "search results", IsError: false},
		elapsed: 120 * time.Millisecond,
	}
	results[2] = toolExecResult{
		err: fmt.Errorf("network timeout"),
	}

	// Verify index-based access preserves ordering
	if results[0].result.Content != "file contents" {
		t.Errorf("results[0]: expected 'file contents', got %q", results[0].result.Content)
	}
	if results[1].result.Content != "search results" {
		t.Errorf("results[1]: expected 'search results', got %q", results[1].result.Content)
	}
	if results[2].err == nil || results[2].err.Error() != "network timeout" {
		t.Errorf("results[2]: expected 'network timeout' error, got %v", results[2].err)
	}
}
