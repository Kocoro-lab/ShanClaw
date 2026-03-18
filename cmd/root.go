package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/agents"
	"github.com/Kocoro-lab/ShanClaw/internal/audit"
	"github.com/Kocoro-lab/ShanClaw/internal/client"
	"github.com/Kocoro-lab/ShanClaw/internal/config"
	"github.com/Kocoro-lab/ShanClaw/internal/hooks"
	mcppkg "github.com/Kocoro-lab/ShanClaw/internal/mcp"
	"github.com/Kocoro-lab/ShanClaw/internal/session"
	"github.com/Kocoro-lab/ShanClaw/internal/skills"
	"github.com/Kocoro-lab/ShanClaw/internal/tools"
	"github.com/Kocoro-lab/ShanClaw/internal/tui"
	"github.com/Kocoro-lab/ShanClaw/internal/update"
)

var Version = "dev"
var autoApprove = false
var runSetup = false
var dangerouslySkipPermissions = false
var agentName string

var rootCmd = &cobra.Command{
	Use:     "shan [query]",
	Short:   "Shannon AI agent CLI",
	Long:    "Interactive AI agent powered by Shannon. Local file/bash tools + remote research/swarm orchestration.",
	Version: Version,
	Args:    cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}

		// Explicit --setup flag
		if runSetup {
			return config.RunSetup(cfg, os.Stdin, os.Stdout)
		}

		// First-run: no API key on remote endpoint
		if config.NeedsSetup(cfg) {
			if !stdinIsTTY() {
				return fmt.Errorf("no API key configured. Run 'shan --setup' to configure")
			}
			if err := config.RunSetup(cfg, os.Stdin, os.Stdout); err != nil {
				return err
			}
			fmt.Println("Setup complete. Run 'shan' again to start.")
			return nil
		}

		var agentOverride *agents.Agent
		if agentName != "" {
			agentOverride, err = agents.LoadAgent(filepath.Join(config.ShannonDir(), "agents"), agentName)
			if err != nil {
				return fmt.Errorf("agent %q: %w", agentName, err)
			}
			// Ensure agent sessions directory exists
			os.MkdirAll(filepath.Join(config.ShannonDir(), "agents", agentName, "sessions"), 0700)
		}

		if len(args) > 0 {
			// One-shot mode
			query := strings.Join(args, " ")
			return runOneShot(cfg, query, agentOverride)
		}

		// Interactive mode
		m := tui.New(cfg, Version, agentOverride)
		m.SetBypassPermissions(dangerouslySkipPermissions)
		p := tea.NewProgram(m)
		m.SetProgram(p)
		_, err = p.Run()
		return err
	},
}

func init() {
	rootCmd.Flags().BoolVarP(
		&autoApprove,
		"yes",
		"y",
		false,
		"Automatically approve all one-shot tool calls without prompting",
	)
	rootCmd.Flags().BoolVar(
		&runSetup,
		"setup",
		false,
		"Run interactive setup to configure endpoint and API key",
	)
	rootCmd.Flags().BoolVar(
		&dangerouslySkipPermissions,
		"dangerously-skip-permissions",
		false,
		"Skip all permission checks (hard-blocks still enforced). Use at your own risk.",
	)
	rootCmd.Flags().StringVar(&agentName, "agent", "", "Named agent to use (from ~/.shannon/agents/)")
}

func runOneShot(cfg *config.Config, query string, agentOverride *agents.Agent) error {
	// Background auto-update (non-blocking)
	if cfg.AutoUpdateCheck {
		go func() {
			if shanDir := config.ShannonDir(); shanDir != "" {
				if msg := update.AutoUpdate(Version, shanDir); msg != "" {
					fmt.Fprintf(os.Stderr, "  %s\n", msg)
				}
			}
		}()
	}

	gw := client.NewGatewayClient(cfg.Endpoint, cfg.APIKey)
	reg, skillsPtr, cleanup, serverErr := tools.RegisterAll(gw, cfg, agentOverride)
	defer cleanup()
	if serverErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", serverErr)
	}

	// Cloud delegation tool (uses same gateway client)
	var cloudAgentPrompt string
	if agentOverride != nil {
		cloudAgentPrompt = agentOverride.Prompt
	}
	tools.RegisterCloudDelegate(reg, gw, cfg, nil, agentName, cloudAgentPrompt) // handler set later via loop.SetHandler

	shannonDir := config.ShannonDir()

	// Create audit logger (best-effort)
	var auditor *audit.AuditLogger
	if shannonDir != "" {
		logDir := filepath.Join(shannonDir, "logs")
		var err error
		auditor, err = audit.NewAuditLogger(logDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not create audit logger: %v\n", err)
		}
	}
	if auditor != nil {
		defer auditor.Close()
	}

	// Resolve scoped MCP context
	scopedMCPCtx := tools.ResolveMCPContext(cfg, agentOverride)

	hookRunner := hooks.NewHookRunner(cfg.Hooks)
	loop := agent.NewAgentLoop(gw, reg, cfg.ModelTier, shannonDir, cfg.Agent.MaxIterations, cfg.Tools.ResultTruncation, cfg.Tools.ArgsTruncation, &cfg.Permissions, auditor, hookRunner)
	loop.SetMaxTokens(cfg.Agent.MaxTokens)
	loop.SetTemperature(cfg.Agent.Temperature)
	loop.SetContextWindow(cfg.Agent.ContextWindow)
	if cfg.Agent.Model != "" {
		loop.SetSpecificModel(cfg.Agent.Model)
	}
	if cfg.Agent.Thinking {
		if cfg.Agent.ThinkingMode == "enabled" {
			loop.SetThinking(&client.ThinkingConfig{Type: "enabled", BudgetTokens: cfg.Agent.ThinkingBudget})
		} else {
			loop.SetThinking(&client.ThinkingConfig{Type: "adaptive"})
		}
	}
	if cfg.Agent.ReasoningEffort != "" {
		loop.SetReasoningEffort(cfg.Agent.ReasoningEffort)
	}
	// Per-agent model config overrides
	if agentOverride != nil && agentOverride.Config != nil && agentOverride.Config.Agent != nil {
		ac := agentOverride.Config.Agent
		if ac.Model != nil {
			loop.SetSpecificModel(*ac.Model)
		}
		if ac.MaxIterations != nil {
			loop.SetMaxIterations(*ac.MaxIterations)
		}
		if ac.Temperature != nil {
			loop.SetTemperature(*ac.Temperature)
		}
		if ac.MaxTokens != nil {
			loop.SetMaxTokens(*ac.MaxTokens)
		}
		if ac.ContextWindow != nil {
			loop.SetContextWindow(*ac.ContextWindow)
		}
	}
	loop.SetHandler(&cliEventHandler{autoApprove: autoApprove})
	loop.SetBypassPermissions(dangerouslySkipPermissions)

	// Load skills (agent-scoped or global) and wire to loop + use_skill tool
	var loadedSkills []*skills.Skill
	if agentOverride != nil {
		loadedSkills = agentOverride.Skills
	} else {
		var err error
		loadedSkills, err = agents.LoadGlobalSkills(config.ShannonDir())
		if err != nil {
			log.Printf("WARNING: failed to load global skills: %v", err)
		}
	}
	*skillsPtr = loadedSkills

	if agentOverride != nil {
		agentDir := filepath.Join(shannonDir, "agents", agentName)
		loop.SwitchAgent(agentOverride.Prompt, agentDir, nil, scopedMCPCtx, loadedSkills)
	} else {
		// Default agent: memory lives in shannonDir/memory/
		loop.SetMemoryDir(filepath.Join(shannonDir, "memory"))
		if loadedSkills != nil {
			loop.SetSkills(loadedSkills)
		}
		if scopedMCPCtx != "" {
			loop.SetMCPContext(scopedMCPCtx)
		}
	}
	// Create session for persistence
	var sessDir string
	if agentName != "" {
		sessDir = filepath.Join(shannonDir, "agents", agentName, "sessions")
	} else {
		sessDir = filepath.Join(shannonDir, "sessions")
	}
	sessMgr := session.NewManager(sessDir)
	tools.RegisterSessionSearch(reg, sessMgr)
	sess := sessMgr.NewSession()
	sess.Title = sessionTitleFromQuery(query)

	result, usage, err := loop.Run(context.Background(), query, nil)
	if err != nil && !errors.Is(err, agent.ErrMaxIterReached) {
		return err
	}

	// Persist session to disk
	now := time.Now()
	sess.Messages = append(sess.Messages,
		client.Message{Role: "user", Content: client.NewTextContent(query)},
		client.Message{Role: "assistant", Content: client.NewTextContent(result)},
	)
	sess.MessageMeta = append(sess.MessageMeta,
		session.MessageMeta{Source: "local", Timestamp: session.TimePtr(now)},
		session.MessageMeta{Source: "local", Timestamp: session.TimePtr(time.Now())},
	)
	if saveErr := sessMgr.Save(); saveErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save session: %v\n", saveErr)
	}
	sessMgr.Close()

	fmt.Print(renderMarkdown(result))
	usageLine := fmt.Sprintf("\n[tokens: %d | cost: $%.4f", usage.TotalTokens, usage.CostUSD)
	if usage.Model != "" {
		usageLine += " | model: " + usage.Model
	}
	fmt.Println(usageLine + "]")
	return nil
}

// cliEventHandler prompts for approval on stdout/stdin in one-shot mode
type cliEventHandler struct {
	autoApprove bool
}

func (h *cliEventHandler) OnToolCall(name string, args string) {
	// Deferred — prints on OnToolResult
}

func (h *cliEventHandler) OnToolResult(name string, args string, result agent.ToolResult, elapsed time.Duration) {
	icon := "✓"
	if result.IsError {
		icon = "✗"
	}
	keyArg := cliToolKeyArg(name, args)
	brief := ""
	if elapsed > 100*time.Millisecond {
		brief = fmt.Sprintf("  %.1fs", elapsed.Seconds())
	}
	if result.IsError {
		errMsg := result.Content
		if len([]rune(errMsg)) > 60 {
			errMsg = string([]rune(errMsg)[:60]) + "..."
		}
		brief += "  " + errMsg
	}
	fmt.Printf("  ⏵ %s(%s)  %s%s\n", name, keyArg, icon, brief)
}

func (h *cliEventHandler) OnText(text string) {}

func (h *cliEventHandler) OnStreamDelta(delta string) {
	fmt.Print(delta)
}

func (h *cliEventHandler) OnUsage(usage agent.TurnUsage) {}

func (h *cliEventHandler) OnApprovalNeeded(tool string, args string) bool {
	if h.autoApprove {
		return true
	}

	if !stdinIsTTY() {
		fmt.Printf("  Tool %s requires approval but stdin is not interactive. Use --yes to auto-approve.\n", tool)
		return false
	}

	fmt.Printf("  Allow %s? [y/N] ", tool)
	var response string
	if _, err := fmt.Scanln(&response); err != nil {
		return false
	}
	return response == "y" || response == "Y"
}

// cliToolKeyArg extracts a short key argument for display in one-shot mode.
func cliToolKeyArg(toolName, argsJSON string) string {
	var m map[string]interface{}
	if json.Unmarshal([]byte(argsJSON), &m) != nil {
		if len([]rune(argsJSON)) > 40 {
			return string([]rune(argsJSON)[:40]) + "..."
		}
		return argsJSON
	}
	var key string
	switch toolName {
	case "bash":
		key, _ = m["command"].(string)
	case "file_read", "file_write", "file_edit", "directory_list":
		key, _ = m["path"].(string)
	case "glob", "grep":
		key, _ = m["pattern"].(string)
	case "web_search":
		key, _ = m["query"].(string)
	case "screenshot":
		key = "screen"
	case "computer":
		key, _ = m["action"].(string)
	default:
		for _, f := range []string{"query", "path", "url", "command", "name"} {
			if v, ok := m[f].(string); ok && v != "" {
				key = v
				break
			}
		}
	}
	if key == "" {
		if len([]rune(argsJSON)) > 40 {
			return string([]rune(argsJSON)[:40]) + "..."
		}
		return argsJSON
	}
	if len([]rune(key)) > 50 {
		return string([]rune(key)[:50]) + "..."
	}
	return key
}

func stdinIsTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func sessionTitleFromQuery(query string) string {
	if idx := strings.IndexAny(query, "\n\r"); idx >= 0 {
		query = query[:idx]
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "New session"
	}
	if len(query) <= 50 {
		return query
	}
	return query[:50] + "..."
}

func renderMarkdown(text string) string {
	if text == "" {
		return text
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(0),
	)
	if err != nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	return out
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP server commands",
}

var mcpServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start MCP server over stdio (exposes local tools to MCP clients)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		reg, _, cleanup := tools.RegisterLocalTools(cfg)
		defer cleanup()

		shannonDir := config.ShannonDir()

		// Create audit logger (best-effort)
		var auditor *audit.AuditLogger
		if shannonDir != "" {
			logDir := filepath.Join(shannonDir, "logs")
			auditor, _ = audit.NewAuditLogger(logDir)
		}
		if auditor != nil {
			defer auditor.Close()
		}

		hookRunner := hooks.NewHookRunner(cfg.Hooks)

		srv := mcppkg.NewServer(reg, "shannon-cli", Version, &cfg.Permissions, auditor, hookRunner)
		return srv.Serve(context.Background(), os.Stdin, os.Stdout)
	},
}

func init() {
	mcpCmd.AddCommand(mcpServeCmd)
	rootCmd.AddCommand(mcpCmd)
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
