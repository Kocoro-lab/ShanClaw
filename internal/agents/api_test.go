package agents

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/skills"
)

func TestAgentToAPI_Minimal(t *testing.T) {
	a := &Agent{Name: "test", Prompt: "hello"}
	api := a.ToAPI()
	if api.Name != "test" {
		t.Errorf("name = %q", api.Name)
	}
	if api.Memory != nil {
		t.Error("expected nil memory")
	}
	if api.Config != nil {
		t.Error("expected nil config")
	}
}

func TestAgentToAPI_Full(t *testing.T) {
	a := &Agent{
		Name:   "test",
		Prompt: "hello",
		Memory: "some memory",
		Config: &AgentConfig{
			Tools: &AgentToolsFilter{Allow: []string{"bash"}},
		},
		Commands: map[string]string{"review": "do review"},
		Skills:   []*skills.Skill{{Name: "check", Description: "check things", Prompt: "check it"}},
	}
	api := a.ToAPI()
	if api.Memory == nil || *api.Memory != "some memory" {
		t.Error("expected memory")
	}
	if api.Config == nil || api.Config.Tools == nil {
		t.Error("expected config with tools")
	}
	if len(api.Commands) != 1 {
		t.Error("expected 1 command")
	}
	if len(api.Skills) != 1 {
		t.Error("expected 1 skill")
	}
}

func TestWriteAndLoadAgent(t *testing.T) {
	dir := t.TempDir()
	name := "test-agent"

	if err := WriteAgentPrompt(dir, name, "You are test."); err != nil {
		t.Fatalf("WriteAgentPrompt: %v", err)
	}
	if err := WriteAgentCommand(dir, name, "greet", "Say hello"); err != nil {
		t.Fatalf("WriteAgentCommand: %v", err)
	}
	if err := WriteAgentSkill(dir, name, &skills.Skill{
		Name: "check", Description: "check things", Prompt: "check things",
	}); err != nil {
		t.Fatalf("WriteAgentSkill: %v", err)
	}

	a, err := LoadAgent(dir, name)
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if a.Prompt != "You are test." {
		t.Errorf("prompt = %q", a.Prompt)
	}
	if a.Commands["greet"] != "Say hello" {
		t.Errorf("command = %q", a.Commands["greet"])
	}
	// Agent skill "check" should be present (bundled skills also loaded).
	found := false
	for _, s := range a.Skills {
		if s.Name == "check" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("agent skill 'check' not found in skills (got %d skills)", len(a.Skills))
	}
}

func TestDeleteAgentDir(t *testing.T) {
	dir := t.TempDir()
	WriteAgentPrompt(dir, "doomed", "bye")
	if err := DeleteAgentDir(dir, "doomed"); err != nil {
		t.Fatalf("DeleteAgentDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "doomed")); !os.IsNotExist(err) {
		t.Error("expected directory removed")
	}
}

func TestAgentCreateRequest_Validate(t *testing.T) {
	// Missing name
	r := &AgentCreateRequest{Prompt: "hi"}
	if err := r.Validate(); err == nil {
		t.Error("expected error for empty name")
	}
	// Missing prompt
	r = &AgentCreateRequest{Name: "test"}
	if err := r.Validate(); err == nil {
		t.Error("expected error for empty prompt")
	}
	// Both allow and deny
	r = &AgentCreateRequest{
		Name: "test", Prompt: "hi",
		Config: &AgentConfigAPI{Tools: &AgentToolsFilter{Allow: []string{"a"}, Deny: []string{"b"}}},
	}
	if err := r.Validate(); err == nil {
		t.Error("expected error for both allow+deny")
	}
	// Valid
	r = &AgentCreateRequest{Name: "test", Prompt: "hi"}
	if err := r.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	r = &AgentCreateRequest{
		Name:     "bad-skill",
		Prompt:   "hi",
		Skills:   []*skills.Skill{nil},
	}
	if err := r.Validate(); err == nil {
		t.Error("expected error for null skill entry")
	}
}

func TestAgentConfigAPI_WatchHeartbeatRoundTrip(t *testing.T) {
	agent := &Agent{
		Name:   "test",
		Prompt: "test prompt",
		Config: &AgentConfig{
			Watch: []WatchEntry{{Path: "~/Code", Glob: "*.go"}},
			Heartbeat: &HeartbeatConfig{
				Every: "30m",
			},
		},
	}
	api := agent.ToAPI()
	if api.Config == nil {
		t.Fatal("expected config")
	}
	if len(api.Config.Watch) != 1 {
		t.Fatalf("expected 1 watch entry, got %d", len(api.Config.Watch))
	}
	if api.Config.Heartbeat == nil {
		t.Fatal("expected heartbeat config")
	}
	if api.Config.Heartbeat.Every != "30m" {
		t.Errorf("expected 30m, got %s", api.Config.Heartbeat.Every)
	}
}
