package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeChromeExec struct {
	mu              sync.Mutex
	calls           []string
	kill0Live       map[string]bool
	kill9OK         bool
	pgrepOK         bool
	pgrepOutput     string
	osascriptOutput []string
}

func (f *fakeChromeExec) command(name string, args ...string) *exec.Cmd {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	f.mu.Unlock()

	switch name {
	case "kill":
		if len(args) >= 2 && args[0] == "-0" {
			if f.kill0Live[args[1]] {
				return successCmd()
			}
			return failureCmd()
		}
		if len(args) >= 2 && args[0] == "-9" {
			if f.kill9OK {
				return successCmd()
			}
			return failureCmd()
		}
		return successCmd()
	case "pkill":
		return successCmd()
	case "pgrep":
		if f.pgrepOK {
			return outputCmd(f.pgrepOutput)
		}
		return failureCmd()
	case "osascript":
		if len(f.osascriptOutput) > 0 {
			out := f.osascriptOutput[0]
			f.osascriptOutput = f.osascriptOutput[1:]
			return outputCmd(out)
		}
		return successCmd()
	default:
		return successCmd()
	}
}

func (f *fakeChromeExec) snapshotCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func successCmd() *exec.Cmd {
	return exec.Command("/bin/sh", "-c", "exit 0")
}

func failureCmd() *exec.Cmd {
	return exec.Command("/bin/sh", "-c", "exit 1")
}

func outputCmd(output string) *exec.Cmd {
	return exec.Command("/bin/echo", "-n", output)
}

func installChromeTestHooks(t *testing.T, home string, execFn func(string, ...string) *exec.Cmd, aliveFn func() bool, pidFn func() string) {
	t.Helper()

	oldExec := cdpExecCommand
	oldHome := cdpUserHomeDir
	oldSleep := cdpSleep
	oldAlive := cdpChromeAliveFn
	oldPID := cdpChromePIDFn
	oldReachable := cdpReachableFn
	oldListening := portListeningFn
	oldEnsure := ensureChromeDebugPortFn

	cdpExecCommand = execFn
	cdpUserHomeDir = func() (string, error) { return home, nil }
	cdpSleep = func(time.Duration) {}
	cdpChromeAliveFn = aliveFn
	cdpChromePIDFn = pidFn
	cdpReachableFn = func(int) bool { return false }
	portListeningFn = func(int) bool { return false }
	ensureChromeDebugPortFn = EnsureChromeDebugPort

	t.Cleanup(func() {
		cdpExecCommand = oldExec
		cdpUserHomeDir = oldHome
		cdpSleep = oldSleep
		cdpChromeAliveFn = oldAlive
		cdpChromePIDFn = oldPID
		cdpReachableFn = oldReachable
		portListeningFn = oldListening
		ensureChromeDebugPortFn = oldEnsure
	})
}

func writeTestCDPPIDFile(t *testing.T, home, pid string) string {
	t.Helper()
	shannonDir := filepath.Join(home, ".shannon")
	if err := os.MkdirAll(shannonDir, 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	path := filepath.Join(shannonDir, "chrome-cdp.pid")
	if err := os.WriteFile(path, []byte(pid+"\n"), 0o600); err != nil {
		t.Fatalf("write pid file failed: %v", err)
	}
	return path
}

func aliveSequence(values ...bool) func() bool {
	var mu sync.Mutex
	idx := 0
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		if idx >= len(values) {
			if len(values) == 0 {
				return false
			}
			return values[len(values)-1]
		}
		v := values[idx]
		idx++
		return v
	}
}

func TestStopCDPChromeRemovesPIDFileWhenChromeNotRunning(t *testing.T) {
	home := t.TempDir()
	pidPath := writeTestCDPPIDFile(t, home, "123")
	runner := &fakeChromeExec{}

	installChromeTestHooks(t, home, runner.command, func() bool { return false }, func() string { return "" })

	StopCDPChrome()

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("expected pid file to be removed, got err=%v", err)
	}

	calls := runner.snapshotCalls()
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "pgrep ") {
		t.Fatalf("expected only pgrep call, got %v", calls)
	}
}

func TestCleanupOrphanedCDPChromeRemovesStalePIDFile(t *testing.T) {
	home := t.TempDir()
	pidPath := writeTestCDPPIDFile(t, home, "123")
	runner := &fakeChromeExec{
		kill0Live: map[string]bool{},
		kill9OK:   true,
	}

	installChromeTestHooks(t, home, runner.command, func() bool { return false }, func() string { return "" })

	CleanupOrphanedCDPChrome()

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale pid file to be removed, got err=%v", err)
	}

	for _, call := range runner.snapshotCalls() {
		if strings.HasPrefix(call, "pkill ") || strings.HasPrefix(call, "kill -9 ") {
			t.Fatalf("expected no cleanup signals for stale pid file, got %v", runner.snapshotCalls())
		}
	}
}

func TestCleanupOrphanedCDPChromeEscalatesAndRemovesPIDFile(t *testing.T) {
	home := t.TempDir()
	pidPath := writeTestCDPPIDFile(t, home, "123")
	runner := &fakeChromeExec{
		kill0Live: map[string]bool{"123": true},
		kill9OK:   true,
	}

	installChromeTestHooks(
		t,
		home,
		runner.command,
		aliveSequence(true, true, true, true, true, true, false),
		func() string { return "123" },
	)

	CleanupOrphanedCDPChrome()

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("expected pid file to be removed after successful cleanup, got err=%v", err)
	}

	calls := runner.snapshotCalls()
	if !containsCall(calls, "pkill -f user-data-dir="+filepath.Join(home, ".shannon", "chrome-cdp")) {
		t.Fatalf("expected SIGTERM cleanup call, got %v", calls)
	}
	if !containsCall(calls, "kill -9 123") {
		t.Fatalf("expected SIGKILL escalation, got %v", calls)
	}
}

func TestCleanupOrphanedCDPChromeFallsBackWithoutPIDFile(t *testing.T) {
	home := t.TempDir()
	runner := &fakeChromeExec{
		kill0Live: map[string]bool{},
		kill9OK:   true,
	}

	installChromeTestHooks(
		t,
		home,
		runner.command,
		aliveSequence(true, false),
		func() string { return "" },
	)

	CleanupOrphanedCDPChrome()

	calls := runner.snapshotCalls()
	if containsPrefix(calls, "kill -0 ") {
		t.Fatalf("expected no kill -0 probe without pid file, got %v", calls)
	}
	if !containsPrefix(calls, "pkill ") {
		t.Fatalf("expected fallback cleanup to send SIGTERM, got %v", calls)
	}
	if containsPrefix(calls, "kill -9 ") {
		t.Fatalf("expected no SIGKILL when SIGTERM cleanup succeeds, got %v", calls)
	}
}

func TestCleanupOrphanedCDPChromePreservesPIDFileWhenChromeSurvivesSigKill(t *testing.T) {
	home := t.TempDir()
	pidPath := writeTestCDPPIDFile(t, home, "123")
	runner := &fakeChromeExec{
		kill0Live: map[string]bool{"123": true},
		kill9OK:   true,
	}

	installChromeTestHooks(
		t,
		home,
		runner.command,
		aliveSequence(true, true, true, true, true, true, true),
		func() string { return "123" },
	)

	CleanupOrphanedCDPChrome()

	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("expected pid file to be preserved for investigation, got err=%v", err)
	}

	if !containsCall(runner.snapshotCalls(), "kill -9 123") {
		t.Fatalf("expected SIGKILL attempt, got %v", runner.snapshotCalls())
	}
}

func TestCreateCDPTargetUsesHTTPGet(t *testing.T) {
	clearCachedBrowserWS()
	t.Cleanup(clearCachedBrowserWS)

	var gotMethod string
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"tab-123"}`))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	_, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	targetID, err := createCDPTarget(port)
	if err != nil {
		t.Fatalf("createCDPTarget returned error: %v", err)
	}
	if targetID != "tab-123" {
		t.Fatalf("expected target id tab-123, got %q", targetID)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("expected GET /json/new, got %s %s", gotMethod, gotPath)
	}
	if gotPath != "/json/new?about:blank" {
		t.Fatalf("expected /json/new?about:blank, got %s", gotPath)
	}
}

func TestGetCDPBrowserWSURLReadsPersistedValue(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".shannon"), 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	data, err := json.Marshal(persistedBrowserWS{
		PID:   "123",
		WSURL: "ws://persisted.example/devtools/browser/abc",
	})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if err := os.WriteFile(browserWSCachePath(home), data, 0o600); err != nil {
		t.Fatalf("write persisted ws url failed: %v", err)
	}

	installChromeTestHooks(t, home, successCmdFn, func() bool { return true }, func() string { return "123" })
	resetBrowserWSMemoryCache(t)

	wsURL, err := getCDPBrowserWSURL(9222)
	if err != nil {
		t.Fatalf("getCDPBrowserWSURL returned error: %v", err)
	}
	if wsURL != "ws://persisted.example/devtools/browser/abc" {
		t.Fatalf("expected persisted ws url, got %q", wsURL)
	}
}

func TestGetCDPBrowserWSURLIgnoresStalePersistedValueAndFetchesFresh(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".shannon"), 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	data, err := json.Marshal(persistedBrowserWS{
		PID:   "old-pid",
		WSURL: "ws://stale.example/devtools/browser/old",
	})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if err := os.WriteFile(browserWSCachePath(home), data, 0o600); err != nil {
		t.Fatalf("write persisted ws url failed: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"webSocketDebuggerUrl":"ws://fresh.example/devtools/browser/new"}`))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	_, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	installChromeTestHooks(t, home, successCmdFn, func() bool { return true }, func() string { return "new-pid" })
	resetBrowserWSMemoryCache(t)

	wsURL, err := getCDPBrowserWSURL(port)
	if err != nil {
		t.Fatalf("getCDPBrowserWSURL returned error: %v", err)
	}
	if wsURL != "ws://fresh.example/devtools/browser/new" {
		t.Fatalf("expected fresh ws url, got %q", wsURL)
	}

	saved, err := os.ReadFile(browserWSCachePath(home))
	if err != nil {
		t.Fatalf("read persisted ws url failed: %v", err)
	}
	var persisted persistedBrowserWS
	if err := json.Unmarshal(saved, &persisted); err != nil {
		t.Fatalf("unmarshal persisted ws url failed: %v", err)
	}
	if persisted.PID != "new-pid" || persisted.WSURL != "ws://fresh.example/devtools/browser/new" {
		t.Fatalf("expected refreshed persisted ws url, got %+v", persisted)
	}
}

func TestShouldPreflightDedicatedChrome(t *testing.T) {
	home := t.TempDir()
	runner := &fakeChromeExec{}
	installChromeTestHooks(t, home, runner.command, func() bool { return false }, func() string { return "" })

	if !ShouldPreflightDedicatedChrome(DefaultCDPPort) {
		t.Fatal("expected dedicated default port to preflight when Chrome is absent and port is free")
	}

	portListeningFn = func(int) bool { return true }
	if ShouldPreflightDedicatedChrome(DefaultCDPPort) {
		t.Fatal("expected occupied port to skip dedicated preflight")
	}

	portListeningFn = func(int) bool { return false }
	if ShouldPreflightDedicatedChrome(9333) {
		t.Fatal("expected custom ports to skip dedicated preflight")
	}

	cdpChromeAliveFn = func() bool { return true }
	if ShouldPreflightDedicatedChrome(DefaultCDPPort) {
		t.Fatal("expected running dedicated Chrome to skip preflight")
	}
}

func TestEnsureChromeDebugPortSkipsLaunchAndCleansUpDaemonChromeOnPortConflict(t *testing.T) {
	home := t.TempDir()
	runner := &fakeChromeExec{
		pgrepOK:     true,
		pgrepOutput: "123\n",
	}
	installChromeTestHooks(t, home, runner.command, func() bool { return true }, func() string { return "123" })
	portListeningFn = func(int) bool { return true }

	if err := EnsureChromeDebugPort(DefaultCDPPort); err != nil {
		t.Fatalf("EnsureChromeDebugPort returned error: %v", err)
	}

	calls := runner.snapshotCalls()
	if containsPrefix(calls, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome ") {
		t.Fatalf("expected no Chrome relaunch during port conflict, got %v", calls)
	}
	if !containsPrefix(calls, "pgrep ") {
		t.Fatalf("expected daemon-owned Chrome cleanup probe, got %v", calls)
	}
	if !containsPrefix(calls, "pkill ") {
		t.Fatalf("expected daemon-owned Chrome cleanup signal, got %v", calls)
	}
}

func TestShowCDPChromeFallsBackToMainChromeOnlyWhenNoDedicatedChromeExists(t *testing.T) {
	home := t.TempDir()
	runner := &fakeChromeExec{osascriptOutput: []string{"false"}}
	installChromeTestHooks(t, home, runner.command, func() bool { return false }, func() string { return "" })

	if err := ShowCDPChrome(); err != nil {
		t.Fatalf("ShowCDPChrome returned error: %v", err)
	}

	calls := runner.snapshotCalls()
	if !containsPrefix(calls, "osascript ") {
		t.Fatalf("expected AppleScript activation when no dedicated Chrome exists, got %v", calls)
	}
}

func TestShowCDPChromeReturnsNotRunningWhenMainChromeIsNotRunning(t *testing.T) {
	home := t.TempDir()
	runner := &fakeChromeExec{osascriptOutput: []string{"NOT_RUNNING"}}
	installChromeTestHooks(t, home, runner.command, func() bool { return false }, func() string { return "" })

	err := ShowCDPChrome()
	if err == nil || !errors.Is(err, ErrChromeNotRunning) {
		t.Fatalf("expected ErrChromeNotRunning, got %v", err)
	}
}

func TestShowCDPChromeDoesNotFallbackToMainChromeWhenDedicatedChromeRecoveryFails(t *testing.T) {
	home := t.TempDir()
	runner := &fakeChromeExec{}
	installChromeTestHooks(t, home, runner.command, func() bool { return true }, func() string { return "123" })
	cdpReachableFn = func(int) bool { return false }
	ensureChromeDebugPortFn = func(int) error { return fmt.Errorf("boom") }

	err := ShowCDPChrome()
	if err == nil {
		t.Fatal("expected ShowCDPChrome to fail when dedicated Chrome recovery fails")
	}
	if !strings.Contains(err.Error(), "recover dedicated Chrome") {
		t.Fatalf("expected dedicated Chrome recovery error, got %v", err)
	}

	calls := runner.snapshotCalls()
	if containsPrefix(calls, "osascript ") {
		t.Fatalf("expected no AppleScript fallback when dedicated Chrome exists, got %v", calls)
	}
}

func TestHideCDPChromeFallsBackToMainChromeOnlyWhenNoDedicatedChromeExists(t *testing.T) {
	home := t.TempDir()
	runner := &fakeChromeExec{osascriptOutput: []string{"true"}}
	installChromeTestHooks(t, home, runner.command, func() bool { return false }, func() string { return "" })

	if err := HideCDPChrome(); err != nil {
		t.Fatalf("HideCDPChrome returned error: %v", err)
	}

	calls := runner.snapshotCalls()
	if !containsPrefix(calls, "osascript ") {
		t.Fatalf("expected AppleScript hide when no dedicated Chrome exists, got %v", calls)
	}
}

func TestHideCDPChromeReturnsNotRunningWhenMainChromeIsNotRunning(t *testing.T) {
	home := t.TempDir()
	runner := &fakeChromeExec{osascriptOutput: []string{"NOT_RUNNING"}}
	installChromeTestHooks(t, home, runner.command, func() bool { return false }, func() string { return "" })

	err := HideCDPChrome()
	if err == nil || !errors.Is(err, ErrChromeNotRunning) {
		t.Fatalf("expected ErrChromeNotRunning, got %v", err)
	}
}

func TestHideCDPChromeDoesNotFallbackToMainChromeWhenDedicatedChromeRecoveryFails(t *testing.T) {
	home := t.TempDir()
	runner := &fakeChromeExec{}
	installChromeTestHooks(t, home, runner.command, func() bool { return true }, func() string { return "123" })
	cdpReachableFn = func(int) bool { return false }
	ensureChromeDebugPortFn = func(int) error { return fmt.Errorf("boom") }

	err := HideCDPChrome()
	if err == nil {
		t.Fatal("expected HideCDPChrome to fail when dedicated Chrome recovery fails")
	}
	if !strings.Contains(err.Error(), "recover dedicated Chrome") {
		t.Fatalf("expected dedicated Chrome recovery error, got %v", err)
	}

	calls := runner.snapshotCalls()
	if containsPrefix(calls, "osascript ") {
		t.Fatalf("expected no AppleScript fallback when dedicated Chrome exists, got %v", calls)
	}
}

func TestGetCDPChromeStatusReturnsProbeErrorWhenDedicatedChromeRecoveryFails(t *testing.T) {
	home := t.TempDir()
	runner := &fakeChromeExec{}
	installChromeTestHooks(t, home, runner.command, func() bool { return true }, func() string { return "123" })
	cdpReachableFn = func(int) bool { return false }
	ensureChromeDebugPortFn = func(int) error { return fmt.Errorf("boom") }

	status := GetCDPChromeStatus()
	if !status.Running || !status.ProbeError {
		t.Fatalf("expected running+probe_error for unhealthy dedicated Chrome, got %+v", status)
	}

	calls := runner.snapshotCalls()
	if containsPrefix(calls, "osascript ") {
		t.Fatalf("expected no AppleScript fallback when dedicated Chrome exists, got %v", calls)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func containsPrefix(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func successCmdFn(name string, args ...string) *exec.Cmd {
	return successCmd()
}

func resetBrowserWSMemoryCache(t *testing.T) {
	t.Helper()
	cachedBrowserMu.Lock()
	cachedBrowserWS = ""
	cachedBrowserMu.Unlock()
	t.Cleanup(func() {
		cachedBrowserMu.Lock()
		cachedBrowserWS = ""
		cachedBrowserMu.Unlock()
	})
}
