package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/Kocoro-lab/shan/internal/agent"
)

// WaitTool wraps the ax_server wait_for method.
type WaitTool struct {
	client *AXClient
}

type waitArgs struct {
	Condition string  `json:"condition"`
	Value     string  `json:"value,omitempty"`
	Query     string  `json:"query,omitempty"`
	Role      string  `json:"role,omitempty"`
	App       string  `json:"app,omitempty"`
	Timeout   float64 `json:"timeout,omitempty"`
	Interval  float64 `json:"interval,omitempty"`
}

func (t *WaitTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "wait_for",
		Description: "Wait for a UI condition instead of fixed delays. Use after navigation, app launch, or actions that trigger async changes. Conditions: elementExists, elementGone, titleContains, urlContains, titleChanged, urlChanged. Always use this instead of 'sleep' or 'bash sleep'.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"condition": map[string]any{"type": "string", "description": "Condition to wait for: elementExists, elementGone, titleContains, urlContains, titleChanged, urlChanged"},
				"value":     map[string]any{"type": "string", "description": "Substring to match (for titleContains, urlContains)"},
				"query":     map[string]any{"type": "string", "description": "Text to search for (for elementExists, elementGone)"},
				"role":      map[string]any{"type": "string", "description": "AX role filter (for elementExists, elementGone, e.g. AXButton)"},
				"app":       map[string]any{"type": "string", "description": "Target app name (defaults to frontmost app)"},
				"timeout":   map[string]any{"type": "number", "description": "Max seconds to wait (default: 10)"},
				"interval":  map[string]any{"type": "number", "description": "Poll interval in seconds (default: 0.5)"},
			},
			"required": []string{"condition"},
		},
		Required: []string{"condition"},
	}
}

func (t *WaitTool) RequiresApproval() bool { return true }

func (t *WaitTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	if runtime.GOOS != "darwin" || t.client == nil {
		return agent.ToolResult{Content: "wait_for tool is only available on macOS", IsError: true}, nil
	}

	var args waitArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	if args.Condition == "" {
		return agent.ToolResult{Content: "missing required parameter: condition", IsError: true}, nil
	}

	// Resolve PID from app name
	var pid int
	if args.App != "" {
		if !validAppNamePattern.MatchString(args.App) {
			return agent.ToolResult{
				Content: fmt.Sprintf("invalid app name %q", args.App),
				IsError: true,
			}, nil
		}
		result, err := t.client.Call(ctx, "resolve_pid", map[string]any{"app_name": args.App})
		if err != nil {
			return agent.ToolResult{
				Content: fmt.Sprintf("app %q not found or not running", args.App),
				IsError: true,
			}, nil
		}
		var pidResult struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(result, &pidResult); err != nil {
			return agent.ToolResult{
				Content: fmt.Sprintf("could not parse PID for %q", args.App),
				IsError: true,
			}, nil
		}
		pid = pidResult.PID
	}

	params := map[string]any{
		"condition": args.Condition,
	}
	if pid > 0 {
		params["pid"] = pid
	}
	if args.Value != "" {
		params["value"] = args.Value
	}
	if args.Query != "" {
		params["query"] = args.Query
	}
	if args.Role != "" {
		params["role"] = args.Role
	}
	if args.Timeout > 0 {
		params["timeout"] = args.Timeout
	}
	if args.Interval > 0 {
		params["interval"] = args.Interval
	}

	result, err := t.client.Call(ctx, "wait_for", params)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("wait_for: %v", err), IsError: true}, nil
	}

	var actionResult struct {
		Result string `json:"result"`
	}
	json.Unmarshal(result, &actionResult)
	return agent.ToolResult{Content: actionResult.Result}, nil
}
