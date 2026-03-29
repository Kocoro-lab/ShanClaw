package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeChromeExec struct {
	mu        sync.Mutex
	calls     []string
	kill0Live map[string]bool
	kill9OK   bool
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
		return failureCmd()
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

func installChromeTestHooks(t *testing.T, home string, execFn func(string, ...string) *exec.Cmd, aliveFn func() bool, pidFn func() string) {
	t.Helper()

	oldExec := cdpExecCommand
	oldHome := cdpUserHomeDir
	oldSleep := cdpSleep
	oldAlive := cdpChromeAliveFn
	oldPID := cdpChromePIDFn

	cdpExecCommand = execFn
	cdpUserHomeDir = func() (string, error) { return home, nil }
	cdpSleep = func(time.Duration) {}
	cdpChromeAliveFn = aliveFn
	cdpChromePIDFn = pidFn

	t.Cleanup(func() {
		cdpExecCommand = oldExec
		cdpUserHomeDir = oldHome
		cdpSleep = oldSleep
		cdpChromeAliveFn = oldAlive
		cdpChromePIDFn = oldPID
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
