package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Kocoro-lab/shan/internal/agent"
	"github.com/Kocoro-lab/shan/internal/client"
)

type ComputerTool struct {
	client  *AXClient
	screenW int
	screenH int
}

func (t *ComputerTool) ensureScreenDims() {
	if t.screenW > 0 {
		return
	}
	w, h, err := GetScreenDimensions()
	if err != nil {
		t.screenW = DefaultAPIWidth
		t.screenH = DefaultAPIHeight
		return
	}
	t.screenW = w
	t.screenH = h
}

func (t *ComputerTool) scaleXY(apiX, apiY int) (int, int) {
	t.ensureScreenDims()
	x, y := ScaleCoordinates(apiX, apiY, DefaultAPIWidth, DefaultAPIHeight, t.screenW, t.screenH)
	return ClampCoordinates(x, y, t.screenW, t.screenH)
}

func (t *ComputerTool) captureAfterAction(result agent.ToolResult) agent.ToolResult {
	time.Sleep(500 * time.Millisecond)
	_, block, err := CaptureAndEncode(DefaultAPIWidth)
	if err != nil {
		return result // Non-fatal
	}
	result.Images = []agent.ImageBlock{block}
	return result
}

type computerArgs struct {
	Action     string `json:"action"`
	X          int    `json:"x,omitempty"`
	Y          int    `json:"y,omitempty"`
	Text       string `json:"text,omitempty"`
	Keys       string `json:"keys,omitempty"`
	Button     string `json:"button,omitempty"`
	Clicks     int    `json:"clicks,omitempty"`
	Coordinate []int  `json:"coordinate,omitempty"` // Anthropic native: [x, y]
}

// normalizeArgs maps Anthropic native action names and coordinate format
// to our internal format.
func normalizeArgs(args *computerArgs) {
	// Map Anthropic coordinate array to x, y
	if len(args.Coordinate) == 2 {
		args.X = args.Coordinate[0]
		args.Y = args.Coordinate[1]
	}

	// Map Anthropic native action names to our actions
	switch args.Action {
	case "left_click":
		args.Action = "click"
		args.Button = "left"
		args.Clicks = 1
	case "right_click":
		args.Action = "click"
		args.Button = "right"
		args.Clicks = 1
	case "double_click":
		args.Action = "click"
		args.Button = "left"
		args.Clicks = 2
	case "middle_click":
		args.Action = "click"
		args.Button = "left" // fallback — no middle click support
		args.Clicks = 1
	case "triple_click":
		args.Action = "click"
		args.Button = "left"
		args.Clicks = 3
	case "mouse_move":
		args.Action = "move"
	case "key":
		args.Action = "hotkey"
		if args.Text != "" && args.Keys == "" {
			args.Keys = args.Text // Anthropic sends key combo in "text" field
		}
	case "screenshot":
		args.Action = "screenshot"
	}
}

func (t *ComputerTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "computer",
		Description: "OS-level mouse and keyboard control for macOS. Use for coordinate-based clicks, typing text (CJK/emoji safe), and keyboard shortcuts. For clicking UI elements, prefer accessibility tool (ref-based) over coordinate clicks. Actions: click, type, hotkey, move, screenshot.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "description": "Action to perform: click, type, hotkey, move"},
				"x":      map[string]any{"type": "integer", "description": "Screen X coordinate (for click/move)"},
				"y":      map[string]any{"type": "integer", "description": "Screen Y coordinate (for click/move)"},
				"text":   map[string]any{"type": "string", "description": "Text to type (for type action)"},
				"keys":   map[string]any{"type": "string", "description": "Key combination like command+c, command+shift+4 (for hotkey action)"},
				"button": map[string]any{"type": "string", "description": "Mouse button: left (default), right (for click action)"},
				"clicks": map[string]any{"type": "integer", "description": "Number of clicks: 1 (default), 2 for double-click (for click action)"},
			},
		},
		Required: []string{"action"},
	}
}

func (t *ComputerTool) RequiresApproval() bool { return true }

func (t *ComputerTool) NativeToolDef() *client.NativeToolDef {
	return &client.NativeToolDef{
		Type:            "computer_20251124",
		Name:            "computer",
		DisplayWidthPx:  DefaultAPIWidth,
		DisplayHeightPx: DefaultAPIHeight,
	}
}

func (t *ComputerTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args computerArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	if args.Action == "" {
		return agent.ToolResult{Content: "missing required parameter: action", IsError: true}, nil
	}

	normalizeArgs(&args)

	switch args.Action {
	case "screenshot":
		return t.screenshot()
	case "click":
		if t.client == nil {
			return agent.ToolResult{Content: "computer tool requires macOS with ax_server", IsError: true}, nil
		}
		return t.click(ctx, args)
	case "type":
		if t.client == nil {
			return agent.ToolResult{Content: "computer tool requires macOS with ax_server", IsError: true}, nil
		}
		return t.typeText(ctx, args)
	case "hotkey":
		if t.client == nil {
			return agent.ToolResult{Content: "computer tool requires macOS with ax_server", IsError: true}, nil
		}
		return t.hotkey(ctx, args)
	case "move":
		if t.client == nil {
			return agent.ToolResult{Content: "computer tool requires macOS with ax_server", IsError: true}, nil
		}
		return t.move(ctx, args)
	default:
		return agent.ToolResult{
			Content: fmt.Sprintf("unknown action: %q (valid: click, type, hotkey, move, screenshot)", args.Action),
			IsError: true,
		}, nil
	}
}

func (t *ComputerTool) screenshot() (agent.ToolResult, error) {
	path, block, err := CaptureAndEncode(DefaultAPIWidth)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("screenshot error: %v", err), IsError: true}, nil
	}
	return agent.ToolResult{
		Content: fmt.Sprintf("Screenshot captured. Saved to: %s", path),
		Images:  []agent.ImageBlock{block},
	}, nil
}

func (t *ComputerTool) click(ctx context.Context, args computerArgs) (agent.ToolResult, error) {
	x, y := t.scaleXY(args.X, args.Y)
	button := args.Button
	if button == "" {
		button = "left"
	}
	clicks := args.Clicks
	if clicks < 1 {
		clicks = 1
	}

	rawResult, err := t.client.Call(ctx, "mouse_event", map[string]any{
		"type":   "click",
		"x":      float64(x),
		"y":      float64(y),
		"button": button,
		"clicks": clicks,
	})
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("click error: %v", err),
			IsError: true,
		}, nil
	}

	msg := fmt.Sprintf("Clicked %s button %d time(s) at (%d, %d)", button, clicks, x, y)
	msg += parseActionContext(rawResult)
	result := agent.ToolResult{Content: msg}
	return t.captureAfterAction(result), nil
}

func (t *ComputerTool) typeText(ctx context.Context, args computerArgs) (agent.ToolResult, error) {
	if args.Text == "" {
		return agent.ToolResult{Content: "type action requires 'text' parameter", IsError: true}, nil
	}

	// ax_server handles CJK/non-ASCII via clipboard paste automatically
	rawResult, err := t.client.Call(ctx, "type_text", map[string]any{
		"value": args.Text,
	})
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("type error: %v", err),
			IsError: true,
		}, nil
	}

	msg := fmt.Sprintf("Typed: %s", args.Text)
	msg += parseActionContext(rawResult)
	result := agent.ToolResult{Content: msg}
	return t.captureAfterAction(result), nil
}

func (t *ComputerTool) hotkey(ctx context.Context, args computerArgs) (agent.ToolResult, error) {
	if args.Keys == "" {
		return agent.ToolResult{Content: "hotkey action requires 'keys' parameter", IsError: true}, nil
	}

	parts := strings.Split(strings.ToLower(args.Keys), "+")
	if len(parts) == 0 {
		return agent.ToolResult{Content: fmt.Sprintf("invalid key combination: %q", args.Keys), IsError: true}, nil
	}

	key := strings.TrimSpace(parts[len(parts)-1])
	var modifiers []string
	for _, part := range parts[:len(parts)-1] {
		modifiers = append(modifiers, strings.TrimSpace(part))
	}

	rawResult, err := t.client.Call(ctx, "key_event", map[string]any{
		"key":       key,
		"modifiers": modifiers,
	})
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("hotkey error: %v", err),
			IsError: true,
		}, nil
	}

	msg := fmt.Sprintf("Pressed: %s", args.Keys)
	msg += parseActionContext(rawResult)
	result := agent.ToolResult{Content: msg}
	return t.captureAfterAction(result), nil
}

func (t *ComputerTool) move(ctx context.Context, args computerArgs) (agent.ToolResult, error) {
	x, y := t.scaleXY(args.X, args.Y)

	rawResult, err := t.client.Call(ctx, "mouse_event", map[string]any{
		"type": "move",
		"x":    float64(x),
		"y":    float64(y),
	})
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("move error: %v", err),
			IsError: true,
		}, nil
	}

	msg := fmt.Sprintf("Moved cursor to (%d, %d)", x, y)
	msg += parseActionContext(rawResult)
	result := agent.ToolResult{Content: msg}
	return t.captureAfterAction(result), nil
}

// parseActionContext extracts the context field from an ax_server action response
// and formats it as a human-readable string.
func parseActionContext(raw json.RawMessage) string {
	var resp struct {
		Context *appContext `json:"context,omitempty"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return ""
	}
	return formatContext(resp.Context)
}
