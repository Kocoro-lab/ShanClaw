package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// DefaultCDPPort is the default Chrome DevTools Protocol debugging port.
	DefaultCDPPort = 9223
	// LegacyCDPPort was the original hardcoded Playwright CDP endpoint.
	LegacyCDPPort = 9222
)

// cdpMu serializes all EnsureChromeDebugPort calls to prevent concurrent
// callers (boot, tool call, supervisor) from racing to launch/kill Chrome.
var cdpMu sync.Mutex

var (
	cdpExecCommand          = exec.Command
	cdpUserHomeDir          = os.UserHomeDir
	cdpSleep                = time.Sleep
	cdpChromeAliveFn        = cdpChromeAlive
	cdpChromePIDFn          = cdpChromePID
	cdpReachableFn          = IsChromeCDPReachable
	portListeningFn         = isPortListening
	ensureChromeDebugPortFn = EnsureChromeDebugPort
)

var chromeLoopbackHosts = []string{"127.0.0.1", "::1"}

// IsChromeCDPReachable checks if Chrome's CDP endpoint is responding on the given port.
// Checks both IPv4 and IPv6 — Chrome may bind to [::1] if 127.0.0.1 is already in use.
func IsChromeCDPReachable(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	for _, host := range chromeLoopbackHosts {
		resp, err := client.Get(fmt.Sprintf("http://%s/json/version", net.JoinHostPort(host, fmt.Sprintf("%d", port))))
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return true
		}
	}
	return false
}

// EnsureChromeDebugPort checks if Chrome's CDP is reachable; if not, launches
// a CDP Chrome instance (minimized). Returns nil if CDP is available after the call.
// Serialized — concurrent callers block rather than racing to launch Chrome.
func EnsureChromeDebugPort(port int) error {
	cdpMu.Lock()
	defer cdpMu.Unlock()
	if cdpReachableFn(port) {
		// Eagerly cache browser WS URL while Chrome may be idle
		// (before Playwright connects and makes HTTP endpoints flaky).
		primeCachedBrowserWS(port)
		return nil
	}
	// If the port is already in use (e.g. user's main Chrome), don't launch
	// a competing instance — Playwright connects to whatever is there.
	if portListeningFn(port) {
		log.Printf("[chrome-cdp] Port %d is occupied but CDP HTTP not available — skipping launch", port)
		// Clean up any daemon-owned Chrome that started uselessly behind
		// this port conflict. It can't bind and is just wasting resources.
		if cdpChromeAliveFn() {
			log.Printf("[chrome-cdp] Cleaning up useless daemon-owned Chrome (port conflict)")
			// Keep StopCDPChrome non-locking: the conflict cleanup path may call
			// it while EnsureChromeDebugPort already holds cdpMu.
			StopCDPChrome()
		}
		return nil
	}
	return LaunchCDPChrome(port)
}

// ShouldPreflightDedicatedChrome reports whether a browser tool invocation
// should proactively boot the daemon-owned dedicated Chrome before calling
// Playwright. This is only needed for the default dedicated port on first use,
// when the MCP server may already be connected but Chrome itself is not running yet.
func ShouldPreflightDedicatedChrome(port int) bool {
	if port != DefaultCDPPort {
		return false
	}
	if cdpChromeAliveFn() {
		return false
	}
	return !portListeningFn(port)
}

// LaunchCDPChrome launches a separate Chrome instance with a copied profile
// and --remote-debugging-port enabled. The window starts minimized to avoid
// stealing focus. The user's regular Chrome is left untouched.
// Only supported on macOS.
func LaunchCDPChrome(port int) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("Chrome CDP only supported on macOS")
	}

	home, err := cdpUserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}
	cdpDataDir := filepath.Join(home, ".shannon", "chrome-cdp")

	// If a CDP Chrome is already running with our profile, give it a few seconds
	// to respond. If it doesn't, kill it and relaunch — the CDP port may be stuck.
	if cdpChromeAliveFn() {
		log.Printf("[chrome-cdp] Chrome already running, checking CDP on port %d", port)
		for i := 0; i < 6; i++ {
			cdpSleep(500 * time.Millisecond)
			if cdpReachableFn(port) {
				return nil
			}
		}
		log.Printf("[chrome-cdp] CDP not responding, killing stale Chrome and relaunching")
		StopCDPChrome()
		// Wait for ALL Chrome processes (main + helpers) to exit before relaunching.
		// If they won't die, bail out — launching against a still-active profile
		// causes corruption.
		dead := false
		for i := 0; i < 10; i++ {
			cdpSleep(500 * time.Millisecond)
			if !cdpChromeAliveFn() {
				dead = true
				break
			}
		}
		if !dead {
			// Escalate: SIGKILL the main browser process
			if pid := cdpChromePIDFn(); pid != "" {
				log.Printf("[chrome-cdp] Chrome pid %s won't die, sending SIGKILL", pid)
				cdpExecCommand("kill", "-9", pid).Run() //nolint:errcheck
				cdpSleep(1 * time.Second)
				if cdpChromeAliveFn() {
					return fmt.Errorf("Chrome processes still alive after SIGKILL — cannot relaunch safely")
				}
			}
		}
		// Remove stale profile locks so the new instance can start cleanly
		os.Remove(filepath.Join(cdpDataDir, "SingletonLock"))
		os.Remove(filepath.Join(cdpDataDir, "SingletonSocket"))
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
	clearCachedBrowserWS()
	cmd := cdpExecCommand("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		fmt.Sprintf("--user-data-dir=%s", cdpDataDir),
		"--no-startup-window",
		"--no-first-run",
		"--no-default-browser-check",
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch Chrome: %w", err)
	}
	// Persist Chrome PID so orphaned processes can be cleaned up after a hard kill.
	writeCDPPIDFile(home, cmd.Process.Pid)
	go cmd.Wait() //nolint:errcheck

	// Wait for CDP to become reachable.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		cdpSleep(500 * time.Millisecond)
		if cdpReachableFn(port) {
			log.Printf("[chrome-cdp] Chrome CDP reachable on port %d", port)
			// Eagerly cache the browser WS URL while Chrome is freshly started
			// and no other CDP clients are connected yet.
			primeCachedBrowserWS(port)
			// Minimize after a short delay — window may not exist yet when CDP first becomes reachable.
			go func() {
				cdpSleep(2 * time.Second)
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
	if err := cdpExecCommand("osascript", "-e", script).Run(); err != nil {
		log.Printf("[chrome-cdp] minimize failed: %v", err)
	}
}

// StopCDPChrome sends SIGTERM to the CDP Chrome instance. Non-blocking — best
// effort for the signal-handler path where the daemon is about to exit anyway.
// The PID file is left intact; the next daemon startup will clean up via
// CleanupOrphanedCDPChrome if Chrome survives.
func StopCDPChrome() {
	clearCachedBrowserWS()
	home, err := cdpUserHomeDir()
	if err != nil {
		return
	}
	cdpDataDir := filepath.Join(home, ".shannon", "chrome-cdp")
	out, err := cdpExecCommand("pgrep", "-f", fmt.Sprintf("user-data-dir=%s", cdpDataDir)).Output()
	if err != nil || len(out) == 0 {
		removeCDPPIDFile(home)
		return
	}
	cdpExecCommand("pkill", "-f", fmt.Sprintf("user-data-dir=%s", cdpDataDir)).Run() //nolint:errcheck
	log.Printf("[chrome-cdp] SIGTERM sent to CDP Chrome")
}

// CleanupOrphanedCDPChrome kills any Chrome CDP processes left behind by a
// previous daemon that was hard-killed (SIGKILL). Must be called AFTER the
// daemon PID file lock is acquired — this guarantees no other daemon is alive,
// so any Chrome CDP we find is truly orphaned.
//
// Uses SIGTERM → wait → SIGKILL escalation and only removes the PID file once
// Chrome is confirmed dead.
func CleanupOrphanedCDPChrome() {
	home, err := cdpUserHomeDir()
	if err != nil {
		return
	}

	// Check if any CDP Chrome is alive — by PID file first, pgrep fallback.
	alive := false
	pidFile := filepath.Join(home, ".shannon", "chrome-cdp.pid")
	data, err := os.ReadFile(pidFile)
	if err == nil {
		pidStr := strings.TrimSpace(string(data))
		if pidStr != "" {
			if cdpExecCommand("kill", "-0", pidStr).Run() == nil {
				alive = true
			} else {
				// Stale PID file — process already dead.
				os.Remove(pidFile)
			}
		} else {
			os.Remove(pidFile)
		}
	}
	if !alive {
		// No PID file or PID is dead — fallback: check by process pattern.
		alive = cdpChromeAliveFn()
	}
	if !alive {
		return
	}

	log.Printf("[chrome-cdp] Orphaned CDP Chrome from previous run, cleaning up")

	// SIGTERM first.
	cdpDataDir := filepath.Join(home, ".shannon", "chrome-cdp")
	cdpExecCommand("pkill", "-f", fmt.Sprintf("user-data-dir=%s", cdpDataDir)).Run() //nolint:errcheck

	// Wait up to 3s for graceful exit.
	for i := 0; i < 6; i++ {
		cdpSleep(500 * time.Millisecond)
		if !cdpChromeAliveFn() {
			removeCDPPIDFile(home)
			log.Printf("[chrome-cdp] Orphaned CDP Chrome stopped")
			return
		}
	}

	// Escalate: SIGKILL the main browser process.
	if pid := cdpChromePIDFn(); pid != "" {
		log.Printf("[chrome-cdp] Chrome won't die, sending SIGKILL to pid %s", pid)
		cdpExecCommand("kill", "-9", pid).Run() //nolint:errcheck
		cdpSleep(1 * time.Second)
	}

	if !cdpChromeAliveFn() {
		removeCDPPIDFile(home)
		log.Printf("[chrome-cdp] Orphaned CDP Chrome stopped (after SIGKILL)")
	} else {
		// Don't remove PID file — preserve it for manual investigation.
		log.Printf("[chrome-cdp] WARNING: orphaned CDP Chrome still alive after SIGKILL")
	}
}

type chromeControlMode int

const (
	chromeControlModeMain chromeControlMode = iota
	chromeControlModeDedicated
)

func resolveChromeControlMode(port int) (chromeControlMode, error) {
	if !cdpChromeAliveFn() {
		return chromeControlModeMain, nil
	}
	if cdpReachableFn(port) {
		return chromeControlModeDedicated, nil
	}
	log.Printf("[chrome-cdp] Dedicated Chrome detected without reachable CDP; attempting recovery")
	if err := ensureChromeDebugPortFn(port); err != nil {
		return chromeControlModeDedicated, fmt.Errorf("recover dedicated Chrome: %w", err)
	}
	return chromeControlModeDedicated, nil
}

// ShowCDPChrome restores the browser Playwright should use.
// Dedicated daemon-owned Chrome is always controlled via CDP.
// Main Chrome is only controlled when no daemon-owned Chrome exists.
func ShowCDPChrome() error {
	return ShowCDPChromeOnPort(DefaultCDPPort)
}

// ShowCDPChromeOnPort restores the browser Playwright should use for the
// configured CDP port.
func ShowCDPChromeOnPort(port int) error {
	mode, err := resolveChromeControlMode(port)
	if err != nil {
		return fmt.Errorf("show chrome: %w", err)
	}
	if mode == chromeControlModeDedicated {
		return showViaCDP(port)
	}
	return showMainChrome()
}

// HideCDPChrome minimizes the browser Playwright should use.
func HideCDPChrome() error {
	return HideCDPChromeOnPort(DefaultCDPPort)
}

// HideCDPChromeOnPort minimizes the browser Playwright should use for the
// configured CDP port.
func HideCDPChromeOnPort(port int) error {
	mode, err := resolveChromeControlMode(port)
	if err != nil {
		return fmt.Errorf("hide chrome: %w", err)
	}
	if mode == chromeControlModeDedicated {
		return hideViaCDP(port)
	}
	return hideMainChrome()
}

// CDPChromeStatus describes the state of the browser Playwright is using.
type CDPChromeStatus struct {
	Running    bool
	Visible    bool
	ProbeError bool // true if visibility could not be determined
}

// GetCDPChromeStatus queries the browser state.
func GetCDPChromeStatus() CDPChromeStatus {
	return GetCDPChromeStatusOnPort(DefaultCDPPort)
}

// GetCDPChromeStatusOnPort queries the browser state for the configured CDP port.
func GetCDPChromeStatusOnPort(port int) CDPChromeStatus {
	mode, err := resolveChromeControlMode(port)
	if err != nil {
		return CDPChromeStatus{Running: cdpChromeAliveFn(), ProbeError: true}
	}
	if mode == chromeControlModeDedicated {
		return getStatusViaCDP(port)
	}
	return getStatusMainChrome()
}

// --- CDP mode (dedicated instance) ---

func showViaCDP(port int) error {
	targets, err := getAllCDPPageTargets(port)
	if err != nil || len(targets) == 0 {
		newID, createErr := createCDPTarget(port)
		if createErr != nil {
			return fmt.Errorf("show chrome: %w", createErr)
		}
		targets = []string{newID}
	}
	windowIDs, err := getUniqueWindowIDs(port, targets)
	if err != nil {
		return fmt.Errorf("show chrome: %w", err)
	}
	var restored int
	for _, wid := range windowIDs {
		if err := setWindowBoundsByID(port, wid, "normal"); err == nil {
			restored++
		}
	}
	if restored == 0 {
		return fmt.Errorf("show chrome: failed to restore any window")
	}
	if _, err := cdpBrowserCall(port, "Target.activateTarget", map[string]interface{}{
		"targetId": targets[0],
	}); err != nil {
		log.Printf("[chrome-cdp] activateTarget failed: %v", err)
	}
	return nil
}

func hideViaCDP(port int) error {
	targets, err := getAllCDPPageTargets(port)
	if err != nil || len(targets) == 0 {
		return nil
	}
	windowIDs, err := getUniqueWindowIDs(port, targets)
	if err != nil {
		return fmt.Errorf("hide chrome: %w", err)
	}
	var minimized int
	for _, wid := range windowIDs {
		if err := setWindowBoundsByID(port, wid, "minimized"); err == nil {
			minimized++
		}
	}
	if minimized == 0 {
		return fmt.Errorf("hide chrome: failed to minimize any window")
	}
	return nil
}

func getStatusViaCDP(port int) CDPChromeStatus {
	targets, err := getAllCDPPageTargets(port)
	if err != nil || len(targets) == 0 {
		return CDPChromeStatus{Running: true, Visible: false}
	}
	windowIDs, err := getUniqueWindowIDs(port, targets)
	if err != nil {
		return CDPChromeStatus{Running: true, ProbeError: true}
	}
	var probed int
	for _, wid := range windowIDs {
		state, err := getWindowStatByID(port, wid)
		if err != nil {
			continue
		}
		probed++
		if state != "minimized" {
			return CDPChromeStatus{Running: true, Visible: true}
		}
	}
	if probed == 0 {
		return CDPChromeStatus{Running: true, ProbeError: true}
	}
	return CDPChromeStatus{Running: true, Visible: false}
}

// --- Main Chrome mode (Playwright using user's browser) ---

func showMainChrome() error {
	running, _, err := mainChromeVisibility()
	if err != nil {
		return fmt.Errorf("show chrome: %w", err)
	}
	if !running {
		return ErrChromeNotRunning
	}
	script := `
tell application "Google Chrome"
	activate
end tell`
	if err := cdpExecCommand("osascript", "-e", script).Run(); err != nil {
		return fmt.Errorf("activate Chrome: %w", err)
	}
	return nil
}

func hideMainChrome() error {
	running, _, err := mainChromeVisibility()
	if err != nil {
		return fmt.Errorf("hide chrome: %w", err)
	}
	if !running {
		return ErrChromeNotRunning
	}
	script := `
tell application "System Events"
	try
		set visible of process "Google Chrome" to false
	end try
end tell`
	if err := cdpExecCommand("osascript", "-e", script).Run(); err != nil {
		return fmt.Errorf("hide Chrome: %w", err)
	}
	return nil
}

func getStatusMainChrome() CDPChromeStatus {
	running, visible, err := mainChromeVisibility()
	if err != nil {
		return CDPChromeStatus{ProbeError: true}
	}
	if !running {
		return CDPChromeStatus{}
	}
	return CDPChromeStatus{Running: true, Visible: visible}
}

func mainChromeVisibility() (bool, bool, error) {
	script := `
tell application "System Events"
	try
		if exists process "Google Chrome" then
			return visible of process "Google Chrome"
		else
			return "NOT_RUNNING"
		end if
	on error
		return "ERROR"
	end try
end tell`
	out, err := cdpExecCommand("osascript", "-e", script).Output()
	if err != nil {
		return false, false, err
	}
	result := strings.TrimSpace(string(out))
	switch result {
	case "NOT_RUNNING":
		return false, false, nil
	case "ERROR":
		return true, false, fmt.Errorf("chrome visibility probe failed")
	case "true":
		return true, true, nil
	case "false":
		return true, false, nil
	default:
		return false, false, fmt.Errorf("unexpected chrome visibility result: %q", result)
	}
}

// isPortListening checks if something is listening on the given TCP port.
func isPortListening(port int) bool {
	for _, host := range chromeLoopbackHosts {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), 1*time.Second)
		if err != nil {
			continue
		}
		conn.Close()
		return true
	}
	return false
}

// ErrChromeNotRunning indicates the CDP Chrome process is not running.
var ErrChromeNotRunning = fmt.Errorf("chrome CDP not running")

// --- CDP window control helpers ---

var (
	cdpMsgID        atomic.Int64
	cachedBrowserWS string
	cachedBrowserMu sync.Mutex
)

type persistedBrowserWS struct {
	PID   string `json:"pid,omitempty"`
	WSURL string `json:"ws_url"`
}

func browserWSCachePath(home string) string {
	return filepath.Join(home, ".shannon", "chrome-cdp-ws.url")
}

// clearCachedBrowserWS resets the cached WebSocket URL (call when Chrome restarts).
func clearCachedBrowserWS() {
	cachedBrowserMu.Lock()
	cachedBrowserWS = ""
	cachedBrowserMu.Unlock()
	// Also remove the persisted file.
	if home, err := cdpUserHomeDir(); err == nil {
		os.Remove(browserWSCachePath(home))
	}
}

func primeCachedBrowserWS(port int) {
	url, err := getCDPBrowserWSURL(port)
	if err != nil {
		log.Printf("[chrome-cdp] failed to cache browser WS URL: %v", err)
		return
	}
	persistBrowserWS(url)
}

func persistBrowserWS(url string) {
	home, err := cdpUserHomeDir()
	if err != nil {
		return
	}
	payload, err := json.Marshal(persistedBrowserWS{
		PID:   cdpChromePIDFn(),
		WSURL: url,
	})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Join(home, ".shannon"), 0o700); err != nil {
		return
	}
	os.WriteFile(browserWSCachePath(home), payload, 0o600) //nolint:errcheck
}

func loadPersistedBrowserWS() (string, error) {
	home, err := cdpUserHomeDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(browserWSCachePath(home))
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return "", fmt.Errorf("empty persisted browser WS URL")
	}

	// Legacy format: plain URL string.
	if !strings.HasPrefix(raw, "{") {
		return raw, nil
	}

	var persisted persistedBrowserWS
	if err := json.Unmarshal(data, &persisted); err != nil {
		return "", err
	}
	if persisted.WSURL == "" {
		return "", fmt.Errorf("persisted browser WS URL missing ws_url")
	}
	if persisted.PID != "" {
		currentPID := cdpChromePIDFn()
		if currentPID == "" || currentPID != persisted.PID {
			return "", fmt.Errorf("persisted browser WS URL pid mismatch")
		}
	}
	return persisted.WSURL, nil
}

// getAllCDPPageTargets returns IDs of all "page" targets.
// Tries HTTP /json/list first, falls back to CDP Target.getTargets via
// WebSocket when HTTP is flaky (common when Playwright is connected).
func getAllCDPPageTargets(port int) ([]string, error) {
	// Try HTTP first (fast, no WS overhead).
	ids, err := httpListPageTargets(port)
	if err == nil && len(ids) > 0 {
		return ids, nil
	}
	// Fallback: use CDP WebSocket.
	return wsListPageTargets(port)
}

func httpListPageTargets(port int) ([]string, error) {
	transport := &http.Transport{DisableKeepAlives: true}
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readAllLimited(resp.Body, 256*1024)
	if err != nil || len(body) == 0 {
		return nil, fmt.Errorf("empty /json/list response")
	}
	var targets []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &targets); err != nil {
		return nil, err
	}
	var ids []string
	for _, t := range targets {
		if t.Type == "page" {
			ids = append(ids, t.ID)
		}
	}
	return ids, nil
}

func wsListPageTargets(port int) ([]string, error) {
	result, err := cdpBrowserCall(port, "Target.getTargets", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	var ids []string
	for _, t := range resp.TargetInfos {
		if t.Type == "page" {
			ids = append(ids, t.TargetID)
		}
	}
	return ids, nil
}

// createCDPTarget creates a new blank page and returns its target ID.
// Prefer the HTTP endpoint first — it works without a browser-level WS URL.
// Fall back to Target.createTarget over WebSocket when HTTP is unavailable.
func createCDPTarget(port int) (string, error) {
	id, httpErr := httpCreateCDPTarget(port)
	if httpErr == nil {
		return id, nil
	}
	id, wsErr := wsCreateCDPTarget(port)
	if wsErr == nil {
		return id, nil
	}
	return "", fmt.Errorf("http create: %v; ws create: %w", httpErr, wsErr)
}

func httpCreateCDPTarget(port int) (string, error) {
	transport := &http.Transport{DisableKeepAlives: true}
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/new?about:blank", port), nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := readAllLimited(resp.Body, 64*1024)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if len(body) == 0 {
		return "", fmt.Errorf("empty response")
	}
	var respBody struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &respBody); err != nil {
		return "", fmt.Errorf("parse id: %w", err)
	}
	if respBody.ID == "" {
		return "", fmt.Errorf("missing id")
	}
	return respBody.ID, nil
}

func wsCreateCDPTarget(port int) (string, error) {
	result, err := cdpBrowserCall(port, "Target.createTarget", map[string]interface{}{
		"url": "about:blank",
	})
	if err != nil {
		return "", fmt.Errorf("Target.createTarget: %w", err)
	}
	var resp struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", fmt.Errorf("parse targetId: %w", err)
	}
	return resp.TargetID, nil
}

// getUniqueWindowIDs maps target IDs to unique window IDs.
func getUniqueWindowIDs(port int, targetIDs []string) ([]int, error) {
	seen := make(map[int]bool)
	var windowIDs []int
	for _, tid := range targetIDs {
		result, err := cdpBrowserCall(port, "Browser.getWindowForTarget", map[string]interface{}{
			"targetId": tid,
		})
		if err != nil {
			continue
		}
		var win struct {
			WindowID int `json:"windowId"`
		}
		if err := json.Unmarshal(result, &win); err != nil {
			continue
		}
		if !seen[win.WindowID] {
			seen[win.WindowID] = true
			windowIDs = append(windowIDs, win.WindowID)
		}
	}
	if len(windowIDs) == 0 {
		return nil, fmt.Errorf("no windows found")
	}
	return windowIDs, nil
}

// setWindowBoundsByID sets the window state for a specific window ID.
func setWindowBoundsByID(port, windowID int, state string) error {
	_, err := cdpBrowserCall(port, "Browser.setWindowBounds", map[string]interface{}{
		"windowId": windowID,
		"bounds":   map[string]interface{}{"windowState": state},
	})
	return err
}

// getWindowStatByID returns the window state for a specific window ID.
func getWindowStatByID(port, windowID int) (string, error) {
	result, err := cdpBrowserCall(port, "Browser.getWindowBounds", map[string]interface{}{
		"windowId": windowID,
	})
	if err != nil {
		return "", err
	}
	var bounds struct {
		Bounds struct {
			WindowState string `json:"windowState"`
		} `json:"bounds"`
	}
	if err := json.Unmarshal(result, &bounds); err != nil {
		return "", err
	}
	return bounds.Bounds.WindowState, nil
}

// getCDPBrowserWSURL returns the browser-level WebSocket debugger URL.
// Resolution order: memory cache → persisted file → HTTP fetch (with retry).
// The URL is stable for the Chrome process lifetime.
func getCDPBrowserWSURL(port int) (string, error) {
	// 1. Memory cache.
	cachedBrowserMu.Lock()
	cached := cachedBrowserWS
	cachedBrowserMu.Unlock()
	if cached != "" {
		return cached, nil
	}

	// 2. Persisted file (survives daemon restarts).
	if wsURL, err := loadPersistedBrowserWS(); err == nil {
		cachedBrowserMu.Lock()
		cachedBrowserWS = wsURL
		cachedBrowserMu.Unlock()
		return wsURL, nil
	} else if !os.IsNotExist(err) {
		log.Printf("[chrome-cdp] ignoring persisted browser WS URL: %v", err)
		clearCachedBrowserWS()
	}

	// 3. HTTP fetch with retry — may fail when Playwright is connected.
	var wsURL string
	for attempt := 0; attempt < 2; attempt++ {
		url, err := fetchBrowserWSURL(port)
		if err == nil {
			wsURL = url
			break
		}
		if attempt == 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if wsURL == "" {
		return "", fmt.Errorf("failed to get browser WS URL after retries")
	}

	cachedBrowserMu.Lock()
	cachedBrowserWS = wsURL
	cachedBrowserMu.Unlock()
	persistBrowserWS(wsURL)
	return wsURL, nil
}

func fetchBrowserWSURL(port int) (string, error) {
	// Use a short timeout and disable keep-alive — Chrome's DevTools HTTP
	// server can stall when Playwright has an active WebSocket session.
	transport := &http.Transport{
		DisableKeepAlives: true,
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// Read full body first — streaming decode can return EOF on partial responses.
	body, err := readAllLimited(resp.Body, 64*1024)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if len(body) == 0 {
		return "", fmt.Errorf("empty response")
	}
	var info struct {
		WSURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("parse json: %w", err)
	}
	if info.WSURL == "" {
		return "", fmt.Errorf("no webSocketDebuggerUrl")
	}
	return info.WSURL, nil
}

func readAllLimited(r interface{ Read([]byte) (int, error) }, limit int) ([]byte, error) {
	lr := &io.LimitedReader{R: r, N: int64(limit) + 1}
	body, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return body, nil
}

// cdpBrowserCall sends a single CDP Browser domain command and returns the result.
func cdpBrowserCall(port int, method string, params map[string]interface{}) (json.RawMessage, error) {
	conn, err := dialCDPBrowser(port)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	id := cdpMsgID.Add(1)
	msg := map[string]interface{}{
		"id":     id,
		"method": method,
	}
	if params != nil {
		msg["params"] = params
	}
	if err := conn.WriteJSON(msg); err != nil {
		return nil, fmt.Errorf("ws write: %w", err)
	}

	// Read responses until we get ours (skip events).
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("ws read: %w", err)
		}
		var resp struct {
			ID     int64            `json:"id"`
			Result json.RawMessage  `json:"result"`
			Error  *json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			continue // skip malformed
		}
		if resp.ID != id {
			continue // skip events / other responses
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("cdp error: %s", string(*resp.Error))
		}
		return resp.Result, nil
	}
}

func dialCDPBrowser(port int) (*websocket.Conn, error) {
	wsURL, err := getCDPBrowserWSURL(port)
	if err != nil {
		return nil, fmt.Errorf("get ws url: %w", err)
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		return conn, nil
	}

	log.Printf("[chrome-cdp] browser WS dial failed, invalidating cached URL and retrying: %v", err)
	clearCachedBrowserWS()

	wsURL, err = getCDPBrowserWSURL(port)
	if err != nil {
		return nil, fmt.Errorf("get ws url retry: %w", err)
	}
	conn, _, err = websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ws dial retry: %w", err)
	}
	return conn, nil
}

// BringCDPChromeToFront unminimizes and activates the CDP Chrome.
// Runs asynchronously to avoid blocking tool calls.
// Deprecated: use ShowCDPChrome() for synchronous control.
func BringCDPChromeToFront() {
	go func() { ShowCDPChrome() }()
}

// CDPChromePID returns the PID of the CDP Chrome main process, or "" if not running.
func CDPChromePID() string {
	return cdpChromePID()
}

// cdpChromeAlive returns true if any process (main or helper) is still running
// with our CDP user-data-dir. Used for shutdown/relaunch safety — ensures all
// Chrome processes have exited before relaunching against the same profile.
func cdpChromeAlive() bool {
	home, err := cdpUserHomeDir()
	if err != nil {
		return false
	}
	cdpDataDir := filepath.Join(home, ".shannon", "chrome-cdp")
	out, err := cdpExecCommand("pgrep", "-f", fmt.Sprintf("user-data-dir=%s", cdpDataDir)).Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// cdpChromePID returns the PID of the CDP Chrome main browser process, or "" if not running.
// Filters out Chrome Helper subprocesses which share the same --user-data-dir flag.
// Use for window management (front/hide/minimize) and targeted force-kill.
func cdpChromePID() string {
	home, err := cdpUserHomeDir()
	if err != nil {
		return ""
	}
	cdpDataDir := filepath.Join(home, ".shannon", "chrome-cdp")
	out, err := cdpExecCommand("pgrep", "-f", fmt.Sprintf("user-data-dir=%s", cdpDataDir)).Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	// pgrep returns all matching PIDs (main + helpers). Find the main browser
	// process by checking each PID's command — helpers contain "Helper" in path.
	for _, pid := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		cmdOut, err := cdpExecCommand("ps", "-p", pid, "-o", "command=").Output()
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(string(cmdOut))
		if strings.Contains(cmd, "Helper") || strings.Contains(cmd, "--type=") {
			continue // skip renderer, GPU, network, storage helpers
		}
		return pid
	}
	return ""
}

// cdpPIDFilePath returns the path to the Chrome CDP PID file.
func cdpPIDFilePath(home string) string {
	return filepath.Join(home, ".shannon", "chrome-cdp.pid")
}

// writeCDPPIDFile records the Chrome main process PID so it can be cleaned up
// after a hard kill.
func writeCDPPIDFile(home string, pid int) {
	path := cdpPIDFilePath(home)
	os.WriteFile(path, []byte(fmt.Sprintf("%d\n", pid)), 0600) //nolint:errcheck
}

// removeCDPPIDFile removes the Chrome CDP PID file.
func removeCDPPIDFile(home string) {
	os.Remove(cdpPIDFilePath(home))
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
