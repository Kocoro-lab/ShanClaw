package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
		reg, skillsPtr, cleanup, serverErr := tools.RegisterAll(gw, cfg)
		if serverErr != nil {
			log.Printf("Warning: %v", serverErr)
		}
		_ = skillsPtr // skills are set per-request in RunAgent

		tools.RegisterCloudDelegate(reg, gw, cfg, nil, "", "") // daemon: agent forwarding per-message not yet supported

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

		deps := &daemon.ServerDeps{
			Config:       cfg,
			GW:           gw,
			Registry:     reg,
			Cleanup:      cleanup,
			ShannonDir:   shanDir,
			AgentsDir:    agentsDir,
			Auditor:      auditor,
			HookRunner:   hookRunner,
			SessionCache: sessionCache,
		}
		defer deps.ShutdownCleanup()

		wsClient := daemon.NewClient(wsEndpoint, cfg.APIKey, func(msg daemon.MessagePayload) string {
			req := daemon.RunAgentRequest{
				Text:  msg.Text,
				Agent: msg.AgentName,
			}
			// Fall back to @mention parsing if cloud didn't set agent name.
			if req.Agent == "" {
				agentName, prompt := agents.ParseAgentMention(msg.Text)
				req.Agent = agentName
				req.Text = prompt
			}
			if req.Text == "" {
				req.Text = msg.Text
			}

			result, err := daemon.RunAgent(ctx, deps, req, &daemonEventHandler{})
			if err != nil {
				log.Printf("daemon: agent error: %v", err)
				return fmt.Sprintf("Sorry, I encountered an error: %v", err)
			}

			log.Printf("daemon: reply to %s (%d tokens, $%.4f)", result.Agent, result.Usage.TotalTokens, result.Usage.CostUSD)
			return result.Reply
		}, func(text string) {
			log.Printf("daemon: [system] %s", text)
		})

		localServer := daemon.NewServer(7533, wsClient, deps, Version)
		localServer.SetCancelFunc(cancel)
		serverErrCh := make(chan error, 1)
		go func() {
			serverErrCh <- localServer.Start(ctx)
		}()
		// Give the listener a moment to bind, then check for immediate failure.
		time.Sleep(50 * time.Millisecond)
		select {
		case err := <-serverErrCh:
			log.Printf("daemon: local server failed to start: %v (continuing without it)", err)
		default:
			log.Printf("daemon: local server listening on http://127.0.0.1:7533")
		}

		log.Printf("daemon: connecting to %s", wsEndpoint)
		wsClient.RunWithReconnect(ctx)
		sessionCache.CloseAll()
		return nil
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the background daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := http.Post("http://127.0.0.1:7533/shutdown", "application/json", nil)
		if err != nil {
			return fmt.Errorf("daemon not running or not reachable: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			fmt.Println("Daemon stopped.")
		} else {
			return fmt.Errorf("unexpected response: %s", resp.Status)
		}
		return nil
	},
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := http.Get("http://127.0.0.1:7533/status")
		if err != nil {
			fmt.Println("Daemon is not running.")
			return nil
		}
		defer resp.Body.Close()

		var status struct {
			IsConnected bool   `json:"is_connected"`
			ActiveAgent string `json:"active_agent"`
			Uptime      int    `json:"uptime"`
			Version     string `json:"version"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			return fmt.Errorf("failed to parse status: %w", err)
		}

		fmt.Printf("Status:    running\n")
		if status.Version != "" {
			fmt.Printf("Version:   %s\n", status.Version)
		}
		fmt.Printf("Connected: %v\n", status.IsConnected)
		if status.ActiveAgent != "" {
			fmt.Printf("Agent:     %s\n", status.ActiveAgent)
		}
		uptime := time.Duration(status.Uptime) * time.Second
		fmt.Printf("Uptime:    %s\n", uptime)
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
func (h *daemonEventHandler) OnApprovalNeeded(tool string, args string) bool {
	if daemon.DaemonDeniedTools[tool] {
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
