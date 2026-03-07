package agents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgent_ReadsAgentAndMemory(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "ops-bot")
	os.MkdirAll(agentDir, 0700)
	os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte("You are ops-bot."), 0600)
	os.WriteFile(filepath.Join(agentDir, "MEMORY.md"), []byte("Last deploy: ok"), 0600)

	a, err := LoadAgent(dir, "ops-bot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Name != "ops-bot" {
		t.Errorf("name = %q, want %q", a.Name, "ops-bot")
	}
	if a.Prompt != "You are ops-bot." {
		t.Errorf("prompt = %q, want %q", a.Prompt, "You are ops-bot.")
	}
	if a.Memory != "Last deploy: ok" {
		t.Errorf("memory = %q, want %q", a.Memory, "Last deploy: ok")
	}
}

func TestLoadAgent_MissingAgentMD(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "ops-bot"), 0700)
	_, err := LoadAgent(dir, "ops-bot")
	if err == nil {
		t.Fatal("expected error for missing AGENT.md")
	}
}

func TestLoadAgent_MissingMemoryIsOK(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "ops-bot")
	os.MkdirAll(agentDir, 0700)
	os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte("You are ops-bot."), 0600)

	a, err := LoadAgent(dir, "ops-bot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Memory != "" {
		t.Errorf("memory = %q, want empty", a.Memory)
	}
}

func TestLoadAgent_RejectsInvalidNames(t *testing.T) {
	dir := t.TempDir()
	invalid := []string{"../etc", "a/b", "", ".hidden", "a b", "A_UPPER", "名前"}
	for _, name := range invalid {
		_, err := LoadAgent(dir, name)
		if err == nil {
			t.Errorf("expected error for invalid name %q", name)
		}
	}
}

func TestValidateAgentName(t *testing.T) {
	valid := []string{"ops-bot", "a", "my_agent_123", "x-1"}
	for _, name := range valid {
		if err := ValidateAgentName(name); err != nil {
			t.Errorf("ValidateAgentName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "../x", "a/b", ".dot", "UPPER", "a b", "名前"}
	for _, name := range invalid {
		if err := ValidateAgentName(name); err == nil {
			t.Errorf("ValidateAgentName(%q) = nil, want error", name)
		}
	}
}

func TestListAgents(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		agentDir := filepath.Join(dir, name)
		os.MkdirAll(agentDir, 0700)
		os.WriteFile(filepath.Join(agentDir, "AGENT.md"), []byte("agent"), 0600)
	}
	os.MkdirAll(filepath.Join(dir, "no-agent"), 0700)

	names, err := ListAgents(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("got %d agents, want 2", len(names))
	}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("agents = %v, want [alpha beta]", names)
	}
}

func TestParseAgentMention(t *testing.T) {
	tests := []struct {
		input     string
		wantAgent string
		wantMsg   string
	}{
		{"@ops-bot check prod", "ops-bot", "check prod"},
		{"@OPS-BOT check prod", "ops-bot", "check prod"},
		{"check prod", "", "check prod"},
		{"@ops-bot", "ops-bot", ""},
		{"@ broken", "", "@ broken"},
		{"@invalid/name test", "", "@invalid/name test"},
	}
	for _, tt := range tests {
		agent, msg := ParseAgentMention(tt.input)
		if agent != tt.wantAgent || msg != tt.wantMsg {
			t.Errorf("ParseAgentMention(%q) = (%q, %q), want (%q, %q)",
				tt.input, agent, msg, tt.wantAgent, tt.wantMsg)
		}
	}
}
