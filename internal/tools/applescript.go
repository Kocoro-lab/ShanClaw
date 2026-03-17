package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type AppleScriptTool struct{}

type appleScriptArgs struct {
	Script string `json:"script"`
}

func (t *AppleScriptTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "applescript",
		Description: "Execute an AppleScript script via osascript. Use for opening/activating apps, window management, calendar events, and macOS-specific operations. NOT for web page interaction — use browser tool for web content.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"script": map[string]any{"type": "string", "description": "AppleScript code to execute"},
			},
		},
		Required: []string{"script"},
	}
}

func (t *AppleScriptTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args appleScriptArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	// Split multi-line scripts into separate -e arguments for osascript
	cmdArgs := []string{}
	for _, line := range splitScriptLines(args.Script) {
		cmdArgs = append(cmdArgs, "-e", line)
	}
	cmd := exec.CommandContext(ctx, "osascript", cmdArgs...)
	output, err := cmd.CombinedOutput()

	result := string(output)
	if len(result) > 10240 {
		result = result[:10240] + "\n... (truncated)"
	}

	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("osascript error: %v\n%s", err, result),
			IsError: true,
		}, nil
	}

	var toolResult agent.ToolResult
	if result == "" {
		toolResult = agent.ToolResult{Content: "script executed successfully (no output)"}
	} else {
		toolResult = agent.ToolResult{Content: result}
	}

	// Auto-screenshot after GUI actions so the LLM can verify the outcome.
	// Brief delay to let the UI settle.
	time.Sleep(500 * time.Millisecond)
	_, block, captureErr := CaptureAndEncode(DefaultAPIWidth)
	if captureErr == nil {
		toolResult.Images = []agent.ImageBlock{block}
	}

	return toolResult, nil
}

func (t *AppleScriptTool) RequiresApproval() bool { return true }

// splitScriptLines splits an AppleScript into individual lines for -e args.
// Preserves empty lines as they can be significant in AppleScript blocks.
func splitScriptLines(script string) []string {
	lines := strings.Split(script, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			result = append(result, line)
		}
	}
	if len(result) == 0 {
		return []string{script}
	}
	return result
}

// escapeAppleScript escapes a string for safe embedding in AppleScript string literals.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	return s
}
