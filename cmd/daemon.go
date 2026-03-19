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
	"sync"
	"syscall"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/agents"
	"github.com/Kocoro-lab/ShanClaw/internal/audit"
	"github.com/Kocoro-lab/ShanClaw/internal/client"
	"github.com/Kocoro-lab/ShanClaw/internal/config"
	"github.com/Kocoro-lab/ShanClaw/internal/daemon"
	"github.com/Kocoro-lab/ShanClaw/internal/heartbeat"
	"github.com/Kocoro-lab/ShanClaw/internal/hooks"
	"github.com/Kocoro-lab/ShanClaw/internal/permissions"
	"github.com/Kocoro-lab/ShanClaw/internal/schedule"
	"github.com/Kocoro-lab/ShanClaw/internal/tools"
	"github.com/Kocoro-lab/ShanClaw/internal/watcher"
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
		detach, _ := cmd.Flags().GetBool("detach")
		if detach {
			return daemonStartDetached()
		}

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}

		shanDir := config.ShannonDir()
		agentsDir := filepath.Join(shanDir, "agents")
		pidPath := filepath.Join(shanDir, "daemon.pid")

		force, _ := cmd.Flags().GetBool("force")
		if force {
			stopExistingDaemon(pidPath)
		}

		if daemon.IsDaemonServiceLoaded() {
			log.Println("Warning: daemon is managed by launchd. Use 'shan daemon stop' to remove launchd management.")
		}

		pidFile, err := daemon.AcquirePIDFile(pidPath)
		if err != nil {
			return err
		}
		defer pidFile.Close()

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
		scheduleManager := schedule.NewManager(filepath.Join(shanDir, "schedules.json"))

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
			Config:          cfg,
			GW:              gw,
			Registry:        reg,
			Cleanup:         cleanup,
			ShannonDir:      shanDir,
			AgentsDir:       agentsDir,
			Auditor:         auditor,
			HookRunner:      hookRunner,
			SessionCache:    sessionCache,
			ScheduleManager: scheduleManager,
		}
		defer deps.ShutdownCleanup()

		if !cfg.Daemon.AutoApprove {
			log.Println("daemon: interactive approval mode — tools requiring approval will be sent to the client for user confirmation. Set daemon.auto_approve: true in config to auto-approve all tools.")
		}

		// Create WS client first, then broker (broker needs client's send method).
		var wsClient *daemon.Client
		var broker *daemon.ApprovalBroker

		wsClient = daemon.NewClient(wsEndpoint, cfg.APIKey, func(msg daemon.MessagePayload) string {
			// Wire cloud_delegate workflow_id notification for streaming card replies.
			if msg.MessageID != "" {
				if ct, ok := deps.Registry.Get("cloud_delegate"); ok {
					if cdt, ok := ct.(*tools.CloudDelegateTool); ok {
						msgID := msg.MessageID // capture for closure
						cdt.SetOnWorkflowStarted(func(workflowID string) {
							_ = wsClient.SendProgressWithWorkflow(msgID, workflowID)
						})
					}
				}
			}

			// Use msg.Source if Cloud populates it; fall back to msg.Channel during rolling deploy
			source := msg.Source
			if source == "" {
				source = msg.Channel
			}
			req := daemon.RunAgentRequest{
				Text:     msg.Text,
				Agent:    msg.AgentName,
				Source:   source,
				Channel:  msg.Channel,
				ThreadID: msg.ThreadID,
				Sender:   msg.Sender,
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
			req.EnsureRouteKey()

			// Try injecting into an active run on the same route.
			if req.RouteKey != "" {
				switch deps.SessionCache.InjectMessage(req.RouteKey, req.Text) {
				case daemon.InjectOK:
					// Message injected — running loop will incorporate it.
					return ""
				case daemon.InjectQueueFull:
					// Active run exists but queue saturated — don't start a new run.
					log.Printf("daemon: inject queue full for route %q, message dropped", req.RouteKey)
					return ""
				case daemon.InjectNoActiveRun:
					// Fall through to start a new RunAgent
				}
			}

			// Resolve auto_approve: per-agent overrides global
			autoApprove := cfg.Daemon.AutoApprove
			if req.Agent != "" {
				if a, err := agents.LoadAgent(agentsDir, req.Agent); err == nil && a.Config != nil && a.Config.AutoApprove != nil {
					autoApprove = *a.Config.AutoApprove
				}
			}

			handler := &daemonEventHandler{
				broker:      broker,
				ctx:         ctx,
				channel:     msg.Channel,
				threadID:    msg.ThreadID,
				agent:       req.Agent,
				autoApprove: autoApprove,
				shannonDir:  shanDir,
				deps:        deps,
			}

			result, err := daemon.RunAgent(ctx, deps, req, handler)
			if err != nil {
				log.Printf("daemon: agent error: %v", err)
				return fmt.Sprintf("Sorry, I encountered an error: %v", err)
			}

			log.Printf("daemon: reply to %s (%d tokens, $%.4f)", result.Agent, result.Usage.TotalTokens, result.Usage.CostUSD)
			return result.Reply
		}, func(text string) {
			log.Printf("daemon: [system] %s", text)
		})

		broker = daemon.NewApprovalBroker(wsClient.SendApprovalRequest)
		wsClient.SetApprovalBroker(broker)

		localServer := daemon.NewServer(7533, wsClient, deps, Version)
		localServer.SetCancelFunc(cancel)
		localServer.SetApprovalResolvedNotifier(wsClient.SendApprovalResolved)
		wsClient.SetEventBus(localServer.EventBus())
		deps.EventBus = localServer.EventBus()

		// Start file watcher and heartbeat manager.
		var triggerMu sync.Mutex
		var fileWatcher *watcher.Watcher
		var hbManager *heartbeat.Manager

		watchRunFn := func(watchCtx context.Context, agentName, prompt string) {
			req := daemon.RunAgentRequest{
				Agent:  agentName,
				Source: "watcher",
				Text:   prompt,
			}
			handler := &autoApproveHandler{}
			result, err := daemon.RunAgent(watchCtx, deps, req, handler)
			if err != nil {
				log.Printf("daemon: watcher agent %q error: %v", agentName, err)
				return
			}
			log.Printf("daemon: watcher agent %q reply (%d tokens): %s", agentName, result.Usage.TotalTokens, truncateReply(result.Reply, 200))
		}
		agentWatches := collectAgentWatches(agentsDir)
		if len(agentWatches) > 0 {
			fw, err := watcher.New(agentWatches, watchRunFn)
			if err != nil {
				log.Printf("daemon: watcher init failed: %v", err)
			} else {
				fw.Start(ctx)
				fileWatcher = fw
				log.Printf("daemon: file watcher started (%d agents)", len(agentWatches))
			}
		}

		hbMgr, err := heartbeat.New(agentsDir, deps)
		if err != nil {
			log.Printf("daemon: heartbeat init failed: %v", err)
		} else {
			hbMgr.Start(ctx)
			hbManager = hbMgr
			log.Printf("daemon: heartbeat manager started")
		}

		// Start internal cron scheduler (evaluates schedules each minute).
		cronScheduler := daemon.NewScheduler(scheduleManager, deps)
		go cronScheduler.Start(ctx)
		log.Println("daemon: cron scheduler started")

		localServer.SetOnReload(func() {
			triggerMu.Lock()
			defer triggerMu.Unlock()

			// Close old watcher/heartbeat.
			if fileWatcher != nil {
				fileWatcher.Close()
				fileWatcher = nil
			}
			if hbManager != nil {
				hbManager.Close()
				hbManager = nil
			}

			// Rebuild from fresh agent configs.
			newWatches := collectAgentWatches(agentsDir)
			if len(newWatches) > 0 {
				fw, err := watcher.New(newWatches, watchRunFn)
				if err != nil {
					log.Printf("daemon: reload watcher init failed: %v", err)
				} else {
					fw.Start(ctx)
					fileWatcher = fw
					log.Printf("daemon: file watcher restarted (%d agents)", len(newWatches))
				}
			}

			newHb, err := heartbeat.New(agentsDir, deps)
			if err != nil {
				log.Printf("daemon: reload heartbeat init failed: %v", err)
			} else {
				newHb.Start(ctx)
				hbManager = newHb
				log.Printf("daemon: heartbeat manager restarted")
			}
		})

		broker.SetOnRequest(func(requestID, tool, args string) {
			if localServer.EventBus() != nil {
				payload, _ := json.Marshal(map[string]string{
					"request_id": requestID,
					"tool":       tool,
					"args":       args,
				})
				localServer.EventBus().Emit(daemon.Event{Type: daemon.EventApprovalRequest, Payload: payload})
			}
		})
		serverErrCh := make(chan error, 1)
		go func() {
			serverErrCh <- localServer.Start(ctx)
		}()
		// Give the listener a moment to bind, then check for immediate failure.
		time.Sleep(50 * time.Millisecond)
		select {
		case err := <-serverErrCh:
			return fmt.Errorf("daemon: local server failed to start: %w", err)
		default:
			log.Printf("daemon: local server listening on http://127.0.0.1:7533")
		}

		log.Printf("daemon: connecting to %s", wsEndpoint)
		wsClient.RunWithReconnect(ctx)

		triggerMu.Lock()
		if fileWatcher != nil {
			fileWatcher.Close()
			fileWatcher = nil
		}
		if hbManager != nil {
			hbManager.Close()
			hbManager = nil
		}
		triggerMu.Unlock()

		sessionCache.CloseAll()
		return nil
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the background daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		launchdManaged := daemon.IsDaemonServiceLoaded()
		if launchdManaged {
			if err := daemon.LaunchctlBootout(); err != nil {
				log.Printf("Warning: launchctl bootout failed: %v", err)
			}
			daemon.RemoveDaemonPlist()
		}

		pidPath := filepath.Join(config.ShannonDir(), "daemon.pid")

		// If launchd bootout already killed the process, we're done.
		if launchdManaged {
			// Brief wait for process to exit after bootout.
			time.Sleep(500 * time.Millisecond)
			if _, locked := daemon.IsLocked(pidPath); !locked {
				fmt.Println("Daemon stopped (launchd service removed).")
				return nil
			}
			// Process still alive — fall through to HTTP/SIGTERM.
		}

		// Try graceful HTTP shutdown first.
		resp, err := http.Post("http://127.0.0.1:7533/shutdown", "application/json", nil)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("unexpected response: %s", resp.Status)
			}
			// Wait for process to fully exit (PID file lock released).
			deadline := time.After(5 * time.Second)
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-deadline:
					fmt.Println("Daemon shutdown requested (still exiting).")
					return nil
				case <-ticker.C:
					if _, locked := daemon.IsLocked(pidPath); !locked {
						fmt.Println("Daemon stopped.")
						return nil
					}
				}
			}
		}

		// HTTP failed — fall back to SIGTERM via PID file.
		pid, locked := daemon.IsLocked(pidPath)
		if !locked {
			return fmt.Errorf("daemon not running")
		}
		if pid <= 0 {
			return fmt.Errorf("daemon PID file is locked but contains invalid PID")
		}

		proc, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("cannot find daemon process %d: %w", pid, err)
		}
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			return fmt.Errorf("failed to send SIGTERM to PID %d: %w", pid, err)
		}
		fmt.Printf("Sent SIGTERM to daemon (PID %d).\n", pid)

		// Wait for process to exit (up to 5s).
		deadline := time.After(5 * time.Second)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-deadline:
				fmt.Printf("Warning: daemon (PID %d) did not exit within 5s.\n", pid)
				return nil
			case <-ticker.C:
				if _, locked := daemon.IsLocked(pidPath); !locked {
					fmt.Println("Daemon stopped.")
					return nil
				}
			}
		}
	},
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		pidPath := filepath.Join(config.ShannonDir(), "daemon.pid")

		resp, err := http.Get("http://127.0.0.1:7533/status")
		if err != nil {
			// HTTP failed — check PID file to distinguish "not running" from "running but no HTTP server".
			if pid, locked := daemon.IsLocked(pidPath); locked {
				fmt.Printf("Status:    running (HTTP server unavailable)\n")
				fmt.Printf("PID:       %d\n", pid)
				return nil
			}
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

		pid, _ := daemon.ReadPID(pidPath)
		fmt.Printf("Status:    running\n")
		if pid > 0 {
			fmt.Printf("PID:       %d\n", pid)
		}
		if status.Version != "" {
			fmt.Printf("Version:   %s\n", status.Version)
		}
		fmt.Printf("Connected: %v\n", status.IsConnected)
		if status.ActiveAgent != "" {
			fmt.Printf("Agent:     %s\n", status.ActiveAgent)
		}
		uptime := time.Duration(status.Uptime) * time.Second
		fmt.Printf("Uptime:    %s\n", uptime)
		if daemon.IsDaemonServiceLoaded() {
			fmt.Printf("Launchd:   managed\n")
		} else {
			fmt.Printf("Launchd:   not installed\n")
		}
		return nil
	},
}

type daemonEventHandler struct {
	broker      *daemon.ApprovalBroker
	ctx         context.Context
	channel     string
	threadID    string
	agent       string
	autoApprove bool
	shannonDir  string
	deps        *daemon.ServerDeps
}

func (h *daemonEventHandler) OnToolCall(name string, args string) {}
func (h *daemonEventHandler) OnToolResult(name string, args string, result agent.ToolResult, elapsed time.Duration) {
	log.Printf("daemon: tool %s completed (%.1fs)", name, elapsed.Seconds())
}
func (h *daemonEventHandler) OnText(text string)            {}
func (h *daemonEventHandler) OnStreamDelta(delta string)    {}
func (h *daemonEventHandler) OnUsage(usage agent.TurnUsage) {}
func (h *daemonEventHandler) OnApprovalNeeded(tool string, args string) bool {
	if h.autoApprove {
		log.Printf("daemon: auto-approving %s (auto_approve=true)", tool)
		return true
	}
	decision := h.broker.Request(h.ctx, h.channel, h.threadID, h.agent, tool, args)
	if decision == daemon.DecisionAlwaysAllow {
		if tool == "bash" {
			cmd := permissions.ExtractField(args, "command")
			if cmd != "" {
				if err := config.AppendAllowedCommand(h.shannonDir, cmd); err != nil {
					log.Printf("daemon: failed to persist always-allow: %v", err)
				} else {
					// Update in-memory config under write lock.
					// Access deps.Config directly (not a captured pointer) so
					// we always mutate the current config, even after reloads.
					h.deps.WriteLock()
					perms := &h.deps.Config.Permissions
					if !containsString(perms.AllowedCommands, cmd) {
						perms.AllowedCommands = append(perms.AllowedCommands, cmd)
					}
					h.deps.WriteUnlock()
					log.Printf("daemon: always-allow persisted: %s", cmd)
				}
			}
		} else {
			h.broker.SetToolAutoApprove(tool)
			log.Printf("daemon: always-allow (session): %s", tool)
		}
	}
	return decision == daemon.DecisionAllow || decision == daemon.DecisionAlwaysAllow
}

// autoApproveHandler is a minimal EventHandler for internal triggers (watcher, heartbeat).
type autoApproveHandler struct{}

func (h *autoApproveHandler) OnToolCall(name string, args string) {}
func (h *autoApproveHandler) OnToolResult(name string, args string, result agent.ToolResult, elapsed time.Duration) {
	log.Printf("daemon: tool %s completed (%.1fs)", name, elapsed.Seconds())
}
func (h *autoApproveHandler) OnText(text string)            {}
func (h *autoApproveHandler) OnStreamDelta(delta string)    {}
func (h *autoApproveHandler) OnUsage(usage agent.TurnUsage) {}
func (h *autoApproveHandler) OnApprovalNeeded(tool string, args string) bool { return true }

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func stopExistingDaemon(pidPath string) {
	// Try graceful HTTP shutdown.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post("http://127.0.0.1:7533/shutdown", "application/json", nil)
	if err == nil {
		resp.Body.Close()
	}

	// If HTTP failed, try SIGTERM via PID file.
	if err != nil {
		if pid, locked := daemon.IsLocked(pidPath); locked && pid > 0 {
			if proc, err := os.FindProcess(pid); err == nil {
				proc.Signal(syscall.SIGTERM)
			}
		}
	}

	// Wait for lock to be released (up to 3s).
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			log.Printf("daemon: existing daemon did not stop within 3s, proceeding anyway")
			return
		case <-ticker.C:
			if _, locked := daemon.IsLocked(pidPath); !locked {
				return
			}
		}
	}
}

func daemonStartDetached() error {
	shanDir := config.ShannonDir()
	pidPath := filepath.Join(shanDir, "daemon.pid")

	if _, locked := daemon.IsLocked(pidPath); locked {
		return fmt.Errorf("daemon is already running (PID file locked)")
	}

	logDir := filepath.Join(shanDir, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	logPath := filepath.Join(logDir, "daemon.log")

	plistContent := daemon.GenerateDaemonPlist(daemon.ShanBinary(), logPath)
	plistPath := daemon.DaemonPlistPath()
	if err := daemon.WriteDaemonPlist(plistPath, plistContent); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	if err := daemon.LaunchctlBootstrap(plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}

	fmt.Printf("Daemon started via launchd.\n")
	fmt.Printf("  Plist: %s\n", plistPath)
	fmt.Printf("  Logs:  %s\n", logPath)
	fmt.Printf("Use 'shan daemon stop' to stop.\n")
	return nil
}

func truncateReply(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func collectAgentWatches(agentsDir string) map[string][]watcher.WatchEntry {
	result := make(map[string][]watcher.WatchEntry)
	names, err := agents.ListAgents(agentsDir)
	if err != nil {
		return result
	}
	for _, name := range names {
		a, err := agents.LoadAgent(agentsDir, name)
		if err != nil || a.Config == nil || len(a.Config.Watch) == 0 {
			continue
		}
		for _, w := range a.Config.Watch {
			result[name] = append(result[name], watcher.WatchEntry{
				Path: w.Path,
				Glob: w.Glob,
			})
		}
	}
	return result
}

func init() {
	daemonStartCmd.Flags().Bool("force", false, "Stop any existing daemon before starting")
	daemonStartCmd.Flags().BoolP("detach", "d", false, "Run as background service via launchd (macOS only)")
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	rootCmd.AddCommand(daemonCmd)
}
