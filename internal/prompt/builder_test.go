package prompt

import (
	"strings"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/skills"
)

func TestBuildSystemPrompt_FullAssembly(t *testing.T) {
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt:   "You are Shannon.",
		Memory:       "User prefers Go.",
		Instructions: "Always use gofmt.",
		ToolNames:    []string{"file_read", "file_write", "bash"},
		ServerTools:  []string{"web_search", "code_review"},
		CWD:          "/home/user/project",
		SessionInfo:  "Session: abc123",
	})

	// Verify assembly order: base → context → memory → instructions → tools
	sections := []struct {
		name    string
		content string
	}{
		{"base", "You are Shannon."},
		{"context header", "## Context"},
		{"cwd", "Working directory: /home/user/project"},
		{"session", "Session: abc123"},
		{"memory header", "## Memory"},
		{"memory content", "User prefers Go."},
		{"instructions header", "## Instructions"},
		{"instructions content", "Always use gofmt."},
		{"tools header", "## Available Tools"},
		{"local tools", "You have these local tools: file_read, file_write, bash."},
		{"server tools", "You also have server-side tools: web_search, code_review."},
	}

	lastIdx := -1
	for _, s := range sections {
		idx := strings.Index(result, s.content)
		if idx == -1 {
			t.Errorf("missing %s: %q", s.name, s.content)
			continue
		}
		if idx <= lastIdx {
			t.Errorf("%s appears before previous section (idx=%d, lastIdx=%d)", s.name, idx, lastIdx)
		}
		lastIdx = idx
	}
}

func TestBuildSystemPrompt_MinimalOptions(t *testing.T) {
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt: "Base only.",
	})

	if !strings.HasPrefix(result, "Base only.") {
		t.Errorf("expected base prompt at start, got: %q", result[:20])
	}

	// No memory or instructions sections
	if strings.Contains(result, "## Memory") {
		t.Errorf("unexpected Memory section when memory is empty")
	}
	if strings.Contains(result, "## Instructions") {
		t.Errorf("unexpected Instructions section when instructions is empty")
	}

	// Tools section should still exist (even if empty)
	if !strings.Contains(result, "## Available Tools") {
		t.Errorf("expected Available Tools section")
	}
}

func TestBuildSystemPrompt_EmptyMemoryAndInstructions(t *testing.T) {
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt: "Base.",
		Memory:     "   ",
		ToolNames:  []string{"bash"},
	})

	if strings.Contains(result, "## Memory") {
		t.Errorf("unexpected Memory section for whitespace-only memory")
	}
}

func TestBuildSystemPrompt_OnlyLocalTools(t *testing.T) {
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt: "Base.",
		ToolNames:  []string{"file_read", "grep"},
	})

	if !strings.Contains(result, "You have these local tools: file_read, grep.") {
		t.Errorf("expected local tools line, got:\n%s", result)
	}
	if strings.Contains(result, "server-side") {
		t.Errorf("unexpected server tools line when no server tools")
	}
}

func TestBuildSystemPrompt_OnlyServerTools(t *testing.T) {
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt:  "Base.",
		ServerTools: []string{"web_search"},
	})

	if strings.Contains(result, "local tools") {
		t.Errorf("unexpected local tools line when no local tools")
	}
	if !strings.Contains(result, "You also have server-side tools: web_search.") {
		t.Errorf("expected server tools line, got:\n%s", result)
	}
}

func TestBuildSystemPrompt_MemoryTruncation(t *testing.T) {
	bigMemory := strings.Repeat("m", maxMemoryChars+500)
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt: "Base.",
		Memory:     bigMemory,
	})

	memIdx := strings.Index(result, "## Memory\n")
	if memIdx == -1 {
		t.Fatalf("missing Memory section")
	}
	memSection := result[memIdx:]

	if !strings.Contains(memSection, "[truncated]") {
		t.Errorf("expected truncation marker in memory section")
	}

	// Memory content should be exactly maxMemoryChars + "[truncated]" suffix
	memContent := memSection[len("## Memory\n"):]
	// Find end of memory section (next ## or end)
	nextSection := strings.Index(memContent, "\n\n##")
	if nextSection != -1 {
		memContent = memContent[:nextSection]
	}
	// Count the m's
	mCount := strings.Count(memContent, "m")
	if mCount != maxMemoryChars {
		t.Errorf("expected %d chars of memory content, got %d", maxMemoryChars, mCount)
	}
}

func TestBuildSystemPrompt_InstructionsTruncation(t *testing.T) {
	bigInstructions := strings.Repeat("i", maxInstructionsChars+1000)
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt:   "Base.",
		Instructions: bigInstructions,
	})

	if !strings.Contains(result, "[truncated]") {
		t.Errorf("expected truncation marker in instructions section")
	}
}

func TestBuildSystemPrompt_ContextTruncation(t *testing.T) {
	bigSession := strings.Repeat("s", maxContextChars+500)
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt:  "Base.",
		SessionInfo: bigSession,
	})

	if !strings.Contains(result, "[truncated]") {
		t.Errorf("expected truncation marker in context section")
	}
}

func TestBuildSystemPrompt_NoContext(t *testing.T) {
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt: "Base.",
	})

	// Context section always present (contains current date)
	if !strings.Contains(result, "## Context") {
		t.Errorf("expected Context section with current date")
	}
	if !strings.Contains(result, "Current date:") {
		t.Errorf("expected current date in context")
	}
}

func TestBuildSystemPrompt_CWDOnly(t *testing.T) {
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt: "Base.",
		CWD:        "/tmp/test",
	})

	if !strings.Contains(result, "## Context") {
		t.Errorf("expected Context section with CWD")
	}
	if !strings.Contains(result, "Working directory: /tmp/test") {
		t.Errorf("expected CWD in context")
	}
}

func TestBuildSystemPrompt_SessionInfoOnly(t *testing.T) {
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt:  "Base.",
		SessionInfo: "Resuming session X",
	})

	if !strings.Contains(result, "Resuming session X") {
		t.Errorf("expected session info in context")
	}
}

func TestBuildSystemPrompt_StickyContext(t *testing.T) {
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt:    "Base.",
		Memory:        "Some memory.",
		StickyContext: "Customer: Alice. Order #8891. Refund amount: $247.83.",
	})

	memIdx := strings.Index(result, "## Memory")
	stickyIdx := strings.Index(result, "## Session Facts")
	toolsIdx := strings.Index(result, "## Available Tools")

	if stickyIdx == -1 {
		t.Fatal("missing ## Session Facts section")
	}
	if memIdx != -1 && stickyIdx < memIdx {
		t.Errorf("Session Facts appears before Memory (sticky=%d, mem=%d)", stickyIdx, memIdx)
	}
	if stickyIdx > toolsIdx {
		t.Errorf("Session Facts appears after Available Tools (sticky=%d, tools=%d)", stickyIdx, toolsIdx)
	}
	if !strings.Contains(result, "Customer: Alice. Order #8891. Refund amount: $247.83.") {
		t.Error("StickyContext content not found verbatim in output")
	}
}

func TestBuildSystemPrompt_StickyContext_NeverTruncated(t *testing.T) {
	longFacts := strings.Repeat("fact: x. ", 500) // ~4500 chars, well over any limit
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt:    "Base.",
		StickyContext: longFacts,
	})

	if strings.Contains(result, "[truncated]") {
		t.Error("StickyContext must never be truncated")
	}
	// TrimSpace is applied before injection, so check trimmed content
	if !strings.Contains(result, strings.TrimSpace(longFacts)) {
		t.Error("StickyContext full content must appear verbatim")
	}
}

func TestBuildSystemPrompt_NoStickyContext(t *testing.T) {
	result := BuildSystemPrompt(PromptOptions{
		BasePrompt: "Base.",
	})
	if strings.Contains(result, "## Session Facts") {
		t.Error("unexpected Session Facts section when StickyContext is empty")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{"under limit", "hello", 10, "hello"},
		{"at limit", "hello", 5, "hello"},
		{"over limit", "hello world", 5, "hello\n[truncated]"},
		{"empty", "", 10, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.max)
			if got != tt.expected {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.expected)
			}
		})
	}
}

func TestBuildSystemPrompt_SkillCatalog(t *testing.T) {
	opts := PromptOptions{
		BasePrompt: "Base prompt.",
		Skills: []*skills.Skill{
			{Name: "pdf", Description: "Extract text from PDFs"},
			{Name: "mcp-builder", Description: "Guide for creating MCP servers"},
		},
	}
	result := BuildSystemPrompt(opts)
	if !strings.Contains(result, "## Available Skills") {
		t.Error("missing Available Skills section")
	}
	if !strings.Contains(result, "| pdf") {
		t.Error("missing pdf skill in catalog")
	}
	if !strings.Contains(result, "| mcp-builder") {
		t.Error("missing mcp-builder skill in catalog")
	}
	if !strings.Contains(result, "use_skill") {
		t.Error("missing use_skill instruction")
	}
}

func TestBuildSystemPrompt_NoSkills(t *testing.T) {
	opts := PromptOptions{
		BasePrompt: "Base prompt.",
	}
	result := BuildSystemPrompt(opts)
	if strings.Contains(result, "## Available Skills") {
		t.Error("should not have Available Skills section when no skills")
	}
}
