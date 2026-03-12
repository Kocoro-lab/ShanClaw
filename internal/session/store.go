package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Kocoro-lab/shan/internal/client"
)

// MessageMeta holds per-message metadata not sent to the LLM gateway.
// Indexed parallel to Session.Messages.
type MessageMeta struct {
	Source string `json:"source,omitempty"` // "slack", "line", "ptfrog", "webhook"
}

type Session struct {
	ID          string           `json:"id"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	Title       string           `json:"title"`
	CWD         string           `json:"cwd"`
	Messages    []client.Message `json:"messages"`
	RemoteTasks []string         `json:"remote_tasks,omitempty"`
	MessageMeta []MessageMeta    `json:"message_meta,omitempty"`
}

// SourceAt returns the source for message at index i, or "unknown" if not available.
func (s *Session) SourceAt(i int) string {
	if i >= 0 && i < len(s.MessageMeta) && s.MessageMeta[i].Source != "" {
		return s.MessageMeta[i].Source
	}
	return "unknown"
}

type SessionSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	MsgCount  int       `json:"msg_count"`
}

type Store struct {
	dir   string
	index *Index // nil = index unavailable (graceful degradation)
}

func NewStore(dir string) *Store {
	os.MkdirAll(dir, 0700)
	s := &Store{dir: dir}
	idx, err := OpenIndex(dir)
	if err == nil {
		s.index = idx
		// First-launch migration: if index is empty but JSON files exist, rebuild
		if empty, _ := idx.IsEmpty(); empty {
			idx.Rebuild(s) // best-effort
		}
	}
	return s
}

func (s *Store) Save(sess *Session) error {
	sess.UpdatedAt = time.Now()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = sess.UpdatedAt
	}

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	path := filepath.Join(s.dir, sess.ID+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}

	if s.index != nil {
		s.index.UpsertSession(sess) // best-effort, don't fail save on index error
	}
	return nil
}

func (s *Store) Load(id string) (*Session, error) {
	path := filepath.Join(s.dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}

	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("parse session: %w", err)
	}
	return &sess, nil
}

func (s *Store) List() ([]SessionSummary, error) {
	if s.index != nil {
		if summaries, err := s.index.ListSessions(); err == nil {
			return summaries, nil
		}
		// Fall through to JSON scan on index error
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	var summaries []SessionSummary
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		sess, err := s.Load(id)
		if err != nil {
			continue
		}
		summaries = append(summaries, SessionSummary{
			ID:        sess.ID,
			Title:     sess.Title,
			CreatedAt: sess.CreatedAt,
			MsgCount:  len(sess.Messages),
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
	})
	return summaries, nil
}

func (s *Store) Delete(id string) error {
	path := filepath.Join(s.dir, id+".json")
	if err := os.Remove(path); err != nil {
		return err
	}

	if s.index != nil {
		s.index.DeleteSession(id) // best-effort
	}
	return nil
}

func (s *Store) Search(query string, limit int) ([]SearchResult, error) {
	if s.index == nil {
		return nil, fmt.Errorf("search index not available")
	}
	return s.index.Search(query, limit)
}

func (s *Store) Close() error {
	if s.index != nil {
		return s.index.Close()
	}
	return nil
}

func (s *Store) RebuildIndex() error {
	if s.index == nil {
		return fmt.Errorf("search index not available")
	}
	return s.index.Rebuild(s)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
