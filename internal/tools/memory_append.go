package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	ctxwin "github.com/Kocoro-lab/ShanClaw/internal/context"
)

// MemoryAppendTool appends entries to the agent's MEMORY.md via BoundedAppend,
// preventing concurrent sessions from clobbering each other's writes and
// overflowing to detail files when the line limit is reached.
// The memory directory is read from context (set by AgentLoop.Run).
type MemoryAppendTool struct{}

type memoryAppendArgs struct {
	Content string `json:"content"`
}

func (t *MemoryAppendTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "memory_append",
		Description: "Append new entries to MEMORY.md. Use this instead of file_write or file_edit for memory updates. Writes are atomic, flock-protected, and auto-overflow to detail files when MEMORY.md exceeds the line limit.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "New entries to append (markdown bullet points)",
				},
			},
		},
		Required: []string{"content"},
	}
}

func (t *MemoryAppendTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args memoryAppendArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	if strings.TrimSpace(args.Content) == "" {
		return agent.ToolResult{Content: "content must not be empty", IsError: true}, nil
	}

	memDir := agent.MemoryDirFromContext(ctx)
	if memDir == "" {
		return agent.ToolResult{Content: "memory not configured for this agent", IsError: true}, nil
	}

	if err := ctxwin.BoundedAppend(memDir, args.Content); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("error appending: %v", err), IsError: true}, nil
	}

	return agent.ToolResult{Content: fmt.Sprintf("appended to %s/MEMORY.md", memDir)}, nil
}

func (t *MemoryAppendTool) RequiresApproval() bool { return false }
