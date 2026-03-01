package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Kocoro-lab/shan/internal/agent"
	"github.com/Kocoro-lab/shan/internal/audit"
	"github.com/Kocoro-lab/shan/internal/client"
	"github.com/Kocoro-lab/shan/internal/config"
	"github.com/Kocoro-lab/shan/internal/hooks"
	"github.com/Kocoro-lab/shan/internal/instructions"
	"github.com/Kocoro-lab/shan/internal/mcp"
	"github.com/Kocoro-lab/shan/internal/session"
	"github.com/Kocoro-lab/shan/internal/tools"
	"github.com/Kocoro-lab/shan/internal/update"
)

type state int

const (
	stateInput state = iota
	stateProcessing
	stateApproval
	stateSessionPicker
)

type agentDoneMsg struct {
	result string
	usage  *agent.TurnUsage
	err    error
}

type approvalRequestMsg struct {
	tool string
	args string
}

type healthCheckMsg struct {
	gatewayOK  bool
	newVersion string
}

type serverToolsLoadedMsg struct {
	registry *agent.ToolRegistry
	err      error
}

// streamOutputMsg is sent from goroutines to update the TUI output safely.
type streamOutputMsg struct {
	text string
}

// spinnerTickMsg rotates the spinner text
type spinnerTickMsg struct{}

// streamDeltaMsg carries an incremental text chunk from SSE streaming.
type streamDeltaMsg struct {
	delta string
}

type Model struct {
	cfg           *config.Config
	gateway       *client.GatewayClient
	sessions      *session.Manager
	toolRegistry  *agent.ToolRegistry
	toolCleanup   func()
	agentLoop     *agent.AgentLoop
	textarea      textarea.Model
	output        []string
	pendingPrints []string
	streamingText  string
	spinnerIdx     int
	spinnerTexts   []string
	lastSessions      []session.SessionSummary // cached for session picker
	sessionPickerIdx  int
	state         state
	width         int
	height        int
	version       string
	approvalCh    chan bool
	program       *tea.Program
	shannonDir    string
	auditor       *audit.AuditLogger
	hookRunner    *hooks.HookRunner
	serverToolErr        error // non-nil if server tools failed to load
	customCommands       map[string]string // name → prompt content from commands/*.md
	bypassPermissions    bool
	// Slash command completion menu
	menuVisible   bool
	menuIndex     int
	menuItems     []slashCmd
}

type slashCmd struct {
	cmd  string
	desc string
}

// SetProgram stores the bubbletea program reference so goroutines can
// inject messages (e.g. approval prompts) into the TUI event loop.
func (m *Model) SetProgram(p *tea.Program) {
	m.program = p
}

func (m *Model) SetBypassPermissions(bypass bool) {
	m.bypassPermissions = bypass
	if m.agentLoop != nil {
		m.agentLoop.SetBypassPermissions(bypass)
	}
}

func New(cfg *config.Config, version string) *Model {
	// Get terminal width for initial sizing
	width := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = w
	}

	ta := textarea.New()
	ta.Placeholder = "Type a message or /help..."
	ta.Prompt = "" // We render our own "> " prompt in View()
	ta.Focus()
	ta.SetHeight(1)
	ta.SetWidth(width - 4)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0 // unlimited

	gateway := client.NewGatewayClient(cfg.Endpoint, cfg.APIKey)
	shannonDir := config.ShannonDir()
	sessDir := shannonDir + "/sessions"
	sessMgr := session.NewManager(sessDir)
	sessMgr.NewSession()

	// Create audit logger (best-effort)
	var auditor *audit.AuditLogger
	if shannonDir != "" {
		logDir := filepath.Join(shannonDir, "logs")
		if a, err := audit.NewAuditLogger(logDir); err == nil {
			auditor = a
		}
	}

	reg, toolCleanup := tools.RegisterLocalTools(cfg)

	// Connect MCP servers (best-effort, before TUI starts)
	var mcpMgr *mcp.ClientManager
	if len(cfg.MCPServers) > 0 {
		mcpMgr = mcp.NewClientManager()
		mcpCtx, mcpCancel := context.WithTimeout(context.Background(), 10*time.Second)
		mcpTools, mcpErr := mcpMgr.ConnectAll(mcpCtx, cfg.MCPServers)
		mcpCancel()
		if mcpErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: MCP servers: %v\n", mcpErr)
		}
		for _, t := range mcpTools {
			if _, exists := reg.Get(t.Tool.Name); exists {
				continue
			}
			reg.Register(tools.NewMCPTool(t.ServerName, t.Tool, mcpMgr))
		}
		if len(mcpTools) > 0 {
			fmt.Fprintf(os.Stderr, "MCP: %d tools from %d servers\n", len(mcpTools), len(cfg.MCPServers))
		}
	}

	origCleanup := toolCleanup
	toolCleanup = func() {
		origCleanup()
		if mcpMgr != nil {
			mcpMgr.Close()
		}
	}

	hookRunner := hooks.NewHookRunner(cfg.Hooks)
	loop := agent.NewAgentLoop(gateway, reg, cfg.ModelTier, shannonDir, cfg.Agent.MaxIterations, cfg.Tools.ResultTruncation, cfg.Tools.ArgsTruncation, &cfg.Permissions, auditor, hookRunner)
	if mcpCtx := mcp.BuildContext(cfg.MCPServers); mcpCtx != "" {
		loop.SetMCPContext(mcpCtx)
	}

	settings := config.LoadSettings()

	// Load custom commands and add to slash command list
	customCmds, _ := instructions.LoadCustomCommands(shannonDir, ".")
	for name, _ := range customCmds {
		allSlashCommands = append(allSlashCommands, slashCmd{
			cmd:  "/" + name,
			desc: "Custom command",
		})
	}

	m := &Model{
		cfg:          cfg,
		gateway:      gateway,
		sessions:     sessMgr,
		agentLoop:    loop,
		textarea:     ta,
		width:        width,
		version:      version,
		approvalCh:   make(chan bool, 1),
		spinnerTexts: settings.SpinnerTexts,
		toolRegistry:   reg,
		toolCleanup:    toolCleanup,
		shannonDir:     shannonDir,
		auditor:        auditor,
		hookRunner:     hookRunner,
		customCommands: customCmds,
	}

	return m
}

func (m *Model) Init() tea.Cmd {
	m.appendOutput(fmt.Sprintf("  Shannon CLI %s", m.version))
	m.hookRunner.RunSessionStart(context.Background(), "")
	return tea.Batch(
		textarea.Blink,
		m.checkHealth(),
		m.loadServerTools(),
		m.flushPrints(),
	)
}

func (m *Model) loadServerTools() tea.Cmd {
	return func() tea.Msg {
		if m.toolRegistry == nil {
			return serverToolsLoadedMsg{err: fmt.Errorf("tool registry not initialized")}
		}

		reg := m.toolRegistry.Clone()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := tools.RegisterServerTools(ctx, m.gateway, reg)
		return serverToolsLoadedMsg{
			registry: reg,
			err:      err,
		}
	}
}

func (m *Model) checkHealth() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		msg := healthCheckMsg{}
		msg.gatewayOK = m.gateway.Health(ctx) == nil

		if m.cfg.AutoUpdateCheck {
			if release, found, _ := update.CheckForUpdate(m.version); found {
				msg.newVersion = release.Version()
			}
		}
		return msg
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.update(msg)
	if flush := m.flushPrints(); flush != nil {
		cmd = tea.Batch(cmd, flush)
	}
	return model, cmd
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			m.hookRunner.RunStop(context.Background(), "")
			m.sessions.Save()
			if m.toolCleanup != nil {
				m.toolCleanup()
			}
			return m, tea.Quit
		case tea.KeyEscape:
			if m.menuVisible {
				m.menuVisible = false
				return m, nil
			}
		case tea.KeyTab:
			if m.menuVisible && len(m.menuItems) > 0 {
				selected := m.menuItems[m.menuIndex]
				m.textarea.SetValue(selected.cmd + " ")
				m.menuVisible = false
				return m, nil
			}
		case tea.KeyEnter:
			// Alt+Enter: insert newline instead of submitting
			if m.state == stateInput && !m.menuVisible && msg.Alt {
				m.textarea.InsertString("\n")
				m.adjustTextareaHeight()
				return m, nil
			}
			if m.menuVisible && len(m.menuItems) > 0 {
				selected := m.menuItems[m.menuIndex]
				m.textarea.SetValue(selected.cmd + " ")
				m.menuVisible = false
				return m, nil
			}
			if m.state == stateApproval {
				// handled below
			} else if m.state == stateInput {
				return m.handleSubmit()
			}
		case tea.KeyUp:
			if m.state == stateInput && m.menuVisible && len(m.menuItems) > 0 {
				m.menuIndex--
				if m.menuIndex < 0 {
					m.menuIndex = len(m.menuItems) - 1
				}
				return m, nil
			}
		case tea.KeyDown:
			if m.state == stateInput && m.menuVisible && len(m.menuItems) > 0 {
				m.menuIndex++
				if m.menuIndex >= len(m.menuItems) {
					m.menuIndex = 0
				}
				return m, nil
			}
		}

		if m.state == stateSessionPicker {
			switch msg.Type {
			case tea.KeyUp:
				m.sessionPickerIdx--
				if m.sessionPickerIdx < 0 {
					m.sessionPickerIdx = len(m.lastSessions) - 1
				}
				return m, nil
			case tea.KeyDown:
				m.sessionPickerIdx++
				if m.sessionPickerIdx >= len(m.lastSessions) {
					m.sessionPickerIdx = 0
				}
				return m, nil
			case tea.KeyEnter:
				if len(m.lastSessions) > 0 {
					target := m.lastSessions[m.sessionPickerIdx].ID
					sess, err := m.sessions.Resume(target)
					if err != nil {
						m.appendOutput(fmt.Sprintf("Error: %v", err))
					} else {
						m.loadSessionHistory(sess)
					}
				}
				m.state = stateInput
				return m, nil
			case tea.KeyEscape:
				m.state = stateInput
				return m, nil
			}
			return m, nil
		}

		if m.state == stateApproval {
			switch msg.String() {
			case "y", "Y":
				m.approvalCh <- true
				m.state = stateProcessing
				return m, nil
			case "n", "N":
				m.approvalCh <- false
				m.state = stateProcessing
				return m, nil
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.textarea.SetWidth(msg.Width - 4)
		return m, nil

	case spinnerTickMsg:
		if m.state == stateProcessing {
			m.spinnerIdx = (m.spinnerIdx + 1) % len(m.spinnerTexts)
			return m, spinnerTick()
		}
		return m, nil

	case agentDoneMsg:
		m.state = stateInput
		if m.streamingText != "" {
			m.appendOutput(m.streamingText)
			m.streamingText = ""
		}
		if msg.err != nil {
			m.appendOutput("Error: " + msg.err.Error())
		}
		if msg.usage != nil {
			m.appendOutput(fmt.Sprintf("  [tokens: %d | cost: $%.4f]", msg.usage.TotalTokens, msg.usage.CostUSD))
		}
		m.sessions.Save()
		return m, nil

	case approvalRequestMsg:
		m.state = stateApproval
		m.appendOutput(fmt.Sprintf("\n  Tool: %s\n  Args: %s\n  Allow? [y/n]", msg.tool, msg.args))
		return m, nil

	case serverToolsLoadedMsg:
		if msg.err != nil {
			m.serverToolErr = msg.err
			m.appendOutput(fmt.Sprintf("  %v", msg.err))
			return m, nil
		}
		if msg.registry != nil {
			m.toolRegistry = msg.registry
			m.agentLoop = agent.NewAgentLoop(m.gateway, msg.registry, m.cfg.ModelTier, m.shannonDir, m.cfg.Agent.MaxIterations, m.cfg.Tools.ResultTruncation, m.cfg.Tools.ArgsTruncation, &m.cfg.Permissions, m.auditor, m.hookRunner)
			m.agentLoop.SetBypassPermissions(m.bypassPermissions)
			m.serverToolErr = nil
		}
		return m, nil

	case healthCheckMsg:
		if msg.gatewayOK {
			m.appendOutput(fmt.Sprintf("  Connected to %s", m.cfg.Endpoint))
		} else {
			m.appendOutput(fmt.Sprintf("  Warning: API unreachable at %s", m.cfg.Endpoint))
		}
		if m.serverToolErr != nil {
			m.appendOutput(fmt.Sprintf("  %v", m.serverToolErr))
		}
		if msg.newVersion != "" {
			m.appendOutput(fmt.Sprintf("  Update available: v%s — run /update", msg.newVersion))
		}
		m.appendOutput("")
		return m, nil

	case streamOutputMsg:
		m.appendOutput(msg.text)
		return m, nil

	case streamDeltaMsg:
		m.streamingText += msg.delta
		return m, nil

	case clipboardResultMsg:
		if msg.err != nil {
			m.appendOutput(fmt.Sprintf("Copy failed: %v", msg.err))
		} else {
			m.appendOutput(fmt.Sprintf("Copied to clipboard (%d chars)", msg.len))
		}
		return m, nil
	}

	if m.state == stateInput {
		var taCmd tea.Cmd
		m.textarea, taCmd = m.textarea.Update(msg)
		m.adjustTextareaHeight()
		m.updateMenu()
		return m, taCmd
	}
	return m, nil
}

func (m *Model) View() string {
	var sb strings.Builder

	// --- Input / status line ---
	switch m.state {
	case stateInput:
		prompt := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("> ")
		sb.WriteString(prompt)
		input := m.textarea.Value()
		if strings.HasPrefix(input, "/") {
			cmdStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Bold(true)
			spaceIdx := strings.Index(input, " ")
			if spaceIdx == -1 {
				sb.WriteString(cmdStyle.Render(input))
			} else {
				sb.WriteString(cmdStyle.Render(input[:spaceIdx]))
				sb.WriteString(input[spaceIdx:])
			}
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Render("\u2588"))
		} else {
			sb.WriteString(m.textarea.View())
		}
	case stateProcessing:
		if m.streamingText != "" {
			lines := strings.Split(m.streamingText, "\n")
			sb.WriteString(lines[len(lines)-1])
		} else {
			spinnerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
			spinnerText := m.spinnerTexts[m.spinnerIdx%len(m.spinnerTexts)]
			sb.WriteString(spinnerStyle.Render("* " + spinnerText))
		}
	case stateApproval:
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("  [y/n] "))
	case stateSessionPicker:
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("  Sessions (Up/Down, Enter, Esc)"))
	}

	// --- Dropdown (only when visible) ---
	if m.state == stateInput && m.menuVisible {
		sb.WriteString("\n")
		sb.WriteString(m.renderMenu())
	} else if m.state == stateSessionPicker {
		sb.WriteString("\n")
		sb.WriteString(renderDropList(dropListSize, len(m.lastSessions), m.sessionPickerIdx, func(i int) (string, string) {
			s := m.lastSessions[i]
			title := s.Title
			if len(title) > 40 {
				title = title[:37] + "..."
			}
			desc := fmt.Sprintf("[%s] %d msgs", s.CreatedAt.Format("Jan 02 15:04"), s.MsgCount)
			return title, desc
		}))
	}

	return sb.String()
}

func (m *Model) handleSubmit() (tea.Model, tea.Cmd) {
	input := strings.TrimSpace(m.textarea.Value())
	m.textarea.Reset()
	m.textarea.SetHeight(1)

	if input == "" {
		return m, nil
	}

	m.appendOutput(fmt.Sprintf("> %s", input))

	// Check slash commands
	if strings.HasPrefix(input, "/") {
		return m.handleSlashCommand(input)
	}

	// Local agent loop
	m.state = stateProcessing
	sess := m.sessions.Current()
	// Set title from first user message
	if sess.Title == "New session" {
		sess.Title = sessionTitle(input)
	}
	sess.Messages = append(sess.Messages, client.Message{Role: "user", Content: input})

	m.spinnerIdx = 0
	return m, tea.Batch(m.runAgentLoop(input, sess.Messages[:len(sess.Messages)-1]), spinnerTick())
}

func (m *Model) runAgentLoop(query string, history []client.Message) tea.Cmd {
	return func() tea.Msg {
		handler := &tuiEventHandler{model: m}
		m.agentLoop.SetHandler(handler)

		result, usage, err := m.agentLoop.Run(context.Background(), query, history)
		if err == nil && result != "" {
			sess := m.sessions.Current()
			sess.Messages = append(sess.Messages, client.Message{Role: "assistant", Content: result})
		}
		return agentDoneMsg{result: result, usage: usage, err: err}
	}
}

func (m *Model) loadSessionHistory(sess *session.Session) {
	m.output = nil
	m.appendOutput(fmt.Sprintf("  Session: %s", sess.Title))
	m.appendOutput("")
	for _, msg := range sess.Messages {
		switch msg.Role {
		case "user":
			m.appendOutput(fmt.Sprintf("> %s", msg.Content))
		case "assistant":
			m.appendOutput(msg.Content)
			m.appendOutput("")
		}
	}
}

func (m *Model) appendOutput(text string) {
	m.output = append(m.output, text)
	m.pendingPrints = append(m.pendingPrints, text)
}

func (m *Model) adjustTextareaHeight() {
	lines := strings.Count(m.textarea.Value(), "\n") + 1
	height := lines
	if height > 6 {
		height = 6
	}
	if height < 1 {
		height = 1
	}
	m.textarea.SetHeight(height)
}

// flushPrints returns a Cmd that prints all pending output above the view.
// Println sends to an unbuffered channel (p.msgs) so it MUST NOT be called
// synchronously inside Update — that deadlocks. Running it as a Cmd
// (in a goroutine) avoids the deadlock.
func (m *Model) flushPrints() tea.Cmd {
	if len(m.pendingPrints) == 0 {
		return nil
	}
	texts := make([]string, len(m.pendingPrints))
	copy(texts, m.pendingPrints)
	m.pendingPrints = m.pendingPrints[:0]
	return func() tea.Msg {
		for _, text := range texts {
			m.program.Println(text)
		}
		return nil
	}
}

func spinnerTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// sendOutput sends output from a goroutine through the bubbletea event loop
// so the TUI actually re-renders. Use this instead of appendOutput from goroutines.
func (m *Model) sendOutput(text string) {
	if m.program != nil {
		m.program.Send(streamOutputMsg{text: text})
	}
}

// sendStatus sends an ephemeral status pill from a goroutine. It replaces the
// previous status (like the desktop frontend's status pills).


func (m *Model) handleSlashCommand(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	cmd := parts[0]

	switch cmd {
	case "/quit", "/exit":
		m.hookRunner.RunStop(context.Background(), "")
		m.sessions.Save()
		if m.toolCleanup != nil {
			m.toolCleanup()
		}
		return m, tea.Quit
	case "/help":
		m.appendOutput(helpText())
	case "/clear":
		m.output = nil
	case "/sessions":
		sessions, err := m.sessions.List()
		if err != nil {
			m.appendOutput(fmt.Sprintf("Error: %v", err))
		} else if len(sessions) == 0 {
			m.appendOutput("No saved sessions")
		} else {
			m.lastSessions = sessions
			m.sessionPickerIdx = 0
			m.state = stateSessionPicker
		}
	case "/session":
		if len(parts) > 1 {
			switch parts[1] {
			case "new":
				m.sessions.NewSession()
				m.appendOutput("Started new session")
			case "resume":
				if len(parts) < 3 {
					m.appendOutput("Usage: /session resume <number or id>")
				} else {
					target := parts[2]
					// Try as 1-based index from /sessions list
					if n, err := strconv.Atoi(target); err == nil && n >= 1 && n <= len(m.lastSessions) {
						target = m.lastSessions[n-1].ID
					}
					sess, err := m.sessions.Resume(target)
					if err != nil {
						m.appendOutput(fmt.Sprintf("Error: %v", err))
					} else {
						m.loadSessionHistory(sess)
					}
				}
			}
		}
	case "/model":
		if len(parts) > 1 {
			m.cfg.ModelTier = parts[1]
			m.agentLoop.SetModelTier(parts[1])
			m.appendOutput(fmt.Sprintf("Model tier: %s", parts[1]))
		} else {
			m.appendOutput(fmt.Sprintf("Current model tier: %s", m.cfg.ModelTier))
		}
	case "/config":
		m.appendOutput(formatConfigDisplay(m.cfg))
	case "/setup":
		m.appendOutput("Setup cannot run inside the TUI. Exit and run: shan --setup")
	case "/update":
		m.appendOutput("Checking for updates...")
		newVersion, err := update.DoUpdate(m.version)
		if err != nil {
			m.appendOutput(fmt.Sprintf("  %v", err))
		} else {
			m.appendOutput(fmt.Sprintf("  Updated to %s. Restart to use new version.", newVersion))
		}
	case "/copy":
		sess := m.sessions.Current()
		if sess != nil && len(sess.Messages) > 0 {
			// Find the last assistant message
			for i := len(sess.Messages) - 1; i >= 0; i-- {
				if sess.Messages[i].Role == "assistant" {
					return m, copyToClipboard(sess.Messages[i].Content)
				}
			}
			m.appendOutput("No assistant message to copy")
		} else {
			m.appendOutput("No messages in session")
		}
	case "/research":
		return m.handleResearch(parts[1:])
	case "/swarm":
		return m.handleSwarm(parts[1:])
	default:
		// Check custom commands
		cmdName := strings.TrimPrefix(cmd, "/")
		if promptContent, ok := m.customCommands[cmdName]; ok {
			// Replace $ARGUMENTS with the rest of the input
			args := ""
			if len(parts) > 1 {
				args = strings.Join(parts[1:], " ")
			}
			expandedPrompt := strings.ReplaceAll(promptContent, "$ARGUMENTS", args)
			// Send as a regular user message through the agent loop
			m.state = stateProcessing
			m.spinnerIdx = 0
			sess := m.sessions.Current()
			var history []client.Message
			if sess != nil {
				history = sess.Messages
			}
			return m, tea.Batch(m.runAgentLoop(expandedPrompt, history), spinnerTick())
		}
		m.appendOutput(fmt.Sprintf("Unknown command: %s (type /help)", cmd))
	}

	return m, nil
}

func (m *Model) handleResearch(args []string) (tea.Model, tea.Cmd) {
	strategy := "standard"
	query := strings.Join(args, " ")

	if len(args) > 0 {
		switch args[0] {
		case "quick", "standard", "deep", "academic":
			strategy = args[0]
			query = strings.Join(args[1:], " ")
		}
	}

	if query == "" {
		m.appendOutput("Usage: /research [quick|standard|deep] <query>")
		return m, nil
	}

	m.state = stateProcessing
	m.spinnerIdx = 0
	m.appendOutput(fmt.Sprintf("Starting %s research...", strategy))

	return m, tea.Batch(m.runRemote(query, map[string]any{"force_research": true}, strategy), spinnerTick())
}

func (m *Model) handleSwarm(args []string) (tea.Model, tea.Cmd) {
	query := strings.Join(args, " ")
	if query == "" {
		m.appendOutput("Usage: /swarm <query>")
		return m, nil
	}

	m.state = stateProcessing
	m.spinnerIdx = 0
	m.appendOutput("Starting swarm workflow...")

	return m, tea.Batch(m.runRemote(query, map[string]any{"force_swarm": true}, ""), spinnerTick())
}

func (m *Model) runRemote(query string, ctx map[string]any, strategy string) tea.Cmd {
	// Set title from query if still default
	sess := m.sessions.Current()
	if sess.Title == "New session" {
		sess.Title = sessionTitle(query)
	}
	return func() tea.Msg {
		taskReq := client.TaskRequest{
			Query:            query,
			SessionID:        m.sessions.Current().ID,
			Context:          ctx,
			ResearchStrategy: strategy,
		}

		resp, err := m.gateway.SubmitTaskStream(context.Background(), taskReq)
		if err != nil {
			return agentDoneMsg{err: fmt.Errorf("submit task: %w", err)}
		}

		m.sessions.Current().RemoteTasks = append(m.sessions.Current().RemoteTasks, resp.WorkflowID)

		var finalResult string
		var workflowErr error

		// Use API-provided stream URL if available, otherwise construct from base
		streamURL := resp.StreamURL
		if streamURL == "" {
			streamURL = m.gateway.StreamURL(resp.WorkflowID)
		}
		streamURL = m.gateway.ResolveURL(streamURL)

		err = client.StreamSSE(context.Background(), streamURL, m.cfg.APIKey, func(ev client.SSEEvent) {
			// Common event structure — most events have a message field
			var event struct {
				Message   string `json:"message"`
				AgentID   string `json:"agent_id"`
				Delta     string `json:"delta"`
				Response  string `json:"response"`
				Type      string `json:"type"`
			}
			json.Unmarshal([]byte(ev.Data), &event)

			switch ev.Event {
			// --- Streaming content ---
			case "thread.message.delta", "LLM_PARTIAL":
				// Skip title_generator deltas (not user-facing content)
				if event.AgentID == "title_generator" {
					break
				}
				if event.Delta != "" && m.program != nil {
					m.program.Send(streamDeltaMsg{delta: event.Delta})
				}
			case "thread.message.completed", "LLM_OUTPUT":
				if event.AgentID == "title_generator" {
					// Capture generated title for session
					if event.Response != "" {
						title := strings.TrimSpace(event.Response)
						title = strings.Trim(title, "\"'`")
						if title != "" {
							m.sessions.Current().Title = sessionTitle(title)
						}
					}
					break
				}
				if event.Response != "" {
					finalResult = event.Response
				}

			// --- Status pill events (ephemeral, replace previous) ---
			case "WORKFLOW_STARTED":
				m.sendOutput("  > " + statusMessage(event.Message, "Starting workflow..."))
			case "PROGRESS", "STATUS_UPDATE":
				m.sendOutput("  > " + statusMessage(event.Message, "Processing..."))
			case "AGENT_STARTED":
				m.sendOutput("  > " + statusMessage(event.Message, "Agent working..."))
			case "AGENT_THINKING":
				msg := event.Message
				if len(msg) > 100 {
					msg = "" // skip verbose reasoning (matches desktop behavior)
				}
				m.sendOutput("  ~ " + statusMessage(msg, "Thinking..."))
			case "DELEGATION":
				m.sendOutput("  > " + statusMessage(event.Message, "Delegating task..."))
			case "DATA_PROCESSING":
				m.sendOutput("  > " + statusMessage(event.Message, "Processing data..."))
			case "TOOL_INVOKED", "TOOL_STARTED":
				m.sendOutput("  ? " + statusMessage(event.Message, "Calling tool..."))
			case "TOOL_OBSERVATION", "TOOL_COMPLETED":
				m.sendOutput("  * " + statusMessage(event.Message, "Tool completed"))
			case "WAITING":
				m.sendOutput("  . " + statusMessage(event.Message, "Waiting..."))
			case "LLM_PROMPT":
				// Not shown in conversation (matches desktop)

			// --- Terminal events (persist in output) ---
			case "AGENT_COMPLETED":
				m.sendOutput("  + " + statusMessage(event.Message, "Agent completed"))
			case "WORKFLOW_COMPLETED":

				if finalResult == "" {
					finalResult = event.Message
				}
			case "WORKFLOW_FAILED", "error", "ERROR_OCCURRED":
				m.sendOutput("  ! Error: " + statusMessage(event.Message, "Workflow failed"))
				workflowErr = fmt.Errorf("workflow failed: %s", event.Message)

			// --- Control flow events ---
			case "workflow.pausing":
				m.sendOutput("  || Pausing at next checkpoint...")
			case "workflow.paused":
				m.sendOutput("  || Workflow paused")
			case "workflow.resumed":
				m.sendOutput("  > Resumed")
			case "workflow.cancelling":
				m.sendOutput("  x Cancelling...")
			case "workflow.cancelled":
				m.sendOutput("  Task was cancelled.")
				workflowErr = fmt.Errorf("workflow cancelled")

			// --- Informational (show as status briefly) ---
			case "APPROVAL_REQUESTED":
				m.sendOutput("  ! " + statusMessage(event.Message, "Awaiting approval..."))
			case "ERROR_RECOVERY":
				m.sendOutput("  ~ " + statusMessage(event.Message, "Recovering from error..."))
			case "ROLE_ASSIGNED", "TEAM_RECRUITED", "TEAM_RETIRED", "TEAM_STATUS",
				"DEPENDENCY_SATISFIED", "MESSAGE_SENT", "MESSAGE_RECEIVED",
				"WORKSPACE_UPDATED", "APPROVAL_DECISION", "BUDGET_THRESHOLD":
				if event.Message != "" {
					m.sendOutput("  > " + event.Message)
				}

			// --- Research plan HITL ---
			case "RESEARCH_PLAN_READY", "RESEARCH_PLAN_UPDATED":
				m.sendOutput("  Research plan ready for review")
			case "RESEARCH_PLAN_APPROVED":
				m.sendOutput("  Research plan approved, executing...")

			default:
				// Unknown events — show message if present, skip raw JSON
				if event.Message != "" {
					m.sendOutput("  > " + event.Message)
				}
			}
		})

		if err != nil {
			return agentDoneMsg{err: fmt.Errorf("stream: %w", err)}
		}
		if workflowErr != nil {
			return agentDoneMsg{err: workflowErr}
		}

		if finalResult != "" {
			m.sendOutput(renderMarkdown(finalResult))
			sess := m.sessions.Current()
			sess.Messages = append(sess.Messages,
				client.Message{Role: "user", Content: query},
				client.Message{Role: "assistant", Content: finalResult},
			)
		} else {
			return agentDoneMsg{err: fmt.Errorf("workflow completed but returned no response")}
		}

		return agentDoneMsg{result: finalResult}
	}
}

func (m *Model) showSessions() {
	sessions, err := m.sessions.List()
	if err != nil {
		m.appendOutput(fmt.Sprintf("Error: %v", err))
		return
	}
	if len(sessions) == 0 {
		m.appendOutput("No saved sessions")
		return
	}
	m.lastSessions = sessions
	for i, s := range sessions {
		m.appendOutput(fmt.Sprintf("  %d. [%s] %s (%d messages)",
			i+1, s.CreatedAt.Format("Jan 02"), s.Title, s.MsgCount))
	}
	m.appendOutput("  Use /session resume <number> to resume")
}

func helpText() string {
	return `Keys:
  Alt+Enter                      Insert newline (multi-line input)
  Enter                          Submit message

Commands:
  /help                          Show this help
  /research [quick|standard|deep] <query>  Remote research
  /swarm <query>                 Multi-agent swarm
  /config                        Show configuration
  /setup                         Reconfigure endpoint & API key
  /sessions                      List saved sessions
  /session new                   Start new session
  /session resume <id>           Resume a saved session
  /model [small|medium|large]    Switch model tier
  /copy                          Copy last response to clipboard
  /clear                         Clear screen
  /quit                          Exit`
}

// tuiEventHandler bridges agent events to the TUI
type tuiEventHandler struct {
	model *Model
}

func (h *tuiEventHandler) OnToolCall(name string, args string) {
	h.model.sendOutput(fmt.Sprintf("  Tool: %s(%s)", name, truncate(args, 80)))
}

func (h *tuiEventHandler) OnToolResult(name string, result agent.ToolResult) {
	if result.IsError {
		h.model.sendOutput(fmt.Sprintf("  Error: %s", truncate(result.Content, 200)))
	} else if result.Content != "" {
		dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
		h.model.sendOutput(dimStyle.Render(fmt.Sprintf("  Result: %s", truncate(result.Content, 500))))
	}
}

func (h *tuiEventHandler) OnText(text string) {
	h.model.sendOutput(renderMarkdown(text))
}

func (h *tuiEventHandler) OnUsage(usage agent.TurnUsage) {}

func (h *tuiEventHandler) OnApprovalNeeded(tool string, args string) bool {
	// Send approval prompt to the TUI event loop, then block until user responds.
	// This runs inside a tea.Cmd goroutine so blocking is safe — it won't freeze the UI.
	if h.model.program != nil {
		h.model.program.Send(approvalRequestMsg{tool: tool, args: truncate(args, 200)})
		return <-h.model.approvalCh
	}
	// No program reference — deny by default (should not happen in normal flow)
	return false
}

type clipboardResultMsg struct {
	err error
	len int
}

func copyToClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		// macOS: pbcopy, Linux: xclip or xsel
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("pbcopy")
		case "linux":
			cmd = exec.Command("xclip", "-selection", "clipboard")
		default:
			return clipboardResultMsg{err: fmt.Errorf("clipboard not supported on %s", runtime.GOOS)}
		}
		cmd.Stdin = strings.NewReader(text)
		err := cmd.Run()
		return clipboardResultMsg{err: err, len: len(text)}
	}
}

// sessionTitle creates a short, readable title from user input.
// Truncates to 50 chars at a word boundary, strips leading/trailing whitespace
// and newlines, and ensures single-line output.
func sessionTitle(input string) string {
	// Take first line only
	if idx := strings.IndexAny(input, "\n\r"); idx >= 0 {
		input = input[:idx]
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return "New session"
	}
	const maxLen = 50
	if len(input) <= maxLen {
		return input
	}
	// Truncate at word boundary
	truncated := input[:maxLen]
	if lastSpace := strings.LastIndex(truncated, " "); lastSpace > maxLen/2 {
		truncated = truncated[:lastSpace]
	}
	return truncated + "..."
}

var allSlashCommands = []slashCmd{
	{"/help", "Show help"},
	{"/research", "Remote research"},
	{"/swarm", "Multi-agent swarm"},
	{"/copy", "Copy last response"},
	{"/model", "Switch model tier"},
	{"/config", "Show configuration"},
	{"/setup", "Reconfigure endpoint & API key"},
	{"/sessions", "List saved sessions"},
	{"/session", "new | resume <n>"},
	{"/clear", "Clear screen"},
	{"/update", "Check for updates"},
	{"/quit", "Exit"},
}

func (m *Model) updateMenu() {
	input := m.textarea.Value()
	if !strings.HasPrefix(input, "/") || strings.Contains(input, " ") {
		m.menuVisible = false
		m.menuItems = nil
		m.menuIndex = 0
		return
	}

	var matches []slashCmd
	for _, c := range allSlashCommands {
		if strings.HasPrefix(c.cmd, input) {
			matches = append(matches, c)
		}
	}
	m.menuItems = matches
	m.menuVisible = len(matches) > 0
	if m.menuIndex >= len(matches) {
		m.menuIndex = 0
	}
}


const dropListSize = 5

func (m *Model) renderMenu() string {
	return renderDropList(dropListSize, len(m.menuItems), m.menuIndex, func(i int) (string, string) {
		return m.menuItems[i].cmd, m.menuItems[i].desc
	})
}

// renderDropList renders a scrollable drop-down list with a fixed visible window.
// Always pads to maxVisible lines so the layout doesn't jump.
func renderDropList(maxVisible, total, selected int, item func(i int) (label, desc string)) string {
	if total == 0 {
		// Pad empty lines to keep layout stable
		return strings.Repeat("\n", maxVisible)
	}

	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	highlightLabel := lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Bold(true)
	highlightDesc := lipgloss.NewStyle().Foreground(lipgloss.Color("146"))

	// Calculate sliding window
	visible := total
	if visible > maxVisible {
		visible = maxVisible
	}
	start := 0
	if selected >= maxVisible {
		start = selected - maxVisible + 1
	}
	if start+visible > total {
		start = total - visible
	}
	if start < 0 {
		start = 0
	}

	var sb strings.Builder
	for i := start; i < start+visible; i++ {
		label, desc := item(i)
		if i == selected {
			sb.WriteString(fmt.Sprintf("  > %s  %s\n",
				highlightLabel.Render(fmt.Sprintf("%-14s", label)),
				highlightDesc.Render(desc)))
		} else {
			sb.WriteString(fmt.Sprintf("    %s  %s\n",
				dimStyle.Render(fmt.Sprintf("%-14s", label)),
				dimStyle.Render(desc)))
		}
	}

	// Pad remaining lines to keep layout stable
	for i := visible; i < maxVisible; i++ {
		sb.WriteString("\n")
	}
	return sb.String()
}

func statusMessage(msg, fallback string) string {
	if msg == "" {
		return fallback
	}
	if len(msg) > 150 {
		return msg[:147] + "..."
	}
	return msg
}

func formatConfigDisplay(cfg *config.Config) string {
	var sb strings.Builder
	sb.WriteString("Shannon CLI Configuration\n")

	srcLabel := func(key string) string {
		if cfg.Sources == nil {
			return ""
		}
		s, ok := cfg.Sources[key]
		if !ok {
			return "(default)"
		}
		if s.File == "" {
			return fmt.Sprintf("(%s)", s.Level)
		}
		return fmt.Sprintf("(%s: %s)", s.Level, s.File)
	}

	apiKeyDisplay := "(not set)"
	if cfg.APIKey != "" {
		if len(cfg.APIKey) > 4 {
			apiKeyDisplay = "****" + cfg.APIKey[len(cfg.APIKey)-4:]
		} else {
			apiKeyDisplay = "****"
		}
	}

	sb.WriteString(fmt.Sprintf("  endpoint: %s %s\n", cfg.Endpoint, srcLabel("endpoint")))
	sb.WriteString(fmt.Sprintf("  api_key: %s %s\n", apiKeyDisplay, srcLabel("api_key")))
	sb.WriteString(fmt.Sprintf("  model_tier: %s %s\n", cfg.ModelTier, srcLabel("model_tier")))
	sb.WriteString(fmt.Sprintf("  auto_update_check: %v %s\n", cfg.AutoUpdateCheck, srcLabel("auto_update_check")))

	sb.WriteString("\nPermissions:\n")
	if len(cfg.Permissions.AllowedDirs) > 0 {
		sb.WriteString("  allowed_dirs:\n")
		for _, d := range cfg.Permissions.AllowedDirs {
			sb.WriteString(fmt.Sprintf("    - %s\n", d))
		}
	}
	if len(cfg.Permissions.AllowedCommands) > 0 {
		sb.WriteString("  allowed_commands:\n")
		for _, c := range cfg.Permissions.AllowedCommands {
			sb.WriteString(fmt.Sprintf("    - %s\n", c))
		}
	}
	if len(cfg.Permissions.DeniedCommands) > 0 {
		sb.WriteString("  denied_commands:\n")
		for _, c := range cfg.Permissions.DeniedCommands {
			sb.WriteString(fmt.Sprintf("    - %s\n", c))
		}
	}
	if len(cfg.Permissions.AllowedDirs) == 0 && len(cfg.Permissions.AllowedCommands) == 0 && len(cfg.Permissions.DeniedCommands) == 0 {
		sb.WriteString("  (none configured)\n")
	}

	sb.WriteString("\nAgent:\n")
	sb.WriteString(fmt.Sprintf("  max_iterations: %d %s\n", cfg.Agent.MaxIterations, srcLabel("agent.max_iterations")))
	sb.WriteString(fmt.Sprintf("  temperature: %g %s\n", cfg.Agent.Temperature, srcLabel("agent.temperature")))
	sb.WriteString(fmt.Sprintf("  max_tokens: %d %s\n", cfg.Agent.MaxTokens, srcLabel("agent.max_tokens")))

	sb.WriteString("\nTools:\n")
	sb.WriteString(fmt.Sprintf("  bash_timeout: %d %s\n", cfg.Tools.BashTimeout, srcLabel("tools.bash_timeout")))
	sb.WriteString(fmt.Sprintf("  bash_max_output: %d %s\n", cfg.Tools.BashMaxOutput, srcLabel("tools.bash_max_output")))
	sb.WriteString(fmt.Sprintf("  result_truncation: %d %s\n", cfg.Tools.ResultTruncation, srcLabel("tools.result_truncation")))
	sb.WriteString(fmt.Sprintf("  grep_max_results: %d %s\n", cfg.Tools.GrepMaxResults, srcLabel("tools.grep_max_results")))

	return sb.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
