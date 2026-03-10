package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Kocoro-lab/shan/internal/agent"
)

func agentColor(name string) string {
	h := fnv.New32a()
	h.Write([]byte(name))
	hue := float64(h.Sum32() % 360)
	r, g, b := hslToRGB(hue, 0.65, 0.45)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

func hslToRGB(h, s, l float64) (uint8, uint8, uint8) {
	c := (1 - math.Abs(2*l-1)) * s
	hPrime := h / 60
	x := c * (1 - math.Abs(math.Mod(hPrime, 2)-1))
	var r1, g1, b1 float64
	switch {
	case hPrime < 1:
		r1, g1, b1 = c, x, 0
	case hPrime < 2:
		r1, g1, b1 = x, c, 0
	case hPrime < 3:
		r1, g1, b1 = 0, c, x
	case hPrime < 4:
		r1, g1, b1 = 0, x, c
	case hPrime < 5:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	m := l - c/2
	return uint8(math.Round((r1 + m) * 255)),
		uint8(math.Round((g1 + m) * 255)),
		uint8(math.Round((b1 + m) * 255))
}

// Tab registry

type tabRef struct {
	windowIndex int
	tabIndex    int
}

type tabRegistry struct {
	mu   sync.RWMutex
	tabs map[string]tabRef
}

func newTabRegistry() *tabRegistry {
	return &tabRegistry{tabs: make(map[string]tabRef)}
}

func (r *tabRegistry) add(title string, ref tabRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tabs[title] = ref
}

func (r *tabRegistry) lookup(title string) (tabRef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ref, ok := r.tabs[title]
	return ref, ok
}

func (r *tabRegistry) list() map[string]tabRef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]tabRef, len(r.tabs))
	for k, v := range r.tabs {
		out[k] = v
	}
	return out
}

// GhosttyTool exposes Ghostty terminal control as an agent tool.
type GhosttyTool struct {
	tabs *tabRegistry
}

type ghosttyArgs struct {
	Action    string `json:"action"`
	Command   string `json:"command,omitempty"`
	Title     string `json:"title,omitempty"`
	Direction string `json:"direction,omitempty"`
	Target    string `json:"target,omitempty"`
	Text      string `json:"text,omitempty"`
}

func (t *GhosttyTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name: "ghostty",
		Description: "Control Ghostty terminal (macOS only, via AppleScript). " +
			"Actions: new_tab (open tab, optional command/title), " +
			"new_split (direction: right|down, optional command/title), " +
			"send_input (target: tab title, text: keystrokes to send), " +
			"list_tabs (show tracked tabs). " +
			"Ghostty AppleScript API reference — object hierarchy: application > windows > tabs > terminals. " +
			"Create: 'set cfg to new surface configuration' then 'new window with configuration cfg' or 'new tab in win with configuration cfg'. " +
			"Split: 'split <terminal> direction right|left|down|up with configuration cfg'. " +
			"Input: 'input text \"...\" to <terminal>' then 'send key \"enter\" to <terminal>'. " +
			"References: 'set win to front window', 'set t to focused terminal of selected tab of win'. " +
			"Tab title: 'tell selected tab of win' then 'set title to \"name\"'. " +
			"Focus: 'focus <terminal>'. Close: not via AppleScript, use File > Close menu. " +
			"IMPORTANT: Do NOT use 'make new window/tab', 'write text', or 'set name of' — those are NOT valid Ghostty AppleScript.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":    map[string]any{"type": "string", "description": "Action: new_tab, new_split, send_input, list_tabs"},
				"command":   map[string]any{"type": "string", "description": "Shell command to run in the new tab/split"},
				"title":     map[string]any{"type": "string", "description": "Tab title (defaults to command basename)"},
				"direction": map[string]any{"type": "string", "description": "Split direction: right or down (for new_split)"},
				"target":    map[string]any{"type": "string", "description": "Tab title to send input to (for send_input)"},
				"text":      map[string]any{"type": "string", "description": "Text/keystrokes to send (for send_input)"},
			},
		},
		Required: []string{"action"},
	}
}

func (t *GhosttyTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args ghosttyArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}
	if !ghosttyAvailable() {
		return agent.ToolResult{
			Content: "Ghostty >= " + minGhosttyVersion + " is required but not found. " +
				"Use the applescript tool with macOS Terminal.app instead. " +
				"Example: tell application \"Terminal\" to do script \"<command>\"",
			IsError: true,
		}, nil
	}
	switch args.Action {
	case "new_tab":
		return t.runNewTab(args)
	case "new_split":
		return t.runNewSplit(args)
	case "send_input":
		return t.runSendInput(args)
	case "list_tabs":
		return t.runListTabs()
	default:
		return agent.ToolResult{
			Content: fmt.Sprintf("unknown action %q — use new_tab, new_split, send_input, or list_tabs", args.Action),
			IsError: true,
		}, nil
	}
}

func (t *GhosttyTool) runNewTab(args ghosttyArgs) (agent.ToolResult, error) {
	title := resolveTitle(args.Title, args.Command)
	color := agentColor(title)
	winIdx, tabIdx, err := ghosttyNewTab(args.Command, title, color)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	t.tabs.add(title, tabRef{windowIndex: winIdx, tabIndex: tabIdx})
	result := agent.ToolResult{Content: fmt.Sprintf("opened tab %q (window:%d, tab:%d)", title, winIdx, tabIdx)}
	appendScreenshot(&result)
	return result, nil
}

func (t *GhosttyTool) runNewSplit(args ghosttyArgs) (agent.ToolResult, error) {
	dir := args.Direction
	if dir == "" {
		dir = "right"
	}
	if dir != "right" && dir != "down" {
		return agent.ToolResult{Content: fmt.Sprintf("invalid direction %q — use right or down", dir), IsError: true}, nil
	}
	title := resolveTitle(args.Title, args.Command)
	color := agentColor(title)
	winIdx, tabIdx, err := ghosttyNewSplit(dir, args.Command, title, color)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	t.tabs.add(title, tabRef{windowIndex: winIdx, tabIndex: tabIdx})
	result := agent.ToolResult{Content: fmt.Sprintf("opened %s split %q", dir, title)}
	appendScreenshot(&result)
	return result, nil
}

func (t *GhosttyTool) runSendInput(args ghosttyArgs) (agent.ToolResult, error) {
	if args.Target == "" {
		return agent.ToolResult{Content: "target is required for send_input", IsError: true}, nil
	}
	if args.Text == "" {
		return agent.ToolResult{Content: "text is required for send_input", IsError: true}, nil
	}
	ref, ok := t.tabs.lookup(args.Target)
	if !ok {
		known := make([]string, 0)
		for name := range t.tabs.list() {
			known = append(known, name)
		}
		return agent.ToolResult{
			Content: fmt.Sprintf("tab %q not found — known tabs: %s", args.Target, strings.Join(known, ", ")),
			IsError: true,
		}, nil
	}
	if err := ghosttySendInput(ref.windowIndex, ref.tabIndex, args.Text); err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	return agent.ToolResult{Content: fmt.Sprintf("sent input to %q", args.Target)}, nil
}

func (t *GhosttyTool) runListTabs() (agent.ToolResult, error) {
	tabs := t.tabs.list()
	if len(tabs) == 0 {
		return agent.ToolResult{Content: "no tracked tabs"}, nil
	}
	var sb strings.Builder
	for name, ref := range tabs {
		sb.WriteString(fmt.Sprintf("- %s (window:%d, tab:%d)\n", name, ref.windowIndex, ref.tabIndex))
	}
	return agent.ToolResult{Content: sb.String()}, nil
}

func (t *GhosttyTool) RequiresApproval() bool { return true }

func resolveTitle(title, command string) string {
	if title != "" {
		return title
	}
	if command != "" {
		parts := strings.Fields(command)
		if len(parts) > 0 {
			segments := strings.Split(parts[0], "/")
			return segments[len(segments)-1]
		}
	}
	return "terminal"
}

func appendScreenshot(result *agent.ToolResult) {
	time.Sleep(500 * time.Millisecond)
	_, block, err := CaptureAndEncode(DefaultAPIWidth)
	if err == nil {
		result.Images = []agent.ImageBlock{block}
	}
}
