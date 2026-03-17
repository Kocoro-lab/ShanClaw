package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type GlobTool struct{}

type globArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

func (t *GlobTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "glob",
		Description: "Find files by path pattern (e.g. '**/*.csv', 'reports/*.pdf', 'src/**/*.go'). Matches file NAMES/paths — not file contents. Use grep to search inside files.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Glob pattern"},
				"path":    map[string]any{"type": "string", "description": "Base directory (default: current dir)"},
			},
		},
		Required: []string{"pattern"},
	}
}

func (t *GlobTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args globArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	root := "."
	if args.Path != "" {
		root = ExpandHome(args.Path)
	}

	matches, err := doublestar.Glob(os.DirFS(root), args.Pattern)
	if err != nil {
		return agent.ValidationError(fmt.Sprintf("glob error: %v", err)), nil
	}

	if len(matches) == 0 {
		return agent.ToolResult{Content: "no files matched"}, nil
	}

	return agent.ToolResult{Content: strings.Join(matches, "\n")}, nil
}

func (t *GlobTool) RequiresApproval() bool { return true }

func (t *GlobTool) IsSafeArgs(argsJSON string) bool {
	var args globArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return false
	}
	path := args.Path
	if path == "" {
		path = "."
	}
	return isPathUnderCWD(path)
}
