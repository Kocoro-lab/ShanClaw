package tools

import (
	"fmt"
	"os/exec"
	"strings"
)

const minGhosttyVersion = "1.3.0"

// ghosttyAvailable checks if Ghostty.app is installed and >= 1.3.0.
func ghosttyAvailable() bool {
	out, err := exec.Command("mdfind", "kMDItemCFBundleIdentifier == 'com.mitchellh.ghostty'").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return false
	}
	appPath := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	ver, err := exec.Command("defaults", "read", appPath+"/Contents/Info.plist", "CFBundleShortVersionString").CombinedOutput()
	if err != nil {
		return false
	}
	return compareVersions(strings.TrimSpace(string(ver)), minGhosttyVersion) >= 0
}


// execGhosttyScript runs an AppleScript targeting the Ghostty application.
func execGhosttyScript(script string) (string, error) {
	var cmdArgs []string
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			cmdArgs = append(cmdArgs, "-e", trimmed)
		}
	}
	out, err := exec.Command("osascript", cmdArgs...).CombinedOutput()
	result := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("osascript error: %w\n%s", err, result)
	}
	return result, nil
}

// ghosttyNewTab opens a new tab in the frontmost Ghostty window.
// Returns the window and tab IDs as strings for later reference.
func ghosttyNewTab(command, title, color string) (windowIdx, tabIdx int, err error) {
	cmdPart := "/bin/zsh"
	if command != "" {
		escaped := strings.ReplaceAll(command, `"`, `\"`)
		cmdPart = fmt.Sprintf("/bin/zsh -c %q", escaped)
	}
	_ = cmdPart // command is sent via input text after creation

	script := `tell application "Ghostty"
	activate
	set win to front window
	set cfg to new surface configuration
	set newTab to new tab in win with configuration cfg
	set t to focused terminal of selected tab of win
end tell`
	_, err = execGhosttyScript(script)
	if err != nil {
		return 0, 0, fmt.Errorf("new_tab: %w", err)
	}

	if title != "" {
		setTabTitle(title)
	}
	if command != "" {
		sendCommand(command)
	}

	// Get tab index for registry
	idxScript := `tell application "Ghostty"
	tell front window
		set tabIdx to count of tabs
	end tell
	return tabIdx as text
end tell`
	result, err := execGhosttyScript(idxScript)
	if err != nil {
		return 1, 1, nil
	}
	fmt.Sscanf(result, "%d", &tabIdx)
	return 1, tabIdx, nil
}

// ghosttyNewSplit opens a new split in the given direction (right or down).
func ghosttyNewSplit(direction, command, title, color string) (windowIdx, tabIdx int, err error) {
	script := fmt.Sprintf(`tell application "Ghostty"
	activate
	set win to front window
	set t1 to focused terminal of selected tab of win
	set cfg to new surface configuration
	set t2 to split t1 direction %s with configuration cfg
end tell`, direction)
	_, err = execGhosttyScript(script)
	if err != nil {
		return 0, 0, fmt.Errorf("new_split: %w", err)
	}

	if title != "" {
		setTabTitle(title)
	}
	if command != "" {
		sendCommand(command)
	}

	return 1, 1, nil
}

// ghosttySendInput sends text to a specific tab by index.
func ghosttySendInput(windowIdx, tabIdx int, text string) error {
	escaped := strings.ReplaceAll(text, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	script := fmt.Sprintf(`tell application "Ghostty"
	set win to window 1
	set targetTab to tab %d of win
	set t to focused terminal of targetTab
	input text "%s" to t
end tell`, tabIdx, escaped)
	_, err := execGhosttyScript(script)
	return err
}

// setTabTitle sets the title of the current tab in the front window.
func setTabTitle(title string) {
	escaped := strings.ReplaceAll(title, `"`, `\"`)
	script := fmt.Sprintf(`tell application "Ghostty"
	tell selected tab of front window
		set title to "%s"
	end tell
end tell`, escaped)
	execGhosttyScript(script)
}

// sendCommand sends a command string + enter to the focused terminal.
func sendCommand(command string) {
	escaped := strings.ReplaceAll(command, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	script := fmt.Sprintf(`tell application "Ghostty"
	set t to focused terminal of selected tab of front window
	input text "%s" to t
	send key "enter" to t
end tell`, escaped)
	execGhosttyScript(script)
}

// SetGhosttyTabAppearance sets tab title for the current terminal.
// Exported for use from tui package. Fails silently if not in Ghostty.
func SetGhosttyTabAppearance(agentName string) {
	if agentName == "" {
		return
	}
	setTabTitle(agentName)
}

// ghosttyWorkspaceScript builds an AppleScript that opens one Ghostty window
// per agent.
func ghosttyWorkspaceScript(shanBinary string, agentNames []string) string {
	escaped := strings.ReplaceAll(shanBinary, `"`, `\"`)
	var sb strings.Builder
	sb.WriteString("tell application \"Ghostty\"\n")
	sb.WriteString("\tactivate\n")
	sb.WriteString("\tdelay 0.5\n")

	for i, name := range agentNames {
		if i > 0 {
			sb.WriteString("\tdelay 1\n")
		}
		sb.WriteString("\tset cfg to new surface configuration\n")
		sb.WriteString("\tset win to new window with configuration cfg\n")
		sb.WriteString("\tdelay 0.5\n")
		sb.WriteString("\tset t to focused terminal of selected tab of win\n")
		sb.WriteString(fmt.Sprintf("\ttell selected tab of win\n"))
		sb.WriteString(fmt.Sprintf("\t\tset title to \"%s\"\n", name))
		sb.WriteString("\tend tell\n")
		sb.WriteString(fmt.Sprintf("\tinput text \"%s --agent %s\" to t\n", escaped, name))
		sb.WriteString("\tsend key \"enter\" to t\n")
	}

	sb.WriteString("end tell")
	return sb.String()
}

// GhosttyWorkspaceScript is the exported wrapper for cmd package.
func GhosttyWorkspaceScript(shanBinary string, agentNames []string) string {
	return ghosttyWorkspaceScript(shanBinary, agentNames)
}

// GhosttyAvailable is the exported wrapper for cmd package.
func GhosttyAvailable() bool { return ghosttyAvailable() }

// ExecGhosttyScript is the exported wrapper for cmd package.
func ExecGhosttyScript(script string) error {
	_, err := execGhosttyScript(script)
	return err
}
