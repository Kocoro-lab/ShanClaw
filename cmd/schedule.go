package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/Kocoro-lab/ShanClaw/internal/config"
	"github.com/Kocoro-lab/ShanClaw/internal/schedule"
	"github.com/spf13/cobra"
)

func newScheduleManager() *schedule.Manager {
	dir := config.ShannonDir()
	home, _ := os.UserHomeDir()
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	return schedule.NewManager(
		filepath.Join(dir, "schedules.json"),
		plistDir,
	)
}

var scheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "Manage local scheduled tasks",
}

var scheduleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all scheduled tasks",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := newScheduleManager()
		list, err := mgr.List()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("No scheduled tasks.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tAGENT\tCRON\tENABLED\tSYNC\tPROMPT")
		for _, s := range list {
			prompt := s.Prompt
			if len([]rune(prompt)) > 50 {
				prompt = string([]rune(prompt)[:50]) + "..."
			}
			agent := s.Agent
			if agent == "" {
				agent = "(default)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%s\t%s\n", s.ID, agent, s.Cron, s.Enabled, s.SyncStatus, prompt)
		}
		w.Flush()
		return nil
	},
}

var (
	schedCreateAgent  string
	schedCreateCron   string
	schedCreatePrompt string
)

var scheduleCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new scheduled task",
	RunE: func(cmd *cobra.Command, args []string) error {
		if schedCreateCron == "" || schedCreatePrompt == "" {
			return fmt.Errorf("--cron and --prompt are required")
		}
		mgr := newScheduleManager()
		id, err := mgr.Create(schedCreateAgent, schedCreateCron, schedCreatePrompt)
		if err != nil {
			return err
		}
		fmt.Printf("Created schedule %s\n", id)
		return nil
	},
}

var (
	schedUpdateCron   string
	schedUpdatePrompt string
)

var scheduleUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a scheduled task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if schedUpdateCron == "" && schedUpdatePrompt == "" {
			return fmt.Errorf("at least one of --cron or --prompt is required")
		}
		mgr := newScheduleManager()
		opts := &schedule.UpdateOpts{}
		if schedUpdateCron != "" {
			opts.Cron = &schedUpdateCron
		}
		if schedUpdatePrompt != "" {
			opts.Prompt = &schedUpdatePrompt
		}
		if err := mgr.Update(args[0], opts); err != nil {
			return err
		}
		fmt.Printf("Updated schedule %s\n", args[0])
		return nil
	},
}

var scheduleRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a scheduled task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := newScheduleManager()
		if err := mgr.Remove(args[0]); err != nil {
			return err
		}
		fmt.Printf("Removed schedule %s\n", args[0])
		return nil
	},
}

var scheduleEnableCmd = &cobra.Command{
	Use:   "enable <id>",
	Short: "Enable a scheduled task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := newScheduleManager()
		enabled := true
		if err := mgr.Update(args[0], &schedule.UpdateOpts{Enabled: &enabled}); err != nil {
			return err
		}
		fmt.Printf("Enabled schedule %s\n", args[0])
		return nil
	},
}

var scheduleDisableCmd = &cobra.Command{
	Use:   "disable <id>",
	Short: "Disable a scheduled task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := newScheduleManager()
		disabled := false
		if err := mgr.Update(args[0], &schedule.UpdateOpts{Enabled: &disabled}); err != nil {
			return err
		}
		fmt.Printf("Disabled schedule %s\n", args[0])
		return nil
	},
}

var scheduleSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Re-sync failed/pending launchd plists",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := newScheduleManager()
		n, err := mgr.Sync()
		if err != nil {
			return err
		}
		fmt.Printf("Synced %d schedule(s)\n", n)
		return nil
	},
}

func init() {
	scheduleCreateCmd.Flags().StringVar(&schedCreateAgent, "agent", "", "Agent to run (empty for default)")
	scheduleCreateCmd.Flags().StringVar(&schedCreateCron, "cron", "", "Cron expression (5-field, supports ranges/steps/lists)")
	scheduleCreateCmd.Flags().StringVar(&schedCreatePrompt, "prompt", "", "Prompt to send")

	scheduleUpdateCmd.Flags().StringVar(&schedUpdateCron, "cron", "", "New cron expression")
	scheduleUpdateCmd.Flags().StringVar(&schedUpdatePrompt, "prompt", "", "New prompt")

	scheduleCmd.AddCommand(scheduleListCmd)
	scheduleCmd.AddCommand(scheduleCreateCmd)
	scheduleCmd.AddCommand(scheduleUpdateCmd)
	scheduleCmd.AddCommand(scheduleRemoveCmd)
	scheduleCmd.AddCommand(scheduleEnableCmd)
	scheduleCmd.AddCommand(scheduleDisableCmd)
	scheduleCmd.AddCommand(scheduleSyncCmd)
	rootCmd.AddCommand(scheduleCmd)
}
