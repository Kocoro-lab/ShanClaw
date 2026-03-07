package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Kocoro-lab/shan/internal/agent"
	"github.com/Kocoro-lab/shan/internal/client"
	"github.com/Kocoro-lab/shan/internal/config"
	"github.com/Kocoro-lab/shan/internal/mcp"
	"github.com/Kocoro-lab/shan/internal/schedule"
)

// RegisterLocalTools registers only the local tools.
// If cfg is non-nil, extra safe commands from permissions.allowed_commands
// are passed to the BashTool so they skip approval.
// Returns the registry and a cleanup function that shuts down any active
// tool resources (e.g. browser process).
func RegisterLocalTools(cfg *config.Config) (*agent.ToolRegistry, func()) {
	reg := agent.NewToolRegistry()

	reg.Register(&FileReadTool{})
	reg.Register(&FileWriteTool{})
	reg.Register(&FileEditTool{})
	reg.Register(&GlobTool{})
	reg.Register(&GrepTool{})

	bashTool := &BashTool{}
	if cfg != nil {
		bashTool.ExtraSafeCommands = cfg.Permissions.AllowedCommands
	}
	reg.Register(bashTool)

	reg.Register(&ThinkTool{})
	reg.Register(&DirectoryListTool{})
	reg.Register(&HTTPTool{})
	reg.Register(&SystemInfoTool{})
	reg.Register(&ClipboardTool{})
	reg.Register(&NotifyTool{})
	reg.Register(&ProcessTool{})
	reg.Register(&AppleScriptTool{})
	reg.Register(&AccessibilityTool{})

	browser := &BrowserTool{}
	reg.Register(browser)
	reg.Register(&ScreenshotTool{})
	reg.Register(&ComputerTool{})

	// Schedule tools
	if shanDir := config.ShannonDir(); shanDir != "" {
		home, _ := os.UserHomeDir()
		plistDir := filepath.Join(home, "Library", "LaunchAgents")
		schMgr := schedule.NewManager(
			filepath.Join(shanDir, "schedules.json"),
			plistDir,
		)
		for _, tool := range NewScheduleTools(schMgr) {
			reg.Register(tool)
		}
	}

	cleanup := func() {
		browser.Cleanup()
	}
	return reg, cleanup
}

// RegisterServerTools fetches server-side tools from the gateway and appends
// entries to the provided registry. Local tools always keep priority.
func RegisterServerTools(ctx context.Context, gw *client.GatewayClient, reg *agent.ToolRegistry) error {
	if reg == nil {
		return fmt.Errorf("tool registry is nil")
	}

	schemas, err := gw.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("server tools unavailable: %w", err)
	}

	for _, schema := range schemas {
		if _, exists := reg.Get(schema.Name); exists {
			continue // local tool takes priority
		}
		reg.Register(NewServerTool(schema, gw))
	}

	return nil
}

// RegisterAll registers local tools, connects MCP servers, and then fetches
// server-side tools from the gateway. Local tools take priority, then MCP, then gateway.
// The returned cleanup function must be called on shutdown.
func RegisterAll(gw *client.GatewayClient, cfg *config.Config) (*agent.ToolRegistry, func(), error) {
	reg, baseCleanup := RegisterLocalTools(cfg)

	// MCP server tools (best-effort)
	var mcpMgr *mcp.ClientManager
	if cfg != nil && len(cfg.MCPServers) > 0 {
		mcpMgr = mcp.NewClientManager()
		mcpTools, mcpErr := mcpMgr.ConnectAll(context.Background(), cfg.MCPServers)
		if mcpErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: MCP servers: %v\n", mcpErr)
		}
		registered := 0
		servers := make(map[string]bool)
		for _, t := range mcpTools {
			if _, exists := reg.Get(t.Tool.Name); exists {
				fmt.Fprintf(os.Stderr, "MCP: skipping %s/%s (local tool takes priority)\n", t.ServerName, t.Tool.Name)
				continue // local tool takes priority
			}
			reg.Register(NewMCPTool(t.ServerName, t.Tool, mcpMgr))
			servers[t.ServerName] = true
			registered++
		}
		if registered > 0 {
			fmt.Fprintf(os.Stderr, "MCP: %d tools from %d servers\n", registered, len(servers))
		}
	}

	// Gateway server tools (best-effort)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := RegisterServerTools(ctx, gw, reg)

	cleanup := func() {
		baseCleanup()
		if mcpMgr != nil {
			mcpMgr.Close()
		}
	}

	if err != nil {
		return reg, cleanup, err
	}

	return reg, cleanup, nil
}
