package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

func TestFileEdit_RejectsUnreadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world"), 0644)

	// Create a context with a ReadTracker that has NOT read this file
	tracker := agent.NewReadTracker()
	ctx := context.WithValue(context.Background(), agent.ReadTrackerKey(), tracker)

	tool := &FileEditTool{}
	args, _ := json.Marshal(fileEditArgs{
		Path:      path,
		OldString: "hello",
		NewString: "goodbye",
	})

	result, err := tool.Run(ctx, string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result when file not read first")
	}
	if !contains(result.Content, "file_read") {
		t.Errorf("error message should mention file_read, got: %s", result.Content)
	}

	// Verify the file was NOT modified
	data, _ := os.ReadFile(path)
	if string(data) != "hello world" {
		t.Error("file should not have been modified")
	}
}

func TestFileEdit_AllowsReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world"), 0644)

	// Create a context with a ReadTracker that HAS read this file
	tracker := agent.NewReadTracker()
	tracker.MarkRead(path)
	ctx := context.WithValue(context.Background(), agent.ReadTrackerKey(), tracker)

	tool := &FileEditTool{}
	args, _ := json.Marshal(fileEditArgs{
		Path:      path,
		OldString: "hello",
		NewString: "goodbye",
	})

	result, err := tool.Run(ctx, string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "goodbye world" {
		t.Errorf("expected 'goodbye world', got: %s", string(data))
	}
}

func TestFileEdit_NoTrackerInContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world"), 0644)

	// No tracker in context (e.g., tool called outside agent loop) - should allow
	tool := &FileEditTool{}
	args, _ := json.Marshal(fileEditArgs{
		Path:      path,
		OldString: "hello",
		NewString: "goodbye",
	})

	result, err := tool.Run(context.Background(), string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success without tracker, got error: %s", result.Content)
	}
}
