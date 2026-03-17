package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type DirectoryListTool struct{}

type dirListArgs struct {
	Path string `json:"path"`
}

func (t *DirectoryListTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "directory_list",
		Description: "List files and directories at a specific path. Use for exploring one directory. Use glob to find files by pattern across subdirectories.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Directory path (default: current dir)"},
			},
		},
		Required: nil,
	}
}

func (t *DirectoryListTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args dirListArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	path := ExpandHome(args.Path)
	if path == "" {
		path = "."
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error: %v", err), IsError: true}, nil
	}

	var sb strings.Builder
	for _, e := range entries {
		info, _ := e.Info()
		prefix := "  "
		if e.IsDir() {
			prefix = "d "
		}
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		fmt.Fprintf(&sb, "%s %8d %s\n", prefix, size, e.Name())
	}

	return agent.ToolResult{Content: sb.String()}, nil
}

func (t *DirectoryListTool) RequiresApproval() bool { return true }

func (t *DirectoryListTool) IsSafeArgs(argsJSON string) bool {
	var args dirListArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return false
	}
	path := args.Path
	if path == "" {
		path = "."
	}
	return isPathUnderCWD(path)
}
