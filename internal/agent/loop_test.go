package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestAgentLoop_PlanThenExecute verifies continuation when model plans without
// tool calls first (numbered steps detected), then executes on next iteration.
func TestAgentLoop_PlanThenExecute(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1:
			// Model outputs a plan with numbered steps — triggers continuation
			json.NewEncoder(w).Encode(nativeResponse(
				"Plan:\n1. Read the file\n2. Edit the config\n3. Verify changes", "end_turn", nil, 10, 5))
		case 2:
			// After continuation, model executes the plan with tool calls
			json.NewEncoder(w).Encode(nativeResponse("Reading...", "tool_use",
				toolCall("mock_tool", `{"action":"read"}`), 10, 5))
		case 3:
			// Final summary after tool use — stops immediately (totalToolCalls > 0)
			json.NewEncoder(w).Encode(nativeResponse("Done. File updated.", "end_turn", nil, 10, 5))
		default:
			json.NewEncoder(w).Encode(nativeResponse("unexpected", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "update the config file", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Done. File updated." {
		t.Errorf("unexpected result: %q", result)
	}
	// Plan (1, continue) → tool call (2) → text summary (3, stop) = 3 calls
	if callCount != 3 {
		t.Errorf("expected 3 LLM calls (plan + tool + summary), got %d", callCount)
	}
}

// TestAgentLoop_PlanChinese verifies plan detection works with Chinese text.
func TestAgentLoop_PlanChinese(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1:
			json.NewEncoder(w).Encode(nativeResponse(
				"我的计划：\n1. 读取配置文件\n2. 修改设置\n3. 验证结果", "end_turn", nil, 10, 5))
		case 2:
			json.NewEncoder(w).Encode(nativeResponse("", "tool_use",
				toolCall("mock_tool", `{}`), 10, 5))
		case 3:
			json.NewEncoder(w).Encode(nativeResponse("完成。", "end_turn", nil, 10, 5))
		default:
			json.NewEncoder(w).Encode(nativeResponse("unexpected", "end_turn", nil, 10, 5))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "mock_tool"})
	loop := NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)

	result, _, err := loop.Run(context.Background(), "更新配置", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "完成。" {
		t.Errorf("unexpected result: %q", result)
	}
	if callCount != 3 {
		t.Errorf("expected 3 LLM calls (Chinese plan + tool + summary), got %d", callCount)
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

// TestIsPlanningResponse verifies language-agnostic plan detection.
func TestIsPlanningResponse(t *testing.T) {
	tools := []string{"bash", "file_read", "file_edit", "screenshot"}

	tests := []struct {
		name   string
		text   string
		expect bool
	}{
		{"short direct answer", "The answer is 42.", false},
		{"empty", "", false},
		{"short summary", "Done. File updated.", false},
		{"english numbered plan", "Here's my plan:\n1. Build the project\n2. Run tests\n3. Fix errors", true},
		{"chinese numbered plan", "我的计划：\n1. 读取配置文件\n2. 修改设置\n3. 验证结果", true},
		{"bullet points", "Steps to take:\n- Read the config\n- Update the value\n- Verify", true},
		{"mentions 2 long tools", "I will use file_read to check and file_edit to update the output for the task.", true},
		{"mentions short tools only", "I checked with bash and grep and here is the result of the analysis.", false},
		{"japanese numbered", "手順：\n1、設定ファイルを読む\n2、値を更新する\n3、確認する", true},
		{"long but no structure", strings.Repeat("This is a detailed explanation. ", 10), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPlanningResponse(tt.text, tools)
			if got != tt.expect {
				t.Errorf("isPlanningResponse(%q) = %v, want %v", tt.text[:min(len(tt.text), 60)], got, tt.expect)
			}
		})
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
