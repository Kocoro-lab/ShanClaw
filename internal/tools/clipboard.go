package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type ClipboardTool struct{}

type clipboardArgs struct {
	Action  string `json:"action"`
	Content string `json:"content,omitempty"`
}

func (t *ClipboardTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "clipboard",
		Description: "Read or write the system clipboard (macOS only). Use action 'read' to get clipboard contents, 'write' to set them.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":  map[string]any{"type": "string", "description": "Action: 'read' or 'write'"},
				"content": map[string]any{"type": "string", "description": "Content to write (required for write action)"},
			},
		},
		Required: []string{"action"},
	}
}

func (t *ClipboardTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args clipboardArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	switch args.Action {
	case "read":
		cmd := exec.CommandContext(ctx, "pbpaste")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("clipboard read error: %v", err), IsError: true}, nil
		}
		return agent.ToolResult{Content: string(output)}, nil

	case "write":
		if args.Content == "" {
			return agent.ToolResult{Content: "content is required for write action", IsError: true}, nil
		}
		cmd := exec.CommandContext(ctx, "pbcopy")
		cmd.Stdin = bytes.NewReader([]byte(args.Content))
		if err := cmd.Run(); err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("clipboard write error: %v", err), IsError: true}, nil
		}
		return agent.ToolResult{Content: fmt.Sprintf("wrote %d bytes to clipboard", len(args.Content))}, nil

	default:
		return agent.ToolResult{Content: fmt.Sprintf("unknown action: %q (use 'read' or 'write')", args.Action), IsError: true}, nil
	}
}

func (t *ClipboardTool) RequiresApproval() bool { return true }
