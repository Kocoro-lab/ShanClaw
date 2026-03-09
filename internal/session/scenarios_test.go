package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kocoro-lab/shan/internal/client"
)

func countJSON(entries []os.DirEntry) int {
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

// These tests validate every session storage scenario:
//
// | Mode           | Session per  | Resume        | Persist          | Storage                              |
// |----------------|-------------|---------------|------------------|--------------------------------------|
// | Daemon         | agent       | auto (latest) | every turn       | ~/.shannon/agents/<name>/sessions/   |
// | Daemon default | default     | auto (latest) | every turn       | ~/.shannon/sessions/                 |
// | TUI            | invocation  | manual        | on quit+per turn | same dirs                            |
// | One-shot       | invocation  | never         | after completion | same dirs                            |
// | Schedule       | invocation  | never         | after completion | same dirs (goes through runOneShot)  |

func TestScenario_DaemonNamedAgent_PersistsAndResumes(t *testing.T) {
	shanDir := t.TempDir()
	sessDir := filepath.Join(shanDir, "agents", "ops-bot", "sessions")
	os.MkdirAll(sessDir, 0700)

	// Turn 1: daemon creates a new session for ops-bot
	mgr := NewManager(sessDir)
	defer mgr.Close()
	sess, _ := mgr.ResumeLatest() // nil — no sessions yet
	if sess != nil {
		t.Fatal("expected no session on first run")
	}
	sess = mgr.NewSession()
	sess.Title = "ops-bot daemon session"
	sess.Messages = append(sess.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("check prod")},
		client.Message{Role: "assistant", Content: client.NewTextContent("prod is healthy")},
	)
	if err := mgr.Save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	sessionID := sess.ID

	// Turn 2: simulate daemon receiving another message (same agent)
	// In real daemon, GetOrCreate returns cached manager, but here we simulate restart
	mgr2 := NewManager(sessDir)
	defer mgr2.Close()
	resumed, err := mgr2.ResumeLatest()
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if resumed == nil {
		t.Fatal("expected to resume session")
	}
	if resumed.ID != sessionID {
		t.Errorf("expected same session ID %q, got %q", sessionID, resumed.ID)
	}
	if len(resumed.Messages) != 2 {
		t.Fatalf("expected 2 messages from turn 1, got %d", len(resumed.Messages))
	}

	// Append turn 2
	resumed.Messages = append(resumed.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("deploy staging")},
		client.Message{Role: "assistant", Content: client.NewTextContent("deployed")},
	)
	if err := mgr2.Save(); err != nil {
		t.Fatalf("save turn 2 failed: %v", err)
	}

	// Turn 3: simulate another restart — should see all 4 messages
	mgr3 := NewManager(sessDir)
	defer mgr3.Close()
	final, err := mgr3.ResumeLatest()
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if len(final.Messages) != 4 {
		t.Errorf("expected 4 messages across turns, got %d", len(final.Messages))
	}
	if final.Messages[2].Content.Text() != "deploy staging" {
		t.Errorf("turn 2 message not persisted: got %q", final.Messages[2].Content.Text())
	}
}

func TestScenario_DaemonDefaultAgent_UsesGlobalSessionsDir(t *testing.T) {
	shanDir := t.TempDir()
	sessDir := filepath.Join(shanDir, "sessions")
	os.MkdirAll(sessDir, 0700)

	mgr := NewManager(sessDir)
	defer mgr.Close()
	sess := mgr.NewSession()
	sess.Messages = append(sess.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("hello")},
		client.Message{Role: "assistant", Content: client.NewTextContent("hi")},
	)
	mgr.Save()

	// Verify file exists in the right directory
	files, _ := os.ReadDir(sessDir)
	jsonCount := countJSON(files)
	if jsonCount != 1 {
		t.Errorf("expected 1 session file in %s, got %d", sessDir, jsonCount)
	}

	// Resume should find it
	mgr2 := NewManager(sessDir)
	defer mgr2.Close()
	resumed, _ := mgr2.ResumeLatest()
	if resumed == nil {
		t.Fatal("expected to resume default session")
	}
	if len(resumed.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(resumed.Messages))
	}
}

func TestScenario_OneShotCreatesNewSession(t *testing.T) {
	shanDir := t.TempDir()
	sessDir := filepath.Join(shanDir, "agents", "reviewer", "sessions")
	os.MkdirAll(sessDir, 0700)

	// Simulate two one-shot invocations
	for i, query := range []string{"review PR #123", "review PR #456"} {
		mgr := NewManager(sessDir)
		defer mgr.Close()
		sess := mgr.NewSession()
		sess.Title = query
		sess.Messages = append(sess.Messages,
			client.Message{Role: "user", Content: client.NewTextContent(query)},
			client.Message{Role: "assistant", Content: client.NewTextContent("reviewed")},
		)
		if err := mgr.Save(); err != nil {
			t.Fatalf("one-shot %d save failed: %v", i, err)
		}
		// Small delay to ensure different timestamps
		time.Sleep(10 * time.Millisecond)
	}

	// Should have 2 separate session files
	files, _ := os.ReadDir(sessDir)
	jsonCount := countJSON(files)
	if jsonCount != 2 {
		t.Errorf("expected 2 session files for 2 one-shot runs, got %d", jsonCount)
	}

	// ResumeLatest picks the most recent one (PR #456)
	mgr := NewManager(sessDir)
	defer mgr.Close()
	latest, _ := mgr.ResumeLatest()
	if latest == nil {
		t.Fatal("expected to find a session")
	}
	if latest.Title != "review PR #456" {
		t.Errorf("expected latest to be 'review PR #456', got %q", latest.Title)
	}
}

func TestScenario_TUICreatesNewAndResumeByID(t *testing.T) {
	sessDir := t.TempDir()

	// TUI session 1
	mgr := NewManager(sessDir)
	defer mgr.Close()
	s1 := mgr.NewSession()
	s1.Title = "TUI session 1"
	s1.Messages = append(s1.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("first")},
	)
	mgr.Save()
	id1 := s1.ID

	// TUI session 2 (new invocation)
	time.Sleep(10 * time.Millisecond)
	mgr2 := NewManager(sessDir)
	defer mgr2.Close()
	s2 := mgr2.NewSession()
	s2.Title = "TUI session 2"
	s2.Messages = append(s2.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("second")},
	)
	mgr2.Save()

	// User /resume on session 1 by ID
	mgr3 := NewManager(sessDir)
	defer mgr3.Close()
	resumed, err := mgr3.Resume(id1)
	if err != nil {
		t.Fatalf("resume by ID failed: %v", err)
	}
	if resumed.Title != "TUI session 1" {
		t.Errorf("expected 'TUI session 1', got %q", resumed.Title)
	}
}

func TestScenario_DaemonAndOneShotSameAgent_NoConflict(t *testing.T) {
	shanDir := t.TempDir()
	sessDir := filepath.Join(shanDir, "agents", "ops-bot", "sessions")
	os.MkdirAll(sessDir, 0700)

	// Daemon creates and uses a session
	daemonMgr := NewManager(sessDir)
	defer daemonMgr.Close()
	daemonSess := daemonMgr.NewSession()
	daemonSess.Title = "daemon session"
	daemonSess.Messages = append(daemonSess.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("daemon msg 1")},
		client.Message{Role: "assistant", Content: client.NewTextContent("daemon reply 1")},
	)
	daemonMgr.Save()
	daemonID := daemonSess.ID

	time.Sleep(10 * time.Millisecond)

	// One-shot creates a separate session
	oneshotMgr := NewManager(sessDir)
	defer oneshotMgr.Close()
	oneshotSess := oneshotMgr.NewSession()
	oneshotSess.Title = "oneshot task"
	oneshotSess.Messages = append(oneshotSess.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("quick task")},
		client.Message{Role: "assistant", Content: client.NewTextContent("done")},
	)
	oneshotMgr.Save()

	// Two session files
	files, _ := os.ReadDir(sessDir)
	jsonCount := countJSON(files)
	if jsonCount != 2 {
		t.Errorf("expected 2 session files, got %d", jsonCount)
	}

	time.Sleep(10 * time.Millisecond)

	// Daemon appends another turn to its session
	daemonMgr2 := NewManager(sessDir)
	defer daemonMgr2.Close()
	resumed, _ := daemonMgr2.Resume(daemonID)
	if resumed == nil {
		t.Fatal("daemon should resume its own session by ID")
	}
	resumed.Messages = append(resumed.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("daemon msg 2")},
		client.Message{Role: "assistant", Content: client.NewTextContent("daemon reply 2")},
	)
	daemonMgr2.Save()

	// ResumeLatest should pick daemon session (most recently updated)
	latestMgr := NewManager(sessDir)
	defer latestMgr.Close()
	latest, _ := latestMgr.ResumeLatest()
	if latest == nil {
		t.Fatal("expected to find latest session")
	}
	if latest.ID != daemonID {
		t.Errorf("expected daemon session (most recently updated), got %q", latest.ID)
	}
	if len(latest.Messages) != 4 {
		t.Errorf("expected 4 messages in daemon session, got %d", len(latest.Messages))
	}
}

func TestScenario_DaemonResumeLatest_PicksUpdatedNotCreated(t *testing.T) {
	sessDir := t.TempDir()

	// Session A: created first
	mgrA := NewManager(sessDir)
	defer mgrA.Close()
	sA := mgrA.NewSession()
	sA.Title = "Session A (older)"
	sA.Messages = append(sA.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("A")},
	)
	mgrA.Save()

	time.Sleep(20 * time.Millisecond)

	// Session B: created second (newer CreatedAt)
	mgrB := NewManager(sessDir)
	defer mgrB.Close()
	sB := mgrB.NewSession()
	sB.Title = "Session B (newer created)"
	sB.Messages = append(sB.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("B")},
	)
	mgrB.Save()

	time.Sleep(20 * time.Millisecond)

	// Update session A (now has latest UpdatedAt despite older CreatedAt)
	mgrA2 := NewManager(sessDir)
	defer mgrA2.Close()
	resumedA, _ := mgrA2.Resume(sA.ID)
	resumedA.Messages = append(resumedA.Messages,
		client.Message{Role: "assistant", Content: client.NewTextContent("reply to A")},
	)
	mgrA2.Save()

	// ResumeLatest should pick A (most recent UpdatedAt), not B (most recent CreatedAt)
	mgrFinal := NewManager(sessDir)
	defer mgrFinal.Close()
	latest, _ := mgrFinal.ResumeLatest()
	if latest == nil {
		t.Fatal("expected to find session")
	}
	if latest.ID != sA.ID {
		t.Errorf("expected session A (most recently updated), got %q with title %q", latest.ID, latest.Title)
	}
}
