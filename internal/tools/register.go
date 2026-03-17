package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/agents"
	"github.com/Kocoro-lab/ShanClaw/internal/client"
	"github.com/Kocoro-lab/ShanClaw/internal/config"
	"github.com/Kocoro-lab/ShanClaw/internal/mcp"
	"github.com/Kocoro-lab/ShanClaw/internal/schedule"
	"github.com/Kocoro-lab/ShanClaw/internal/session"
	"github.com/Kocoro-lab/ShanClaw/internal/skills"
)

// RegisterLocalTools registers only the local tools.
// If cfg is non-nil, extra safe commands from permissions.allowed_commands
// are passed to the BashTool so they skip approval.
// Returns the registry and a cleanup function that shuts down any active
// tool resources (e.g. browser process).
func RegisterLocalTools(cfg *config.Config) (*agent.ToolRegistry, *[]*skills.Skill, func()) {
	reg := agent.NewToolRegistry()

	skillsPtr := &[]*skills.Skill{}
	reg.Register(newUseSkillTool(skillsPtr))

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

	reg.Register(&MemoryAppendTool{})
	reg.Register(&ThinkTool{})
	reg.Register(&DirectoryListTool{})
	reg.Register(&HTTPTool{})
	reg.Register(&SystemInfoTool{})
	reg.Register(&ClipboardTool{})
	reg.Register(&NotifyTool{})
	reg.Register(&ProcessTool{})
	reg.Register(&AppleScriptTool{})
	axClient := &AXClient{}
	reg.Register(&AccessibilityTool{client: axClient})
	reg.Register(&GhosttyTool{tabs: newTabRegistry()})

	browser := &BrowserTool{}
	reg.Register(browser)
	reg.Register(&ScreenshotTool{})
	reg.Register(&ComputerTool{client: axClient})
	reg.Register(&WaitTool{client: axClient})

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
		axClient.Close()
	}
	return reg, skillsPtr, cleanup
}

// gatewayAllowedTools is the allowlist of server-side tools worth registering
// locally. Cloud-only tools (python_executor, calculator, etc.) are excluded
// to prevent the LLM from choosing them over better local equivalents.
// All cloud tools remain available via cloud_delegate.
var gatewayAllowedTools = map[string]bool{
	// Research
	"web_search":        true,
	"web_fetch":         true,
	"web_subpage_fetch": true,
	"web_crawl":         true,
	// Financial
	"getStockBars":      true,
	"alpaca_news":       true,
	"sec_filings":       true,
	"news_aggregator":   true,
	"twitter_sentiment": true,
	// Ads/Enterprise
	"ads_serp_extract":        true,
	"ads_transparency_search": true,
	"ads_competitor_discover": true,
	"lp_visual_analyze":       true,
	"lp_batch_analyze":        true,
	"ads_creative_analyze":    true,
	"yahoo_jp_ads_discover":   true,
	"meta_ad_library_search":  true,
	// Analytics
	"ga4_run_report":          true,
	"ga4_run_realtime_report": true,
	"ga4_get_metadata":        true,
	// Visual
	"page_screenshot": true,
	// Browser
	"browser": true,
}

// RegisterServerTools fetches server-side tools from the gateway and appends
// entries to the provided registry. Only allowlisted tools are registered;
// others are skipped (still available via cloud_delegate). Local tools always
// keep priority.
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
		if !gatewayAllowedTools[schema.Name] {
			continue // not allowlisted; available via cloud_delegate
		}
		reg.Register(NewServerTool(schema, gw))
	}

	return nil
}

// SetRegistrySkills updates the use_skill tool in a registry to point to the
// given skills slice. Returns the skills pointer for the caller to keep in sync.
// This is safe for concurrent use because it creates a new use_skill tool instance.
func SetRegistrySkills(reg *agent.ToolRegistry, s []*skills.Skill) {
	reg.Register(newUseSkillTool(&s))
}

// ApplyToolFilter applies the agent's tool allow/deny filter to a registry.
// Returns a new filtered registry, or the original if no filter applies.
func ApplyToolFilter(reg *agent.ToolRegistry, agentDef ...*agents.Agent) *agent.ToolRegistry {
	if len(agentDef) == 0 || agentDef[0] == nil || agentDef[0].Config == nil || agentDef[0].Config.Tools == nil {
		return reg
	}
	f := agentDef[0].Config.Tools
	if len(f.Allow) > 0 {
		return reg.FilterByAllow(f.Allow)
	}
	if len(f.Deny) > 0 {
		return reg.FilterByDeny(f.Deny)
	}
	return reg
}

// CompleteRegistration connects MCP servers and gateway tools on top of a base
// local-only registry, then applies per-agent tool filtering. The filter runs
// AFTER all tool sources are registered so it applies to MCP and gateway tools too.
// The returned cleanup function closes MCP connections.
func CompleteRegistration(ctx context.Context, gw *client.GatewayClient, cfg *config.Config, baseReg *agent.ToolRegistry, agentDef ...*agents.Agent) (*agent.ToolRegistry, func(), error) {
	reg := baseReg.Clone()

	mcpServers := resolveMCPServers(cfg, agentDef...)

	var mcpMgr *mcp.ClientManager
	if len(mcpServers) > 0 {
		mcpMgr = mcp.NewClientManager()
		mcpTools, _ := mcpMgr.ConnectAll(context.Background(), mcpServers)
		for _, t := range mcpTools {
			if _, exists := reg.Get(t.Tool.Name); exists {
				continue
			}
			reg.Register(NewMCPTool(t.ServerName, t.Tool, mcpMgr))
		}
	}

	err := RegisterServerTools(ctx, gw, reg)

	// Apply tool filter AFTER all sources are registered
	reg = ApplyToolFilter(reg, agentDef...)

	cleanup := func() {
		if mcpMgr != nil {
			mcpMgr.Close()
		}
	}

	return reg, cleanup, err
}

// RegisterAll registers local tools, connects MCP servers, and then fetches
// server-side tools from the gateway. Local tools take priority, then MCP, then gateway.
// If agentDef is non-nil, tool filtering and MCP scoping are applied per-agent.
// The returned cleanup function must be called on shutdown.
func RegisterAll(gw *client.GatewayClient, cfg *config.Config, agentDef ...*agents.Agent) (*agent.ToolRegistry, *[]*skills.Skill, func(), error) {
	reg, skillsPtr, baseCleanup := RegisterLocalTools(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reg, remoteCleanup, err := CompleteRegistration(ctx, gw, cfg, reg, agentDef...)

	cleanup := func() {
		baseCleanup()
		remoteCleanup()
	}

	return reg, skillsPtr, cleanup, err
}

// resolveMCPServers determines which MCP servers to connect based on agent config.
// If the agent has no MCP config, returns the global set.
// If _inherit is true, agent servers are merged on top of global.
// If _inherit is false, only agent servers are used.
func resolveMCPServers(cfg *config.Config, agentDef ...*agents.Agent) map[string]mcp.MCPServerConfig {
	if cfg == nil {
		return nil
	}

	// No agent or no agent MCP config → use global
	if len(agentDef) == 0 || agentDef[0] == nil || agentDef[0].Config == nil || agentDef[0].Config.MCPServers == nil {
		return cfg.MCPServers
	}

	agentMCP := agentDef[0].Config.MCPServers
	result := make(map[string]mcp.MCPServerConfig)

	// If inherit, start with global servers
	if agentMCP.Inherit {
		for name, srv := range cfg.MCPServers {
			result[name] = srv
		}
	}

	// Overlay agent-specific servers
	for name, ref := range agentMCP.Servers {
		result[name] = mcp.MCPServerConfig{
			Command:  ref.Command,
			Args:     ref.Args,
			Env:      ref.Env,
			Type:     ref.Type,
			URL:      ref.URL,
			Disabled: ref.Disabled,
			Context:  ref.Context,
		}
	}

	return result
}

// ResolveMCPContext builds the MCP context string scoped to the agent's servers.
// If the agent has no MCP config, falls back to global servers.
func ResolveMCPContext(cfg *config.Config, agentDef ...*agents.Agent) string {
	servers := resolveMCPServers(cfg, agentDef...)
	return mcp.BuildContext(servers)
}

// RegisterSessionSearch registers the session_search tool if a manager is available.
func RegisterSessionSearch(reg *agent.ToolRegistry, mgr *session.Manager) {
	if mgr == nil {
		return
	}
	reg.Register(&SessionSearchTool{manager: mgr})
}

// RegisterCloudDelegate registers the cloud_delegate tool if cloud is enabled.
func RegisterCloudDelegate(reg *agent.ToolRegistry, gw *client.GatewayClient, cfg *config.Config, handler agent.EventHandler, agentName, agentPrompt string) {
	if cfg == nil || !cfg.Cloud.Enabled {
		return
	}
	timeout := time.Duration(cfg.Cloud.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 3600 * time.Second
	}
	reg.Register(NewCloudDelegateTool(gw, cfg.APIKey, timeout, handler, agentName, agentPrompt))
}
