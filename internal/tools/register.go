package tools

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
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
		schMgr := schedule.NewManager(filepath.Join(shanDir, "schedules.json"))
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
func CompleteRegistration(ctx context.Context, gw *client.GatewayClient, cfg *config.Config, baseReg *agent.ToolRegistry, agentDef ...*agents.Agent) (*agent.ToolRegistry, *mcp.ClientManager, func(), error) {
	reg := baseReg.Clone()

	mcpServers := resolveMCPServers(cfg, agentDef...)

	// Gate playwright: require local readiness marker before spawning.
	playwrightNeedsSetup := false
	if pwCfg, hasPW := mcpServers["playwright"]; hasPW && !pwCfg.Disabled {
		home, _ := os.UserHomeDir()
		localDir := filepath.Join(home, ".shannon", "local")
		sig := mcp.CommandSignature(pwCfg.Command, pwCfg.Args)
		if !mcp.ValidatePlaywrightMarker(localDir, sig) {
			delete(mcpServers, "playwright")
			playwrightNeedsSetup = true
			log.Printf("Playwright MCP needs setup on this machine — skipping launch")
		}
	}

	var mcpMgr *mcp.ClientManager
	if len(mcpServers) > 0 {
		mcpMgr = mcp.NewClientManager()
		mcpTools, mcpErr := mcpMgr.ConnectAll(ctx, mcpServers)
		if mcpErr != nil {
			log.Printf("MCP connection warning: %v", mcpErr)
		}
		hasPlaywright := false
		for _, t := range mcpTools {
			if _, exists := reg.Get(t.Tool.Name); exists {
				continue
			}
			reg.Register(NewMCPTool(t.ServerName, t.Tool, mcpMgr))
			if t.Tool.Name == "browser_navigate" {
				hasPlaywright = true
			}
		}
		// Disable legacy browser tool when Playwright MCP is available
		if hasPlaywright {
			// Shut down any chromedp Chrome instance before removing the tool
			if bt, ok := reg.Get("browser"); ok {
				if browserTool, ok := bt.(*BrowserTool); ok {
					browserTool.Cleanup()
				}
			}
			reg.Remove("browser")
			log.Printf("Playwright MCP connected — disabled legacy browser tool")
			// Unless keep_alive is set, disconnect Playwright after discovering
			// its tools. The tool cache persists, and on-demand reconnect in
			// MCPTool.Run will restart playwright-mcp when actually needed.
			if cfg, ok := mcpMgr.ConfigFor("playwright"); !ok || !cfg.KeepAlive {
				mcpMgr.Disconnect("playwright")
				log.Printf("Playwright MCP disconnected — will reconnect on demand")
			} else {
				go func() {
					time.Sleep(2 * time.Second)
					hideChrome()
				}()
			}
		}
	}

	if playwrightNeedsSetup {
		if mcpMgr == nil {
			mcpMgr = mcp.NewClientManager()
		}
		mcpMgr.SetNeedsSetup("playwright")
	}

	err := RegisterServerTools(ctx, gw, reg)

	// Apply tool filter AFTER all sources are registered
	reg = ApplyToolFilter(reg, agentDef...)

	cleanup := func() {
		if mcpMgr != nil {
			mcpMgr.Close()
		}
	}

	return reg, mcpMgr, cleanup, err
}

// RegisterAllWithBaseline is like RegisterAll but also returns the baseline (local-only)
// registry separately, for use by the MCP health supervisor's registry rebuild.
func RegisterAllWithBaseline(gw *client.GatewayClient, cfg *config.Config, agentDef ...*agents.Agent) (
	baseline *agent.ToolRegistry,
	reg *agent.ToolRegistry,
	skillsPtr *[]*skills.Skill,
	mcpMgr *mcp.ClientManager,
	cleanup func(),
	err error,
) {
	CleanupOrphanedChromedp()
	localReg, sp, baseCleanup := RegisterLocalTools(cfg)
	baseline = localReg

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	reg, mcpMgr, remoteCleanup, err := CompleteRegistration(ctx, gw, cfg, localReg, agentDef...)

	cleanup = func() {
		baseCleanup()
		remoteCleanup()
	}

	return baseline, reg, sp, mcpMgr, cleanup, err
}

// RegisterAll registers local tools, connects MCP servers, and then fetches
// server-side tools from the gateway. Local tools take priority, then MCP, then gateway.
// If agentDef is non-nil, tool filtering and MCP scoping are applied per-agent.
// The returned cleanup function must be called on shutdown.
func RegisterAll(gw *client.GatewayClient, cfg *config.Config, agentDef ...*agents.Agent) (*agent.ToolRegistry, *[]*skills.Skill, *mcp.ClientManager, func(), error) {
	// Kill any orphaned chromedp Chrome processes from previous daemon runs
	CleanupOrphanedChromedp()

	reg, skillsPtr, baseCleanup := RegisterLocalTools(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	reg, mcpMgr, remoteCleanup, err := CompleteRegistration(ctx, gw, cfg, reg, agentDef...)

	cleanup := func() {
		baseCleanup()
		remoteCleanup()
	}

	return reg, skillsPtr, mcpMgr, cleanup, err
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
			Command:   ref.Command,
			Args:      ref.Args,
			Env:       ref.Env,
			Type:      ref.Type,
			URL:       ref.URL,
			Disabled:  ref.Disabled,
			Context:   ref.Context,
			KeepAlive: ref.KeepAlive,
		}
	}

	return result
}


// CleanupPlaywrightReconnect runs after a supervisor-driven reconnect.
// In keep_alive mode, hides Chrome so the persistent connection doesn't
// steal focus. In on-demand mode, leaves Chrome visible so the user can
// watch the browser automation.
func CleanupPlaywrightReconnect(ctx context.Context, mcpMgr *mcp.ClientManager) {
	if ctx.Err() != nil {
		return
	}
	cfg, ok := mcpMgr.ConfigFor("playwright")
	if !ok || !cfg.KeepAlive {
		return // on-demand: leave Chrome visible during the turn
	}
	// keep_alive: hide Chrome so it doesn't steal focus.
	time.Sleep(2 * time.Second)
	if ctx.Err() != nil {
		return
	}
	hideChrome()
}

// hideChrome sends Chrome to the background so it doesn't steal focus
// after the Playwright Bridge extension reconnects.
func hideChrome() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "osascript", "-e", `tell application "System Events" to set visible of process "Google Chrome" to false`)
	if err := cmd.Run(); err != nil {
		log.Printf("Playwright: failed to hide Chrome: %v", err)
	}
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

// ExtractGatewayTools returns all *ServerTool entries from a registry.
func ExtractGatewayTools(reg *agent.ToolRegistry) []agent.Tool {
	var result []agent.Tool
	for _, t := range reg.All() {
		if _, ok := t.(*ServerTool); ok {
			result = append(result, t)
		}
	}
	return result
}

// ExtractPostOverlays returns tools in full that are not in baseline,
// not *MCPTool, and not *ServerTool.
func ExtractPostOverlays(full, baseline *agent.ToolRegistry) []agent.Tool {
	var result []agent.Tool
	for _, t := range full.All() {
		name := t.Info().Name
		if _, inBaseline := baseline.Get(name); inBaseline {
			continue
		}
		if _, isMCP := t.(*MCPTool); isMCP {
			continue
		}
		if _, isGW := t.(*ServerTool); isGW {
			continue
		}
		result = append(result, t)
	}
	return result
}

// RebuildRegistryForHealth creates a new registry from cached layers,
// including tools from MCP servers. Healthy servers' tools work directly.
// Disconnected servers with cached tools are included with an on-demand
// reconnect capability (via supervisor) so the LLM can trigger reconnect
// only when it actually invokes a browser tool. When Playwright tools are
// present (healthy or cached), the legacy browser tool is removed.
func RebuildRegistryForHealth(
	baseline *agent.ToolRegistry,
	gatewayOverlay []agent.Tool,
	postOverlays []agent.Tool,
	healthStates map[string]mcp.ServerHealth,
	mcpMgr *mcp.ClientManager,
	supervisor *mcp.Supervisor,
) *agent.ToolRegistry {
	reg := baseline.Clone()

	playwrightPresent := false
	if mcpMgr != nil {
		for serverName, health := range healthStates {
			if health.State != mcp.StateHealthy && health.State != mcp.StateDisconnected {
				continue
			}
			tools := mcpMgr.CachedTools(serverName)
			for _, t := range tools {
				if _, exists := reg.Get(t.Tool.Name); exists {
					continue
				}
				mt := NewMCPTool(t.ServerName, t.Tool, mcpMgr)
				// Disconnected servers get the supervisor for on-demand reconnect:
				// Chrome only relaunches when the LLM actually invokes a browser tool.
				if health.State == mcp.StateDisconnected && supervisor != nil {
					mt.SetSupervisor(supervisor)
				}
				reg.Register(mt)
				if t.Tool.Name == "browser_navigate" {
					playwrightPresent = true
				}
			}
		}
	}

	// Do NOT call browserTool.Cleanup() — in-flight sessions share the instance.
	// Only remove from the NEW registry.
	if playwrightPresent {
		reg.Remove("browser")
	}

	for _, t := range gatewayOverlay {
		if _, exists := reg.Get(t.Info().Name); !exists {
			reg.Register(t)
		}
	}

	for _, t := range postOverlays {
		reg.Register(t)
	}

	return reg
}
