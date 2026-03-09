package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLoadAgent_WithConfig(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "review-agent")
	os.MkdirAll(agentDir, 0700)
	os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte("You are review-agent."), 0600)

	configYAML := `
tools:
  allow:
    - file_read
    - grep
    - bash

mcp_servers:
  _inherit: false
  github:
    command: mcp-server-github
    env:
      GITHUB_TOKEN: "test-token"

agent:
  model: "claude-sonnet-4-6"
  max_iterations: 10
`
	os.WriteFile(filepath.Join(agentDir, "config.yaml"), []byte(configYAML), 0600)

	a, err := LoadAgent(dir, "review-agent")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}

	if a.Config == nil {
		t.Fatal("config is nil")
	}

	// Tools filter
	if a.Config.Tools == nil {
		t.Fatal("tools filter is nil")
	}
	if len(a.Config.Tools.Allow) != 3 {
		t.Errorf("allow list = %v, want 3 items", a.Config.Tools.Allow)
	}

	// MCP servers
	if a.Config.MCPServers == nil {
		t.Fatal("MCP servers is nil")
	}
	if a.Config.MCPServers.Inherit {
		t.Error("expected inherit=false")
	}
	if _, ok := a.Config.MCPServers.Servers["github"]; !ok {
		t.Error("github server not found")
	}

	// Agent model config
	if a.Config.Agent == nil {
		t.Fatal("agent model config is nil")
	}
	if *a.Config.Agent.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q", *a.Config.Agent.Model)
	}
	if *a.Config.Agent.MaxIterations != 10 {
		t.Errorf("max_iterations = %d", *a.Config.Agent.MaxIterations)
	}
}

func TestLoadAgent_WithInheritMCP(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "test-agent")
	os.MkdirAll(agentDir, 0700)
	os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte("test"), 0600)

	configYAML := `
mcp_servers:
  _inherit: true
  custom:
    command: my-server
`
	os.WriteFile(filepath.Join(agentDir, "config.yaml"), []byte(configYAML), 0600)

	a, err := LoadAgent(dir, "test-agent")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if !a.Config.MCPServers.Inherit {
		t.Error("expected inherit=true")
	}
	if len(a.Config.MCPServers.Servers) != 1 {
		t.Errorf("servers = %d, want 1", len(a.Config.MCPServers.Servers))
	}
}

func TestLoadAgent_NoConfig(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "simple")
	os.MkdirAll(agentDir, 0700)
	os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte("simple agent"), 0600)

	a, err := LoadAgent(dir, "simple")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if a.Config != nil {
		t.Error("expected nil config for agent without config.yaml")
	}
}

func TestLoadAgent_WithCommands(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "cmd-agent")
	os.MkdirAll(filepath.Join(agentDir, "commands"), 0700)
	os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte("agent"), 0600)
	os.WriteFile(filepath.Join(agentDir, "commands", "review.md"), []byte("Review this code"), 0600)
	os.WriteFile(filepath.Join(agentDir, "commands", "deploy.md"), []byte("Deploy to prod"), 0600)

	a, err := LoadAgent(dir, "cmd-agent")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if len(a.Commands) != 2 {
		t.Fatalf("commands = %d, want 2", len(a.Commands))
	}
	if a.Commands["review"] != "Review this code" {
		t.Errorf("review command = %q", a.Commands["review"])
	}
}

func TestLoadAgent_BadConfig(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "bad-cfg")
	os.MkdirAll(agentDir, 0700)
	os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte("agent"), 0600)
	os.WriteFile(filepath.Join(agentDir, "config.yaml"), []byte("{{invalid yaml"), 0600)

	_, err := LoadAgent(dir, "bad-cfg")
	if err == nil {
		t.Error("expected error for bad config.yaml")
	}
}

func TestLoadAgent_CommandUTF8Truncation(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "utf8-agent")
	os.MkdirAll(filepath.Join(agentDir, "commands"), 0700)
	os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte("agent"), 0600)

	// Each あ is 3 bytes. Create content longer than maxAgentCommandChars runes.
	content := strings.Repeat("あ", 8010)
	os.WriteFile(filepath.Join(agentDir, "commands", "test.md"), []byte(content), 0600)

	a, err := LoadAgent(dir, "utf8-agent")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}

	cmd := a.Commands["test"]
	runes := []rune(cmd)
	if len(runes) != 8000 {
		t.Errorf("expected 8000 runes, got %d", len(runes))
	}
	if !utf8.ValidString(cmd) {
		t.Error("truncated command is not valid UTF-8")
	}
}

func TestLoadAgent_ToolsDenyList(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "deny-agent")
	os.MkdirAll(agentDir, 0700)
	os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte("agent"), 0600)

	configYAML := `
tools:
  deny:
    - computer
    - browser
    - screenshot
`
	os.WriteFile(filepath.Join(agentDir, "config.yaml"), []byte(configYAML), 0600)

	a, err := LoadAgent(dir, "deny-agent")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if len(a.Config.Tools.Deny) != 3 {
		t.Errorf("deny list = %v, want 3 items", a.Config.Tools.Deny)
	}
}
