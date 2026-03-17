package tools

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type SystemInfoTool struct{}

func (t *SystemInfoTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "system_info",
		Description: "Get system information: OS, architecture, hostname, CPU count, memory, and disk usage.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Required: nil,
	}
}

func (t *SystemInfoTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	hostname, _ := os.Hostname()

	var sb strings.Builder
	fmt.Fprintf(&sb, "OS: %s\n", runtime.GOOS)
	fmt.Fprintf(&sb, "Arch: %s\n", runtime.GOARCH)
	fmt.Fprintf(&sb, "Hostname: %s\n", hostname)
	fmt.Fprintf(&sb, "CPUs: %d\n", runtime.NumCPU())

	memInfo := getMemoryInfo()
	if memInfo != "" {
		fmt.Fprintf(&sb, "%s", memInfo)
	}

	diskInfo := getDiskInfo()
	if diskInfo != "" {
		fmt.Fprintf(&sb, "%s", diskInfo)
	}

	return agent.ToolResult{Content: sb.String()}, nil
}

func (t *SystemInfoTool) RequiresApproval() bool { return false }
