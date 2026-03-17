package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Kocoro-lab/ShanClaw/internal/agents"
	"github.com/Kocoro-lab/ShanClaw/internal/config"
	"github.com/Kocoro-lab/ShanClaw/internal/tools"
	"github.com/spf13/cobra"
)

var ghosttyCmd = &cobra.Command{
	Use:   "ghostty",
	Short: "Ghostty terminal integration",
}

var workspaceCmd = &cobra.Command{
	Use:   "workspace [agent1] [agent2] ...",
	Short: "Open a Ghostty window with one tab per agent (defaults to all agents)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !tools.GhosttyAvailable() {
			return fmt.Errorf("Ghostty >= 1.3.0 is required. Install/update from https://ghostty.org")
		}
		agentNames := args
		if len(agentNames) == 0 {
			agentsDir := filepath.Join(config.ShannonDir(), "agents")
			names, err := agents.ListAgents(agentsDir)
			if err != nil {
				return fmt.Errorf("failed to list agents: %w", err)
			}
			if len(names) == 0 {
				return fmt.Errorf("no agents found in %s", agentsDir)
			}
			agentNames = names
		}
		shanBin, _ := os.Executable()
		if shanBin == "" {
			shanBin = "shan"
		}
		script := tools.GhosttyWorkspaceScript(shanBin, agentNames)
		if script == "" {
			return fmt.Errorf("ghostty workspace requires macOS")
		}
		return tools.ExecGhosttyScript(script)
	},
}

func init() {
	ghosttyCmd.AddCommand(workspaceCmd)
	rootCmd.AddCommand(ghosttyCmd)
}
