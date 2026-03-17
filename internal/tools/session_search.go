package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/session"
)

type SessionSearchTool struct {
	manager *session.Manager
}

type sessionSearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (t *SessionSearchTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "session_search",
		Description: "Search through past session messages for keyword matches. Includes results from scheduled task runs — use this to check what a schedule found.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search keywords or quoted phrase",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Max results (default 20)",
				},
			},
		},
		Required: []string{"query"},
	}
}

func (t *SessionSearchTool) RequiresApproval() bool { return false }

func (t *SessionSearchTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args sessionSearchArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if args.Query == "" {
		return agent.ToolResult{Content: "query is required", IsError: true}, nil
	}
	if args.Limit <= 0 {
		args.Limit = 20
	}
	if t.manager == nil {
		return agent.ToolResult{Content: "session manager not available", IsError: true}, nil
	}

	results, err := t.manager.Search(args.Query, args.Limit)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("search error: %v", err), IsError: true}, nil
	}
	if len(results) == 0 {
		return agent.ToolResult{Content: "No matching sessions found."}, nil
	}

	out, _ := json.MarshalIndent(results, "", "  ")
	return agent.ToolResult{Content: string(out)}, nil
}
