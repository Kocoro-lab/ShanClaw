package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPServerConfig describes how to connect to an MCP server.
type MCPServerConfig struct {
	Command   string            `yaml:"command"              mapstructure:"command"   json:"command"`
	Args      []string          `yaml:"args,omitempty"       mapstructure:"args"      json:"args,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"        mapstructure:"env"       json:"env,omitempty"`
	Type      string            `yaml:"type,omitempty"       mapstructure:"type"      json:"type,omitempty"`        // "stdio" (default) or "http"
	URL       string            `yaml:"url,omitempty"        mapstructure:"url"       json:"url,omitempty"`         // for http type
	Disabled  bool              `yaml:"disabled,omitempty"   mapstructure:"disabled"  json:"disabled,omitempty"`    // skip this server
	Context   string            `yaml:"context,omitempty"    mapstructure:"context"   json:"context,omitempty"`     // LLM context injected into system prompt
	KeepAlive bool              `yaml:"keep_alive,omitempty" mapstructure:"keep_alive" json:"keep_alive,omitempty"` // stay connected between turns (skip on-demand teardown)
}

// RemoteTool represents a tool discovered from an MCP server.
type RemoteTool struct {
	ServerName string
	Tool       mcp.Tool
}

// ClientManager manages connections to multiple MCP servers.
type ClientManager struct {
	mu          sync.Mutex
	clients     map[string]mcpclient.MCPClient // server name → client
	configs     map[string]MCPServerConfig     // server name → config (for reconnect)
	toolCache   map[string][]RemoteTool        // server name → last-known tools
	reconnectMu map[string]*sync.Mutex         // per-server reconnect serialization
	supervised  bool                           // when true, skip inline reconnect in CallTool
	idleTimers  map[string]*time.Timer         // per-server idle disconnect timers
	needsSetup  map[string]bool                // servers gated by missing readiness marker
}

// NewClientManager creates a new MCP client manager.
func NewClientManager() *ClientManager {
	return &ClientManager{
		clients:     make(map[string]mcpclient.MCPClient),
		configs:     make(map[string]MCPServerConfig),
		toolCache:   make(map[string][]RemoteTool),
		reconnectMu: make(map[string]*sync.Mutex),
		needsSetup:  make(map[string]bool),
	}
}

// ConnectAll connects to all configured MCP servers in parallel and returns discovered tools.
func (m *ClientManager) ConnectAll(ctx context.Context, servers map[string]MCPServerConfig) ([]RemoteTool, error) {
	type result struct {
		tools []RemoteTool
		err   error
		name  string
	}

	var wg sync.WaitGroup
	results := make(chan result, len(servers))

	for name, cfg := range servers {
		if cfg.Disabled {
			continue
		}
		wg.Add(1)
		go func(name string, cfg MCPServerConfig) {
			defer wg.Done()
			serverCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			tools, err := m.connect(serverCtx, name, cfg)
			results <- result{tools: tools, err: err, name: name}
		}(name, cfg)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var allTools []RemoteTool
	var errs []string
	for r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", r.name, r.err))
			continue
		}
		allTools = append(allTools, r.tools...)
	}

	if len(errs) > 0 {
		combined := fmt.Errorf("%s", strings.Join(errs, "; "))
		if len(allTools) == 0 {
			return nil, combined
		}
		return allTools, combined
	}

	return allTools, nil
}

// ConnectedServers returns the names of all servers that have an active client connection.
// IsConnected returns true if the named server has an active client connection.
func (m *ClientManager) IsConnected(serverName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.clients[serverName]
	return ok
}

func (m *ClientManager) ConnectedServers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}

func (m *ClientManager) connect(ctx context.Context, name string, cfg MCPServerConfig) ([]RemoteTool, error) {
	m.mu.Lock()
	m.configs[name] = cfg
	m.mu.Unlock()

	var c mcpclient.MCPClient
	var err error

	switch cfg.Type {
	case "http":
		if cfg.URL == "" {
			return nil, fmt.Errorf("http MCP server requires url")
		}
		c, err = mcpclient.NewStreamableHttpClient(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP client: %w", err)
		}
		if starter, ok := c.(interface{ Start(context.Context) error }); ok {
			if err := starter.Start(ctx); err != nil {
				return nil, fmt.Errorf("failed to start HTTP client: %w", err)
			}
		}
	default: // stdio
		if cfg.Command == "" {
			return nil, fmt.Errorf("stdio MCP server requires command")
		}
		envSlice := buildEnvSlice(cfg.Env)
		c, err = mcpclient.NewStdioMCPClient(cfg.Command, envSlice, cfg.Args...)
		if err != nil {
			return nil, fmt.Errorf("failed to start MCP server %q: %w", cfg.Command, err)
		}
	}

	// Initialize handshake
	_, err = c.Initialize(ctx, mcp.InitializeRequest{
		Params: struct {
			ProtocolVersion string                 `json:"protocolVersion"`
			Capabilities    mcp.ClientCapabilities `json:"capabilities"`
			ClientInfo      mcp.Implementation     `json:"clientInfo"`
		}{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "shannon-cli", Version: "1.0.0"},
		},
	})
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize failed: %w", err)
	}

	// List available tools
	toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("tools/list failed: %w", err)
	}

	m.mu.Lock()
	m.clients[name] = c
	m.mu.Unlock()

	var tools []RemoteTool
	for _, t := range toolsResult.Tools {
		tools = append(tools, RemoteTool{
			ServerName: name,
			Tool:       t,
		})
	}

	m.mu.Lock()
	m.toolCache[name] = tools
	m.mu.Unlock()

	return tools, nil
}

// CallTool invokes a tool on the specified MCP server.
// If the call fails with a connection error, it attempts to reconnect once and retry.
func (m *ClientManager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (string, bool, error) {
	m.mu.Lock()
	c, ok := m.clients[serverName]
	cfg, hasCfg := m.configs[serverName]
	m.mu.Unlock()

	// Lazy-start: server was discovered at boot but disconnected (keepAlive=false).
	// Reconnect on first tool invocation, serialized per-server to avoid duplicate processes.
	if !ok && hasCfg {
		m.mu.Lock()
		rmu, rmOK := m.reconnectMu[serverName]
		if !rmOK {
			rmu = &sync.Mutex{}
			m.reconnectMu[serverName] = rmu
		}
		m.mu.Unlock()

		rmu.Lock()
		// Double-check: another goroutine may have connected while we waited.
		m.mu.Lock()
		c, ok = m.clients[serverName]
		m.mu.Unlock()
		if !ok {
			log.Printf("[mcp] %s: not connected, attempting on-demand connect", serverName)
			reconnectCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if _, err := m.connect(reconnectCtx, serverName, cfg); err != nil {
				cancel()
				rmu.Unlock()
				return "", true, fmt.Errorf("MCP server %q on-demand connect failed: %w", serverName, err)
			}
			cancel()
			m.mu.Lock()
			c = m.clients[serverName]
			m.mu.Unlock()
		}
		rmu.Unlock()
	} else if !ok {
		return "", true, fmt.Errorf("MCP server %q not connected", serverName)
	}

	result, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: args,
		},
	})
	if err != nil && isTransportError(err) {
		m.mu.Lock()
		skip := m.supervised
		m.mu.Unlock()
		if skip {
			return "", true, fmt.Errorf("tools/call failed (supervised, no inline reconnect): %w", err)
		}
		// Transport failure (process died, broken pipe, EOF).
		// Attempt a one-shot reconnect using a fresh background context so a
		// cancelled request context doesn't prevent recovery.
		origErr := err
		m.mu.Lock()
		cfg, hasCfg := m.configs[serverName]
		stale := m.clients[serverName]
		m.mu.Unlock()

		if hasCfg {
			// Close the stale client to release its resources before reconnecting.
			if stale != nil {
				_ = stale.Close()
			}
			reconnectCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, reconnErr := m.connect(reconnectCtx, serverName, cfg); reconnErr == nil {
				m.mu.Lock()
				c = m.clients[serverName]
				m.mu.Unlock()
				result, err = c.CallTool(ctx, mcp.CallToolRequest{
					Params: mcp.CallToolParams{
						Name:      toolName,
						Arguments: args,
					},
				})
			}
		}
		if err != nil {
			// Preserve the original transport error for diagnostics.
			return "", true, fmt.Errorf("tools/call failed: %w (reconnect attempted after: %v)", origErr, err)
		}
	} else if err != nil {
		return "", true, fmt.Errorf("tools/call failed: %w", err)
	}

	// Extract text content from result
	var texts []string
	for _, block := range result.Content {
		if textContent, ok := block.(mcp.TextContent); ok {
			texts = append(texts, textContent.Text)
		} else {
			// For non-text content, marshal to JSON
			b, _ := json.Marshal(block)
			texts = append(texts, string(b))
		}
	}

	content := ""
	if len(texts) > 0 {
		content = texts[0]
		for _, t := range texts[1:] {
			content += "\n" + t
		}
	}

	return content, result.IsError, nil
}

// Close shuts down all connected MCP servers in parallel.
func (m *ClientManager) Close() {
	m.mu.Lock()
	clients := make(map[string]mcpclient.MCPClient, len(m.clients))
	for name, c := range m.clients {
		clients[name] = c
		delete(m.clients, name)
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c mcpclient.MCPClient) {
			defer wg.Done()
			_ = c.Close()
		}(c)
	}
	wg.Wait()
}

// ConfigFor returns the config for a server, if any.
func (m *ClientManager) ConfigFor(serverName string) (MCPServerConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.configs[serverName]
	return cfg, ok
}

// Disconnect closes a single server's client, removing it from the active map.
// The config and tool cache are preserved so the server can reconnect later.
func (m *ClientManager) Disconnect(serverName string) {
	m.mu.Lock()
	// Cancel any pending idle timer for this server.
	if t, ok := m.idleTimers[serverName]; ok {
		t.Stop()
		delete(m.idleTimers, serverName)
	}
	c, ok := m.clients[serverName]
	if ok {
		delete(m.clients, serverName)
	}
	m.mu.Unlock()
	if ok && c != nil {
		_ = c.Close()
	}
}

// DisconnectAfterIdle schedules a Disconnect after the given idle duration.
// If called again before the timer fires, the timer resets. This allows
// multi-turn browser workflows to keep the connection alive while
// disconnecting after a period of inactivity.
func (m *ClientManager) DisconnectAfterIdle(serverName string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idleTimers == nil {
		m.idleTimers = make(map[string]*time.Timer)
	}
	if t, ok := m.idleTimers[serverName]; ok {
		t.Stop()
	}
	m.idleTimers[serverName] = time.AfterFunc(d, func() {
		log.Printf("[mcp] %s: idle timeout reached, disconnecting", serverName)
		m.Disconnect(serverName)
	})
}

// CancelIdleDisconnect cancels a pending idle disconnect timer, if any.
func (m *ClientManager) CancelIdleDisconnect(serverName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.idleTimers[serverName]; ok {
		t.Stop()
		delete(m.idleTimers, serverName)
	}
}

func (m *ClientManager) SetSupervised(v bool) {
	m.mu.Lock()
	m.supervised = v
	m.mu.Unlock()
}

// SetNeedsSetup marks a server as needing setup (e.g. readiness marker absent).
func (m *ClientManager) SetNeedsSetup(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.needsSetup[name] = true
}

// NeedsSetup reports whether a server is gated by a missing readiness marker.
func (m *ClientManager) NeedsSetup(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.needsSetup[name]
}

func (m *ClientManager) CachedTools(serverName string) []RemoteTool {
	m.mu.Lock()
	defer m.mu.Unlock()
	tools := m.toolCache[serverName]
	if tools == nil {
		return nil
	}
	cp := make([]RemoteTool, len(tools))
	copy(cp, tools)
	return cp
}

// SeedToolCache sets cached tools for a server. Test helper only.
func (m *ClientManager) SeedToolCache(serverName string, tools []RemoteTool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolCache[serverName] = tools
}

// SeedClient injects a client for a server. Test helper only.
func (m *ClientManager) SeedClient(serverName string, c mcpclient.MCPClient) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[serverName] = c
}

// SeedConfig sets the config for a server. Test helper only.
func (m *ClientManager) SeedConfig(serverName string, cfg MCPServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[serverName] = cfg
}

func (m *ClientManager) ProbeTransport(ctx context.Context, serverName string) error {
	m.mu.Lock()
	c, ok := m.clients[serverName]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("MCP server %q not connected", serverName)
	}
	_, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("transport probe failed: %w", err)
	}
	return nil
}

func (m *ClientManager) Reconnect(ctx context.Context, serverName string) ([]RemoteTool, error) {
	m.mu.Lock()
	cfg, hasCfg := m.configs[serverName]
	if !hasCfg {
		m.mu.Unlock()
		return nil, fmt.Errorf("no config for MCP server %q", serverName)
	}
	rmu, ok := m.reconnectMu[serverName]
	if !ok {
		rmu = &sync.Mutex{}
		m.reconnectMu[serverName] = rmu
	}
	stale := m.clients[serverName]
	m.mu.Unlock()

	rmu.Lock()
	defer rmu.Unlock()

	if stale != nil {
		_ = stale.Close()
	}
	m.mu.Lock()
	delete(m.clients, serverName)
	m.mu.Unlock()

	return m.connect(ctx, serverName, cfg)
}

// isTransportError reports whether err indicates a transport/connection failure
// (process exited, broken pipe, EOF) rather than a tool-logic or protocol error.
// Only transport errors should trigger a reconnect attempt — retrying on logic
// errors risks duplicating non-idempotent side effects.
func isTransportError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "read/write on closed pipe") ||
		strings.Contains(s, "signal: killed") ||
		strings.Contains(s, "process already finished")
}

func buildEnvSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

// BuildContext collects context strings from all configured MCP servers.
func BuildContext(servers map[string]MCPServerConfig) string {
	var parts []string
	for name, cfg := range servers {
		if cfg.Disabled || cfg.Context == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s] %s", name, cfg.Context))
	}
	if len(parts) == 0 {
		return ""
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result
}

// IsPlaywrightCDPMode reports whether the args include --cdp-endpoint.
func IsPlaywrightCDPMode(cfg MCPServerConfig) bool {
	for _, arg := range cfg.Args {
		if arg == "--cdp-endpoint" {
			return true
		}
	}
	return false
}

// NormalizePlaywrightCDPConfig migrates legacy localhost:9222 configs to the
// dedicated daemon-owned CDP port while preserving explicit custom endpoints.
func NormalizePlaywrightCDPConfig(cfg MCPServerConfig) MCPServerConfig {
	if !IsPlaywrightCDPMode(cfg) {
		return cfg
	}
	args := append([]string(nil), cfg.Args...)
	for i := 0; i < len(args)-1; i++ {
		if args[i] != "--cdp-endpoint" {
			continue
		}
		args[i+1] = normalizePlaywrightCDPEndpoint(args[i+1])
		break
	}
	cfg.Args = args
	return cfg
}

// PlaywrightCDPPort extracts the configured CDP port, defaulting to the
// daemon-owned dedicated port when absent or invalid.
func PlaywrightCDPPort(cfg MCPServerConfig) int {
	if !IsPlaywrightCDPMode(cfg) {
		return DefaultCDPPort
	}
	// Stop at len-1 so a malformed trailing "--cdp-endpoint" without a value
	// safely falls back to the dedicated default instead of misparsing args.
	for i := 0; i < len(cfg.Args)-1; i++ {
		if cfg.Args[i] != "--cdp-endpoint" {
			continue
		}
		u, err := url.Parse(cfg.Args[i+1])
		if err != nil {
			return DefaultCDPPort
		}
		if port := u.Port(); port != "" {
			if n, err := strconv.Atoi(port); err == nil && n > 0 {
				return n
			}
		}
		return DefaultCDPPort
	}
	return DefaultCDPPort
}

func normalizePlaywrightCDPEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := u.Hostname()
	port := u.Port()
	if port != strconv.Itoa(LegacyCDPPort) {
		return raw
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return raw
	}
	u.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(DefaultCDPPort))
	return u.String()
}
