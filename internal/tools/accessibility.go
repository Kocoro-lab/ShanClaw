package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/Kocoro-lab/shan/internal/agent"
)

type refEntry struct {
	path string
	role string
	pid  int
}

type AccessibilityTool struct {
	refs    map[string]refEntry
	lastPID int
}

type accessibilityArgs struct {
	Action   string  `json:"action"`
	App      string  `json:"app,omitempty"`
	MaxDepth int     `json:"max_depth,omitempty"`
	Filter   string  `json:"filter,omitempty"`
	Ref      string  `json:"ref,omitempty"`
	Value    *string `json:"value,omitempty"`
}

func (t *AccessibilityTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "accessibility",
		Description: "Read the macOS accessibility tree and interact with UI elements by reference. Use read_tree to see elements, then click/press/set_value/get_value by ref. More reliable than coordinate-based clicking for standard macOS apps.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":    map[string]any{"type": "string", "description": "Action: read_tree, click, press, set_value, get_value"},
				"app":       map[string]any{"type": "string", "description": "Target app name (defaults to frontmost app)"},
				"max_depth": map[string]any{"type": "integer", "description": "Tree traversal depth (default: 4, for read_tree)"},
				"filter":    map[string]any{"type": "string", "description": "Filter: all (default) or interactive (for read_tree)"},
				"ref":       map[string]any{"type": "string", "description": "Element ref from read_tree (e.g. e14, for click/press/set_value/get_value)"},
				"value":     map[string]any{"type": "string", "description": "Value to set (for set_value)"},
			},
		},
		Required: []string{"action"},
	}
}

func (t *AccessibilityTool) RequiresApproval() bool { return true }

func (t *AccessibilityTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	if runtime.GOOS != "darwin" {
		return agent.ToolResult{Content: "accessibility tool is only available on macOS", IsError: true}, nil
	}

	var args accessibilityArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	if args.Action == "" {
		return agent.ToolResult{Content: "missing required parameter: action", IsError: true}, nil
	}

	switch args.Action {
	case "read_tree":
		return t.readTree(ctx, args)
	case "click", "press":
		return t.performAction(ctx, args.Action, args.Ref)
	case "set_value":
		return t.setValue(ctx, args.Ref, args.Value)

	case "get_value":
		return t.getValue(ctx, args.Ref)
	default:
		return agent.ToolResult{
			Content: fmt.Sprintf("unknown action: %q (valid: read_tree, click, press, set_value, get_value)", args.Action),
			IsError: true,
		}, nil
	}
}

func scriptPath() (string, error) {
	// 1. Next to the shan binary (tar.gz release)
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		p := filepath.Join(dir, "scripts", "ax_helper.swift")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		// 2. Homebrew: binary in bin/, script in ../lib/shan/scripts/
		p = filepath.Join(dir, "..", "lib", "shan", "scripts", "ax_helper.swift")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// 3. Development: relative to working directory
	p := filepath.Join("internal", "tools", "scripts", "ax_helper.swift")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("ax_helper.swift not found — ensure it exists in internal/tools/scripts/")
}

func (t *AccessibilityTool) runSwift(ctx context.Context, input map[string]any) (map[string]any, error) {
	path, err := scriptPath()
	if err != nil {
		return nil, err
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal input: %v", err)
	}

	cmd := exec.CommandContext(ctx, "swift", path)
	cmd.Stdin = strings.NewReader(string(inputJSON))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("swift error: %v\n%s", err, stderr.String())
	}

	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("parse swift output: %v\nraw: %s", err, stdout.String())
	}

	return result, nil
}

// validAppName checks that an app name contains only safe characters.
var validAppNamePattern = regexp.MustCompile(`^[a-zA-Z0-9 ._\-()]+$`)

func (t *AccessibilityTool) resolvePID(ctx context.Context, appName string) (int, error) {
	if appName == "" {
		return 0, nil
	}
	if !validAppNamePattern.MatchString(appName) {
		return 0, fmt.Errorf("invalid app name %q — only letters, numbers, spaces, dots, hyphens, underscores, and parentheses allowed", appName)
	}
	script := fmt.Sprintf(`tell application "System Events" to unix id of process "%s"`, appName)
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("app %q not found or not running", appName)
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &pid); err != nil {
		return 0, fmt.Errorf("could not parse PID for %q", appName)
	}
	return pid, nil
}

func (t *AccessibilityTool) readTree(ctx context.Context, args accessibilityArgs) (agent.ToolResult, error) {
	pid, err := t.resolvePID(ctx, args.App)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	input := map[string]any{
		"action":    "read_tree",
		"max_depth": args.MaxDepth,
		"filter":    args.Filter,
	}
	if args.MaxDepth == 0 {
		input["max_depth"] = 4
	}
	if args.Filter == "" {
		input["filter"] = "all"
	}
	if pid > 0 {
		input["pid"] = pid
	}

	result, err := t.runSwift(ctx, input)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("accessibility error: %v", err), IsError: true}, nil
	}

	if errMsg, ok := result["error"].(string); ok {
		return agent.ToolResult{Content: errMsg, IsError: true}, nil
	}

	t.refs = make(map[string]refEntry)

	resultPID := 0
	if p, ok := result["pid"].(float64); ok {
		resultPID = int(p)
	}
	t.lastPID = resultPID

	if paths, ok := result["ref_paths"].(map[string]any); ok {
		for ref, val := range paths {
			if entry, ok := val.(map[string]any); ok {
				p, _ := entry["path"].(string)
				r, _ := entry["role"].(string)
				t.refs[ref] = refEntry{path: p, role: r, pid: resultPID}
			}
		}
	}

	delete(result, "ref_paths")

	outputJSON, _ := json.MarshalIndent(result, "", "  ")
	content := string(outputJSON)

	// If output is too large, trim elements array and re-serialize valid JSON
	if len(content) > 8000 {
		if elems, ok := result["elements"].([]any); ok {
			// Binary search for element count that fits
			lo, hi := 0, len(elems)
			for lo < hi {
				mid := (lo + hi + 1) / 2
				result["elements"] = elems[:mid]
				trial, _ := json.MarshalIndent(result, "", "  ")
				if len(trial) <= 7800 { // leave room for truncation notice
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			result["elements"] = elems[:lo]
			result["truncated"] = fmt.Sprintf("showing %d of %d elements — use filter='interactive' or lower max_depth", lo, len(elems))
			outputJSON, _ = json.MarshalIndent(result, "", "  ")
			content = string(outputJSON)
		}
	}

	return agent.ToolResult{Content: content}, nil
}

func (t *AccessibilityTool) lookupRef(ref string) (refEntry, error) {
	if ref == "" {
		return refEntry{}, fmt.Errorf("missing required parameter: ref")
	}
	if t.refs == nil || len(t.refs) == 0 {
		return refEntry{}, fmt.Errorf("no refs available — call read_tree first")
	}
	entry, ok := t.refs[ref]
	if !ok {
		return refEntry{}, fmt.Errorf("unknown ref %q — call read_tree to get current refs", ref)
	}
	return entry, nil
}

func (t *AccessibilityTool) performAction(ctx context.Context, action string, ref string) (agent.ToolResult, error) {
	entry, err := t.lookupRef(ref)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	input := map[string]any{
		"action": action,
		"pid":    entry.pid,
		"path":   entry.path,
	}
	if entry.role != "" {
		input["expected_role"] = entry.role
	}

	result, err := t.runSwift(ctx, input)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("accessibility error: %v", err), IsError: true}, nil
	}

	if errMsg, ok := result["error"].(string); ok {
		return agent.ToolResult{Content: errMsg, IsError: true}, nil
	}

	msg := fmt.Sprintf("%v", result["result"])
	return agent.ToolResult{Content: msg}, nil
}

func (t *AccessibilityTool) setValue(ctx context.Context, ref string, value *string) (agent.ToolResult, error) {
	entry, err := t.lookupRef(ref)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if value == nil {
		return agent.ToolResult{Content: "set_value requires 'value' parameter", IsError: true}, nil
	}

	input := map[string]any{
		"action": "set_value",
		"pid":    entry.pid,
		"path":   entry.path,
		"value":  *value,
	}
	if entry.role != "" {
		input["expected_role"] = entry.role
	}

	result, err := t.runSwift(ctx, input)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("accessibility error: %v", err), IsError: true}, nil
	}

	if errMsg, ok := result["error"].(string); ok {
		return agent.ToolResult{Content: errMsg, IsError: true}, nil
	}

	msg := fmt.Sprintf("%v", result["result"])
	return agent.ToolResult{Content: msg}, nil
}

func (t *AccessibilityTool) getValue(ctx context.Context, ref string) (agent.ToolResult, error) {
	entry, err := t.lookupRef(ref)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	input := map[string]any{
		"action": "get_value",
		"pid":    entry.pid,
		"path":   entry.path,
	}

	result, err := t.runSwift(ctx, input)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("accessibility error: %v", err), IsError: true}, nil
	}

	if errMsg, ok := result["error"].(string); ok {
		return agent.ToolResult{Content: errMsg, IsError: true}, nil
	}

	msg := fmt.Sprintf("%v", result["result"])
	if role, ok := result["role"].(string); ok {
		msg = fmt.Sprintf("%s (role: %s)", msg, role)
	}
	return agent.ToolResult{Content: msg}, nil
}
