package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

// ServerTool wraps a server-side tool schema and proxies execution
// through the gateway's /api/v1/tools/{name}/execute endpoint.
type ServerTool struct {
	schema  client.ServerToolSchema
	gateway *client.GatewayClient
}

func NewServerTool(schema client.ServerToolSchema, gateway *client.GatewayClient) *ServerTool {
	return &ServerTool{schema: schema, gateway: gateway}
}

func (t *ServerTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        t.schema.Name,
		Description: t.schema.Description,
		Parameters:  t.schema.Parameters,
	}
}

func (t *ServerTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args map[string]any
	if argsJSON != "" && argsJSON != "{}" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return agent.ToolResult{
				Content: fmt.Sprintf("invalid arguments: %v", err),
				IsError: true,
			}, nil
		}
	}
	if args == nil {
		args = map[string]any{}
	}

	resp, err := t.gateway.ExecuteTool(ctx, t.schema.Name, args, "")
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("server tool error: %v", err),
			IsError: true,
		}, nil
	}

	if resp.Error != nil && *resp.Error != "" {
		return agent.ToolResult{Content: *resp.Error, IsError: true}, nil
	}

	if !resp.Success {
		return agent.ToolResult{Content: "tool execution failed", IsError: true}, nil
	}

	// Prefer pre-formatted text from backend; fall back to raw JSON output
	if resp.Text != nil && *resp.Text != "" {
		return agent.ToolResult{Content: *resp.Text}, nil
	}
	if len(resp.Output) == 0 || string(resp.Output) == "null" {
		return agent.ToolResult{Content: "no output"}, nil
	}
	return agent.ToolResult{Content: string(resp.Output)}, nil
}

// RequiresApproval returns false — the server enforces its own access control.
func (t *ServerTool) RequiresApproval() bool { return false }
