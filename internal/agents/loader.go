package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

var agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var mentionRe = regexp.MustCompile(`^@([a-zA-Z0-9][a-zA-Z0-9_-]*)(?:\s|$)`)

// AgentToolsFilter controls which local tools an agent can access.
// If Allow is non-empty, only those tools are available.
// If Deny is non-empty, all tools except those are available.
// If both are empty, all tools are available (backwards-compatible).
type AgentToolsFilter struct {
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
}

// AgentMCPConfig holds MCP server configs with an optional inherit flag.
// When Inherit is false (default), only the servers listed here are used.
// When Inherit is true, these servers are merged on top of the global set.
// This struct is populated programmatically by parseAgentConfig, not by
// direct YAML unmarshaling.
type AgentMCPConfig struct {
	Inherit bool
	Servers map[string]AgentMCPServerRef
}

// AgentMCPServerRef mirrors the fields needed for per-agent MCP server config.
// We keep it simple — the full MCPServerConfig is resolved at merge time.
type AgentMCPServerRef struct {
	Command  string            `yaml:"command"`
	Args     []string          `yaml:"args,omitempty"`
	Env      map[string]string `yaml:"env,omitempty"`
	Type     string            `yaml:"type,omitempty"`
	URL      string            `yaml:"url,omitempty"`
	Disabled bool              `yaml:"disabled,omitempty"`
	Context  string            `yaml:"context,omitempty"`
}

// AgentConfig is the per-agent config overlay loaded from config.yaml.
type AgentConfig struct {
	MCPServers *AgentMCPConfig  `yaml:"-"`            // parsed manually for _inherit
	Tools      *AgentToolsFilter `yaml:"tools"`
	Agent      *AgentModelConfig `yaml:"agent"`
}

// AgentModelConfig holds per-agent model/iteration overrides.
type AgentModelConfig struct {
	Model          *string  `yaml:"model"`
	MaxIterations  *int     `yaml:"max_iterations"`
	Temperature    *float64 `yaml:"temperature"`
	MaxTokens      *int     `yaml:"max_tokens"`
	ContextWindow  *int     `yaml:"context_window"`
}

// Agent represents a loaded agent definition.
type Agent struct {
	Name     string
	Prompt   string
	Memory   string
	Config   *AgentConfig      // nil = inherit everything (backwards-compatible)
	Commands map[string]string // agent-scoped slash commands (name → content)
}

func ValidateAgentName(name string) error {
	if !agentNameRe.MatchString(name) {
		return fmt.Errorf("invalid agent name %q: must match %s", name, agentNameRe.String())
	}
	return nil
}

func LoadAgent(agentsDir, name string) (*Agent, error) {
	if err := ValidateAgentName(name); err != nil {
		return nil, err
	}
	dir := filepath.Join(agentsDir, name)
	promptData, err := os.ReadFile(filepath.Join(dir, "AGENT.md"))
	if err != nil {
		return nil, fmt.Errorf("agent %q: missing AGENT.md: %w", name, err)
	}
	var memory string
	if data, err := os.ReadFile(filepath.Join(dir, "MEMORY.md")); err == nil {
		memory = string(data)
	}

	ag := &Agent{Name: name, Prompt: string(promptData), Memory: memory}

	// Load per-agent config overlay (optional)
	if cfgData, err := os.ReadFile(filepath.Join(dir, "config.yaml")); err == nil {
		agCfg, err := parseAgentConfig(cfgData)
		if err != nil {
			return nil, fmt.Errorf("agent %q: bad config.yaml: %w", name, err)
		}
		ag.Config = agCfg
	}

	// Load agent-scoped commands (optional)
	ag.Commands = loadAgentCommands(filepath.Join(dir, "commands"))

	return ag, nil
}

// parseAgentConfig parses the per-agent config.yaml, handling the special
// _inherit key inside mcp_servers.
func parseAgentConfig(data []byte) (*AgentConfig, error) {
	var cfg AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Parse mcp_servers manually to handle _inherit key
	var raw struct {
		MCPServers map[string]yaml.Node `yaml:"mcp_servers"`
	}
	if err := yaml.Unmarshal(data, &raw); err == nil && len(raw.MCPServers) > 0 {
		mcpCfg := &AgentMCPConfig{
			Servers: make(map[string]AgentMCPServerRef),
		}
		for key, node := range raw.MCPServers {
			if key == "_inherit" {
				var inherit bool
				if err := node.Decode(&inherit); err == nil {
					mcpCfg.Inherit = inherit
				}
				continue
			}
			var srv AgentMCPServerRef
			if err := node.Decode(&srv); err == nil {
				mcpCfg.Servers[key] = srv
			}
		}
		cfg.MCPServers = mcpCfg
	}

	return &cfg, nil
}

const maxAgentCommandChars = 8000

// loadAgentCommands loads .md files from the agent's commands/ directory.
func loadAgentCommands(dir string) map[string]string {
	entries, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil || len(entries) == 0 {
		return nil
	}
	sort.Strings(entries)
	commands := make(map[string]string)
	for _, path := range entries {
		name := strings.TrimSuffix(filepath.Base(path), ".md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		if utf8.RuneCountInString(content) > maxAgentCommandChars {
			content = string([]rune(content)[:maxAgentCommandChars])
		}
		commands[name] = content
	}
	return commands
}

func ListAgents(agentsDir string) ([]string, error) {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := ValidateAgentName(e.Name()); err != nil {
			continue
		}
		agentMD := filepath.Join(agentsDir, e.Name(), "AGENT.md")
		if _, err := os.Stat(agentMD); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func ParseAgentMention(msg string) (string, string) {
	m := mentionRe.FindStringSubmatch(msg)
	if m == nil {
		return "", msg
	}
	name := strings.ToLower(m[1])
	if err := ValidateAgentName(name); err != nil {
		return "", msg
	}
	rest := strings.TrimSpace(msg[len(m[0]):])
	return name, rest
}
