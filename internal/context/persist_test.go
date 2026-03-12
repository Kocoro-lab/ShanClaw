package context

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kocoro-lab/shan/internal/client"
)

func TestPersistLearnings(t *testing.T) {
	messages := []client.Message{
		{Role: "system", Content: client.NewTextContent("system prompt")},
		{Role: "user", Content: client.NewTextContent("fix the auth bug")},
		{Role: "assistant", Content: client.NewTextContent("Found that tokens expire after 1 hour, not 24.")},
	}

	t.Run("appends learnings to MEMORY.md", func(t *testing.T) {
		dir := t.TempDir()
		mock := &mockCompleter{
			response: &client.CompletionResponse{
				OutputText: "- Auth tokens expire after 1 hour\n- User prefers direct fixes over explanations",
			},
		}

		err := PersistLearnings(context.Background(), mock, messages, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
		if err != nil {
			t.Fatalf("MEMORY.md not created: %v", err)
		}

		content := string(data)
		if !strings.Contains(content, "Auth tokens expire") {
			t.Error("should contain persisted learning")
		}
		if !strings.Contains(content, "Auto-persisted") {
			t.Error("should contain auto-persisted header")
		}

		// Verify small model used
		if mock.lastReq.ModelTier != "small" {
			t.Errorf("should use small tier, got %q", mock.lastReq.ModelTier)
		}
	})

	t.Run("skips when LLM returns NONE", func(t *testing.T) {
		dir := t.TempDir()
		mock := &mockCompleter{
			response: &client.CompletionResponse{OutputText: "NONE"},
		}

		err := PersistLearnings(context.Background(), mock, messages, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// MEMORY.md should not be created
		if _, err := os.Stat(filepath.Join(dir, "MEMORY.md")); err == nil {
			t.Error("MEMORY.md should not be created when nothing to persist")
		}
	})

	t.Run("skips when memoryDir is empty", func(t *testing.T) {
		mock := &mockCompleter{
			response: &client.CompletionResponse{OutputText: "- something"},
		}

		err := PersistLearnings(context.Background(), mock, messages, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should not even call the completer
		if mock.lastReq != nil {
			t.Error("should not make LLM call when memoryDir is empty")
		}
	})

	t.Run("includes existing memory to avoid duplicates", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- Existing fact"), 0644)

		mock := &mockCompleter{
			response: &client.CompletionResponse{OutputText: "- New fact only"},
		}

		err := PersistLearnings(context.Background(), mock, messages, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify existing memory was included in the prompt
		userMsg := mock.lastReq.Messages[1].Content.Text()
		if !strings.Contains(userMsg, "Existing fact") {
			t.Error("should include existing memory in prompt to avoid duplicates")
		}
	})

	t.Run("overflows to detail file when MEMORY.md is large", func(t *testing.T) {
		dir := t.TempDir()

		// Create a large MEMORY.md close to the limit
		var lines []string
		for i := 0; i < maxMemoryLines-1; i++ {
			lines = append(lines, "- existing line")
		}
		os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(strings.Join(lines, "\n")), 0644)

		mock := &mockCompleter{
			response: &client.CompletionResponse{
				OutputText: "- New learning 1\n- New learning 2\n- New learning 3",
			},
		}

		err := PersistLearnings(context.Background(), mock, messages, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// MEMORY.md should have a pointer, not full content
		data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
		content := string(data)
		if !strings.Contains(content, "auto-") {
			t.Error("should contain pointer to detail file")
		}

		// A detail file should exist
		entries, _ := os.ReadDir(dir)
		found := false
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "auto-") && e.Name() != "MEMORY.md" {
				found = true
				detailData, _ := os.ReadFile(filepath.Join(dir, e.Name()))
				if !strings.Contains(string(detailData), "New learning 1") {
					t.Error("detail file should contain the learnings")
				}
			}
		}
		if !found {
			t.Error("should have created a detail file")
		}
	})

	t.Run("returns error on LLM failure", func(t *testing.T) {
		dir := t.TempDir()
		mock := &mockCompleter{err: context.DeadlineExceeded}

		err := PersistLearnings(context.Background(), mock, messages, dir)
		if err == nil {
			t.Error("expected error when LLM fails")
		}
	})
}
