package prompt

import (
	"runtime"
	"strings"
	"time"
)

// Layer character budgets.
const (
	maxMemoryChars       = 2000
	maxInstructionsChars = 16000
	maxContextChars      = 800
)

// PromptOptions configures the system prompt assembly.
type PromptOptions struct {
	BasePrompt   string   // hardcoded base (~200 tokens)
	Memory       string   // from LoadMemory (~500 tokens budget)
	Instructions string   // from LoadInstructions (~4000 tokens budget)
	ToolNames    []string // from ToolRegistry, auto-generated
	ServerTools  []string // server tool names (optional)
	MCPContext   string   // context from MCP servers (auth info, usage hints)
	CWD          string   // current working directory
	SessionInfo  string   // optional session context
}

// BuildSystemPrompt assembles the complete system prompt from layers.
func BuildSystemPrompt(opts PromptOptions) string {
	var sb strings.Builder

	// 1. Base prompt (unlimited)
	sb.WriteString(opts.BasePrompt)

	// 2. Memory
	if mem := strings.TrimSpace(opts.Memory); mem != "" {
		sb.WriteString("\n\n## Memory\n")
		sb.WriteString(truncate(mem, maxMemoryChars))
	}

	// 3. Instructions
	if inst := strings.TrimSpace(opts.Instructions); inst != "" {
		sb.WriteString("\n\n## Instructions\n")
		sb.WriteString(truncate(inst, maxInstructionsChars))
	}

	// 4. Available Tools (unlimited, auto-generated)
	sb.WriteString("\n\n## Available Tools\n")
	if len(opts.ToolNames) > 0 {
		sb.WriteString("You have these local tools: ")
		sb.WriteString(strings.Join(opts.ToolNames, ", "))
		sb.WriteString(".")
	}
	if len(opts.ServerTools) > 0 {
		if len(opts.ToolNames) > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("You also have server-side tools: ")
		sb.WriteString(strings.Join(opts.ServerTools, ", "))
		sb.WriteString(".")
	}

	// 5. macOS automation guidance (only on darwin with relevant tools)
	if guidance := macOSAutomationGuidance(opts.ToolNames); guidance != "" {
		sb.WriteString("\n\n")
		sb.WriteString(guidance)
	}

	// 6. MCP server context
	if mcp := strings.TrimSpace(opts.MCPContext); mcp != "" {
		sb.WriteString("\n\n## MCP Server Context\n")
		sb.WriteString(mcp)
	}

	// 7. Context
	contextParts := buildContext(opts.CWD, opts.SessionInfo)
	if contextParts != "" {
		sb.WriteString("\n\n## Context\n")
		sb.WriteString(truncate(contextParts, maxContextChars))
	}

	return sb.String()
}

// buildContext assembles the context section from CWD and session info.
func buildContext(cwd, sessionInfo string) string {
	var parts []string
	parts = append(parts, "Current date: "+time.Now().Format("2006-01-02 15:04 MST"))
	if cwd != "" {
		parts = append(parts, "Working directory: "+cwd)
	}
	if sessionInfo != "" {
		parts = append(parts, sessionInfo)
	}
	return strings.Join(parts, "\n")
}

// truncate limits s to maxChars, appending [truncated] if trimmed.
func truncate(s string, maxChars int) string {
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	return string(r[:maxChars]) + "\n[truncated]"
}

// macOSAutomationGuidance returns workflow guidance for macOS automation tools,
// or empty string if not on darwin or no relevant tools are registered.
func macOSAutomationGuidance(toolNames []string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	hasMacTools := false
	for _, name := range toolNames {
		if name == "accessibility" || name == "computer" || name == "wait_for" {
			hasMacTools = true
			break
		}
	}
	if !hasMacTools {
		return ""
	}
	return `## macOS Automation

When controlling macOS applications:

1. **Orient before acting**: Use accessibility annotate to see what's on screen before clicking. It returns a labeled screenshot with numbered elements.
2. **Use refs, not coordinates**: After annotate or read_tree, click elements by ref (e.g. ref="e14"). Only use coordinate clicks as a last resort.
3. **Specify app name**: Always include the app parameter. Use the exact name as shown in the Dock (e.g. "Finder", "Safari", "Google Chrome", "Notes").
4. **Wait, don't sleep**: After launching apps or navigating, use wait_for instead of bash sleep. Example: wait_for with condition="titleContains" value="Google".
5. **Browser tool for web content**: For interacting with web page elements, use the browser tool (DOM-level access). Use accessibility only for native macOS UI.
6. **Focus before typing**: Ensure the target app is frontmost before using computer type/hotkey. Use accessibility click on the target field first.
7. **CJK/emoji text**: computer type handles Chinese, Japanese, Korean, and emoji automatically via clipboard paste.`
}
