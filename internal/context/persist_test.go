package context

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
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

func TestBoundedAppend(t *testing.T) {
	t.Run("appends content directly when under limit", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- existing\n"), 0644)

		err := BoundedAppend(dir, "- new entry")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
		content := string(data)
		if !strings.Contains(content, "existing") || !strings.Contains(content, "new entry") {
			t.Error("should contain both existing and new content")
		}
	})

	t.Run("respects boundary when existing memory has trailing newline", func(t *testing.T) {
		dir := t.TempDir()

		// existing has 149 lines and a trailing newline; without this fix, one extra
		// non-prefixed line could incorrectly fit the cap.
		lines := make([]string, maxMemoryLines-1)
		for i := 0; i < maxMemoryLines-1; i++ {
			lines[i] = "- line"
		}
		existing := strings.Join(lines, "\n") + "\n"
		os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(existing), 0644)

		err := BoundedAppend(dir, "- new line")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
		content := string(data)
		if !strings.Contains(content, "auto-") {
			t.Error("should overflow to detail file when append would exceed cap")
		}
		if strings.Contains(content, "new line") {
			t.Error("overflow content should be in detail file, not MEMORY.md")
		}
	})

	t.Run("overflows to detail file at boundary", func(t *testing.T) {
		dir := t.TempDir()

		// Fill MEMORY.md to just under the limit
		var lines []string
		for i := 0; i < maxMemoryLines-1; i++ {
			lines = append(lines, "- line")
		}
		os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(strings.Join(lines, "\n")), 0644)

		// This 3-line append should overflow
		err := BoundedAppend(dir, "- new1\n- new2\n- new3")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
		content := string(data)
		if !strings.Contains(content, "auto-") {
			t.Error("should contain pointer to detail file")
		}
		if strings.Contains(content, "new1") {
			t.Error("overflow content should be in detail file, not MEMORY.md")
		}

		// Detail file should exist with the content
		entries, _ := os.ReadDir(dir)
		found := false
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "auto-") {
				found = true
				detail, _ := os.ReadFile(filepath.Join(dir, e.Name()))
				if !strings.Contains(string(detail), "new1") {
					t.Error("detail file should contain overflow content")
				}
			}
		}
		if !found {
			t.Error("should have created a detail file")
		}
	})

	t.Run("creates MEMORY.md if missing", func(t *testing.T) {
		dir := t.TempDir()

		err := BoundedAppend(dir, "- first entry")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
		if !strings.Contains(string(data), "first entry") {
			t.Error("should create file with content")
		}
	})
}

func TestConsolidateMemory_SkipsWhenFewFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- user fact 1\n"), 0644)
	// Only 2 auto files (threshold is 12)
	os.WriteFile(filepath.Join(dir, "auto-2026-03-01-aaaaaa.md"), []byte("- fact a\n"), 0644)
	os.WriteFile(filepath.Join(dir, "auto-2026-03-02-bbbbbb.md"), []byte("- fact b\n"), 0644)

	err := ConsolidateMemory(context.Background(), nil, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// MEMORY.md unchanged
	data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if string(data) != "- user fact 1\n" {
		t.Errorf("MEMORY.md should be unchanged, got: %q", string(data))
	}
}

func TestConsolidateMemory_SkipsWhenRecent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- user fact\n"), 0644)
	// 12 auto files
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("auto-2026-03-%02d-%06x.md", i+1, i)
		os.WriteFile(filepath.Join(dir, name), []byte(fmt.Sprintf("- fact %d\n", i)), 0644)
	}
	// Touch marker as recent
	os.WriteFile(filepath.Join(dir, ".memory_gc"), []byte(""), 0644)

	err := ConsolidateMemory(context.Background(), nil, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Auto files should still exist (GC skipped)
	matches, _ := filepath.Glob(filepath.Join(dir, "auto-*.md"))
	if len(matches) != 12 {
		t.Errorf("expected 12 auto files (GC skipped), got %d", len(matches))
	}
}

func TestConsolidateMemory_Consolidates(t *testing.T) {
	dir := t.TempDir()

	// User content + auto-persisted section in MEMORY.md
	memory := "- user preference: dark mode\n- user prefers Go\n\n" +
		"## Auto-persisted (2026-03-01 10:00)\n\n- old auto fact 1\n- old auto fact 2\n\n" +
		"- [2026-03-01] See [auto-2026-03-01-aaaaaa.md](auto-2026-03-01-aaaaaa.md) for details\n"
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(memory), 0644)

	// 12 auto files
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("auto-2026-03-%02d-%06x.md", i+1, i)
		content := fmt.Sprintf("# Auto-persisted Learnings\n\n- detail fact %d\n", i)
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
	}

	// Set marker to 8 days ago so cooldown passes
	markerPath := filepath.Join(dir, ".memory_gc")
	os.WriteFile(markerPath, []byte(""), 0644)
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(markerPath, oldTime, oldTime)

	mock := &mockCompleter{
		response: &client.CompletionResponse{
			OutputText: "- consolidated fact A\n- consolidated fact B",
		},
	}

	err := ConsolidateMemory(context.Background(), mock, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Ensure inline auto-persisted content from MEMORY.md is included in LLM input.
	if !strings.Contains(mock.lastReq.Messages[1].Content.Text(), "old auto fact 1") {
		t.Error("consolidation should include inline auto section in LLM input")
	}

	// Auto files should be deleted
	matches, _ := filepath.Glob(filepath.Join(dir, "auto-*.md"))
	if len(matches) != 0 {
		t.Errorf("expected 0 auto files after GC, got %d", len(matches))
	}

	// MEMORY.md should have user content + consolidated auto content
	data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	result := string(data)
	if !strings.Contains(result, "user preference: dark mode") {
		t.Error("user content should be preserved")
	}
	if !strings.Contains(result, "user prefers Go") {
		t.Error("user content should be preserved")
	}
	if !strings.Contains(result, "consolidated fact A") {
		t.Error("consolidated content should be present")
	}
	if !strings.Contains(result, "Auto-consolidated") {
		t.Error("should have Auto-consolidated header")
	}
	if strings.Contains(result, "old auto fact 1") {
		t.Error("old auto content should be replaced by consolidated version")
	}
	if strings.Contains(result, "See [auto-") {
		t.Error("auto-*.md pointer lines should be removed")
	}

	// Marker file should be updated
	if _, err := os.Stat(filepath.Join(dir, ".memory_gc")); os.IsNotExist(err) {
		t.Error(".memory_gc marker should exist")
	}
}

func TestConsolidateMemory_LLMReturnsNONE(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- user fact\n"), 0644)
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("auto-2026-03-%02d-%06x.md", i+1, i)
		os.WriteFile(filepath.Join(dir, name), []byte(fmt.Sprintf("- stale %d\n", i)), 0644)
	}

	// Set marker to 8 days ago so cooldown passes explicitly
	markerPath := filepath.Join(dir, ".memory_gc")
	os.WriteFile(markerPath, []byte(""), 0644)
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(markerPath, oldTime, oldTime)

	mock := &mockCompleter{
		response: &client.CompletionResponse{OutputText: "NONE"},
	}

	err := ConsolidateMemory(context.Background(), mock, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// MEMORY.md should only have user content
	data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	result := string(data)
	if !strings.Contains(result, "user fact") {
		t.Error("user content should be preserved")
	}
	if strings.Contains(result, "Auto-consolidated") {
		t.Error("should not have auto section when LLM returned NONE")
	}
}

func TestConsolidateMemory_UnreadableFileConsumed(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- user fact\n"), 0644)

	// 11 readable auto files + 1 unreadable = 12 total (meets threshold)
	for i := 0; i < 11; i++ {
		name := fmt.Sprintf("auto-2026-03-%02d-%06x.md", i+1, i)
		os.WriteFile(filepath.Join(dir, name), []byte(fmt.Sprintf("- fact %d\n", i)), 0644)
	}
	// Create an unreadable file (directory with .md name — can't ReadFile a dir)
	unreadable := filepath.Join(dir, "auto-2026-03-12-ffffff.md")
	os.Mkdir(unreadable, 0755)

	// Set marker to 8 days ago
	markerPath := filepath.Join(dir, ".memory_gc")
	os.WriteFile(markerPath, []byte(""), 0644)
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(markerPath, oldTime, oldTime)

	mock := &mockCompleter{
		response: &client.CompletionResponse{OutputText: "- consolidated fact"},
	}

	err := ConsolidateMemory(context.Background(), mock, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The unreadable "file" (dir) should have been removed so threshold drops
	remaining, _ := filepath.Glob(filepath.Join(dir, "auto-*.md"))
	if len(remaining) != 0 {
		t.Errorf("expected 0 auto files remaining (including unreadable), got %d", len(remaining))
	}

	// Consolidation should have produced output from the 11 readable files
	data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if !strings.Contains(string(data), "consolidated fact") {
		t.Error("should contain consolidated output from readable files")
	}
}

func TestConsolidateMemory_MarkerWriteFailure(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- user fact\n"), 0644)

	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("auto-2026-03-%02d-%06x.md", i+1, i)
		os.WriteFile(filepath.Join(dir, name), []byte(fmt.Sprintf("- fact %d\n", i)), 0644)
	}

	// Set old marker so cooldown passes
	markerPath := filepath.Join(dir, ".memory_gc")
	os.WriteFile(markerPath, []byte(""), 0644)
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(markerPath, oldTime, oldTime)

	// Make marker path a directory so WriteFile fails
	os.Remove(markerPath)
	os.Mkdir(markerPath, 0755)
	os.Chtimes(markerPath, oldTime, oldTime) // must be old so cooldown passes

	mock := &mockCompleter{
		response: &client.CompletionResponse{OutputText: "- consolidated"},
	}

	err := ConsolidateMemory(context.Background(), mock, dir)
	if err == nil {
		t.Fatal("expected error when marker write fails")
	}
	if !strings.Contains(err.Error(), "write marker") {
		t.Errorf("error should mention marker write, got: %v", err)
	}

	// MEMORY.md should still be updated (consolidation succeeded, only marker failed)
	data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if !strings.Contains(string(data), "consolidated") {
		t.Error("MEMORY.md should still contain consolidated content despite marker failure")
	}
}

func TestSplitMemory(t *testing.T) {
	input := "- user fact 1\n- user fact 2\n\n" +
		"## Auto-persisted (2026-03-01 10:00)\n\n- auto fact 1\n- auto fact 2\n\n" +
		"- [2026-03-01] See [auto-2026-03-01-aaaaaa.md](auto-2026-03-01-aaaaaa.md) for details\n"

	user, auto := splitMemory(input)

	if !strings.Contains(user, "user fact 1") {
		t.Error("user content should contain user fact 1")
	}
	if !strings.Contains(user, "user fact 2") {
		t.Error("user content should contain user fact 2")
	}
	if strings.Contains(user, "auto fact") {
		t.Error("user content should not contain auto facts")
	}
	if strings.Contains(user, "See [auto-") {
		t.Error("user content should not contain pointer lines")
	}
	if !strings.Contains(auto, "auto fact 1") {
		t.Error("auto content should contain auto fact 1")
	}
	if !strings.Contains(auto, "See [auto-") {
		t.Error("auto content should contain pointer lines")
	}
}

func TestSplitMemory_NoAutoContent(t *testing.T) {
	input := "- user fact 1\n- user fact 2\n"
	user, auto := splitMemory(input)

	if !strings.Contains(user, "user fact 1") {
		t.Error("user content should contain user fact 1")
	}
	if auto != "" {
		t.Errorf("auto content should be empty, got %q", auto)
	}
}

func TestSplitMemory_OnlyAutoContent(t *testing.T) {
	input := "## Auto-persisted (2026-03-01 10:00)\n\n- auto fact 1\n"
	user, auto := splitMemory(input)

	if user != "" {
		t.Errorf("user content should be empty, got %q", user)
	}
	if !strings.Contains(auto, "auto fact 1") {
		t.Error("auto content should contain auto fact 1")
	}
}
