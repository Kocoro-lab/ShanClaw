package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type NotifyTool struct{}

type notifyArgs struct {
	Title   string `json:"title"`
	Body    string `json:"body,omitempty"`
	Message string `json:"message,omitempty"` // alias for body
	Sound   bool   `json:"sound,omitempty"`
}

func (t *NotifyTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "notify",
		Description: "Send a macOS desktop notification using osascript.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{"type": "string", "description": "Notification title"},
				"body":    map[string]any{"type": "string", "description": "Notification body text (alias: message)"},
			"message": map[string]any{"type": "string", "description": "Alias for body"},
				"sound": map[string]any{"type": "boolean", "description": "Play notification sound (default: false)"},
			},
		},
		Required: []string{"title"},
	}
}

func (t *NotifyTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args notifyArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	body := args.Body
	if body == "" {
		body = args.Message
	}

	script := buildNotifyScript(args.Title, body, args.Sound)

	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("notification error: %v\n%s", err, string(output)), IsError: true}, nil
	}

	return agent.ToolResult{Content: "notification sent"}, nil
}

func buildNotifyScript(title, body string, sound bool) string {
	title = escapeAppleScript(title)
	body = escapeAppleScript(body)

	script := fmt.Sprintf(`display notification "%s" with title "%s"`, body, title)
	if sound {
		script += ` sound name "default"`
	}
	return script
}

func (t *NotifyTool) RequiresApproval() bool { return true }
