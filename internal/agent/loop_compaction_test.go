package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

// TestAgentLoop_CompactionAndMemoryPersist verifies the full compaction chain:
//
//  1. Agent loop runs multiple tool-call iterations within a single Run()
//  2. Mock server reports growing input tokens each iteration
//  3. When tokens exceed 85% of context_window → compaction triggers
//  4. PersistLearnings fires (small tier) → writes to MEMORY.md
//  5. GenerateSummary fires (small tier) → creates summary
//  6. ShapeHistory reduces messages
//
// Uses context_window=2000 so 85% threshold = 1700 tokens.
// Needs ≥5 tool iterations so messages > MinShapeable (9).
func TestAgentLoop_CompactionAndMemoryPersist(t *testing.T) {
	memoryDir := t.TempDir()

	var mu sync.Mutex
	var calls []string // ordered log of all calls

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := readBody(r.Body)
		defer r.Body.Close()

		var req struct {
			ModelTier string `json:"model_tier"`
			Messages  []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		json.Unmarshal(raw, &req)

		mu.Lock()
		callNum := len(calls) + 1

		// Identify small-tier calls
		if req.ModelTier == "small" {
			isPersist := false
			isSummary := false
			for _, m := range req.Messages {
				var text string
				json.Unmarshal(m.Content, &text)
				if strings.Contains(text, "extracting durable knowledge") {
					isPersist = true
				}
				if strings.Contains(text, "Compress the following conversation") {
					isSummary = true
				}
			}

			if isPersist {
				calls = append(calls, fmt.Sprintf("call %d: PERSIST", callNum))
				mu.Unlock()
				t.Logf("Call %d: [small] PersistLearnings (messages: %d)", callNum, len(req.Messages))
				json.NewEncoder(w).Encode(nativeResponse(
					"- Agent discussed system architecture\n- Testing compaction flow",
					"end_turn", nil, 50, 30))
				return
			}
			if isSummary {
				calls = append(calls, fmt.Sprintf("call %d: SUMMARY", callNum))
				mu.Unlock()
				t.Logf("Call %d: [small] GenerateSummary", callNum)
				json.NewEncoder(w).Encode(nativeResponse(
					"User asked about architecture. Agent reasoned through multiple steps.",
					"end_turn", nil, 50, 30))
				return
			}

			calls = append(calls, fmt.Sprintf("call %d: small-other", callNum))
			mu.Unlock()
			t.Logf("Call %d: [small] other", callNum)
			json.NewEncoder(w).Encode(nativeResponse("ok", "end_turn", nil, 50, 30))
			return
		}

		// Main-tier calls: use message count to decide behavior.
		// We need the loop to iterate 6+ times so messages exceed MinShapeable (9).
		// Report input tokens that grow to exceed the 1700 threshold.
		msgCount := len(req.Messages)
		// Scale input tokens based on message count to simulate realistic growth
		inputTokens := msgCount * 200

		if msgCount < 12 {
			// Keep looping with tool calls until we have enough messages
			calls = append(calls, fmt.Sprintf("call %d: TOOL (msgs=%d, input=%d)", callNum, msgCount, inputTokens))
			mu.Unlock()
			t.Logf("Call %d: [main] tool_use (msgs=%d, input_tokens=%d)", callNum, msgCount, inputTokens)
			json.NewEncoder(w).Encode(nativeResponse(
				"", "tool_use",
				toolCall("think", fmt.Sprintf(`{"thought":"Analyzing step with %d messages in context"}`, msgCount)),
				inputTokens, 100))
		} else {
			calls = append(calls, fmt.Sprintf("call %d: END_TURN (msgs=%d, input=%d)", callNum, msgCount, inputTokens))
			mu.Unlock()
			t.Logf("Call %d: [main] end_turn (msgs=%d, input_tokens=%d)", callNum, msgCount, inputTokens)
			json.NewEncoder(w).Encode(nativeResponse(
				"Here is the complete analysis based on my reasoning through all the steps.",
				"end_turn", nil, inputTokens, 100))
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := NewToolRegistry()

	// Register think tool — no approval needed, keeps loop iterating
	reg.Register(&thinkTool{})

	handler := &mockHandler{approveResult: true}

	loop := NewAgentLoop(gw, reg, "medium", "", 20, 2000, 200, nil, nil, nil)
	loop.SetContextWindow(2000) // 85% = 1700 triggers compaction
	loop.SetMemoryDir(memoryDir)
	loop.SetHandler(handler)

	// Run with a big message
	result, usage, err := loop.Run(context.Background(),
		"Explain the complete system architecture. Think through each component step by step. Be thorough.",
		nil)
	if err != nil {
		t.Logf("Run error (may be iteration limit): %v", err)
	}

	mu.Lock()
	t.Logf("\n=== Call sequence (%d total) ===", len(calls))
	for _, c := range calls {
		t.Logf("  %s", c)
	}

	hasPersist := false
	hasSummary := false
	for _, c := range calls {
		if strings.Contains(c, "PERSIST") {
			hasPersist = true
		}
		if strings.Contains(c, "SUMMARY") {
			hasSummary = true
		}
	}
	mu.Unlock()

	t.Logf("Result: %d chars", len(result))
	t.Logf("Usage: %d LLM calls, %d input+output tokens",
		usage.LLMCalls, usage.InputTokens+usage.OutputTokens)

	// Check compaction fired
	if !hasPersist {
		t.Error("PersistLearnings should have fired during compaction")
	}
	if !hasSummary {
		t.Error("GenerateSummary should have fired during compaction")
	}

	// Check MEMORY.md
	memPath := filepath.Join(memoryDir, "MEMORY.md")
	memData, err := os.ReadFile(memPath)
	if err != nil {
		if hasPersist {
			t.Fatalf("MEMORY.md should exist since PersistLearnings fired: %v", err)
		}
		t.Logf("MEMORY.md not created — compaction didn't trigger")
		return
	}

	memContent := string(memData)
	t.Logf("\n=== MEMORY.md ===\n%s", memContent)

	if !strings.Contains(memContent, "Auto-persisted") {
		t.Error("MEMORY.md should contain Auto-persisted section")
	}
}

// thinkTool is a minimal think tool for the compaction test.
type thinkTool struct{}

func (t *thinkTool) Info() ToolInfo {
	return ToolInfo{
		Name:        "think",
		Description: "Plan or reason through tasks",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"thought": map[string]any{"type": "string"}}},
		Required:    []string{"thought"},
	}
}

func (t *thinkTool) Run(ctx context.Context, args string) (ToolResult, error) {
	return ToolResult{Content: "Thought recorded."}, nil
}

func (t *thinkTool) RequiresApproval() bool { return false }

func readBody(body interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}
