package watcher

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// fileEvent represents a debounced file system event.
type fileEvent struct {
	Path string
	Type string
}

// agentWatch maps a watch entry to the agent that owns it.
type agentWatch struct {
	Agent string
	Path  string
	Glob  string
}

// WatchEntry is the config shape for a single watch path+glob.
type WatchEntry struct {
	Path string `json:"path" yaml:"path"`
	Glob string `json:"glob,omitempty" yaml:"glob,omitempty"`
}

// RunFunc is the callback invoked when debounced events are ready for an agent.
type RunFunc func(ctx context.Context, agent, prompt string)

// Watcher monitors file system paths and dispatches debounced events to agents.
type Watcher struct {
	fsw      *fsnotify.Watcher
	watches  []agentWatch
	runFn    RunFunc
	Debounce time.Duration

	mu      sync.Mutex
	batches map[string]map[string]string // agent → path → eventType
	timers  map[string]*time.Timer       // agent → debounce timer

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a Watcher from agent watch configurations.
// agentWatches maps agent name → list of watch entries.
func New(agentWatches map[string][]WatchEntry, runFn RunFunc) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	var watches []agentWatch
	added := make(map[string]bool)

	for agent, entries := range agentWatches {
		for _, entry := range entries {
			expanded := ExpandPath(entry.Path)
			watches = append(watches, agentWatch{
				Agent: agent,
				Path:  expanded,
				Glob:  entry.Glob,
			})

			// Walk to add all subdirectories for recursive watching.
			_ = filepath.Walk(expanded, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil // skip inaccessible paths
				}
				if info.IsDir() && !added[path] {
					added[path] = true
					if addErr := fsw.Add(path); addErr != nil {
						log.Printf("watcher: failed to add %s: %v", path, addErr)
					}
				}
				return nil
			})
		}
	}

	return &Watcher{
		fsw:      fsw,
		watches:  watches,
		runFn:    runFn,
		Debounce: 2 * time.Second,
		batches:  make(map[string]map[string]string),
		timers:   make(map[string]*time.Timer),
		done:     make(chan struct{}),
	}, nil
}

// Start begins the event loop. Blocks until ctx is cancelled or Close is called.
func (w *Watcher) Start(ctx context.Context) {
	w.ctx, w.cancel = context.WithCancel(ctx)
	go w.loop()
}

func (w *Watcher) loop() {
	defer close(w.done)
	for {
		select {
		case <-w.ctx.Done():
			return
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			log.Printf("watcher: fsnotify error: %v", err)
		}
	}
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	path, err := filepath.Abs(filepath.Clean(event.Name))
	if err != nil {
		return
	}

	// Auto-add new directories for recursive watching.
	if event.Has(fsnotify.Create) {
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			_ = w.fsw.Add(path)
		}
	}

	eventType := MapEventType(event.Op)
	if eventType == "" {
		return
	}

	filename := filepath.Base(path)

	// Fan out to all matching agent watches.
	for _, aw := range w.watches {
		if !isUnder(path, aw.Path) {
			continue
		}
		if !MatchGlob(aw.Glob, filename) {
			continue
		}
		w.appendEvent(aw.Agent, path, eventType)
	}
}

// isUnder returns true if path is inside (or equal to) the watched directory.
func isUnder(path, watchDir string) bool {
	rel, err := filepath.Rel(watchDir, path)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..")
}

func (w *Watcher) appendEvent(agent, path, eventType string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.batches[agent] == nil {
		w.batches[agent] = make(map[string]string)
	}
	w.batches[agent][path] = eventType

	// Reset debounce timer for this agent.
	if t, ok := w.timers[agent]; ok {
		t.Stop()
	}
	agentCopy := agent
	w.timers[agent] = time.AfterFunc(w.Debounce, func() {
		w.flush(agentCopy)
	})
}

func (w *Watcher) flush(agent string) {
	w.mu.Lock()
	batch := w.batches[agent]
	delete(w.batches, agent)
	delete(w.timers, agent)
	w.mu.Unlock()

	if len(batch) == 0 {
		return
	}

	var events []fileEvent
	for p, t := range batch {
		events = append(events, fileEvent{Path: p, Type: t})
	}

	prompt := FormatPrompt(events)
	w.runFn(w.ctx, agent, prompt)
}

// Close stops the watcher, cancels the context, and waits for the event loop to exit.
func (w *Watcher) Close() {
	if w.cancel != nil {
		w.cancel()
	}
	_ = w.fsw.Close()
	<-w.done

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, t := range w.timers {
		t.Stop()
	}
}

// MatchGlob returns true if filename matches the glob pattern.
// An empty glob matches everything.
func MatchGlob(glob, filename string) bool {
	if glob == "" {
		return true
	}
	matched, err := filepath.Match(glob, filename)
	if err != nil {
		return false
	}
	return matched
}

// MapEventType converts an fsnotify Op to a human-readable event type string.
func MapEventType(op fsnotify.Op) string {
	switch {
	case op.Has(fsnotify.Create):
		return "created"
	case op.Has(fsnotify.Remove):
		return "deleted"
	case op.Has(fsnotify.Rename):
		return "renamed"
	case op.Has(fsnotify.Write):
		return "modified"
	default:
		return ""
	}
}

// ExpandPath expands tilde and environment variables, then cleans and resolves to absolute.
func ExpandPath(path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[1:])
		}
	}
	path = os.ExpandEnv(path)
	path = filepath.Clean(path)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path
}

// FormatPrompt formats a slice of file events into a prompt string.
func FormatPrompt(events []fileEvent) string {
	sort.Slice(events, func(i, j int) bool {
		return events[i].Path < events[j].Path
	})

	var b strings.Builder
	b.WriteString("File changes detected:\n")
	for _, e := range events {
		fmt.Fprintf(&b, "- %s: %s\n", e.Type, e.Path)
	}
	return strings.TrimRight(b.String(), "\n")
}

// InActiveHours checks whether now falls within the given "HH:MM-HH:MM" window.
// Supports overnight windows (e.g. "22:00-02:00").
// Empty window = always active. Invalid format = always active (with log warning).
func InActiveHours(window string, now time.Time) bool {
	if window == "" {
		return true
	}

	parts := strings.SplitN(window, "-", 2)
	if len(parts) != 2 {
		log.Printf("watcher: invalid active_hours format %q, treating as always active", window)
		return true
	}

	startMin, err1 := parseHHMM(parts[0])
	endMin, err2 := parseHHMM(parts[1])
	if err1 != nil || err2 != nil {
		log.Printf("watcher: invalid active_hours format %q, treating as always active", window)
		return true
	}

	nowMin := now.Hour()*60 + now.Minute()

	if startMin <= endMin {
		// Normal window, e.g. "09:00-17:00"
		return nowMin >= startMin && nowMin < endMin
	}
	// Overnight window, e.g. "22:00-02:00"
	return nowMin >= startMin || nowMin < endMin
}

// parseHHMM parses "HH:MM" into minutes since midnight.
func parseHHMM(s string) (int, error) {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("expected HH:MM, got %q", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("invalid hour in %q", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid minute in %q", s)
	}
	return h*60 + m, nil
}
