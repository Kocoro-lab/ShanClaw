package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/skills"
)

type useSkillTool struct {
	skills *[]*skills.Skill
}

type useSkillArgs struct {
	SkillName string `json:"skill_name"`
	Args      string `json:"args"`
}

func newUseSkillTool(s *[]*skills.Skill) *useSkillTool {
	return &useSkillTool{skills: s}
}

func (t *useSkillTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "use_skill",
		Description: "Activate a named skill to load its specialized instructions. Only call this when the user's request clearly matches a skill's purpose. Returns the full skill content as your working instructions.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill_name": map[string]any{
					"type":        "string",
					"description": "Name of the skill to activate",
				},
				"args": map[string]any{
					"type":        "string",
					"description": "Optional context or arguments for the skill",
				},
			},
		},
		Required: []string{"skill_name"},
	}
}

func (t *useSkillTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args useSkillArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}
	if args.SkillName == "" {
		return agent.ToolResult{Content: "skill_name is required", IsError: true}, nil
	}
	if t.skills == nil || len(*t.skills) == 0 {
		return agent.ToolResult{Content: "no skills available", IsError: true}, nil
	}

	var skill *skills.Skill
	for _, s := range *t.skills {
		if s.Name == args.SkillName {
			skill = s
			break
		}
	}
	if skill == nil {
		available := make([]string, 0, len(*t.skills))
		for _, s := range *t.skills {
			available = append(available, s.Name)
		}
		return agent.ToolResult{
			Content: fmt.Sprintf("unknown skill %q. Available skills: %s", args.SkillName, strings.Join(available, ", ")),
			IsError: true,
		}, nil
	}

	body := skill.Prompt
	if skill.Dir != "" {
		body = rewriteRelativePaths(body, skill.Dir)
	}
	if args.Args != "" {
		body += "\n\n## User Context\n\n" + args.Args
	}
	return agent.ToolResult{Content: body}, nil
}

var relativePathPattern = regexp.MustCompile(`(?m)(^|\s)((?:scripts|references|assets)/[^\s)]+)`)

func rewriteRelativePaths(body, dir string) string {
	return relativePathPattern.ReplaceAllStringFunc(body, func(match string) string {
		trimmed := strings.TrimLeft(match, " \t")
		prefix := match[:len(match)-len(trimmed)]
		absPath := filepath.Join(dir, trimmed)
		if _, err := os.Stat(absPath); err == nil {
			return prefix + absPath
		}
		return match
	})
}

func (t *useSkillTool) RequiresApproval() bool { return false }
