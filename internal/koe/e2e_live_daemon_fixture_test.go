//go:build darwin && !ios && cgo

package koe

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/daemon"
)

type liveIsolatedDaemon struct {
	root     string
	binary   string
	stateDir string
	version  string
	port     int
	url      string

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan error
	logs lockedBuffer
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func startLiveIsolatedDaemon(t *testing.T) *liveIsolatedDaemon {
	t.Helper()
	root := liveKoeRepoRoot(t)
	version := liveKoeBuildIdentity(t, root)
	buildDir := t.TempDir()
	binary := filepath.Join(buildDir, "shan")
	build := exec.Command(
		"go", "build", "-o", binary,
		"-ldflags", "-X github.com/Kocoro-lab/ShanClaw/cmd.Version="+version,
		".",
	)
	build.Dir = root
	var buildOutput bytes.Buffer
	build.Stdout = &buildOutput
	build.Stderr = &buildOutput
	if err := build.Run(); err != nil {
		t.Fatalf("build isolated daemon from current worktree: %v\n%s", err, buildOutput.String())
	}

	port := reserveLiveKoePort(t)
	daemon := &liveIsolatedDaemon{
		root:     root,
		binary:   binary,
		stateDir: t.TempDir(),
		version:  version,
		port:     port,
		url:      fmt.Sprintf("http://127.0.0.1:%d", port),
	}
	daemon.start(t)
	t.Cleanup(func() { daemon.stop(t) })
	return daemon
}

func (d *liveIsolatedDaemon) start(t *testing.T) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmd != nil {
		t.Fatal("isolated daemon is already running")
	}
	d.logs = lockedBuffer{}
	cmd := exec.Command(
		d.binary, "daemon", "start", "--isolated",
		"--state-dir", d.stateDir,
		"--port", strconv.Itoa(d.port),
	)
	cmd.Dir = d.root
	cmd.Stdout = &d.logs
	cmd.Stderr = &d.logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start isolated daemon: %v", err)
	}
	d.cmd = cmd
	d.done = make(chan error, 1)
	go func() { d.done <- cmd.Wait() }()
	d.waitReadyLocked(t)
	d.waitContainedLocked(t)
}

func (d *liveIsolatedDaemon) stop(t *testing.T) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmd == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, d.url+"/shutdown", nil)
	response, err := http.DefaultClient.Do(request)
	if err == nil {
		response.Body.Close()
	}
	cancel()
	select {
	case waitErr := <-d.done:
		if waitErr != nil && d.cmd.ProcessState != nil && !d.cmd.ProcessState.Success() {
			t.Logf("isolated daemon exited during shutdown: %v", waitErr)
		}
	case <-time.After(5 * time.Second):
		if err := d.cmd.Process.Kill(); err != nil {
			t.Errorf("kill unresponsive isolated daemon pid=%d: %v", d.cmd.Process.Pid, err)
		} else {
			<-d.done
		}
	}
	d.cmd = nil
	d.done = nil
}

func (d *liveIsolatedDaemon) restart(t *testing.T) {
	t.Helper()
	d.stop(t)
	d.start(t)
}

func (d *liveIsolatedDaemon) crashAndRestart(t *testing.T) (int, int) {
	t.Helper()
	d.mu.Lock()
	if d.cmd == nil || d.cmd.Process == nil {
		d.mu.Unlock()
		t.Fatal("isolated daemon is not running")
	}
	oldPID := d.cmd.Process.Pid
	if err := d.cmd.Process.Kill(); err != nil {
		d.mu.Unlock()
		t.Fatalf("crash isolated daemon pid=%d: %v", oldPID, err)
	}
	select {
	case <-d.done:
	case <-time.After(5 * time.Second):
		d.mu.Unlock()
		t.Fatalf("crashed isolated daemon pid=%d did not exit", oldPID)
	}
	d.cmd = nil
	d.done = nil
	d.mu.Unlock()

	d.start(t)
	d.mu.Lock()
	newPID := d.cmd.Process.Pid
	d.mu.Unlock()
	if newPID == oldPID {
		t.Fatalf("isolated daemon restart reused pid=%d", oldPID)
	}
	return oldPID, newPID
}

func (d *liveIsolatedDaemon) waitReadyLocked(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-d.done:
			d.cmd = nil
			d.done = nil
			t.Fatalf("isolated daemon exited before readiness: %v\n%s", err, tailLiveKoeLog(d.logs.String(), 12000))
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.url+"/status", nil)
		response, err := http.DefaultClient.Do(request)
		cancel()
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("isolated daemon did not become ready at %s\n%s", d.url, tailLiveKoeLog(d.logs.String(), 12000))
}

func (d *liveIsolatedDaemon) waitContainedLocked(t *testing.T) {
	t.Helper()
	want := []string{
		daemon.IsolationMarkerMCPDisabled,
		daemon.IsolationMarkerAutomationDisabled,
		daemon.IsolationMarkerCloudWSSuppressed,
		daemon.IsolationMarkerBackgroundDisabled,
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		log := d.logs.String()
		missing := false
		for _, marker := range want {
			if !strings.Contains(log, marker) {
				missing = true
				break
			}
		}
		if !missing {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("isolated daemon did not confirm contained startup\n%s", tailLiveKoeLog(d.logs.String(), 12000))
}

func liveKoeRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve live Koe fixture source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func liveKoeBuildIdentity(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve current worktree revision: %v", err)
	}
	revision := strings.TrimSpace(string(output))
	if len(revision) < 12 {
		t.Fatalf("unexpected worktree revision %q", revision)
	}
	identity := "koe-e2e-" + revision[:12]
	status := exec.Command("git", "status", "--porcelain")
	status.Dir = root
	output, err = status.Output()
	if err != nil {
		t.Fatalf("resolve current worktree status: %v", err)
	}
	if len(bytes.TrimSpace(output)) > 0 {
		identity += "-dirty"
	}
	return identity
}

func reserveLiveKoePort(t *testing.T) int {
	t.Helper()
	for {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve isolated daemon port: %v", err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatalf("release isolated daemon port reservation: %v", err)
		}
		if port != 7533 {
			return port
		}
	}
}

func tailLiveKoeLog(log string, limit int) string {
	if len(log) <= limit {
		return log
	}
	return log[len(log)-limit:]
}
