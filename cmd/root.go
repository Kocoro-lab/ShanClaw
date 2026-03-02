package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"

	"github.com/Kocoro-lab/shan/internal/agent"
	"github.com/Kocoro-lab/shan/internal/audit"
	"github.com/Kocoro-lab/shan/internal/client"
	"github.com/Kocoro-lab/shan/internal/config"
	"github.com/Kocoro-lab/shan/internal/hooks"
	mcppkg "github.com/Kocoro-lab/shan/internal/mcp"
	"github.com/Kocoro-lab/shan/internal/tools"
	"github.com/Kocoro-lab/shan/internal/tui"
)

var Version = "dev"
var autoApprove = false
var runSetup = false
var dangerouslySkipPermissions = false

var rootCmd = &cobra.Command{
	Use:   "shan [query]",
	Short: "Shannon AI agent CLI",
	Long:  "Interactive AI agent powered by Shannon. Local file/bash tools + remote research/swarm orchestration.",
	Args:  cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}

		// Explicit --setup flag
		if runSetup {
			return config.RunSetup(cfg)
		}

		// First-run: no API key on remote endpoint
		if config.NeedsSetup(cfg) {
			if !stdinIsTTY() {
				return fmt.Errorf("no API key configured. Run 'shan --setup' to configure")
			}
			if err := config.RunSetup(cfg); err != nil {
				return err
			}
			fmt.Println("Setup complete. Run 'shan' again to start.")
			return nil
		}

		if len(args) > 0 {
			// One-shot mode
			query := strings.Join(args, " ")
			return runOneShot(cfg, query)
		}

		// Interactive mode
		m := tui.New(cfg, Version)
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
}

func runOneShot(cfg *config.Config, query string) error {
	gw := client.NewGatewayClient(cfg.Endpoint, cfg.APIKey)
	reg, cleanup, serverErr := tools.RegisterAll(gw, cfg)
	defer cleanup()
	if serverErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", serverErr)
	}

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

	hookRunner := hooks.NewHookRunner(cfg.Hooks)
	loop := agent.NewAgentLoop(gw, reg, cfg.ModelTier, shannonDir, cfg.Agent.MaxIterations, cfg.Tools.ResultTruncation, cfg.Tools.ArgsTruncation, &cfg.Permissions, auditor, hookRunner)
	loop.SetHandler(&cliEventHandler{autoApprove: autoApprove})
	loop.SetBypassPermissions(dangerouslySkipPermissions)
	if mcpCtx := mcppkg.BuildContext(cfg.MCPServers); mcpCtx != "" {
		loop.SetMCPContext(mcpCtx)
	}
	result, usage, err := loop.Run(context.Background(), query, nil)
	if err != nil && !errors.Is(err, agent.ErrMaxIterReached) {
		return err
	}
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
	brief := ""
	if elapsed > 100*time.Millisecond {
		brief = fmt.Sprintf("  (%.1fs)", elapsed.Seconds())
	}
	fmt.Printf("  ⏵ %s  %s%s\n", name, icon, brief)
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

func stdinIsTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
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
		reg, cleanup := tools.RegisterLocalTools(cfg)
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
