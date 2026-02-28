package prompt

import (
	"strings"
	"testing"
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

	// Verify assembly order
	sections := []struct {
		name    string
		content string
	}{
		{"base", "You are Shannon."},
		{"memory header", "## Memory"},
		{"memory content", "User prefers Go."},
		{"instructions header", "## Instructions"},
		{"instructions content", "Always use gofmt."},
		{"tools header", "## Available Tools"},
		{"local tools", "You have these local tools: file_read, file_write, bash."},
		{"server tools", "You also have server-side tools: web_search, code_review."},
		{"context header", "## Context"},
		{"cwd", "Working directory: /home/user/project"},
		{"session", "Session: abc123"},
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
