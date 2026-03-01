package agent

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Kocoro-lab/shan/internal/audit"
	"github.com/Kocoro-lab/shan/internal/client"
	"github.com/Kocoro-lab/shan/internal/hooks"
	"github.com/Kocoro-lab/shan/internal/instructions"
	"github.com/Kocoro-lab/shan/internal/permissions"
	"github.com/Kocoro-lab/shan/internal/prompt"
)

const baseSystemPrompt = `You are Shannon, an AI assistant running in a CLI terminal on the user's local macOS computer.
You MUST use tools to perform actions — never pretend you performed an action without calling a tool.
If the user asks you to DO something (open an app, send a notification, take a screenshot, etc.), you MUST call the appropriate tool. Never say "I've done it" without a tool call.
NEVER output fake tool calls as text (e.g. <function_calls> XML). Only use the actual tool calling mechanism. If a tool fails, explain the error and try a different approach.
Be concise in your responses. Summarize tool results — never quote raw file contents or tool output verbatim.

Tool selection rules:
- For opening apps, window management, UI automation: use applescript (e.g. tell application "Safari" to activate)
- For notifications: use notify
- For clipboard read/write: use clipboard
- For mouse/keyboard control: use computer
- For screen capture: use screenshot
- For browser automation (isolated headless Chrome): use browser — WARNING: this launches a fresh Chrome with no cookies/sessions, public sites like Google will block it with CAPTCHA. Only use for your own sites or simple page fetches.
- For browsing real websites, searching, or interacting with logged-in sites: use web_search/web_fetch (server-side), or applescript+screenshot+computer (OS-level, uses the user's real Chrome)
- For file operations: use file_read, file_write, file_edit, glob, grep, directory_list
- For shell commands, tests, builds: use bash
When reading files, always use file_read before editing.`

type TurnUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
	LLMCalls     int
}

type EventHandler interface {
	OnToolCall(name string, args string)
	OnToolResult(name string, args string, result ToolResult, elapsed time.Duration)
	OnText(text string)
	OnStreamDelta(delta string)
	OnApprovalNeeded(tool string, args string) bool
	OnUsage(usage TurnUsage)
}

type AgentLoop struct {
	client             *client.GatewayClient
	tools              *ToolRegistry
	modelTier          string
	handler            EventHandler
	shannonDir         string
	maxIter            int
	resultTrunc        int
	argsTrunc          int
	permissions        *permissions.PermissionsConfig
	auditor            *audit.AuditLogger
	hookRunner         *hooks.HookRunner
	mcpContext         string
	bypassPermissions  bool
	enableStreaming     bool
}

func NewAgentLoop(gw *client.GatewayClient, tools *ToolRegistry, modelTier string, shannonDir string, maxIter int, resultTrunc int, argsTrunc int, perms *permissions.PermissionsConfig, auditor *audit.AuditLogger, hookRunner *hooks.HookRunner) *AgentLoop {
	if maxIter <= 0 {
		maxIter = 25
	}
	if resultTrunc <= 0 {
		resultTrunc = 2000
	}
	if argsTrunc <= 0 {
		argsTrunc = 200
	}
	return &AgentLoop{
		client:      gw,
		tools:       tools,
		modelTier:   modelTier,
		shannonDir:  shannonDir,
		maxIter:     maxIter,
		resultTrunc: resultTrunc,
		argsTrunc:   argsTrunc,
		permissions: perms,
		auditor:     auditor,
		hookRunner:  hookRunner,
	}
}

func (a *AgentLoop) SetHandler(h EventHandler) {
	a.handler = h
}

func (a *AgentLoop) SetModelTier(tier string) {
	a.modelTier = tier
}

func (a *AgentLoop) SetMCPContext(ctx string) {
	a.mcpContext = ctx
}

func (a *AgentLoop) SetBypassPermissions(bypass bool) {
	a.bypassPermissions = bypass
}

func (a *AgentLoop) SetEnableStreaming(enable bool) {
	a.enableStreaming = enable
}

func (a *AgentLoop) Run(ctx context.Context, userMessage string, history []client.Message) (string, *TurnUsage, error) {
	// Build system prompt using prompt builder with instructions/memory
	var toolNames []string
	for _, t := range a.tools.All() {
		toolNames = append(toolNames, t.Info().Name)
	}

	cwd, _ := os.Getwd()
	memory, _ := instructions.LoadMemory(a.shannonDir, 200)
	instrText, _ := instructions.LoadInstructions(a.shannonDir, ".", 4000)

	systemPrompt := prompt.BuildSystemPrompt(prompt.PromptOptions{
		BasePrompt:   baseSystemPrompt,
		Memory:       memory,
		Instructions: instrText,
		ToolNames:    toolNames,
		MCPContext:   a.mcpContext,
		CWD:          cwd,
	})

	messages := make([]client.Message, 0)
	messages = append(messages, client.Message{Role: "system", Content: client.NewTextContent(systemPrompt)})
	if history != nil {
		messages = append(messages, history...)
	}
	messages = append(messages, client.Message{Role: "user", Content: client.NewTextContent(userMessage)})

	toolSchemas := a.tools.Schemas()
	usage := &TurnUsage{}

	// Track consecutive tool calls for loop detection
	var lastToolCall string // exact signature (name+args)
	var lastToolName string // just the tool name
	dupCount := 0          // exact duplicate count
	sameToolCount := 0     // consecutive same-tool count (any args)

	const maxSameToolCalls = 4 // max consecutive calls to same tool with varying args

	for i := 0; i < a.maxIter; i++ {
		// Call LLM — streaming or blocking
		var resp *client.CompletionResponse
		var err error
		req := client.CompletionRequest{
			Messages:  messages,
			ModelTier: a.modelTier,
			Tools:     toolSchemas,
		}
		if a.enableStreaming && a.handler != nil {
			resp, err = a.client.CompleteStream(ctx, req, func(delta client.StreamDelta) {
				a.handler.OnStreamDelta(delta.Text)
			})
			// Fall back to non-streaming if gateway doesn't support it
			if err != nil {
				resp, err = a.client.Complete(ctx, req)
			}
		} else {
			resp, err = a.client.Complete(ctx, req)
		}
		if err != nil {
			return "", usage, fmt.Errorf("LLM call failed: %w", err)
		}

		usage.InputTokens += resp.Usage.InputTokens
		usage.OutputTokens += resp.Usage.OutputTokens
		usage.TotalTokens += resp.Usage.TotalTokens
		usage.CostUSD += resp.Usage.CostUSD
		usage.LLMCalls++

		// If no tool calls, return text response
		if !resp.HasToolCalls() {
			if a.handler != nil {
				a.handler.OnText(resp.OutputText)
			}
			return resp.OutputText, usage, nil
		}

		// Execute all tool calls
		toolCalls := resp.AllToolCalls()
		var allResults strings.Builder
		if resp.OutputText != "" {
			allResults.WriteString(resp.OutputText)
			allResults.WriteString("\n\n")
		}

		for _, fc := range toolCalls {
			argsStr := fc.ArgumentsString()

			// Loop detection
			callSig := fc.Name + ":" + argsStr
			if callSig == lastToolCall {
				dupCount++
			} else {
				lastToolCall = callSig
				dupCount = 1
			}
			if fc.Name == lastToolName {
				sameToolCount++
			} else {
				lastToolName = fc.Name
				sameToolCount = 1
			}
			if dupCount >= 3 || sameToolCount >= maxSameToolCalls {
				messages = append(messages, client.Message{
					Role:    "user",
					Content: client.NewTextContent("You've called the same tool repeatedly. Please use the results already available and provide your answer now."),
				})
				finalResp, err := a.client.Complete(ctx, client.CompletionRequest{
					Messages:  messages,
					ModelTier: a.modelTier,
				})
				if err != nil {
					return "", usage, fmt.Errorf("LLM call failed: %w", err)
				}
				usage.InputTokens += finalResp.Usage.InputTokens
				usage.OutputTokens += finalResp.Usage.OutputTokens
				usage.TotalTokens += finalResp.Usage.TotalTokens
				usage.CostUSD += finalResp.Usage.CostUSD
				usage.LLMCalls++
				if a.handler != nil {
					a.handler.OnText(finalResp.OutputText)
				}
				return finalResp.OutputText, usage, nil
			}

			if a.handler != nil {
				a.handler.OnToolCall(fc.Name, argsStr)
			}

			tool, ok := a.tools.Get(fc.Name)
			if !ok {
				allResults.WriteString(fmt.Sprintf("I called %s(%s).\n\nError: unknown tool: %s\n\n", fc.Name, truncateStr(argsStr, a.argsTrunc), fc.Name))
				continue
			}

			// Permission check
			decision, wasApproved := a.checkPermissionAndApproval(fc.Name, argsStr, tool, resp.OutputText)
			if decision == "deny" {
				a.logAudit(fc.Name, argsStr, "tool call denied by permission policy", decision, false, 0)
				allResults.WriteString(fmt.Sprintf("I called %s(%s).\n\nError: tool call denied by permission policy\n\n", fc.Name, truncateStr(argsStr, a.argsTrunc)))
				if a.handler != nil {
					a.handler.OnToolResult(fc.Name, argsStr, ToolResult{Content: "denied by policy", IsError: true}, 0)
				}
				continue
			}
			if decision == "ask" && !wasApproved {
				a.logAudit(fc.Name, argsStr, "tool call denied by user", decision, false, 0)
				allResults.WriteString(fmt.Sprintf("I called %s(%s).\n\nError: tool call denied by user\n\n", fc.Name, truncateStr(argsStr, a.argsTrunc)))
				if a.handler != nil {
					a.handler.OnToolResult(fc.Name, argsStr, ToolResult{Content: "denied by user", IsError: true}, 0)
				}
				continue
			}

			// Pre-tool-use hook
			if a.hookRunner != nil {
				hookDecision, hookReason, hookErr := a.hookRunner.RunPreToolUse(ctx, fc.Name, argsStr, "")
				if hookErr != nil {
					fmt.Fprintf(os.Stderr, "[hooks] pre-tool-use error: %v\n", hookErr)
				}
				if hookDecision == "deny" {
					a.logAudit(fc.Name, argsStr, "tool call denied by hook: "+hookReason, "deny", false, 0)
					allResults.WriteString(fmt.Sprintf("I called %s(%s).\n\nError: tool call denied by hook: %s\n\n", fc.Name, truncateStr(argsStr, a.argsTrunc), hookReason))
					continue
				}
			}

			startTime := time.Now()
			result, runErr := tool.Run(ctx, argsStr)
			elapsed := time.Since(startTime)
			if runErr != nil {
				result = ToolResult{Content: fmt.Sprintf("tool error: %v", runErr), IsError: true}
			}

			// Skip sanitizeResult for image results (base64 data is intentional)
			if len(result.Images) == 0 {
				result.Content = sanitizeResult(result.Content)
			}

			if a.hookRunner != nil {
				_ = a.hookRunner.RunPostToolUse(ctx, fc.Name, argsStr, result.Content, "")
			}

			a.logAudit(fc.Name, argsStr, result.Content, decision, wasApproved, elapsed.Milliseconds())

			if a.handler != nil {
				a.handler.OnToolResult(fc.Name, argsStr, result, elapsed)
			}

			if len(result.Images) > 0 {
				// Build content blocks: text result + image blocks
				var blocks []client.ContentBlock
				cleanResult := stripLineNumbers(result.Content)
				text := fmt.Sprintf("I called %s(%s).\n\nResult:\n%s",
					fc.Name, truncateStr(argsStr, a.argsTrunc), truncateStr(cleanResult, a.resultTrunc))
				blocks = append(blocks, client.ContentBlock{Type: "text", Text: text})
				for _, img := range result.Images {
					blocks = append(blocks, client.ContentBlock{
						Type: "image",
						Source: &client.ImageSource{
							Type:      "base64",
							MediaType: img.MediaType,
							Data:      img.Data,
						},
					})
				}
				// Image content must be in a user role message (Anthropic API requirement)
				messages = append(messages, client.Message{
					Role:    "user",
					Content: client.NewBlockContent(blocks),
				})
			} else {
				cleanResult := stripLineNumbers(result.Content)
				if result.IsError {
					allResults.WriteString(fmt.Sprintf("I called %s(%s).\n\nError: %s\n\n", fc.Name, truncateStr(argsStr, a.argsTrunc), truncateStr(cleanResult, a.resultTrunc)))
				} else {
					allResults.WriteString(fmt.Sprintf("I called %s(%s).\n\nResult:\n%s\n\n", fc.Name, truncateStr(argsStr, a.argsTrunc), truncateStr(cleanResult, a.resultTrunc)))
				}
			}
		}

		// Add all tool results as a single assistant message (skip if all results were image tools)
		if allResults.Len() > 0 {
			messages = append(messages, client.Message{
				Role:    "assistant",
				Content: client.NewTextContent(allResults.String()),
			})
		}
	}

	return "", usage, fmt.Errorf("agent loop exceeded %d iterations", a.maxIter)
}

// checkPermissionAndApproval runs the permission engine check, then falls back
// to the existing RequiresApproval/SafeChecker logic if needed.
// Returns (decision, wasApproved). decision is "allow", "deny", or "ask".
// wasApproved is true if the tool call should proceed.
func (a *AgentLoop) checkPermissionAndApproval(toolName, argsStr string, tool Tool, outputText string) (string, bool) {
	// Bypass mode: skip all permission checks including hard-blocks
	if a.bypassPermissions {
		return "allow", true
	}

	// Run permission engine checks based on tool type
	if a.permissions != nil {
		decision, _ := permissions.CheckToolCall(toolName, argsStr, a.permissions)
		if decision != "" {
			if decision == "deny" {
				return "deny", false
			}
			if decision == "allow" {
				return "allow", true
			}
			// decision == "ask" — fall through to existing approval logic
		}
	}

	// Existing RequiresApproval + SafeChecker logic
	needsApproval := tool.RequiresApproval()
	if needsApproval {
		if checker, ok := tool.(SafeChecker); ok && checker.IsSafeArgs(argsStr) {
			needsApproval = false
		}
	}
	if needsApproval {
		approved := false
		if a.handler != nil {
			approved = a.handler.OnApprovalNeeded(toolName, argsStr)
		}
		return "ask", approved
	}
	return "allow", true
}

// logAudit writes an audit entry if the auditor is configured.
func (a *AgentLoop) logAudit(toolName, argsStr, outputSummary, decision string, approved bool, durationMs int64) {
	if a.auditor == nil {
		return
	}
	a.auditor.Log(audit.AuditEntry{
		Timestamp:     time.Now(),
		SessionID:     "",
		ToolName:      toolName,
		InputSummary:  argsStr,
		OutputSummary: outputSummary,
		Decision:      decision,
		Approved:      approved,
		DurationMs:    durationMs,
	})
}

// base64ImagePattern matches long base64 strings that start with known image signatures.
// PNG starts with iVBOR, JPEG with /9j/.
var base64ImagePattern = regexp.MustCompile(`(?:(?:"[^"]*(?:base64|image|data)[^"]*"\s*:\s*")|(?:^|\s))([/+A-Za-z0-9](?:iVBOR|/9j/)[A-Za-z0-9+/=\s]{200,})`)

// rawBase64Pattern matches any standalone base64 blob of 500+ chars (likely binary data).
var rawBase64Pattern = regexp.MustCompile(`[A-Za-z0-9+/]{500,}={0,2}`)

// sanitizeResult replaces base64 image blobs in tool output with a short placeholder
// to avoid polluting LLM context and terminal output with huge binary strings.
func sanitizeResult(content string) string {
	result := base64ImagePattern.ReplaceAllStringFunc(content, func(match string) string {
		// Estimate original byte size (base64 is ~4/3 ratio)
		b64Len := len(strings.Map(func(r rune) rune {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=' {
				return r
			}
			return -1
		}, match))
		bytes := b64Len * 3 / 4
		return fmt.Sprintf("[image: %d bytes]", bytes)
	})
	// Catch any remaining large base64 blobs not matched by the image-specific pattern
	result = rawBase64Pattern.ReplaceAllStringFunc(result, func(match string) string {
		bytes := len(match) * 3 / 4
		return fmt.Sprintf("[binary data: %d bytes]", bytes)
	})
	return result
}

// lineNumPrefix matches the "  42 | " prefix added by file_read.
var lineNumPrefix = regexp.MustCompile(`(?m)^\s*\d+\s*\| `)

// stripLineNumbers removes line-number prefixes from file_read output
// so the LLM sees clean content (saves tokens, prevents verbatim echo).
func stripLineNumbers(s string) string {
	return lineNumPrefix.ReplaceAllString(s, "")
}

// formatToolResult builds a single assistant message containing the tool call
// and its result, so the LLM sees both in one turn and doesn't re-call.
func formatToolResult(name, args, outputText, result string, isError bool, argsTrunc, resultTrunc int) string {
	var sb strings.Builder
	if outputText != "" {
		sb.WriteString(outputText)
		sb.WriteString("\n\n")
	}
	sb.WriteString(fmt.Sprintf("I called %s(%s).\n\n", name, truncateStr(args, argsTrunc)))

	// Strip line numbers from file content — saves tokens, prevents verbatim echo
	cleanResult := stripLineNumbers(result)

	if isError {
		sb.WriteString(fmt.Sprintf("Error: %s", truncateStr(cleanResult, resultTrunc)))
	} else {
		sb.WriteString(fmt.Sprintf("Result:\n%s", truncateStr(cleanResult, resultTrunc)))
	}
	return sb.String()
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
