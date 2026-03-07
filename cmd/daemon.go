package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Kocoro-lab/shan/internal/agent"
	"github.com/Kocoro-lab/shan/internal/agents"
	"github.com/Kocoro-lab/shan/internal/audit"
	"github.com/Kocoro-lab/shan/internal/client"
	"github.com/Kocoro-lab/shan/internal/config"
	"github.com/Kocoro-lab/shan/internal/daemon"
	"github.com/Kocoro-lab/shan/internal/hooks"
	mcppkg "github.com/Kocoro-lab/shan/internal/mcp"
	"github.com/Kocoro-lab/shan/internal/tools"
	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Background daemon for channel messaging",
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon (connects to Shannon Cloud for channel messages)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}

		shanDir := config.ShannonDir()
		agentsDir := filepath.Join(shanDir, "agents")

		gw := client.NewGatewayClient(cfg.Endpoint, cfg.APIKey)
		reg, cleanup, serverErr := tools.RegisterAll(gw, cfg)
		defer cleanup()
		if serverErr != nil {
			log.Printf("Warning: %v", serverErr)
		}

		var auditor *audit.AuditLogger
		if shanDir != "" {
			auditor, _ = audit.NewAuditLogger(filepath.Join(shanDir, "logs"))
		}
		if auditor != nil {
			defer auditor.Close()
		}
		hookRunner := hooks.NewHookRunner(cfg.Hooks)

		sessionCache := daemon.NewSessionCache(shanDir)

		wsEndpoint := strings.Replace(cfg.Endpoint, "https://", "wss://", 1)
		wsEndpoint = strings.Replace(wsEndpoint, "http://", "ws://", 1)
		wsEndpoint += "/v1/ws/messages"

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			log.Println("daemon: shutting down...")
			cancel()
		}()

		var wsClient *daemon.Client
		wsClient = daemon.NewClient(wsEndpoint, cfg.APIKey, func(msg daemon.IncomingMessage) {
			agentName, prompt := agents.ParseAgentMention(msg.Text)

			var agentOverride *agents.Agent
			if agentName != "" {
				a, loadErr := agents.LoadAgent(agentsDir, agentName)
				if loadErr != nil {
					log.Printf("daemon: agent %q not found: %v, using default", agentName, loadErr)
					agentName = ""
					prompt = msg.Text
				} else {
					agentOverride = a
				}
			}
			if prompt == "" {
				prompt = msg.Text
			}

			// Per-agent lock serializes concurrent messages to the same agent
			sessionCache.Lock(agentName)
			defer sessionCache.Unlock(agentName)

			sessMgr := sessionCache.GetOrCreate(agentName)
			sess := sessMgr.Current()
			history := sess.Messages

			loop := agent.NewAgentLoop(gw, reg, cfg.ModelTier, shanDir, cfg.Agent.MaxIterations,
				cfg.Tools.ResultTruncation, cfg.Tools.ArgsTruncation, &cfg.Permissions, auditor, hookRunner)
			loop.SetMaxTokens(cfg.Agent.MaxTokens)
			loop.SetTemperature(cfg.Agent.Temperature)
			loop.SetContextWindow(cfg.Agent.ContextWindow)
			loop.SetEnableStreaming(false)
			if agentOverride != nil {
				loop.SetAgentOverride(agentOverride.Prompt, agentOverride.Memory)
			}
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
			if mcpCtx := mcppkg.BuildContext(cfg.MCPServers); mcpCtx != "" {
				loop.SetMCPContext(mcpCtx)
			}
			loop.SetHandler(&daemonEventHandler{})

			result, usage, runErr := loop.Run(ctx, prompt, history)
			if runErr != nil {
				log.Printf("daemon: agent error for %s: %v", agentName, runErr)
				// Send error reply so the channel sender knows it failed
				wsClient.SendReply(daemon.OutgoingReply{
					Channel:  msg.Channel,
					ThreadID: msg.ThreadID,
					Text:     fmt.Sprintf("Sorry, I encountered an error: %v", runErr),
				})
				return
			}

			sess.Messages = append(sess.Messages,
				client.Message{Role: "user", Content: client.NewTextContent(prompt)},
				client.Message{Role: "assistant", Content: client.NewTextContent(result)},
			)
			if err := sessMgr.Save(); err != nil {
				log.Printf("daemon: failed to save session: %v", err)
			}

			log.Printf("daemon: reply to %s (%d tokens, $%.4f)", agentName, usage.TotalTokens, usage.CostUSD)

			if err := wsClient.SendReply(daemon.OutgoingReply{
				Channel:  msg.Channel,
				ThreadID: msg.ThreadID,
				Text:     result,
			}); err != nil {
				log.Printf("daemon: failed to send reply: %v", err)
			}
		})

		log.Printf("daemon: connecting to %s", wsEndpoint)
		wsClient.RunWithReconnect(ctx)
		return nil
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the background daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Use Ctrl+C to stop foreground daemon. Background mode (-d) not yet implemented.")
		return nil
	},
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Not yet implemented.")
		return nil
	},
}

type daemonEventHandler struct{}

func (h *daemonEventHandler) OnToolCall(name string, args string) {}
func (h *daemonEventHandler) OnToolResult(name string, args string, result agent.ToolResult, elapsed time.Duration) {
	log.Printf("daemon: tool %s completed (%.1fs)", name, elapsed.Seconds())
}
func (h *daemonEventHandler) OnText(text string)            {}
func (h *daemonEventHandler) OnStreamDelta(delta string)    {}
func (h *daemonEventHandler) OnUsage(usage agent.TurnUsage) {}
// daemonDeniedTools are tools that should not be auto-approved in daemon mode.
// Schedule mutation tools can create persistent system-level side effects.
var daemonDeniedTools = map[string]bool{
	"schedule_create": true,
	"schedule_update": true,
	"schedule_remove": true,
}

func (h *daemonEventHandler) OnApprovalNeeded(tool string, args string) bool {
	if daemonDeniedTools[tool] {
		log.Printf("daemon: denied %s (schedule mutation not auto-approved in daemon mode)", tool)
		return false
	}
	log.Printf("daemon: auto-approving %s", tool)
	return true
}

func init() {
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	rootCmd.AddCommand(daemonCmd)
}
