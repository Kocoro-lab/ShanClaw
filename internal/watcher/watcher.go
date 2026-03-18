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
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// maxWatchDirs caps the total number of directories watched across all roots
// to avoid exhausting file descriptors on broad watch paths.
const maxWatchDirs = 4096

// defaultSkipDirs is the built-in set of directory names that should never be
// recursively watched. These are typically large vendored/generated trees.
var defaultSkipDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	"vendor":       true,
	".build":       true,
	"DerivedData":  true,
	"Pods":         true,
	".svn":         true,
	".hg":          true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	".tox":         true,
	"target":       true, // Rust, Java
	"dist":         true,
	"build":        true,
	".next":        true,
	".nuxt":        true,
	".cache":       true,
	".gradle":      true,
}

// Stats tracks watcher health metrics.
type Stats struct {
	WatchedDirs       int
	SkippedDirs       int
	AddFailures       int
	RateLimitedEvents int
	CapHit            bool
}

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
	lastRun map[string]time.Time

	// skip policy: built-in defaults merged with user-supplied ignores
	skipDirs map[string]bool

	// stats tracked atomically for observability
	watchedDirs atomic.Int32
	skippedDirs atomic.Int32
	addFailures atomic.Int32
	rateLimits  atomic.Int32
	capHit      atomic.Bool

	// watch state is protected by watchMu to avoid races between startup and runtime adds.
	watchMu    sync.Mutex
	watchedMap map[string]bool
	rateLimit  time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// Option configures a Watcher.
type Option func(*Watcher)

// WithIgnoreDirs adds extra directory names to the skip list.
func WithIgnoreDirs(names []string) Option {
	return func(w *Watcher) {
		for _, n := range names {
			w.skipDirs[n] = true
		}
	}
}

// WithRateLimit sets a minimum interval between RunFunc calls per agent.
// Zero or negative disables rate limiting.
func WithRateLimit(d time.Duration) Option {
	return func(w *Watcher) {
		w.rateLimit = d
	}
}

// New creates a Watcher from agent watch configurations.
// agentWatches maps agent name → list of watch entries.
func New(agentWatches map[string][]WatchEntry, runFn RunFunc, opts ...Option) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	w := &Watcher{
		fsw:        fsw,
		runFn:      runFn,
		Debounce:   2 * time.Second,
		batches:    make(map[string]map[string]string),
		timers:     make(map[string]*time.Timer),
		lastRun:    make(map[string]time.Time),
		skipDirs:   make(map[string]bool, len(defaultSkipDirs)),
		watchedMap: make(map[string]bool),
		done:       make(chan struct{}),
	}
	for k, v := range defaultSkipDirs {
		w.skipDirs[k] = v
	}
	for _, opt := range opts {
		opt(w)
	}

	var totalSkipped, totalFailures int
	var watches []agentWatch

	for agent, entries := range agentWatches {
		for _, entry := range entries {
			expanded := ExpandPath(entry.Path)
			watches = append(watches, agentWatch{
				Agent: agent,
				Path:  expanded,
				Glob:  entry.Glob,
			})

			// Verify the root watch path exists before walking.
			rootInfo, statErr := os.Lstat(expanded)
			if statErr != nil {
				log.Printf("watcher: watch path %s for agent %s is not accessible: %v", expanded, agent, statErr)
				continue
			}
			if !rootInfo.IsDir() {
				log.Printf("watcher: watch path %s for agent %s is not a directory", expanded, agent)
				continue
			}
			if rootInfo.Mode()&os.ModeSymlink != 0 {
				if resolved, err := filepath.EvalSymlinks(expanded); err == nil {
					expanded = resolved
				}
			}

			rootBefore := int(w.watchedDirs.Load())
			var skipped, failures int
			_ = filepath.Walk(expanded, func(path string, info os.FileInfo, walkErr error) error {
				if walkErr != nil {
					// Only skip the directory itself on access errors; don't
					// abort the entire subtree for a single file-level error.
					if info != nil && info.IsDir() {
						log.Printf("watcher: skipping inaccessible directory %s: %v", path, walkErr)
						skipped++
						w.skippedDirs.Add(1)
						return filepath.SkipDir
					}
					// File-level error: log and continue walking siblings.
					return nil
				}

				if !info.IsDir() {
					return nil
				}

				// Skip symlinked directories to avoid loops.
				if info.Mode()&os.ModeSymlink != 0 {
					skipped++
					w.skippedDirs.Add(1)
					return filepath.SkipDir
				}

				name := filepath.Base(path)
				if w.skipDirs[name] && path != expanded {
					skipped++
					w.skippedDirs.Add(1)
					return filepath.SkipDir
				}

				outcome := w.tryAddWatchDir(path, &failures)
				if outcome == watchCapReached || outcome == addWatchFailed {
					// Don't SkipDir — siblings may still be addable.
					// But do skip children of this dir.
					return filepath.SkipDir
				}

				return nil
			})

			watchedForRoot := int(w.watchedDirs.Load()) - rootBefore
			totalSkipped += skipped
			totalFailures += failures
			log.Printf("watcher: agent %s root %s — %d dirs watched, %d skipped, %d failures",
				agent, expanded, watchedForRoot, skipped, failures)
		}
	}

	w.watches = watches

	if totalFailures > 0 {
		log.Printf("watcher: total add failures: %d (check file descriptor limits)", totalFailures)
	}
	if totalSkipped > 0 {
		log.Printf("watcher: total skipped directories during startup: %d", totalSkipped)
	}
	if w.capHit.Load() {
		log.Printf("watcher: running in degraded mode — directory cap hit, coverage is partial")
	}

	return w, nil
}

// GetStats returns current watcher health metrics.
func (w *Watcher) GetStats() Stats {
	return Stats{
		WatchedDirs:       int(w.watchedDirs.Load()),
		SkippedDirs:       int(w.skippedDirs.Load()),
		AddFailures:       int(w.addFailures.Load()),
		RateLimitedEvents: int(w.rateLimits.Load()),
		CapHit:            w.capHit.Load(),
	}
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

	// Auto-add new directories for recursive watching, with same guards as startup.
	if event.Has(fsnotify.Create) {
		if info, statErr := os.Lstat(path); statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			name := filepath.Base(path)
			// Enforce skip policy and cap on dynamic adds too.
			if !w.skipDirs[name] && info.Mode()&os.ModeSymlink == 0 {
				if outcome := w.tryAddWatchDir(path, nil); outcome == addWatchFailed {
					log.Printf("watcher: failed to add runtime directory %s", path)
				} else if outcome == watchCapReached {
					log.Printf("watcher: WARNING: directory cap (%d) reached at runtime — new directories will not be watched", maxWatchDirs)
				}
			}
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

	now := time.Now()
	if w.rateLimit > 0 {
		if nextAllowed, ok := w.lastRun[agent]; ok && now.Before(nextAllowed) {
			w.rateLimits.Add(1)
			return
		}
	}

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
	if w.ctx.Err() != nil {
		return
	}

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

	if w.rateLimit > 0 {
		w.mu.Lock()
		w.lastRun[agent] = time.Now().Add(w.rateLimit)
		w.mu.Unlock()
	}

	prompt := FormatPrompt(events)
	w.runFn(w.ctx, agent, prompt)
}

type watchAddOutcome int

const (
	watchAdded watchAddOutcome = iota
	watchAlreadyWatched
	watchCapReached
	addWatchFailed
)

func (w *Watcher) tryAddWatchDir(path string, failures *int) watchAddOutcome {
	w.watchMu.Lock()
	defer w.watchMu.Unlock()

	if w.watchedMap[path] {
		return watchAlreadyWatched
	}
	if int(w.watchedDirs.Load()) >= maxWatchDirs {
		if !w.capHit.Load() {
			w.capHit.Store(true)
			log.Printf("watcher: WARNING: directory cap (%d) reached — watch coverage is partial, some changes may be missed", maxWatchDirs)
		}
		return watchCapReached
	}

	if err := w.fsw.Add(path); err != nil {
		log.Printf("watcher: failed to add %s: %v", path, err)
		w.addFailures.Add(1)
		if failures != nil {
			(*failures)++
		}
		return addWatchFailed
	}

	w.watchedMap[path] = true
	w.watchedDirs.Add(1)
	return watchAdded
}

// Close stops the watcher, cancels the context, and waits for the event loop to exit.
func (w *Watcher) Close() {
	if w.cancel != nil {
		w.cancel()
		_ = w.fsw.Close()
		<-w.done
	} else {
		// Start() was never called — just close fsnotify, don't wait on done.
		_ = w.fsw.Close()
	}

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
