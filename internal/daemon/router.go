package daemon

import (
	"log"
	"path/filepath"
	"sync"

	"github.com/Kocoro-lab/shan/internal/session"
)

type agentEntry struct {
	mgr *session.Manager
	mu  sync.Mutex // guards session read/append/save for this agent
}

// SessionCache manages one session.Manager per agent.
// For daemon mode, each agent has a single long-lived session that is
// always resumed. The cache is keyed by agent name ("" = default agent).
// Per-agent mutexes serialize concurrent messages to the same agent.
type SessionCache struct {
	mu         sync.Mutex
	agents     map[string]*agentEntry
	shannonDir string
}

// NewSessionCache creates a cache rooted at the given shannon directory.
func NewSessionCache(shannonDir string) *SessionCache {
	return &SessionCache{
		agents:     make(map[string]*agentEntry),
		shannonDir: shannonDir,
	}
}

// GetOrCreate returns the session.Manager for the given agent, creating one
// if needed. For daemon mode, it auto-resumes the latest session or creates
// a new one if none exists.
func (sc *SessionCache) GetOrCreate(agent string) *session.Manager {
	entry := sc.getEntry(agent)
	return entry.mgr
}

// Lock acquires the per-agent mutex. The caller MUST call Unlock when done.
// Use this to serialize concurrent messages to the same agent:
//
//	mgr := cache.GetOrCreate(agent)
//	cache.Lock(agent)
//	defer cache.Unlock(agent)
//	// ... read history, run agent, append, save ...
func (sc *SessionCache) Lock(agent string) {
	sc.getEntry(agent).mu.Lock()
}

// Unlock releases the per-agent mutex.
func (sc *SessionCache) Unlock(agent string) {
	sc.getEntry(agent).mu.Unlock()
}

func (sc *SessionCache) getEntry(agent string) *agentEntry {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if entry, ok := sc.agents[agent]; ok {
		return entry
	}

	sessDir := sc.sessionsDir(agent)
	mgr := session.NewManager(sessDir)

	sess, err := mgr.ResumeLatest()
	if err != nil {
		log.Printf("daemon: failed to resume session for agent %q: %v (starting fresh)", agent, err)
	}
	if sess == nil {
		mgr.NewSession()
	}

	entry := &agentEntry{mgr: mgr}
	sc.agents[agent] = entry
	return entry
}

// CloseAll closes all session managers in the cache.
func (sc *SessionCache) CloseAll() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, entry := range sc.agents {
		entry.mgr.Close()
	}
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
