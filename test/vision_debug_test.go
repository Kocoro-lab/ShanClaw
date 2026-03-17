package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/client"
	"github.com/Kocoro-lab/ShanClaw/internal/tools"
)

// TestVisionLoop_FullPipeline verifies that a real screenshot's base64 data
// actually arrives in the API request payload as image content blocks.
func TestVisionLoop_FullPipeline(t *testing.T) {
	var capturedMessages []json.RawMessage

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++

		// Capture raw request body to inspect image blocks
		var raw map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&raw)
		if msgs, ok := raw["messages"]; ok {
			capturedMessages = append(capturedMessages[:0], msgs) // store latest
		}

		if callCount == 1 {
			// First call: tell the model to call screenshot
			json.NewEncoder(w).Encode(client.CompletionResponse{
				OutputText:   "",
				FinishReason: "tool_use",
				FunctionCall: &client.FunctionCall{
					Name:      "screenshot",
					Arguments: json.RawMessage(`{"target":"fullscreen"}`),
				},
				Usage: client.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
			})
		} else {
			// Second call: return response — but first, inspect what was sent
			json.NewEncoder(w).Encode(client.CompletionResponse{
				OutputText:   "I see a desktop",
				FinishReason: "end_turn",
				Usage:        client.Usage{InputTokens: 1000, OutputTokens: 50, TotalTokens: 1050},
			})
		}
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	reg := agent.NewToolRegistry()
	reg.Register(&tools.ScreenshotTool{})
	loop := agent.NewAgentLoop(gw, reg, "medium", "", 10, 50000, 200, nil, nil, nil)
	loop.SetBypassPermissions(true)

	result, usage, err := loop.Run(context.Background(), "take a screenshot", nil)
	if err != nil {
		t.Fatalf("agent loop error: %v", err)
	}

	t.Logf("Result: %s", result)
	t.Logf("LLM calls: %d, tokens: %d", usage.LLMCalls, usage.TotalTokens)

	if callCount < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", callCount)
	}

	// Parse the captured messages from the 2nd API call to verify image blocks
	if len(capturedMessages) == 0 {
		t.Fatal("no messages captured from API request")
	}

	var messages []json.RawMessage
	json.Unmarshal(capturedMessages[0], &messages)

	t.Logf("Messages in 2nd API call: %d", len(messages))

	foundImage := false
	imageBytes := 0
	for i, msgRaw := range messages {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		json.Unmarshal(msgRaw, &msg)

		// Check if content is an array (content blocks)
		var blocks []struct {
			Type   string `json:"type"`
			Text   string `json:"text,omitempty"`
			Source *struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source,omitempty"`
		}
		if err := json.Unmarshal(msg.Content, &blocks); err == nil && len(blocks) > 0 {
			for _, b := range blocks {
				if b.Type == "image" && b.Source != nil {
					foundImage = true
					imageBytes = len(b.Source.Data) * 3 / 4
					t.Logf("✅ msg[%d] role=%s: FOUND IMAGE — %s, %d KB base64 (%d KB raw)",
						i, msg.Role, b.Source.MediaType, len(b.Source.Data)/1024, imageBytes/1024)
				}
				if b.Type == "text" {
					t.Logf("   msg[%d] role=%s: text block: %.80s...", i, msg.Role, b.Text)
				}
			}
		} else {
			// String content
			var s string
			json.Unmarshal(msg.Content, &s)
			if len(s) > 100 {
				s = s[:100] + "..."
			}
			t.Logf("   msg[%d] role=%s: string: %.80s", i, msg.Role, s)
		}
	}

	if !foundImage {
		t.Fatal("❌ NO IMAGE BLOCK found in API request — vision pipeline broken!")
	}
	if imageBytes < 10000 {
		t.Errorf("image seems too small (%d bytes) — may be a broken/empty screenshot", imageBytes)
	}
	fmt.Printf("\n✅ Vision pipeline verified: real screenshot (%d KB) delivered as image content block in API request\n", imageBytes/1024)
}
