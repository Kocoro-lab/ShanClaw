package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/client"
	"github.com/Kocoro-lab/ShanClaw/internal/tools"
)

func TestEndToEnd_FileReadAndAnalyze(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++

		if callCount == 1 {
			// First call: LLM decides to read a file
			json.NewEncoder(w).Encode(client.CompletionResponse{
				Model:        "test-model",
				OutputText:   "",
				FinishReason: "tool_use",
				FunctionCall: &client.FunctionCall{
					Name:      "file_read",
					Arguments: json.RawMessage(`{"path": "go.mod"}`),
				},
				Usage:     client.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				RequestID: "req-test",
			})
		} else {
			// Second call: LLM analyzes the file content
			json.NewEncoder(w).Encode(client.CompletionResponse{
				Model:        "test-model",
				OutputText:   "This is a Go module for shannon-cli.",
				FinishReason: "end_turn",
				Usage:        client.Usage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30},
				RequestID:    "req-test",
			})
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := agent.NewToolRegistry()
	reg.Register(&tools.FileReadTool{})

	loop := agent.NewAgentLoop(gw, reg, "medium", "", 25, 2000, 200, nil, nil, nil)
	result, usage, err := loop.Run(context.Background(), "read go.mod", nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "This is a Go module for shannon-cli." {
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
