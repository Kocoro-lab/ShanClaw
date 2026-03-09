package session

import (
	"testing"
	"time"

	"github.com/Kocoro-lab/shan/internal/client"
)

func TestManager_ResumeLatest_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Close()

	sess, err := m.ResumeLatest()
	if err != nil {
		t.Fatalf("unexpected error on empty dir: %v", err)
	}
	if sess != nil {
		t.Error("expected nil session for empty directory")
	}
}

func TestManager_ResumeLatest_FindsMostRecentByUpdatedAt(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	// Create "older-created" session first, then update it later
	// Create "newer-created" session second, but don't update it
	// ResumeLatest should pick "older-created" because it was updated more recently.

	olderCreated := &Session{
		ID:        "older-created",
		Title:     "Created first",
		CreatedAt: time.Now().Add(-2 * time.Hour),
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("first message")},
		},
	}
	store.Save(olderCreated) // UpdatedAt = now

	// Simulate passage of time
	time.Sleep(10 * time.Millisecond)

	newerCreated := &Session{
		ID:        "newer-created",
		Title:     "Created second",
		CreatedAt: time.Now(),
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("second message")},
		},
	}
	store.Save(newerCreated) // UpdatedAt = now (slightly later)

	// Now update the older-created session (simulating daemon appending a turn)
	time.Sleep(10 * time.Millisecond)
	olderCreated.Messages = append(olderCreated.Messages,
		client.Message{Role: "assistant", Content: client.NewTextContent("reply")},
	)
	store.Save(olderCreated) // UpdatedAt = now (latest)

	m := NewManager(dir)
	defer m.Close()
	sess, err := m.ResumeLatest()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess == nil {
		t.Fatal("expected a session, got nil")
	}
	// Should pick "older-created" because it has the latest UpdatedAt
	if sess.ID != "older-created" {
		t.Errorf("expected 'older-created' (most recently updated), got %q", sess.ID)
	}
	if len(sess.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(sess.Messages))
	}
	if m.Current() == nil || m.Current().ID != "older-created" {
		t.Error("ResumeLatest should set the session as current")
	}
}

func TestManager_ResumeLatest_SingleSession(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	store.Save(&Session{
		ID:    "only-one",
		Title: "Only session",
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("hello")},
		},
	})

	m := NewManager(dir)
	defer m.Close()
	sess, err := m.ResumeLatest()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.ID != "only-one" {
		t.Errorf("expected 'only-one', got %q", sess.ID)
	}
}
