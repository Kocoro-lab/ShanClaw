package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var mentionRe = regexp.MustCompile(`^@([a-zA-Z0-9][a-zA-Z0-9_-]*)(?:\s|$)`)

type Agent struct {
	Name   string
	Prompt string
	Memory string
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
	return &Agent{Name: name, Prompt: string(promptData), Memory: memory}, nil
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
