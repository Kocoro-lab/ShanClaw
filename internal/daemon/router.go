package daemon

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/session"
)

type routeEntry struct {
	mu            sync.Mutex
	cancel        context.CancelFunc
	cancelPending bool       // set under sc.mu when CancelRoute fires before cancel is assigned
	done          chan struct{}
	sessionID     string
	lastAccess    time.Time
	injectCh      chan string // buffered channel for mid-run message injection
	evicting      bool
	manager       *session.Manager
}

// SessionCache separates route-level locking from session storage.
// - routes: one lock/cancel/inflight channel per routing key
// - managers: one shared session.Manager per sessions directory for non-routed usage
// - route manager: lazily created session.Manager per route for routed runs
type SessionCache struct {
	mu         sync.Mutex
	routes     map[string]*routeEntry
	managers   map[string]*session.Manager
	shannonDir string
}

// NewSessionCache creates a cache rooted at the given shannon directory.
func NewSessionCache(shannonDir string) *SessionCache {
	return &SessionCache{
		routes:     make(map[string]*routeEntry),
		managers:   make(map[string]*session.Manager),
		shannonDir: shannonDir,
	}
}

// GetOrCreate returns the session.Manager for the given agent, preserving
// compatibility with existing caller paths.
func (sc *SessionCache) GetOrCreate(agent string) *session.Manager {
	return sc.GetOrCreateManager(sc.sessionsDir(agent))
}

// GetOrCreateManager returns the shared session.Manager for a sessions directory.
// Multiple routes that map to the same directory reuse the same manager.
func (sc *SessionCache) GetOrCreateManager(sessionsDir string) *session.Manager {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if mgr, ok := sc.managers[sessionsDir]; ok && mgr != nil {
		return mgr
	}

	mgr := sc.newManager(sessionsDir)
	sc.managers[sessionsDir] = mgr
	return mgr
}

// Lock acquires the route lock for a named agent.
// kept for compatibility with existing caller paths.
func (sc *SessionCache) Lock(agent string) {
	sc.LockRouteWithManager(sc.agentRouteKey(agent), sc.sessionsDir(agent))
}

// Unlock releases the route lock for a named agent.
// kept for compatibility with existing caller paths.
func (sc *SessionCache) Unlock(agent string) {
	sc.UnlockRoute(sc.agentRouteKey(agent))
}

// LockRoute acquires the per-route mutex.
// If another run is in-flight for this route, it is canceled and waited for
// before this call returns.
func (sc *SessionCache) LockRoute(key string) *routeEntry {
	// Preserve the compatibility behavior for non-routed callers.
	// The route manager is not created here because the caller may not know
	// the sessions directory.
	return sc.LockRouteWithManager(key, "")
}

func (sc *SessionCache) LockRouteWithManager(key, sessionsDir string) *routeEntry {
	if key == "" {
		return nil
	}
	sc.mu.Lock()
	entry, ok := sc.routes[key]
	if !ok {
		entry = &routeEntry{
			lastAccess: time.Now(),
		}
		sc.routes[key] = entry
	}
	if entry.manager == nil && sessionsDir != "" {
		entry.manager = sc.newManager(sessionsDir)
	}
	cancel := entry.cancel
	done := entry.done
	// Clear any stale pending cancel from when the route was idle. A cancel
	// arriving after this point (during the startup window before SetRouteCancel
	// is called) will set cancelPending again and be picked up correctly.
	entry.cancelPending = false
	sc.mu.Unlock()

	if cancel != nil && done != nil {
		cancel()
		<-done
	}

	entry.mu.Lock()
	entry.lastAccess = time.Now()
	return entry
}

// UnlockRoute releases the per-route mutex acquired by LockRoute.
// IMPORTANT: entry.mu is already held by the caller (from LockRouteWithManager).
// Do NOT re-acquire it — sync.Mutex is not reentrant.
func (sc *SessionCache) UnlockRoute(key string) {
	sc.mu.Lock()
	entry, ok := sc.routes[key]
	sc.mu.Unlock()
	if !ok || entry == nil {
		return
	}

	// Check evicting flag under the already-held lock.
	var mgr *session.Manager
	entry.cancel = nil
	entry.cancelPending = false
	entry.lastAccess = time.Now()
	if entry.evicting {
		mgr = entry.manager
		entry.manager = nil
		entry.evicting = false
	}

	// Single unlock point — releases the lock from LockRouteWithManager.
	// Entry stays in the map as a reusable shell (never deleted).
	entry.mu.Unlock()

	if mgr != nil {
		if err := mgr.Close(); err != nil {
			log.Printf("daemon: failed to close session for evicted route %q: %v", key, err)
		}
	}
}

// SetRouteCancel registers the cancel function for the active run under sc.mu,
// making it immediately visible to CancelRoute. If a cancel was already
// requested (cancelPending), cancel is called before returning.
//
// Called by the runner while entry.mu is held — sc.mu may be acquired while
// entry.mu is held because all other callers release sc.mu before acquiring
// entry.mu (same pattern as UnlockRoute).
func (sc *SessionCache) SetRouteCancel(key string, cancel context.CancelFunc) {
	sc.mu.Lock()
	entry, ok := sc.routes[key]
	var pending bool
	if ok && entry != nil {
		entry.cancel = cancel
		pending = entry.cancelPending
		entry.cancelPending = false
	}
	sc.mu.Unlock()
	if pending {
		cancel()
	}
}

// SetRouteSessionID stores the current route session id for future resume.
func (sc *SessionCache) SetRouteSessionID(key, sessionID string) {
	sc.mu.Lock()
	entry := sc.routes[key]
	sc.mu.Unlock()
	if entry == nil {
		return
	}
	entry.mu.Lock()
	entry.sessionID = sessionID
	entry.mu.Unlock()
}

// RouteSessionID returns the session id tracked by this route.
func (sc *SessionCache) RouteSessionID(key string) string {
	sc.mu.Lock()
	entry := sc.routes[key]
	sc.mu.Unlock()
	if entry == nil {
		return ""
	}
	entry.mu.Lock()
	sessionID := entry.sessionID
	entry.mu.Unlock()
	return sessionID
}

// InjectResult describes the outcome of an InjectMessage call.
type InjectResult int

const (
	InjectNoActiveRun InjectResult = iota // no in-flight run; caller should start one
	InjectOK                              // message delivered to the running loop
	InjectQueueFull                       // active run exists but queue is saturated
)

// InjectMessage sends a message into a running agent loop for this route.
// Returns InjectOK on success, InjectNoActiveRun if no run is in-flight
// (caller should start a new run), or InjectQueueFull if the channel is
// saturated (caller should NOT start a new run — the active run still owns
// this route).
func (sc *SessionCache) InjectMessage(key, text string) InjectResult {
	if key == "" {
		return InjectNoActiveRun
	}
	sc.mu.Lock()
	entry, ok := sc.routes[key]
	sc.mu.Unlock()
	if !ok || entry == nil {
		return InjectNoActiveRun
	}
	entry.mu.Lock()
	ch := entry.injectCh
	done := entry.done
	entry.mu.Unlock()
	if ch == nil || done == nil {
		return InjectNoActiveRun
	}
	select {
	case ch <- text:
		return InjectOK
	default:
		return InjectQueueFull
	}
}

// CancelRoute cancels the in-flight run for a route without waiting.
// Used by the hard cancel API endpoint.
//
// entry.mu is held for the entire duration of an in-flight run (acquired by
// LockRouteWithManager, released by UnlockRoute). We must NOT acquire it here
// — that would block until the run finishes, making cancel a no-op.
//
// Instead, we operate entirely under sc.mu:
//   - If entry.cancel is set, call it immediately (run is active).
//   - If entry.cancel is nil but the entry exists, set cancelPending so the
//     runner picks it up via SetRouteCancel before entering loop.Run. This
//     covers the narrow window between LockRouteWithManager returning and
//     route.cancel being registered.
//   - If the route key has no entry in the cache yet, this is a no-op (the
//     API layer still returns "cancelled" for idempotency, but no pending
//     intent is stored — the key must appear in sc.routes for pending to work).
func (sc *SessionCache) CancelRoute(key string) {
	sc.mu.Lock()
	entry, ok := sc.routes[key]
	var cancel context.CancelFunc
	if ok && entry != nil {
		cancel = entry.cancel
		if cancel == nil {
			entry.cancelPending = true
		}
	}
	sc.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Evict closes and removes the manager for this agent and drops matching route
// state. For active routes (in-flight run), it marks them as evicting and
// cancels — UnlockRoute finalizes cleanup when the run completes.
// IMPORTANT: sc.mu is released before per-route locking to avoid ABBA deadlock
// (other paths hold entry.mu then briefly acquire sc.mu).
func (sc *SessionCache) Evict(agent string) {
	sc.mu.Lock()
	sessionsDir := sc.sessionsDir(agent)
	if mgr, ok := sc.managers[sessionsDir]; ok && mgr != nil {
		if err := mgr.Close(); err != nil {
			log.Printf("daemon: failed to close session for agent %q: %v", agent, err)
		}
		delete(sc.managers, sessionsDir)
	}

	// Collect route keys to evict, then release sc.mu before per-route work.
	prefix := sc.agentRouteKey(agent)
	var keys []string
	for key := range sc.routes {
		if key == prefix || strings.HasPrefix(key, prefix+":") {
			keys = append(keys, key)
		}
	}
	sc.mu.Unlock()

	for _, key := range keys {
		sc.evictRoute(key)
	}
}

// evictRoute handles a single route eviction without holding sc.mu.
// The entry is never deleted from the map — it stays as a reusable shell.
// This prevents the race where LockRouteWithManager holds an orphaned entry
// and UnlockRoute can't find it to release the mutex.
func (sc *SessionCache) evictRoute(key string) {
	sc.mu.Lock()
	entry := sc.routes[key]
	sc.mu.Unlock()
	if entry == nil {
		return
	}

	entry.mu.Lock()
	mgr := entry.manager
	active := entry.cancel != nil || entry.done != nil
	if active {
		// Route has an in-flight run — mark for deferred cleanup.
		entry.evicting = true
		if entry.cancel != nil {
			entry.cancel()
		}
		entry.mu.Unlock()
		return // UnlockRoute will finalize when the run completes
	}
	// Nil out manager but keep entry in map — LockRouteWithManager will
	// create a fresh manager on next use (it checks entry.manager == nil).
	entry.manager = nil
	entry.mu.Unlock()

	if mgr != nil {
		if err := mgr.Close(); err != nil {
			log.Printf("daemon: failed to close session for route %q: %v", key, err)
		}
	}
}

// CloseAll cancels active routes, closes all session managers, and nils
// route managers. Route entries stay in the map so in-flight goroutines
// can still call UnlockRoute without missing the entry.
//
// cancel/done are snapshot under sc.mu (not entry.mu) to avoid blocking
// on an in-flight run's held entry.mu. This is safe because cancel() is
// idempotent and done channels only close once.
func (sc *SessionCache) CloseAll() {
	// Snapshot cancel/done for all active routes under sc.mu.
	type activeRoute struct {
		key    string
		cancel context.CancelFunc
		done   chan struct{}
	}
	sc.mu.Lock()
	var active []activeRoute
	for key, route := range sc.routes {
		if route != nil && route.cancel != nil {
			active = append(active, activeRoute{key, route.cancel, route.done})
		}
	}
	sc.mu.Unlock()

	// Cancel active routes and wait briefly — no entry.mu needed.
	for _, ar := range active {
		ar.cancel()
		if ar.done != nil {
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-ar.done:
			case <-timer.C:
				log.Printf("daemon: timed out waiting for route %q to stop", ar.key)
			}
			timer.Stop()
		}
	}

	// Now all runs are stopped — safe to close managers.
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for sessionsDir, mgr := range sc.managers {
		if err := mgr.Close(); err != nil {
			log.Printf("daemon: failed to close session for %q: %v", sessionsDir, err)
		}
	}
	for key, route := range sc.routes {
		if route != nil && route.manager != nil {
			if err := route.manager.Close(); err != nil {
				log.Printf("daemon: failed to close session for route %q: %v", key, err)
			}
			route.manager = nil
		}
	}
	sc.managers = make(map[string]*session.Manager)
}

// SessionsDir returns the sessions directory for the given agent.
// Empty agent name returns the default sessions directory.
func (sc *SessionCache) SessionsDir(agent string) string {
	return sc.sessionsDir(agent)
}

func (sc *SessionCache) sessionsDir(agent string) string {
	if agent == "" {
		return filepath.Join(sc.shannonDir, "sessions")
	}
	return filepath.Join(sc.shannonDir, "agents", agent, "sessions")
}

func (sc *SessionCache) agentRouteKey(agent string) string {
	return "agent:" + agent
}

func (sc *SessionCache) newManager(sessionsDir string) *session.Manager {
	mgr := session.NewManager(sessionsDir)

	sess, err := mgr.ResumeLatest()
	if err != nil {
		log.Printf("daemon: failed to resume session for %q: %v (starting fresh)", sessionsDir, err)
	}
	if sess == nil {
		mgr.NewSession()
	}
	return mgr
}
