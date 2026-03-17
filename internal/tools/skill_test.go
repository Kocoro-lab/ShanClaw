package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/skills"
)

func TestUseSkill_HappyPath(t *testing.T) {
	s := &skills.Skill{
		Name: "pdf", Description: "Extract text from PDFs",
		Prompt: "# PDF Guide\n\nUse pypdf to extract text.", Dir: t.TempDir(),
	}
	skillList := []*skills.Skill{s}
	tool := newUseSkillTool(&skillList)

	args, _ := json.Marshal(map[string]string{"skill_name": "pdf"})
	result, err := tool.Run(context.Background(), string(args))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if result.IsError {
		t.Error("should not be error")
	}
	if !strings.Contains(result.Content, "# PDF Guide") {
		t.Errorf("missing body: %s", result.Content)
	}
}

func TestUseSkill_WithArgs(t *testing.T) {
	s := &skills.Skill{Name: "pdf", Prompt: "# PDF Guide", Dir: t.TempDir()}
	skillList := []*skills.Skill{s}
	tool := newUseSkillTool(&skillList)

	args, _ := json.Marshal(map[string]string{"skill_name": "pdf", "args": "merge two PDFs"})
	result, err := tool.Run(context.Background(), string(args))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result.Content, "## User Context") {
		t.Error("missing User Context")
	}
	if !strings.Contains(result.Content, "merge two PDFs") {
		t.Error("missing args")
	}
}

func TestUseSkill_UnknownSkill(t *testing.T) {
	s := &skills.Skill{Name: "pdf", Prompt: "body"}
	skillList := []*skills.Skill{s}
	tool := newUseSkillTool(&skillList)

	args, _ := json.Marshal(map[string]string{"skill_name": "nonexistent"})
	result, err := tool.Run(context.Background(), string(args))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !result.IsError {
		t.Fatal("should be error")
	}
	if !strings.Contains(result.Content, "pdf") {
		t.Errorf("should list available: %s", result.Content)
	}
}

func TestUseSkill_NoSkills(t *testing.T) {
	var skillList []*skills.Skill
	tool := newUseSkillTool(&skillList)

	args, _ := json.Marshal(map[string]string{"skill_name": "anything"})
	result, err := tool.Run(context.Background(), string(args))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !result.IsError {
		t.Fatal("should be error")
	}
}

func TestUseSkill_RelativePathRewrite(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
	os.WriteFile(filepath.Join(dir, "scripts", "extract.py"), []byte("print('hi')"), 0o644)

	s := &skills.Skill{Name: "pdf", Prompt: "Run scripts/extract.py to extract.", Dir: dir}
	skillList := []*skills.Skill{s}
	tool := newUseSkillTool(&skillList)

	args, _ := json.Marshal(map[string]string{"skill_name": "pdf"})
	result, err := tool.Run(context.Background(), string(args))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	expected := filepath.Join(dir, "scripts/extract.py")
	if !strings.Contains(result.Content, expected) {
		t.Errorf("expected absolute path %s in: %s", expected, result.Content)
	}
}
