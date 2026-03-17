package daemon

import (
	"context"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
	"github.com/Kocoro-lab/ShanClaw/internal/session"
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

	// Evict without holding the route lock (normal CRUD API path).
	// Evict must NOT be called from the same goroutine holding the route lock.
	sc.Evict("test-agent")

	// GetOrCreate should return a fresh manager
	mgr2 := sc.GetOrCreate("test-agent")
	if mgr2 == mgr {
		t.Error("expected fresh manager after evict")
	}
}

func TestSessionCache_LockRouteWithManager_ReusesRouteManager(t *testing.T) {
	dir := t.TempDir()
	sc := NewSessionCache(dir)
	sessionsDir := sc.sessionsDir("ops-bot")

	route := sc.LockRouteWithManager("agent:ops-bot", sessionsDir)
	if route == nil || route.manager == nil {
		t.Fatal("expected route manager to be initialized")
	}
	first := route.manager
	sc.UnlockRoute("agent:ops-bot")

	route = sc.LockRouteWithManager("agent:ops-bot", sessionsDir)
	if route == nil || route.manager == nil {
		t.Fatal("expected route manager to still exist on second lock")
	}
	if route.manager != first {
		t.Error("expected same route manager for repeated lock on same route")
	}
	sc.UnlockRoute("agent:ops-bot")
}

func TestSessionCache_LockRouteWithManager_IsolatedAcrossRoutes(t *testing.T) {
	dir := t.TempDir()
	sc := NewSessionCache(dir)
	sessionsDir := sc.sessionsDir("")

	routeA := sc.LockRouteWithManager("default:ch-a", sessionsDir)
	if routeA == nil || routeA.manager == nil {
		t.Fatal("expected first route manager to be initialized")
	}
	managerA := routeA.manager
	sc.UnlockRoute("default:ch-a")

	routeB := sc.LockRouteWithManager("default:ch-b", sessionsDir)
	if routeB == nil || routeB.manager == nil {
		t.Fatal("expected second route manager to be initialized")
	}
	managerB := routeB.manager
	sc.UnlockRoute("default:ch-b")

	if managerA == managerB {
		t.Error("expected separate route managers for separate routes sharing sessions directory")
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

func TestSessionCache_InjectMessage_ActiveRoute(t *testing.T) {
	sc := NewSessionCache(t.TempDir())
	defer sc.CloseAll()

	// Directly insert a route entry to simulate an in-flight run
	// without holding entry.mu (mirrors the state during RunAgent execution).
	injectCh := make(chan string, 5)
	sc.mu.Lock()
	sc.routes["agent:test"] = &routeEntry{
		injectCh: injectCh,
		done:     make(chan struct{}),
	}
	sc.mu.Unlock()

	result := sc.InjectMessage("agent:test", "new instruction")
	if result != InjectOK {
		t.Fatalf("expected InjectOK, got %d", result)
	}

	// Verify message is in channel
	select {
	case msg := <-injectCh:
		if msg != "new instruction" {
			t.Fatalf("expected 'new instruction', got %q", msg)
		}
	default:
		t.Fatal("expected message in channel")
	}
}

func TestSessionCache_InjectMessage_NoActiveRoute(t *testing.T) {
	sc := NewSessionCache(t.TempDir())
	defer sc.CloseAll()

	result := sc.InjectMessage("agent:nonexistent", "hello")
	if result != InjectNoActiveRun {
		t.Fatalf("expected InjectNoActiveRun, got %d", result)
	}
}

func TestSessionCache_InjectMessage_NilChannel(t *testing.T) {
	sc := NewSessionCache(t.TempDir())
	defer sc.CloseAll()

	// Route exists but injectCh is nil (no active run)
	sc.mu.Lock()
	sc.routes["agent:test"] = &routeEntry{
		done: make(chan struct{}),
		// injectCh is nil
	}
	sc.mu.Unlock()

	result := sc.InjectMessage("agent:test", "hello")
	if result != InjectNoActiveRun {
		t.Fatalf("expected InjectNoActiveRun, got %d", result)
	}
}

func TestSessionCache_InjectMessage_EmptyKey(t *testing.T) {
	sc := NewSessionCache(t.TempDir())
	defer sc.CloseAll()

	result := sc.InjectMessage("", "hello")
	if result != InjectNoActiveRun {
		t.Fatalf("expected InjectNoActiveRun, got %d", result)
	}
}

func TestSessionCache_InjectMessage_QueueFull(t *testing.T) {
	sc := NewSessionCache(t.TempDir())
	defer sc.CloseAll()

	// Create route with channel of size 1
	injectCh := make(chan string, 1)
	sc.mu.Lock()
	sc.routes["agent:test"] = &routeEntry{
		injectCh: injectCh,
		done:     make(chan struct{}),
	}
	sc.mu.Unlock()

	// Fill the channel
	result1 := sc.InjectMessage("agent:test", "first")
	if result1 != InjectOK {
		t.Fatalf("expected InjectOK, got %d", result1)
	}

	// Second should fail with QueueFull
	result2 := sc.InjectMessage("agent:test", "second")
	if result2 != InjectQueueFull {
		t.Fatalf("expected InjectQueueFull, got %d", result2)
	}
}

func TestSessionCache_CancelRoute(t *testing.T) {
	sc := NewSessionCache(t.TempDir())
	defer sc.CloseAll()

	ctx, cancel := context.WithCancel(context.Background())
	sc.mu.Lock()
	sc.routes["agent:test"] = &routeEntry{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	sc.mu.Unlock()

	// Cancel from outside
	sc.CancelRoute("agent:test")
	if ctx.Err() == nil {
		t.Fatal("expected context to be cancelled")
	}
}

func TestSessionCache_CancelRoute_Nonexistent(t *testing.T) {
	sc := NewSessionCache(t.TempDir())
	defer sc.CloseAll()

	// Should not panic
	sc.CancelRoute("agent:nonexistent")
}
