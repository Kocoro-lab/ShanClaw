package daemon

import (
	"testing"

	"github.com/Kocoro-lab/shan/internal/client"
	"github.com/Kocoro-lab/shan/internal/session"
)

func TestSessionCache_GetOrCreate_NewAgent(t *testing.T) {
	dir := t.TempDir()
	cache := NewSessionCache(dir)

	mgr := cache.GetOrCreate("ops-bot")
	if mgr == nil {
		t.Fatal("expected a manager, got nil")
	}

	sess := mgr.Current()
	if sess == nil {
		t.Fatal("expected a session to be created or resumed")
	}
}

func TestSessionCache_GetOrCreate_ResumesExisting(t *testing.T) {
	dir := t.TempDir()

	// Pre-create a session for ops-bot
	agentDir := dir + "/agents/ops-bot/sessions"
	store := session.NewStore(agentDir)
	store.Save(&session.Session{
		ID:    "existing-123",
		Title: "Existing ops-bot session",
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("previous task")},
			{Role: "assistant", Content: client.NewTextContent("done")},
		},
	})

	cache := NewSessionCache(dir)
	mgr := cache.GetOrCreate("ops-bot")

	sess := mgr.Current()
	if sess == nil {
		t.Fatal("expected resumed session")
	}
	if sess.ID != "existing-123" {
		t.Errorf("expected to resume 'existing-123', got %q", sess.ID)
	}
	if len(sess.Messages) != 2 {
		t.Errorf("expected 2 messages from existing session, got %d", len(sess.Messages))
	}
}

func TestSessionCache_GetOrCreate_DefaultAgent(t *testing.T) {
	dir := t.TempDir()

	// Pre-create a session in the default sessions dir
	store := session.NewStore(dir + "/sessions")
	store.Save(&session.Session{
		ID:    "default-456",
		Title: "Default session",
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("hello")},
		},
	})

	cache := NewSessionCache(dir)
	mgr := cache.GetOrCreate("")

	sess := mgr.Current()
	if sess == nil {
		t.Fatal("expected resumed default session")
	}
	if sess.ID != "default-456" {
		t.Errorf("expected to resume 'default-456', got %q", sess.ID)
	}
}

func TestSessionCache_GetOrCreate_CachesManager(t *testing.T) {
	dir := t.TempDir()
	cache := NewSessionCache(dir)

	mgr1 := cache.GetOrCreate("ops-bot")
	mgr2 := cache.GetOrCreate("ops-bot")

	// Should return the same manager instance
	if mgr1 != mgr2 {
		t.Error("expected same manager instance for same agent")
	}
}

func TestSessionCache_Evict(t *testing.T) {
	dir := t.TempDir()
	sc := NewSessionCache(dir)
	defer sc.CloseAll()

	// Create an entry
	mgr := sc.GetOrCreate("test-agent")
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}

	// Evict it
	sc.Lock("test-agent")
	sc.Evict("test-agent")
	sc.Unlock("test-agent")

	// GetOrCreate should return a fresh manager
	mgr2 := sc.GetOrCreate("test-agent")
	if mgr2 == mgr {
		t.Error("expected fresh manager after evict")
	}
}

func TestSessionCache_GetOrCreate_DifferentAgentsDifferentSessions(t *testing.T) {
	dir := t.TempDir()
	cache := NewSessionCache(dir)

	mgr1 := cache.GetOrCreate("ops-bot")
	mgr2 := cache.GetOrCreate("reviewer")

	if mgr1 == mgr2 {
		t.Error("different agents should have different managers")
	}

	// Both should have sessions
	if mgr1.Current() == nil || mgr2.Current() == nil {
		t.Error("both agents should have sessions")
	}
}
