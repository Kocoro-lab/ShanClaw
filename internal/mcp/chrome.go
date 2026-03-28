package mcp

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultCDPPort is the default Chrome DevTools Protocol debugging port.
	DefaultCDPPort = 9222
)

// cdpMu serializes all EnsureChromeDebugPort calls to prevent concurrent
// callers (boot, tool call, supervisor) from racing to launch/kill Chrome.
var cdpMu sync.Mutex

// IsChromeCDPReachable checks if Chrome's CDP endpoint is responding on the given port.
func IsChromeCDPReachable(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/json/version", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// EnsureChromeDebugPort checks if Chrome's CDP is reachable; if not, launches
// a CDP Chrome instance (minimized). Returns nil if CDP is available after the call.
// Serialized — concurrent callers block rather than racing to launch Chrome.
func EnsureChromeDebugPort(port int) error {
	cdpMu.Lock()
	defer cdpMu.Unlock()
	if IsChromeCDPReachable(port) {
		return nil
	}
	return LaunchCDPChrome(port)
}

// LaunchCDPChrome launches a separate Chrome instance with a copied profile
// and --remote-debugging-port enabled. The window starts minimized to avoid
// stealing focus. The user's regular Chrome is left untouched.
// Only supported on macOS.
func LaunchCDPChrome(port int) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("Chrome CDP only supported on macOS")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}
	cdpDataDir := filepath.Join(home, ".shannon", "chrome-cdp")

	// If a CDP Chrome is already running with our profile, give it a few seconds
	// to respond. If it doesn't, kill it and relaunch — the CDP port may be stuck.
	if cdpChromePID() != "" {
		log.Printf("[chrome-cdp] Chrome already running, checking CDP on port %d", port)
		for i := 0; i < 6; i++ {
			time.Sleep(500 * time.Millisecond)
			if IsChromeCDPReachable(port) {
				return nil
			}
		}
		log.Printf("[chrome-cdp] CDP not responding, killing stale Chrome and relaunching")
		StopCDPChrome()
		// Wait for Chrome to fully exit and release profile locks
		for i := 0; i < 10; i++ {
			time.Sleep(500 * time.Millisecond)
			if cdpChromePID() == "" {
				break
			}
		}
	}

	// Only seed the CDP profile on first launch — copying into an existing
	// profile while Chrome is running can corrupt lock files.
	cookiesPath := filepath.Join(cdpDataDir, "Default", "Cookies")
	if _, err := os.Stat(cookiesPath); err != nil {
		srcProfile := filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
		if err := prepareCDPProfile(srcProfile, cdpDataDir); err != nil {
			return fmt.Errorf("failed to prepare CDP profile: %w", err)
		}
	}

	log.Printf("[chrome-cdp] Launching CDP Chrome minimized (port %d)", port)
	cmd := exec.Command("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		fmt.Sprintf("--user-data-dir=%s", cdpDataDir),
		"--window-position=-10000,-10000",
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch Chrome: %w", err)
	}
	go cmd.Wait() //nolint:errcheck

	// Wait for CDP to become reachable.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if IsChromeCDPReachable(port) {
			log.Printf("[chrome-cdp] Chrome CDP reachable on port %d", port)
			// Minimize after a short delay — window may not exist yet when CDP first becomes reachable.
			go func() {
				time.Sleep(2 * time.Second)
				minimizeCDPChromeSync()
			}()
			return nil
		}
	}

	return fmt.Errorf("Chrome launched but CDP not reachable on port %d after 15s", port)
}

// minimizeCDPChromeSync minimizes the CDP Chrome windows using its PID.
// Runs synchronously — call from the launch flow after Chrome is ready.
func minimizeCDPChromeSync() {
	pid := cdpChromePID()
	if pid == "" {
		return
	}
	script := fmt.Sprintf(`
tell application "System Events"
	try
		set p to first process whose unix id is %s
		repeat with w in (every window of p)
			set miniaturized of w to true
		end repeat
	end try
end tell`, pid)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		log.Printf("[chrome-cdp] minimize failed: %v", err)
	}
}

// StopCDPChrome kills the CDP Chrome instance (identified by its user-data-dir).
// Called on daemon shutdown to avoid orphaned Chrome processes.
func StopCDPChrome() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	cdpDataDir := filepath.Join(home, ".shannon", "chrome-cdp")
	// Find and kill Chrome processes using our CDP profile
	out, err := exec.Command("pgrep", "-f", fmt.Sprintf("user-data-dir=%s", cdpDataDir)).Output()
	if err != nil || len(out) == 0 {
		return
	}
	// Kill the main Chrome process — child helpers will exit automatically
	exec.Command("pkill", "-f", fmt.Sprintf("user-data-dir=%s", cdpDataDir)).Run() //nolint:errcheck
	log.Printf("[chrome-cdp] CDP Chrome stopped")
}

// BringCDPChromeToFront unminimizes and activates the CDP Chrome.
// Runs asynchronously to avoid blocking tool calls.
func BringCDPChromeToFront() {
	go func() {
		pid := cdpChromePID()
		if pid == "" {
			return
		}
		script := fmt.Sprintf(`
tell application "System Events"
	try
		set p to first process whose unix id is %s
		repeat with w in (every window of p)
			set miniaturized of w to false
		end repeat
		set frontmost of p to true
	end try
end tell`, pid)
		exec.Command("osascript", "-e", script).Run() //nolint:errcheck
	}()
}

// cdpChromePID returns the PID of the CDP Chrome main process, or "" if not running.
func cdpChromePID() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	cdpDataDir := filepath.Join(home, ".shannon", "chrome-cdp")
	// Match by user-data-dir to avoid matching user-launched CDP Chrome instances.
	out, err := exec.Command("pgrep", "-f", fmt.Sprintf("user-data-dir=%s", cdpDataDir)).Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	return strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
}

// prepareCDPProfile creates a Chrome user-data-dir for CDP by copying key
// session files from the user's real Chrome profile.
func prepareCDPProfile(srcProfile, cdpDir string) error {
	defaultSrc := filepath.Join(srcProfile, "Default")
	defaultDst := filepath.Join(cdpDir, "Default")

	if err := os.MkdirAll(defaultDst, 0700); err != nil {
		return err
	}

	if err := copyFile(filepath.Join(srcProfile, "Local State"), filepath.Join(cdpDir, "Local State")); err != nil {
		log.Printf("[chrome-cdp] failed to copy Local State: %v", err)
	}

	// Critical files are logged on failure; others are best-effort.
	criticalFiles := map[string]bool{
		"Cookies":    true,
		"Login Data": true,
	}
	sessionFiles := []string{
		"Cookies",
		"Login Data",
		"Web Data",
		"Preferences",
		"Secure Preferences",
		"Network/Cookies",
		"Network/TransportSecurity",
	}
	for _, f := range sessionFiles {
		src := filepath.Join(defaultSrc, f)
		dst := filepath.Join(defaultDst, f)
		os.MkdirAll(filepath.Dir(dst), 0700) //nolint:errcheck
		if err := copyFile(src, dst); err != nil && criticalFiles[f] {
			log.Printf("[chrome-cdp] failed to copy critical file %s: %v", f, err)
		}
	}

	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0600)
}
