package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

func TestFileWrite_RejectsUnreadExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	os.WriteFile(path, []byte("original"), 0644)

	tracker := agent.NewReadTracker()
	ctx := context.WithValue(context.Background(), agent.ReadTrackerKey(), tracker)

	tool := &FileWriteTool{}
	args, _ := json.Marshal(fileWriteArgs{
		Path:    path,
		Content: "overwritten",
	})

	result, err := tool.Run(ctx, string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result when overwriting unread file")
	}
	if !contains(result.Content, "file_read") {
		t.Errorf("error message should mention file_read, got: %s", result.Content)
	}

	// Verify the file was NOT modified
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Error("file should not have been modified")
	}
}

func TestFileWrite_AllowsNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	tracker := agent.NewReadTracker()
	ctx := context.WithValue(context.Background(), agent.ReadTrackerKey(), tracker)

	tool := &FileWriteTool{}
	args, _ := json.Marshal(fileWriteArgs{
		Path:    path,
		Content: "new content",
	})

	result, err := tool.Run(ctx, string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success for new file, got error: %s", result.Content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "new content" {
		t.Errorf("expected 'new content', got: %s", string(data))
	}
}

func TestFileWrite_AllowsReadExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	os.WriteFile(path, []byte("original"), 0644)

	tracker := agent.NewReadTracker()
	tracker.MarkRead(path)
	ctx := context.WithValue(context.Background(), agent.ReadTrackerKey(), tracker)

	tool := &FileWriteTool{}
	args, _ := json.Marshal(fileWriteArgs{
		Path:    path,
		Content: "overwritten",
	})

	result, err := tool.Run(ctx, string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "overwritten" {
		t.Errorf("expected 'overwritten', got: %s", string(data))
	}
}
