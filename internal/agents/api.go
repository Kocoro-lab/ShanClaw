package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Kocoro-lab/shan/internal/skills"
	"gopkg.in/yaml.v3"
)

// AgentAPI is the JSON representation of an agent for the HTTP API.
type AgentAPI struct {
	Name     string            `json:"name"`
	Prompt   string            `json:"prompt"`
	Memory   *string           `json:"memory"`   // null if no MEMORY.md
	Config   *AgentConfigAPI   `json:"config"`    // null if no config.yaml
	Commands map[string]string `json:"commands"`  // null if no commands
	Skills   []skills.SkillMeta `json:"skills"`    // null if no skills
}

// AgentConfigAPI is the JSON representation of agent config.
type AgentConfigAPI struct {
	Tools      *AgentToolsFilter  `json:"tools,omitempty"`
	MCPServers *AgentMCPConfigAPI `json:"mcp_servers,omitempty"`
	Agent      *AgentModelConfig  `json:"agent,omitempty"`
}

// AgentMCPConfigAPI is the JSON-friendly MCP config.
type AgentMCPConfigAPI struct {
	Inherit bool                          `json:"inherit"`
	Servers map[string]AgentMCPServerRef  `json:"servers,omitempty"`
}

// ToAPI converts a loaded Agent to the API response shape.
func (a *Agent) ToAPI() *AgentAPI {
	api := &AgentAPI{
		Name:   a.Name,
		Prompt: a.Prompt,
	}
	if a.Memory != "" {
		mem := a.Memory
		api.Memory = &mem
	}
	if a.Config != nil {
		api.Config = &AgentConfigAPI{
			Tools: a.Config.Tools,
			Agent: a.Config.Agent,
		}
		if a.Config.MCPServers != nil {
			api.Config.MCPServers = &AgentMCPConfigAPI{
				Inherit: a.Config.MCPServers.Inherit,
				Servers: a.Config.MCPServers.Servers,
			}
		}
	}
	if len(a.Commands) > 0 {
		api.Commands = a.Commands
	}
	if len(a.Skills) > 0 {
		metas := make([]skills.SkillMeta, len(a.Skills))
		for i, s := range a.Skills {
			metas[i] = s.ToMeta()
		}
		api.Skills = metas
	}
	return api
}

// WriteAgentPrompt writes AGENT.md atomically.
func WriteAgentPrompt(agentsDir, name, prompt string) error {
	dir := filepath.Join(agentsDir, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(dir, "AGENT.md"), []byte(prompt))
}

// WriteAgentMemory writes MEMORY.md atomically.
func WriteAgentMemory(agentsDir, name, memory string) error {
	path := filepath.Join(agentsDir, name, "MEMORY.md")
	if memory == "" {
		return os.Remove(path)
	}
	return AtomicWrite(path, []byte(memory))
}

// WriteAgentConfig writes config.yaml from the API shape.
func WriteAgentConfig(agentsDir, name string, cfg *AgentConfigAPI) error {
	dir := filepath.Join(agentsDir, name)
	path := filepath.Join(dir, "config.yaml")
	if cfg == nil {
		return os.Remove(path)
	}
	m := make(map[string]interface{})
	if cfg.Tools != nil {
		m["tools"] = cfg.Tools
	}
	if cfg.Agent != nil {
		m["agent"] = cfg.Agent
	}
	if cfg.MCPServers != nil {
		servers := make(map[string]interface{})
		servers["_inherit"] = cfg.MCPServers.Inherit
		for k, v := range cfg.MCPServers.Servers {
			servers[k] = v
		}
		m["mcp_servers"] = servers
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return AtomicWrite(path, data)
}

// WriteAgentCommand writes a single command file.
func WriteAgentCommand(agentsDir, agentName, cmdName, content string) error {
	dir := filepath.Join(agentsDir, agentName, "commands")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(dir, cmdName+".md"), []byte(content))
}

// DeleteAgentCommand removes a single command file.
func DeleteAgentCommand(agentsDir, agentName, cmdName string) error {
	return os.Remove(filepath.Join(agentsDir, agentName, "commands", cmdName+".md"))
}

// skillFrontmatter is the YAML frontmatter structure for SKILL.md files.
// Must stay in sync with internal/skills/loader.go skillFrontmatter.
type skillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`
	AllowedTools  string            `yaml:"allowed-tools,omitempty"`
}

// WriteAgentSkill writes a skill as a SKILL.md file with YAML frontmatter.
// Uses proper YAML marshalling to handle special characters in values.
func WriteAgentSkill(agentsDir, agentName string, skill *skills.Skill) error {
	dir := filepath.Join(agentsDir, agentName, "skills", skill.Name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	fm := skillFrontmatter{
		Name:          skill.Name,
		Description:   skill.Description,
		License:       skill.License,
		Compatibility: skill.Compatibility,
		Metadata:      skill.Metadata,
	}
	if len(skill.AllowedTools) > 0 {
		fm.AllowedTools = strings.Join(skill.AllowedTools, " ")
	}

	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return fmt.Errorf("marshal frontmatter: %w", err)
	}

	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(fmBytes)
	buf.WriteString("---\n\n")
	buf.WriteString(skill.Prompt)
	if !strings.HasSuffix(skill.Prompt, "\n") {
		buf.WriteString("\n")
	}
	return AtomicWrite(filepath.Join(dir, "SKILL.md"), []byte(buf.String()))
}

// DeleteAgentSkill removes a single skill directory.
func DeleteAgentSkill(agentsDir, agentName, skillName string) error {
	return os.RemoveAll(filepath.Join(agentsDir, agentName, "skills", skillName))
}

// DeleteAgentDir removes the entire agent directory.
func DeleteAgentDir(agentsDir, name string) error {
	return os.RemoveAll(filepath.Join(agentsDir, name))
}

// AtomicWrite writes data to path via temp file + rename.
func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// AgentCreateRequest parses a POST /agents request body.
type AgentCreateRequest struct {
	Name     string            `json:"name"`
	Prompt   string            `json:"prompt"`
	Memory   *string           `json:"memory,omitempty"`
	Config   *AgentConfigAPI   `json:"config,omitempty"`
	Commands map[string]string `json:"commands,omitempty"`
	Skills   []*skills.Skill   `json:"skills,omitempty"`
}

// Validate checks required fields and runs all validators.
func (r *AgentCreateRequest) Validate() error {
	if err := ValidateAgentName(r.Name); err != nil {
		return err
	}
	if r.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	if r.Config != nil && r.Config.Tools != nil {
		if err := ValidateToolsFilter(r.Config.Tools); err != nil {
			return err
		}
	}
	for name := range r.Commands {
		if err := ValidateCommandName(name); err != nil {
			return err
		}
	}
	for _, s := range r.Skills {
		if s == nil {
			return fmt.Errorf("skill entry cannot be null")
		}
		if err := skills.ValidateSkillName(s.Name); err != nil {
			return err
		}
		if s.Description == "" {
			return fmt.Errorf("skill %q requires a description", s.Name)
		}
	}
	return nil
}

// AgentUpdateRequest is a partial update — only non-nil fields are applied.
type AgentUpdateRequest struct {
	Prompt   *string           `json:"prompt,omitempty"`
	Memory   json.RawMessage   `json:"memory,omitempty"`   // string or null
	Config   json.RawMessage   `json:"config,omitempty"`   // object or null
	Commands map[string]string `json:"commands,omitempty"`
	Skills   []*skills.Skill   `json:"skills,omitempty"`
}
