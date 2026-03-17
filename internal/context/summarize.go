package context

import (
	"context"
	"fmt"
	"strings"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

const summarizePrompt = `Compress the following conversation into a concise summary. Capture:
- Key decisions made
- Files read, modified, or created
- Current state of the task
- Next steps or unresolved items
- Important errors or blockers encountered

Be factual and brief. Focus on what a continuation of this conversation would need to know.`

// Completer is the interface for making LLM completion calls.
// Satisfied by *client.GatewayClient.
type Completer interface {
	Complete(ctx context.Context, req client.CompletionRequest) (*client.CompletionResponse, error)
}

// GenerateSummary calls the LLM (small tier) to summarize a conversation.
// It strips the system message from the input to avoid wasting tokens.
// Serializes both plain text and block content (tool_use, tool_result).
func GenerateSummary(ctx context.Context, c Completer, messages []client.Message) (string, error) {
	// Build conversation transcript, skipping system messages
	var transcript strings.Builder
	for _, m := range messages {
		if m.Role == "system" {
			continue
		}
		text := messageText(m)
		if text == "" {
			continue
		}
		fmt.Fprintf(&transcript, "[%s]: %s\n\n", m.Role, text)
	}

	req := client.CompletionRequest{
		Messages: []client.Message{
			{Role: "system", Content: client.NewTextContent(summarizePrompt)},
			{Role: "user", Content: client.NewTextContent(transcript.String())},
		},
		ModelTier:   "small",
		Temperature: 0.2,
		MaxTokens:   2000,
	}

	resp, err := c.Complete(ctx, req)
	if err != nil {
		return "", fmt.Errorf("summarization failed: %w", err)
	}

	return resp.OutputText, nil
}

// messageText extracts readable text from a message, handling both plain text
// and block content (tool_use, tool_result, text blocks).
func messageText(m client.Message) string {
	// Plain text message
	if !m.Content.HasBlocks() {
		return m.Content.Text()
	}

	// Block content — serialize each block type
	var sb strings.Builder
	for _, b := range m.Content.Blocks() {
		switch b.Type {
		case "text":
			sb.WriteString(b.Text)
		case "tool_use":
			fmt.Fprintf(&sb, "[tool_call: %s]", b.Name)
		case "tool_result":
			text := client.ToolResultText(b)
			if text != "" {
				// Truncate long tool results for the summary (rune-safe)
				if r := []rune(text); len(r) > 500 {
					text = string(r[:500]) + "..."
				}
				fmt.Fprintf(&sb, "[tool_result: %s]", text)
			}
		}
		sb.WriteString(" ")
	}
	return strings.TrimSpace(sb.String())
}
