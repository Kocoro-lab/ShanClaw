package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPServerConfig describes how to connect to an MCP server.
type MCPServerConfig struct {
	Command  string            `yaml:"command"            mapstructure:"command"  json:"command"`
	Args     []string          `yaml:"args,omitempty"     mapstructure:"args"     json:"args,omitempty"`
	Env      map[string]string `yaml:"env,omitempty"      mapstructure:"env"      json:"env,omitempty"`
	Type     string            `yaml:"type,omitempty"     mapstructure:"type"     json:"type,omitempty"`     // "stdio" (default) or "http"
	URL      string            `yaml:"url,omitempty"      mapstructure:"url"      json:"url,omitempty"`      // for http type
	Disabled bool              `yaml:"disabled,omitempty" mapstructure:"disabled" json:"disabled,omitempty"` // skip this server
	Context  string            `yaml:"context,omitempty"  mapstructure:"context"  json:"context,omitempty"`  // LLM context injected into system prompt
}

// RemoteTool represents a tool discovered from an MCP server.
type RemoteTool struct {
	ServerName string
	Tool       mcp.Tool
}

// ClientManager manages connections to multiple MCP servers.
type ClientManager struct {
	mu      sync.Mutex
	clients map[string]mcpclient.MCPClient // server name → client
	configs map[string]MCPServerConfig     // server name → config (for reconnect)
}

// NewClientManager creates a new MCP client manager.
func NewClientManager() *ClientManager {
	return &ClientManager{
		clients: make(map[string]mcpclient.MCPClient),
		configs: make(map[string]MCPServerConfig),
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
			serverCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
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

func (m *ClientManager) connect(ctx context.Context, name string, cfg MCPServerConfig) ([]RemoteTool, error) {
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
	m.configs[name] = cfg
	m.mu.Unlock()

	var tools []RemoteTool
	for _, t := range toolsResult.Tools {
		tools = append(tools, RemoteTool{
			ServerName: name,
			Tool:       t,
		})
	}

	return tools, nil
}

// CallTool invokes a tool on the specified MCP server.
// If the call fails with a connection error, it attempts to reconnect once and retry.
func (m *ClientManager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (string, bool, error) {
	m.mu.Lock()
	c, ok := m.clients[serverName]
	m.mu.Unlock()

	if !ok {
		return "", true, fmt.Errorf("MCP server %q not connected", serverName)
	}

	result, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: args,
		},
	})
	if err != nil && isTransportError(err) {
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
