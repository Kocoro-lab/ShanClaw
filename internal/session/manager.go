package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

type Manager struct {
	store   *Store
	current *Session
}

func NewManager(sessionsDir string) *Manager {
	return &Manager{
		store: NewStore(sessionsDir),
	}
}

func (m *Manager) NewSession() *Session {
	id := generateID()
	m.current = &Session{
		ID:        id,
		CreatedAt: time.Now(),
		Title:     "New session",
		CWD:       getCWD(),
	}
	return m.current
}

func (m *Manager) Current() *Session {
	return m.current
}

func (m *Manager) Resume(id string) (*Session, error) {
	sess, err := m.store.Load(id)
	if err != nil {
		return nil, err
	}
	m.current = sess
	return sess, nil
}

func (m *Manager) Save() error {
	if m.current == nil {
		return nil
	}
	return m.store.Save(m.current)
}

func (m *Manager) List() ([]SessionSummary, error) {
	return m.store.List()
}

func (m *Manager) Delete(id string) error {
	return m.store.Delete(id)
}

func (m *Manager) Search(query string, limit int) ([]SearchResult, error) {
	return m.store.Search(query, limit)
}

func (m *Manager) Close() error {
	return m.store.Close()
}

func (m *Manager) RebuildIndex() error {
	return m.store.RebuildIndex()
}

// ResumeLatest loads the most recently updated session from disk.
// Returns (nil, nil) if no sessions exist.
func (m *Manager) ResumeLatest() (*Session, error) {
	// Fast path: use index to find the latest session by updated_at.
	// Only trust a non-empty result — if index says "empty", fall through
	// to brute-force in case index is stale or partially migrated.
	if m.store.index != nil {
		id, err := m.store.index.LatestUpdatedID()
		if err == nil && id != "" {
			if sess, resumeErr := m.Resume(id); resumeErr == nil {
				return sess, nil
			}
			// JSON file missing/corrupt — fall through to brute-force
		}
		// On error, empty result, or failed resume — fall through to JSON scan
	}

	summaries, err := m.store.List()
	if err != nil {
		return nil, err
	}
	if len(summaries) == 0 {
		return nil, nil
	}

	// Find the session with the most recent UpdatedAt.
	// List() only has CreatedAt, so we load each to check UpdatedAt.
	// For typical daemon use (1 session per agent), this is just 1 load.
	var bestID string
	var bestTime time.Time
	for _, s := range summaries {
		sess, err := m.store.Load(s.ID)
		if err != nil {
			continue
		}
		if sess.UpdatedAt.After(bestTime) {
			bestTime = sess.UpdatedAt
			bestID = sess.ID
		}
	}
	if bestID == "" {
		return nil, nil
	}
	return m.Resume(bestID)
}

func generateID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-only ID if entropy fails
		return time.Now().Format("2006-01-02-150405")
	}
	return fmt.Sprintf("%s-%s", time.Now().Format("2006-01-02"), hex.EncodeToString(b))
}

func getCWD() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
