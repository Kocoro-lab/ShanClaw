package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Kocoro-lab/shan/internal/hooks"
	"github.com/Kocoro-lab/shan/internal/mcp"
	"github.com/Kocoro-lab/shan/internal/permissions"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

//go:embed mcp_defaults.yaml
var mcpDefaultsYAML []byte

// ConfigSource tracks which file a config value came from.
type ConfigSource struct {
	File  string
	Level string // "default", "global", "project", "local"
}

type Config struct {
	Endpoint        string                       `mapstructure:"endpoint" yaml:"endpoint"`
	APIKey          string                       `mapstructure:"api_key" yaml:"api_key"`
	ModelTier       string                       `mapstructure:"model_tier" yaml:"model_tier"`
	AutoUpdateCheck bool                         `mapstructure:"auto_update_check" yaml:"auto_update_check"`
	Permissions     permissions.PermissionsConfig `mapstructure:"permissions" yaml:"permissions"`
	Agent           AgentConfig                  `mapstructure:"agent" yaml:"agent"`
	Tools           ToolsConfig                  `mapstructure:"tools" yaml:"tools"`
	Hooks           hooks.HookConfig                `mapstructure:"hooks" yaml:"hooks"`
	MCPServers      map[string]mcp.MCPServerConfig  `mapstructure:"mcp_servers" yaml:"mcp_servers"`
	Sources         map[string]ConfigSource         `mapstructure:"-" yaml:"-"`
}

type AgentConfig struct {
	MaxIterations   int     `mapstructure:"max_iterations" yaml:"max_iterations"`
	Temperature     float64 `mapstructure:"temperature" yaml:"temperature"`
	MaxTokens       int     `mapstructure:"max_tokens" yaml:"max_tokens"`
	Thinking        bool    `mapstructure:"thinking" yaml:"thinking"`
	ThinkingMode    string  `mapstructure:"thinking_mode" yaml:"thinking_mode"` // "adaptive" (default) or "enabled" (fixed budget)
	ThinkingBudget  int     `mapstructure:"thinking_budget" yaml:"thinking_budget"`
	ReasoningEffort string  `mapstructure:"reasoning_effort" yaml:"reasoning_effort"`
	Model           string  `mapstructure:"model" yaml:"model"`          // specific model override
	ContextWindow   int     `mapstructure:"context_window" yaml:"context_window"` // model context window in tokens
}

type ToolsConfig struct {
	BashTimeout       int `mapstructure:"bash_timeout" yaml:"bash_timeout"`
	BashMaxOutput     int `mapstructure:"bash_max_output" yaml:"bash_max_output"`
	ResultTruncation  int `mapstructure:"result_truncation" yaml:"result_truncation"`
	ArgsTruncation    int `mapstructure:"args_truncation" yaml:"args_truncation"`
	ServerToolTimeout int `mapstructure:"server_tool_timeout" yaml:"server_tool_timeout"`
	GrepMaxResults    int `mapstructure:"grep_max_results" yaml:"grep_max_results"`
}

func ShannonDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".shannon")
}

func Load() (*Config, error) {
	dir := ShannonDir()
	if dir == "" {
		return nil, fmt.Errorf("failed to resolve home directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create config directory %s: %w", dir, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0700); err != nil {
		return nil, fmt.Errorf("failed to create sessions directory %s: %w", filepath.Join(dir, "sessions"), err)
	}
	if err := InitSettingsFile(dir); err != nil {
		return nil, fmt.Errorf("failed to init settings: %w", err)
	}

	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(dir)

	viper.SetDefault("endpoint", "https://api-dev.shannon.run")
	viper.SetDefault("api_key", "")
	viper.SetDefault("model_tier", "small")
	viper.SetDefault("auto_update_check", true)
	viper.SetDefault("agent.max_iterations", 25)
	viper.SetDefault("agent.temperature", 0)
	viper.SetDefault("agent.max_tokens", 32000)
	viper.SetDefault("agent.thinking", true)
	viper.SetDefault("agent.thinking_mode", "adaptive")
	viper.SetDefault("agent.thinking_budget", 10000)
	viper.SetDefault("agent.reasoning_effort", "")
	viper.SetDefault("agent.model", "")
	viper.SetDefault("agent.context_window", 128000)
	viper.SetDefault("tools.bash_timeout", 120)
	viper.SetDefault("tools.bash_max_output", 30000)
	viper.SetDefault("tools.result_truncation", 2000)
	viper.SetDefault("tools.args_truncation", 200)
	viper.SetDefault("tools.server_tool_timeout", 5)
	viper.SetDefault("tools.grep_max_results", 100)

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			configPath := filepath.Join(dir, "config.yaml")
			if err := viper.SafeWriteConfigAs(configPath); err != nil {
				return nil, fmt.Errorf("failed to write config: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to read config: %w", err)
		}
	}

	// Migrate old config keys
	migrateOldConfig()

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Re-read MCP servers directly from YAML to preserve env var key casing.
	// Viper lowercases all map keys which breaks env vars like API_KEY → api_key.
	globalFile := filepath.Join(dir, "config.yaml")
	fixMCPEnvKeyCasing(&cfg, globalFile)
	cfg.Sources = buildDefaultSources()
	markGlobalSources(&cfg, globalFile)

	// Merge default MCP servers (user entries take precedence)
	mergeDefaultMCPServers(&cfg)

	// Write MCP defaults into config file or reference file
	initMCPDefaults(dir, globalFile)

	// Merge project-level configs (.shannon/config.yaml and .shannon/config.local.yaml)
	cwd, _ := os.Getwd()
	if cwd != "" {
		projectFile := filepath.Join(cwd, ".shannon", "config.yaml")
		mergeOverlayFile(&cfg, projectFile, "project")

		localFile := filepath.Join(cwd, ".shannon", "config.local.yaml")
		mergeOverlayFile(&cfg, localFile, "local")
	}

	// Validate thinking_mode (only when thinking is enabled)
	if cfg.Agent.Thinking {
		switch cfg.Agent.ThinkingMode {
		case "adaptive", "enabled":
			// valid
		default:
			return nil, fmt.Errorf("invalid agent.thinking_mode %q: must be \"adaptive\" or \"enabled\"", cfg.Agent.ThinkingMode)
		}
	}

	return &cfg, nil
}

// migrateOldConfig handles the transition from llm_url+gateway_url to single endpoint.
// Bypasses viper for writing since viper can't delete keys.
func migrateOldConfig() {
	if !viper.IsSet("llm_url") && !viper.IsSet("gateway_url") {
		return
	}

	// Migrate gateway_url → endpoint if endpoint wasn't explicitly set
	if gw := viper.GetString("gateway_url"); gw != "" && !viper.IsSet("endpoint") {
		viper.Set("endpoint", gw)
	}

	// Write clean config directly, bypassing viper (which would keep old keys)
	clean := map[string]any{
		"endpoint":          viper.GetString("endpoint"),
		"api_key":           viper.GetString("api_key"),
		"model_tier":        viper.GetString("model_tier"),
		"auto_update_check": viper.GetBool("auto_update_check"),
	}

	dir := ShannonDir()
	if dir == "" {
		return
	}
	data, err := yaml.Marshal(clean)
	if err != nil {
		return
	}
	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, data, 0600)

	// Re-read so viper state matches the cleaned file
	viper.ReadInConfig()
}

func Save(cfg *Config) error {
	viper.Set("endpoint", cfg.Endpoint)
	viper.Set("api_key", cfg.APIKey)
	viper.Set("model_tier", cfg.ModelTier)
	viper.Set("auto_update_check", cfg.AutoUpdateCheck)
	return viper.WriteConfig()
}

// overlayConfig is a partial config used for YAML overlay merging.
// Pointer fields distinguish "not set" (nil) from "set to zero value".
type overlayConfig struct {
	Endpoint        *string                        `yaml:"endpoint"`
	APIKey          *string                        `yaml:"api_key"`
	ModelTier       *string                        `yaml:"model_tier"`
	AutoUpdateCheck *bool                          `yaml:"auto_update_check"`
	Permissions     *permissions.PermissionsConfig  `yaml:"permissions"`
	Agent           *overlayAgentConfig            `yaml:"agent"`
	Tools           *overlayToolsConfig            `yaml:"tools"`
	MCPServers      map[string]mcp.MCPServerConfig `yaml:"mcp_servers"`
}

type overlayAgentConfig struct {
	MaxIterations   *int     `yaml:"max_iterations"`
	Temperature     *float64 `yaml:"temperature"`
	MaxTokens       *int     `yaml:"max_tokens"`
	Thinking        *bool    `yaml:"thinking"`
	ThinkingMode    *string  `yaml:"thinking_mode"`
	ThinkingBudget  *int     `yaml:"thinking_budget"`
	ReasoningEffort *string  `yaml:"reasoning_effort"`
	Model           *string  `yaml:"model"`
	ContextWindow   *int     `yaml:"context_window"`
}

type overlayToolsConfig struct {
	BashTimeout       *int `yaml:"bash_timeout"`
	BashMaxOutput     *int `yaml:"bash_max_output"`
	ResultTruncation  *int `yaml:"result_truncation"`
	ArgsTruncation    *int `yaml:"args_truncation"`
	ServerToolTimeout *int `yaml:"server_tool_timeout"`
	GrepMaxResults    *int `yaml:"grep_max_results"`
}

// buildDefaultSources returns source entries for all config keys set to "default".
func buildDefaultSources() map[string]ConfigSource {
	return map[string]ConfigSource{
		"endpoint":                 {Level: "default"},
		"api_key":                  {Level: "default"},
		"model_tier":               {Level: "default"},
		"auto_update_check":        {Level: "default"},
		"agent.max_iterations":     {Level: "default"},
		"agent.temperature":        {Level: "default"},
		"agent.max_tokens":         {Level: "default"},
		"agent.thinking":           {Level: "default"},
		"agent.thinking_mode":      {Level: "default"},
		"agent.thinking_budget":    {Level: "default"},
		"agent.context_window":     {Level: "default"},
		"tools.bash_timeout":       {Level: "default"},
		"tools.bash_max_output":    {Level: "default"},
		"tools.result_truncation":  {Level: "default"},
		"tools.args_truncation":    {Level: "default"},
		"tools.server_tool_timeout": {Level: "default"},
		"tools.grep_max_results":   {Level: "default"},
	}
}

// markGlobalSources marks keys that viper resolved from the global config file.
func markGlobalSources(cfg *Config, file string) {
	src := ConfigSource{File: file, Level: "global"}
	// Mark scalar fields that viper loaded (non-default values)
	if viper.IsSet("endpoint") {
		cfg.Sources["endpoint"] = src
	}
	if viper.IsSet("api_key") {
		cfg.Sources["api_key"] = src
	}
	if viper.IsSet("model_tier") {
		cfg.Sources["model_tier"] = src
	}
	if viper.IsSet("auto_update_check") {
		cfg.Sources["auto_update_check"] = src
	}
	if viper.IsSet("agent.max_iterations") {
		cfg.Sources["agent.max_iterations"] = src
	}
	if viper.IsSet("agent.temperature") {
		cfg.Sources["agent.temperature"] = src
	}
	if viper.IsSet("agent.max_tokens") {
		cfg.Sources["agent.max_tokens"] = src
	}
	if viper.IsSet("agent.thinking") {
		cfg.Sources["agent.thinking"] = src
	}
	if viper.IsSet("agent.thinking_mode") {
		cfg.Sources["agent.thinking_mode"] = src
	}
	if viper.IsSet("agent.thinking_budget") {
		cfg.Sources["agent.thinking_budget"] = src
	}
	if viper.IsSet("agent.context_window") {
		cfg.Sources["agent.context_window"] = src
	}
	if viper.IsSet("tools.bash_timeout") {
		cfg.Sources["tools.bash_timeout"] = src
	}
	if viper.IsSet("tools.bash_max_output") {
		cfg.Sources["tools.bash_max_output"] = src
	}
	if viper.IsSet("tools.result_truncation") {
		cfg.Sources["tools.result_truncation"] = src
	}
	if viper.IsSet("tools.args_truncation") {
		cfg.Sources["tools.args_truncation"] = src
	}
	if viper.IsSet("tools.server_tool_timeout") {
		cfg.Sources["tools.server_tool_timeout"] = src
	}
	if viper.IsSet("tools.grep_max_results") {
		cfg.Sources["tools.grep_max_results"] = src
	}
	// List fields from global
	if len(cfg.Permissions.AllowedDirs) > 0 {
		cfg.Sources["permissions.allowed_dirs"] = src
	}
	if len(cfg.Permissions.AllowedCommands) > 0 {
		cfg.Sources["permissions.allowed_commands"] = src
	}
	if len(cfg.Permissions.DeniedCommands) > 0 {
		cfg.Sources["permissions.denied_commands"] = src
	}
	if len(cfg.Permissions.SensitivePatterns) > 0 {
		cfg.Sources["permissions.sensitive_patterns"] = src
	}
	if len(cfg.Permissions.NetworkAllowlist) > 0 {
		cfg.Sources["permissions.network_allowlist"] = src
	}
}

// mergeOverlayFile reads a YAML file and merges it on top of cfg.
// Scalars override; lists are merged and deduplicated.
func mergeOverlayFile(cfg *Config, file string, level string) {
	data, err := os.ReadFile(file)
	if err != nil {
		return // file doesn't exist or unreadable — skip silently
	}

	var overlay overlayConfig
	if err := yaml.Unmarshal(data, &overlay); err != nil {
		return // malformed — skip silently
	}

	src := ConfigSource{File: file, Level: level}

	// Scalar overrides
	if overlay.Endpoint != nil {
		cfg.Endpoint = *overlay.Endpoint
		cfg.Sources["endpoint"] = src
	}
	if overlay.APIKey != nil {
		cfg.APIKey = *overlay.APIKey
		cfg.Sources["api_key"] = src
	}
	if overlay.ModelTier != nil {
		cfg.ModelTier = *overlay.ModelTier
		cfg.Sources["model_tier"] = src
	}
	if overlay.AutoUpdateCheck != nil {
		cfg.AutoUpdateCheck = *overlay.AutoUpdateCheck
		cfg.Sources["auto_update_check"] = src
	}

	// Agent field-level merge
	if overlay.Agent != nil {
		if overlay.Agent.MaxIterations != nil {
			cfg.Agent.MaxIterations = *overlay.Agent.MaxIterations
			cfg.Sources["agent.max_iterations"] = src
		}
		if overlay.Agent.Temperature != nil {
			cfg.Agent.Temperature = *overlay.Agent.Temperature
			cfg.Sources["agent.temperature"] = src
		}
		if overlay.Agent.MaxTokens != nil {
			cfg.Agent.MaxTokens = *overlay.Agent.MaxTokens
			cfg.Sources["agent.max_tokens"] = src
		}
		if overlay.Agent.Thinking != nil {
			cfg.Agent.Thinking = *overlay.Agent.Thinking
			cfg.Sources["agent.thinking"] = src
		}
		if overlay.Agent.ThinkingMode != nil {
			cfg.Agent.ThinkingMode = *overlay.Agent.ThinkingMode
			cfg.Sources["agent.thinking_mode"] = src
		}
		if overlay.Agent.ThinkingBudget != nil {
			cfg.Agent.ThinkingBudget = *overlay.Agent.ThinkingBudget
			cfg.Sources["agent.thinking_budget"] = src
		}
		if overlay.Agent.ReasoningEffort != nil {
			cfg.Agent.ReasoningEffort = *overlay.Agent.ReasoningEffort
			cfg.Sources["agent.reasoning_effort"] = src
		}
		if overlay.Agent.Model != nil {
			cfg.Agent.Model = *overlay.Agent.Model
			cfg.Sources["agent.model"] = src
		}
		if overlay.Agent.ContextWindow != nil {
			cfg.Agent.ContextWindow = *overlay.Agent.ContextWindow
			cfg.Sources["agent.context_window"] = src
		}
	}

	// Tools field-level merge
	if overlay.Tools != nil {
		if overlay.Tools.BashTimeout != nil {
			cfg.Tools.BashTimeout = *overlay.Tools.BashTimeout
			cfg.Sources["tools.bash_timeout"] = src
		}
		if overlay.Tools.BashMaxOutput != nil {
			cfg.Tools.BashMaxOutput = *overlay.Tools.BashMaxOutput
			cfg.Sources["tools.bash_max_output"] = src
		}
		if overlay.Tools.ResultTruncation != nil {
			cfg.Tools.ResultTruncation = *overlay.Tools.ResultTruncation
			cfg.Sources["tools.result_truncation"] = src
		}
		if overlay.Tools.ArgsTruncation != nil {
			cfg.Tools.ArgsTruncation = *overlay.Tools.ArgsTruncation
			cfg.Sources["tools.args_truncation"] = src
		}
		if overlay.Tools.ServerToolTimeout != nil {
			cfg.Tools.ServerToolTimeout = *overlay.Tools.ServerToolTimeout
			cfg.Sources["tools.server_tool_timeout"] = src
		}
		if overlay.Tools.GrepMaxResults != nil {
			cfg.Tools.GrepMaxResults = *overlay.Tools.GrepMaxResults
			cfg.Sources["tools.grep_max_results"] = src
		}
	}

	// Permissions: merge and deduplicate lists
	if overlay.Permissions != nil {
		if len(overlay.Permissions.AllowedDirs) > 0 {
			cfg.Permissions.AllowedDirs = dedup(append(cfg.Permissions.AllowedDirs, overlay.Permissions.AllowedDirs...))
			cfg.Sources["permissions.allowed_dirs"] = src
		}
		if len(overlay.Permissions.AllowedCommands) > 0 {
			cfg.Permissions.AllowedCommands = dedup(append(cfg.Permissions.AllowedCommands, overlay.Permissions.AllowedCommands...))
			cfg.Sources["permissions.allowed_commands"] = src
		}
		if len(overlay.Permissions.DeniedCommands) > 0 {
			cfg.Permissions.DeniedCommands = dedup(append(cfg.Permissions.DeniedCommands, overlay.Permissions.DeniedCommands...))
			cfg.Sources["permissions.denied_commands"] = src
		}
		if len(overlay.Permissions.SensitivePatterns) > 0 {
			cfg.Permissions.SensitivePatterns = dedup(append(cfg.Permissions.SensitivePatterns, overlay.Permissions.SensitivePatterns...))
			cfg.Sources["permissions.sensitive_patterns"] = src
		}
		if len(overlay.Permissions.NetworkAllowlist) > 0 {
			cfg.Permissions.NetworkAllowlist = dedup(append(cfg.Permissions.NetworkAllowlist, overlay.Permissions.NetworkAllowlist...))
			cfg.Sources["permissions.network_allowlist"] = src
		}
	}

	// MCP servers: merge by name (overlay adds or overrides individual servers)
	if len(overlay.MCPServers) > 0 {
		if cfg.MCPServers == nil {
			cfg.MCPServers = make(map[string]mcp.MCPServerConfig)
		}
		for name, serverCfg := range overlay.MCPServers {
			cfg.MCPServers[name] = serverCfg
		}
		cfg.Sources["mcp_servers"] = src
	}
}

const mcpDefaultsMarker = "# shan-mcp-defaults-catalog"

// initMCPDefaults ensures MCP server defaults are discoverable.
//   - If config.yaml has NO mcp_servers: section → append the full catalog directly
//   - If config.yaml already has mcp_servers: → write a reference file and notify once
func initMCPDefaults(dir, configPath string) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	// Already processed
	if bytes.Contains(data, []byte(mcpDefaultsMarker)) {
		return
	}

	if bytes.Contains(data, []byte("\nmcp_servers:")) || bytes.HasPrefix(data, []byte("mcp_servers:")) {
		// User already has mcp_servers — don't touch their config, write reference file
		refPath := filepath.Join(dir, "mcp_servers.yaml")
		existing, _ := os.ReadFile(refPath)
		if !bytes.Equal(existing, mcpDefaultsYAML) {
			os.WriteFile(refPath, mcpDefaultsYAML, 0600)
			fmt.Fprintf(os.Stderr, "MCP: default server catalog available at %s\n", refPath)
		}
		// Write marker so we don't notify again
		if f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			f.WriteString("\n" + mcpDefaultsMarker + "\n")
			f.Close()
		}
		return
	}

	// No mcp_servers in config — append the full catalog
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()

	var buf bytes.Buffer
	buf.WriteString("\n" + mcpDefaultsMarker + "\n")
	buf.WriteString("mcp_servers:\n")

	// Indent each line of the defaults under mcp_servers:
	lines := bytes.Split(mcpDefaultsYAML, []byte("\n"))
	for _, line := range lines {
		if len(line) == 0 {
			buf.WriteByte('\n')
			continue
		}
		buf.WriteString("  ")
		buf.Write(line)
		buf.WriteByte('\n')
	}

	f.Write(buf.Bytes())
}

// fixMCPEnvKeyCasing re-reads MCP servers from YAML to restore env var key casing.
// Viper normalizes all map keys to lowercase, which breaks env vars (API_KEY → api_key).
func fixMCPEnvKeyCasing(cfg *Config, configPath string) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}
	var raw struct {
		MCPServers map[string]mcp.MCPServerConfig `yaml:"mcp_servers"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return
	}
	for name, srv := range raw.MCPServers {
		if existing, ok := cfg.MCPServers[name]; ok && len(srv.Env) > 0 {
			existing.Env = srv.Env
			cfg.MCPServers[name] = existing
		}
	}
}

// mergeDefaultMCPServers loads the embedded MCP server catalog and merges
// defaults underneath user-configured servers. User entries always win.
func mergeDefaultMCPServers(cfg *Config) {
	if len(mcpDefaultsYAML) == 0 {
		return
	}

	var defaults map[string]mcp.MCPServerConfig
	if err := yaml.Unmarshal(mcpDefaultsYAML, &defaults); err != nil {
		return
	}

	if cfg.MCPServers == nil {
		cfg.MCPServers = defaults
		return
	}

	// Only add defaults for servers the user hasn't configured
	for name, defCfg := range defaults {
		if _, exists := cfg.MCPServers[name]; !exists {
			cfg.MCPServers[name] = defCfg
		}
	}
}

// dedup returns a slice with duplicate strings removed, preserving order.
func dedup(items []string) []string {
	seen := make(map[string]bool, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}
