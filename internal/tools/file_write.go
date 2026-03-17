package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type FileWriteTool struct{}

type fileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *FileWriteTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "file_write",
		Description: "Write complete content to a file (overwrites entirely). Use for creating new files or as fallback when file_edit fails due to non-unique text. Always file_read first if the file already exists.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "File path to write"},
				"content": map[string]any{"type": "string", "description": "Content to write"},
			},
		},
		Required: []string{"path", "content"},
	}
}

func (t *FileWriteTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args fileWriteArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}
	args.Path = ExpandHome(args.Path)

	// Block file_write on the agent's MEMORY.md — always use memory_append.
	// Check unconditionally (not just for existing files) so first-write
	// scenarios also go through the flock-protected bounded-append path.
	if agent.IsMemoryFile(ctx, args.Path) {
		return agent.ToolResult{
			Content: "Cannot write MEMORY.md with file_write — use the memory_append tool instead.",
			IsError: true,
		}, nil
	}

	// Enforce read-before-write for existing files (new files are fine)
	if _, err := os.Stat(args.Path); err == nil {
		if err := agent.CheckReadBeforeWrite(ctx, args.Path); err != nil {
			return agent.ToolResult{Content: err.Error(), IsError: true}, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(args.Path), 0755); err != nil {
		if os.IsPermission(err) {
			return agent.PermissionError(fmt.Sprintf("cannot create directory %s: permission denied", filepath.Dir(args.Path))), nil
		}
		return agent.ToolResult{Content: fmt.Sprintf("error creating directory: %v", err), IsError: true}, nil
	}

	if err := os.WriteFile(args.Path, []byte(args.Content), 0644); err != nil {
		if os.IsPermission(err) {
			return agent.PermissionError(fmt.Sprintf("cannot write %s: permission denied", args.Path)), nil
		}
		return agent.ToolResult{Content: fmt.Sprintf("error writing file: %v", err), IsError: true}, nil
	}

	return agent.ToolResult{Content: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path)}, nil
}

func (t *FileWriteTool) RequiresApproval() bool { return true }
