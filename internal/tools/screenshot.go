package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type ScreenshotTool struct{}

type screenshotArgs struct {
	Target string `json:"target,omitempty"`
	Path   string `json:"path,omitempty"`
	Delay  int    `json:"delay,omitempty"`
}

func (t *ScreenshotTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "screenshot",
		Description: "Capture the macOS desktop screen (fullscreen, window, or region). Use ONLY for native macOS UI. For web page screenshots use browser(action=screenshot) — this tool captures the macOS screen, not browser content.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{"type": "string", "description": "Capture target: fullscreen (default), window, or region"},
				"path":   map[string]any{"type": "string", "description": "Output file path. If not provided, saves to a temp file"},
				"delay":  map[string]any{"type": "integer", "description": "Seconds to wait before capture (default: 0)"},
			},
		},
	}
}

func (t *ScreenshotTool) RequiresApproval() bool { return true }

func (t *ScreenshotTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args screenshotArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	target := args.Target
	if target == "" {
		target = "fullscreen"
	}

	switch target {
	case "fullscreen", "window", "region":
	default:
		return agent.ToolResult{
			Content: fmt.Sprintf("unknown target: %q (valid: fullscreen, window, region)", target),
			IsError: true,
		}, nil
	}

	path := ExpandHome(args.Path)
	if path == "" {
		f, err := os.CreateTemp("", "shannon-screenshot-*.png")
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("failed to create temp file: %v", err), IsError: true}, nil
		}
		path = f.Name()
		f.Close()
	}

	cmdArgs := buildScreencaptureArgs(target, path, args.Delay)
	cmd := exec.CommandContext(ctx, "screencapture", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("screencapture error: %v\n%s", err, string(output)),
			IsError: true,
		}, nil
	}

	// Resize and encode for LLM vision
	if err := ResizeImage(path, DefaultAPIWidth); err != nil {
		// Non-fatal: screenshot saved but resize failed
		return agent.ToolResult{Content: fmt.Sprintf("Screenshot saved to: %s (resize failed: %v)", path, err)}, nil
	}

	block, encErr := EncodeImage(path)
	if encErr != nil {
		return agent.ToolResult{Content: fmt.Sprintf("Screenshot saved to: %s (encode failed: %v)", path, encErr)}, nil
	}

	return agent.ToolResult{
		Content: fmt.Sprintf("Screenshot saved to: %s", path),
		Images:  []agent.ImageBlock{block},
	}, nil
}

func buildScreencaptureArgs(target string, path string, delay int) []string {
	var args []string

	if delay > 0 {
		args = append(args, "-T", strconv.Itoa(delay))
	}

	switch target {
	case "window":
		args = append(args, "-w")
	case "region":
		args = append(args, "-s")
	}

	args = append(args, path)
	return args
}
