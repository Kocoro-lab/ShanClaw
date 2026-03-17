package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type ProcessTool struct{}

type processArgs struct {
	Action string `json:"action"`
	PID    int    `json:"pid,omitempty"`
	Port   int    `json:"port,omitempty"`
}

func (t *ProcessTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "process",
		Description: "Manage processes and ports. Actions: 'list' (ps aux), 'ports' (listening ports), 'kill' (kill a PID).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "description": "Action: 'list', 'ports', or 'kill'"},
				"pid":    map[string]any{"type": "integer", "description": "Process ID (required for kill)"},
				"port":   map[string]any{"type": "integer", "description": "Filter by port number (optional for ports)"},
			},
		},
		Required: []string{"action"},
	}
}

func (t *ProcessTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args processArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	switch args.Action {
	case "list":
		cmd := exec.CommandContext(ctx, "ps", "aux")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("ps error: %v\n%s", err, string(output)), IsError: true}, nil
		}
		result := string(output)
		if len(result) > 30000 {
			result = result[:30000] + "\n... (truncated)"
		}
		return agent.ToolResult{Content: result}, nil

	case "ports":
		cmdArgs := []string{"-i", "-P", "-n"}
		if args.Port > 0 {
			cmdArgs = []string{"-i", fmt.Sprintf(":%d", args.Port), "-P", "-n"}
		}
		cmd := exec.CommandContext(ctx, "lsof", cmdArgs...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			// lsof exits 1 when no results found
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				return agent.ToolResult{Content: "no matching ports found"}, nil
			}
			return agent.ToolResult{Content: fmt.Sprintf("lsof error: %v\n%s", err, string(output)), IsError: true}, nil
		}
		result := string(output)
		if len(result) > 30000 {
			result = result[:30000] + "\n... (truncated)"
		}
		return agent.ToolResult{Content: result}, nil

	case "kill":
		if args.PID == 0 {
			return agent.ToolResult{Content: "pid is required for kill action", IsError: true}, nil
		}
		cmd := exec.CommandContext(ctx, "kill", fmt.Sprintf("%d", args.PID))
		output, err := cmd.CombinedOutput()
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("kill error: %v\n%s", err, string(output)), IsError: true}, nil
		}
		return agent.ToolResult{Content: fmt.Sprintf("sent SIGTERM to PID %d", args.PID)}, nil

	default:
		return agent.ToolResult{Content: fmt.Sprintf("unknown action: %q (use 'list', 'ports', or 'kill')", args.Action), IsError: true}, nil
	}
}

func (t *ProcessTool) RequiresApproval() bool { return true }

func (t *ProcessTool) IsSafeArgs(argsJSON string) bool {
	var args processArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return false
	}
	return args.Action == "list" || args.Action == "ports"
}
