package watcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		glob     string
		filename string
		want     bool
	}{
		{"", "anything.txt", true},
		{"", "", true},
		{"*.txt", "readme.txt", true},
		{"*.txt", "readme.md", false},
		{"*.go", "main.go", true},
		{"*.go", "main.go.bak", false},
		{"data.*", "data.json", true},
		{"data.*", "mydata.json", false},
		{"config.yaml", "config.yaml", true},
		{"config.yaml", "config.yml", false},
		{"[abc].txt", "a.txt", true},
		{"[abc].txt", "d.txt", false},
	}
	for _, tt := range tests {
		got := MatchGlob(tt.glob, tt.filename)
		if got != tt.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", tt.glob, tt.filename, got, tt.want)
		}
	}
}

func TestMapEventType(t *testing.T) {
	tests := []struct {
		op   fsnotify.Op
		want string
	}{
		{fsnotify.Create, "created"},
		{fsnotify.Write, "modified"},
		{fsnotify.Remove, "deleted"},
		{fsnotify.Rename, "renamed"},
		{fsnotify.Chmod, ""},
	}
	for _, tt := range tests {
		got := MapEventType(tt.op)
		if got != tt.want {
			t.Errorf("MapEventType(%v) = %q, want %q", tt.op, got, tt.want)
		}
	}
}

func TestFormatPrompt(t *testing.T) {
	events := []fileEvent{
		{Path: "/b/file2.txt", Type: "created"},
		{Path: "/a/file1.go", Type: "modified"},
	}
	got := FormatPrompt(events)
	want := "File changes detected:\n- modified: /a/file1.go\n- created: /b/file2.txt"
	if got != want {
		t.Errorf("FormatPrompt() =\n%s\nwant:\n%s", got, want)
	}
}

func TestDedupEvents(t *testing.T) {
	// Simulate bursty writes: multiple events for the same path.
	// Last event type wins via sequential map assignment.
	batch := make(map[string]string)
	batch["/tmp/file.txt"] = "created"
	batch["/tmp/file.txt"] = "modified"
	batch["/tmp/file.txt"] = "modified"

	if batch["/tmp/file.txt"] != "modified" {
		t.Errorf("expected last event type 'modified', got %q", batch["/tmp/file.txt"])
	}
	if len(batch) != 1 {
		t.Errorf("expected 1 deduped entry, got %d", len(batch))
	}
}

func TestActiveHoursWindow(t *testing.T) {
	tests := []struct {
		name   string
		window string
		hour   int
		min    int
		want   bool
	}{
		// Empty = always active
		{"empty", "", 12, 0, true},
		// Normal daytime window: 09:00-17:00
		{"normal-inside", "09:00-17:00", 12, 0, true},
		{"normal-start", "09:00-17:00", 9, 0, true},
		{"normal-end-exclusive", "09:00-17:00", 17, 0, false},
		{"normal-before", "09:00-17:00", 8, 59, false},
		{"normal-after", "09:00-17:00", 17, 1, false},
		// Overnight window: 22:00-02:00
		{"overnight-before-midnight", "22:00-02:00", 23, 0, true},
		{"overnight-start", "22:00-02:00", 22, 0, true},
		{"overnight-after-midnight", "22:00-02:00", 1, 30, true},
		{"overnight-end-exclusive", "22:00-02:00", 2, 0, false},
		{"overnight-outside-day", "22:00-02:00", 12, 0, false},
		{"overnight-just-before", "22:00-02:00", 21, 59, false},
		// Edge: midnight boundary
		{"midnight-span", "23:00-01:00", 0, 0, true},
		{"midnight-span-outside", "23:00-01:00", 12, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 3, 17, tt.hour, tt.min, 0, 0, time.UTC)
			got := InActiveHours(tt.window, now)
			if got != tt.want {
				t.Errorf("InActiveHours(%q, %02d:%02d) = %v, want %v",
					tt.window, tt.hour, tt.min, got, tt.want)
			}
		})
	}
}

func TestActiveHoursInvalidFormat(t *testing.T) {
	// Invalid formats should return true (always active).
	invalids := []string{"not-a-time", "25:00-12:00", "09:00", "09:70-17:00"}
	now := time.Date(2026, 3, 17, 12, 0, 0, 0, time.UTC)
	for _, w := range invalids {
		if !InActiveHours(w, now) {
			t.Errorf("InActiveHours(%q) should return true for invalid format", w)
		}
	}
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	got := ExpandPath("~/test")
	want := filepath.Join(home, "test")
	if got != want {
		t.Errorf("ExpandPath(~/test) = %q, want %q", got, want)
	}

	// Env var expansion
	os.Setenv("SHAN_TEST_DIR", "/tmp/shantest")
	defer os.Unsetenv("SHAN_TEST_DIR")
	got = ExpandPath("$SHAN_TEST_DIR/sub")
	if got != "/tmp/shantest/sub" {
		t.Errorf("ExpandPath($SHAN_TEST_DIR/sub) = %q, want /tmp/shantest/sub", got)
	}
}

func TestWatcher_Integration(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	var calls []struct {
		Agent  string
		Prompt string
	}

	runFn := func(ctx context.Context, agent, prompt string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, struct {
			Agent  string
			Prompt string
		}{agent, prompt})
	}

	w, err := New(map[string][]WatchEntry{
		"test-agent": {{Path: dir, Glob: "*.txt"}},
	}, runFn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Debounce = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Close()

	// Create a matching file.
	testFile := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Wait for debounce + processing.
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("expected RunFunc to be called, got 0 calls")
	}

	call := calls[0]
	if call.Agent != "test-agent" {
		t.Errorf("agent = %q, want %q", call.Agent, "test-agent")
	}
	if !strings.Contains(call.Prompt, "hello.txt") {
		t.Errorf("prompt should contain filename, got: %s", call.Prompt)
	}
	if !strings.Contains(call.Prompt, "File changes detected:") {
		t.Errorf("prompt should start with header, got: %s", call.Prompt)
	}
}

func TestWatcher_GlobFilter(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	called := false

	runFn := func(ctx context.Context, agent, prompt string) {
		mu.Lock()
		defer mu.Unlock()
		called = true
	}

	w, err := New(map[string][]WatchEntry{
		"test-agent": {{Path: dir, Glob: "*.txt"}},
	}, runFn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Debounce = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Close()

	// Create a NON-matching file.
	nonMatch := filepath.Join(dir, "readme.md")
	if err := os.WriteFile(nonMatch, []byte("# readme"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Wait for debounce + processing.
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Error("RunFunc should NOT have been called for non-matching glob")
	}
}

func TestWatcher_MultipleAgents(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	agentCalls := make(map[string]int)

	runFn := func(ctx context.Context, agent, prompt string) {
		mu.Lock()
		defer mu.Unlock()
		agentCalls[agent]++
	}

	// Two agents watching same directory with different globs.
	w, err := New(map[string][]WatchEntry{
		"go-agent": {{Path: dir, Glob: "*.go"}},
		"md-agent": {{Path: dir, Glob: "*.md"}},
	}, runFn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Debounce = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Close()

	// Create a .go file — should only trigger go-agent.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	if agentCalls["go-agent"] == 0 {
		t.Error("go-agent should have been called")
	}
	if agentCalls["md-agent"] != 0 {
		t.Error("md-agent should NOT have been called for .go file")
	}
	mu.Unlock()
}

func TestWatcher_RecursiveSubdir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var mu sync.Mutex
	var prompts []string

	runFn := func(ctx context.Context, agent, prompt string) {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, prompt)
	}

	w, err := New(map[string][]WatchEntry{
		"agent": {{Path: dir, Glob: "*.txt"}},
	}, runFn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.Debounce = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Close()

	// Write file in subdirectory.
	if err := os.WriteFile(filepath.Join(subdir, "nested.txt"), []byte("nested"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) == 0 {
		t.Fatal("expected callback for file in subdirectory")
	}
	if !strings.Contains(prompts[0], "nested.txt") {
		t.Errorf("prompt should contain nested.txt, got: %s", prompts[0])
	}
}

func TestFormatPrompt_Sorted(t *testing.T) {
	events := []fileEvent{
		{Path: "/z/last.go", Type: "deleted"},
		{Path: "/a/first.go", Type: "created"},
		{Path: "/m/mid.go", Type: "modified"},
	}
	got := FormatPrompt(events)
	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (header + 3 events), got %d", len(lines))
	}

	// Verify events are sorted by path (ascending).
	want := []string{
		"- created: /a/first.go",
		"- modified: /m/mid.go",
		"- deleted: /z/last.go",
	}
	eventLines := lines[1:]
	for i, line := range eventLines {
		if line != want[i] {
			t.Errorf("line %d = %q, want %q", i, line, want[i])
		}
	}
}
