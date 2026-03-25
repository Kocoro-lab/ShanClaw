package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/mcp"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
)

const maxMCPDescLen = 500

// MCPTool wraps an MCP server tool as a local agent.Tool.
type MCPTool struct {
	serverName string
	tool       mcpproto.Tool
	manager    *mcp.ClientManager
	supervisor *mcp.Supervisor // optional — enables on-demand reconnect
}

// NewMCPTool creates a tool adapter for an MCP server tool.
func NewMCPTool(serverName string, tool mcpproto.Tool, manager *mcp.ClientManager) *MCPTool {
	return &MCPTool{
		serverName: serverName,
		tool:       tool,
		manager:    manager,
	}
}

// SetSupervisor enables on-demand reconnect: if CallTool fails and the server
// is disconnected, ProbeNow triggers reconnect and the call is retried once.
func (t *MCPTool) SetSupervisor(sup *mcp.Supervisor) {
	t.supervisor = sup
}

func (t *MCPTool) Info() agent.ToolInfo {
	desc := t.tool.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool from %s", t.serverName)
	}
	if r := []rune(desc); len(r) > maxMCPDescLen {
		desc = string(r[:maxMCPDescLen]) + "..."
	}

	// Strip control characters from tool name
	name := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, t.tool.Name)

	// Convert MCP input schema to our parameters format
	params := make(map[string]any)
	if t.tool.InputSchema.Properties != nil {
		params["type"] = "object"
		params["properties"] = t.tool.InputSchema.Properties
	}

	var required []string
	for _, r := range t.tool.InputSchema.Required {
		required = append(required, r)
	}

	return agent.ToolInfo{
		Name:        name,
		Description: fmt.Sprintf("[%s] %s", t.serverName, desc),
		Parameters:  params,
		Required:    required,
	}
}

func (t *MCPTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args map[string]any
	if argsJSON != "" && argsJSON != "{}" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
		}
	}
	if args == nil {
		args = make(map[string]any)
	}

	content, isError, err := t.manager.CallTool(ctx, t.serverName, t.tool.Name, args)
	if err != nil && t.supervisor != nil {
		// Connection dead — attempt on-demand reconnect and retry once.
		h := t.supervisor.HealthFor(t.serverName)
		if h.State == mcp.StateDisconnected {
			log.Printf("[mcp-tool] %s/%s: connection dead, triggering on-demand reconnect", t.serverName, t.tool.Name)
			reconHealth := t.supervisor.ProbeNow(t.serverName)
			if reconHealth.State == mcp.StateHealthy {
				content, isError, err = t.manager.CallTool(ctx, t.serverName, t.tool.Name, args)
			}
		}
	}
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("MCP call failed: %v", err), IsError: true}, nil
	}

	return agent.ToolResult{Content: content, IsError: isError}, nil
}

func (t *MCPTool) RequiresApproval() bool { return true }
