package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type FileEditTool struct{}

type fileEditArgs struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

func (t *FileEditTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "file_edit",
		Description: "Replace an exact string in a file (old_string must appear exactly once). Prefer this over file_write for targeted edits. If old_string is not unique, use file_read then file_write instead.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string", "description": "File path to edit"},
				"old_string": map[string]any{"type": "string", "description": "Exact string to find (must be unique)"},
				"new_string": map[string]any{"type": "string", "description": "Replacement string"},
			},
		},
		Required: []string{"path", "old_string", "new_string"},
	}
}

func (t *FileEditTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args fileEditArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}
	args.Path = ExpandHome(args.Path)

	// Enforce read-before-edit
	if err := agent.CheckReadBeforeWrite(ctx, args.Path); err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	// Block file_edit on the agent's MEMORY.md — use memory_append instead.
	// file_edit is a read-modify-write that races under concurrent sessions.
	if agent.IsMemoryFile(ctx, args.Path) {
		return agent.ToolResult{
			Content: "Cannot edit MEMORY.md with file_edit — it races under concurrent sessions. Use the memory_append tool instead.",
			IsError: true,
		}, nil
	}

	data, err := os.ReadFile(args.Path)
	if err != nil {
		if os.IsPermission(err) {
			return agent.PermissionError(fmt.Sprintf("cannot read %s: permission denied", args.Path)), nil
		}
		return agent.ToolResult{Content: fmt.Sprintf("error reading file: %v", err), IsError: true}, nil
	}

	if args.OldString == "" {
		return agent.ValidationError("old_string must not be empty"), nil
	}

	content := string(data)
	count := strings.Count(content, args.OldString)
	if count == 0 {
		return agent.ValidationError("old_string not found in file"), nil
	}
	if count > 1 {
		return agent.ValidationError(fmt.Sprintf("old_string found %d times (must be unique)", count)), nil
	}

	newContent := strings.Replace(content, args.OldString, args.NewString, 1)
	// Preserve original file permissions
	perm := os.FileMode(0644)
	if info, err := os.Stat(args.Path); err == nil {
		perm = info.Mode().Perm()
	}
	if err := os.WriteFile(args.Path, []byte(newContent), perm); err != nil {
		if os.IsPermission(err) {
			return agent.PermissionError(fmt.Sprintf("cannot write %s: permission denied", args.Path)), nil
		}
		return agent.ToolResult{Content: fmt.Sprintf("error writing file: %v", err), IsError: true}, nil
	}

	return agent.ToolResult{Content: fmt.Sprintf("edited %s: replaced 1 occurrence", args.Path)}, nil
}

func (t *FileEditTool) RequiresApproval() bool { return true }
