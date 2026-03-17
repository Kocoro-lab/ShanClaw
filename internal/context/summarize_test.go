package context

import (
	"context"
	"strings"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

// mockCompleter implements Completer for testing.
type mockCompleter struct {
	response *client.CompletionResponse
	err      error
	lastReq  *client.CompletionRequest
}

func (m *mockCompleter) Complete(ctx context.Context, req client.CompletionRequest) (*client.CompletionResponse, error) {
	m.lastReq = &req
	return m.response, m.err
}

func TestGenerateSummary(t *testing.T) {
	t.Run("produces summary from conversation", func(t *testing.T) {
		mock := &mockCompleter{
			response: &client.CompletionResponse{
				OutputText: "User asked to fix a bug in main.go. Assistant read the file, found a nil pointer, and applied a fix.",
			},
		}

		messages := []client.Message{
			{Role: "system", Content: client.NewTextContent("You are helpful.")},
			{Role: "user", Content: client.NewTextContent("fix the bug in main.go")},
			{Role: "assistant", Content: client.NewTextContent("I'll read the file first.")},
			{Role: "user", Content: client.NewTextContent("file_read result: ...")},
			{Role: "assistant", Content: client.NewTextContent("Found a nil pointer. Fixing now.")},
		}

		summary, err := GenerateSummary(context.Background(), mock, messages)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if summary == "" {
			t.Error("summary should not be empty")
		}

		// Verify it used small tier
		if mock.lastReq.ModelTier != "small" {
			t.Errorf("should use small tier, got %q", mock.lastReq.ModelTier)
		}

		// Verify temperature is low
		if mock.lastReq.Temperature != 0.2 {
			t.Errorf("should use temperature 0.2, got %f", mock.lastReq.Temperature)
		}
	})

	t.Run("returns error on LLM failure", func(t *testing.T) {
		mock := &mockCompleter{
			err: context.DeadlineExceeded,
		}

		messages := []client.Message{
			{Role: "user", Content: client.NewTextContent("hello")},
		}

		_, err := GenerateSummary(context.Background(), mock, messages)
		if err == nil {
			t.Error("expected error when LLM fails")
		}
	})

	t.Run("skips system message in summary input", func(t *testing.T) {
		mock := &mockCompleter{
			response: &client.CompletionResponse{
				OutputText: "Summary of conversation.",
			},
		}

		messages := []client.Message{
			{Role: "system", Content: client.NewTextContent("long system prompt here")},
			{Role: "user", Content: client.NewTextContent("do something")},
			{Role: "assistant", Content: client.NewTextContent("done")},
		}

		_, err := GenerateSummary(context.Background(), mock, messages)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// The summarization request should not include the system prompt in its messages
		// (it's wasteful and the system prompt is always kept separately)
		for _, msg := range mock.lastReq.Messages {
			if msg.Role == "system" && msg.Content.Text() == "long system prompt here" {
				t.Error("should not pass the original system prompt to summarization call")
			}
		}
	})

	t.Run("includes block content in transcript", func(t *testing.T) {
		mock := &mockCompleter{
			response: &client.CompletionResponse{
				OutputText: "Summary with tool context.",
			},
		}

		// Build a message with tool_use and tool_result blocks
		assistantBlocks := []client.ContentBlock{
			{Type: "text", Text: "Let me read the file."},
			client.NewToolUseBlock("call1", "file_read", []byte(`{"path":"/tmp/foo.go"}`)),
		}
		resultBlocks := []client.ContentBlock{
			client.NewToolResultBlock("call1", "package main\nfunc main() {}", false),
		}

		messages := []client.Message{
			{Role: "system", Content: client.NewTextContent("system")},
			{Role: "user", Content: client.NewTextContent("read foo.go")},
			{Role: "assistant", Content: client.NewBlockContent(assistantBlocks)},
			{Role: "user", Content: client.NewBlockContent(resultBlocks)},
		}

		_, err := GenerateSummary(context.Background(), mock, messages)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// The transcript sent to the LLM should contain tool call and result info
		transcript := mock.lastReq.Messages[1].Content.Text()
		if !strings.Contains(transcript, "file_read") {
			t.Error("transcript should include tool_use name 'file_read'")
		}
		if !strings.Contains(transcript, "package main") {
			t.Error("transcript should include tool_result content")
		}
	})
}
