package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type GrepTool struct{}

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

func (t *GrepTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "grep",
		Description: "Search file CONTENTS using a regex pattern. Returns matching lines with filenames and line numbers. Use glob to find files by name pattern instead.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":     map[string]any{"type": "string", "description": "Regex pattern to search"},
				"path":        map[string]any{"type": "string", "description": "Directory or file to search (default: current dir)"},
				"glob":        map[string]any{"type": "string", "description": "File glob filter (e.g. '*.csv', '*.txt', '*.go')"},
				"max_results": map[string]any{"type": "integer", "description": "Max results (default: 100)"},
			},
		},
		Required: []string{"pattern"},
	}
}

func (t *GrepTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args grepArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	path := ExpandHome(args.Path)
	if path == "" {
		path = "."
	}
	maxResults := args.MaxResults
	if maxResults == 0 {
		maxResults = 100
	}

	cmdArgs := []string{"-n", "--max-count", fmt.Sprintf("%d", maxResults)}
	if args.Glob != "" {
		cmdArgs = append(cmdArgs, "--glob", args.Glob)
	}
	cmdArgs = append(cmdArgs, args.Pattern, path)

	bin := "rg"
	if _, err := exec.LookPath("rg"); err != nil {
		bin = "grep"
		cmdArgs = []string{"-rn", "--max-count", fmt.Sprintf("%d", maxResults), args.Pattern, path}
	}

	cmd := exec.CommandContext(ctx, bin, cmdArgs...)
	output, err := cmd.CombinedOutput()
	result := string(output)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return agent.ToolResult{Content: "no matches found"}, nil
		}
		// Exit code 2 in rg/grep covers multiple failure modes: bad regex,
		// missing paths, permission errors, etc. Classify by stderr content
		// rather than assuming all exit-2 is regex syntax.
		lower := strings.ToLower(result)
		switch {
		case strings.Contains(lower, "regex") || strings.Contains(lower, "syntax") || strings.Contains(lower, "parse error"):
			return agent.ValidationError(fmt.Sprintf("invalid regex pattern: %s", result)), nil
		case strings.Contains(lower, "permission denied"):
			return agent.PermissionError(fmt.Sprintf("grep: %s", result)), nil
		case strings.Contains(lower, "no such file") || strings.Contains(lower, "not found"):
			return agent.ValidationError(fmt.Sprintf("path not found: %s", result)), nil
		default:
			return agent.ToolResult{Content: fmt.Sprintf("grep error: %v\n%s", err, result), IsError: true}, nil
		}
	}

	return agent.ToolResult{Content: result}, nil
}

func (t *GrepTool) RequiresApproval() bool { return true }

func (t *GrepTool) IsSafeArgs(argsJSON string) bool {
	var args grepArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return false
	}
	path := args.Path
	if path == "" {
		path = "."
	}
	return isPathUnderCWD(path)
}
