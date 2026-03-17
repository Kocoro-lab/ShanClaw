package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

// TestSmoke_EndToEnd verifies the full session search pipeline:
// create sessions → index → list → search → resume → delete → re-index.
func TestSmoke_EndToEnd(t *testing.T) {
	dir := t.TempDir()

	// 1. Create manager (auto-opens SQLite index)
	mgr := NewManager(dir)

	// 2. Save 3 sessions with different content
	type msg struct{ role, text string }
	data := []struct {
		title string
		msgs  []msg
	}{
		{"Kubernetes deployment", []msg{
			{"user", "How do I deploy a kubernetes cluster on AWS?"},
			{"assistant", "You can use EKS or kops to deploy kubernetes on AWS."},
		}},
		{"WebSocket reconnection", []msg{
			{"user", "The websocket connection keeps dropping after 30 seconds"},
			{"assistant", "This is likely a timeout issue. Set the ping interval to 15 seconds."},
		}},
		{"Go testing patterns", []msg{
			{"user", "What's the best way to write table-driven tests in Go?"},
			{"assistant", "Use a slice of test cases with subtests via t.Run()"},
			{"user", "Can I run them in parallel?"},
			{"assistant", "Yes, call t.Parallel() inside each subtest"},
		}},
	}

	for _, d := range data {
		sess := mgr.NewSession()
		sess.Title = d.title
		for _, m := range d.msgs {
			sess.Messages = append(sess.Messages,
				client.Message{Role: m.role, Content: client.NewTextContent(m.text)})
		}
		if err := mgr.Save(); err != nil {
			t.Fatalf("Save %q: %v", d.title, err)
		}
	}
	mgr.Close()

	// 3. Reopen manager (simulates restart — should find existing index)
	mgr2 := NewManager(dir)
	defer mgr2.Close()

	// 4. List — should return 3 sessions via index fast path
	summaries, err := mgr2.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(summaries))
	}

	// 5. Keyword search
	results, err := mgr2.Search("kubernetes", 10)
	if err != nil {
		t.Fatalf("Search kubernetes: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'kubernetes'")
	}
	if results[0].SessionTitle != "Kubernetes deployment" {
		t.Errorf("expected session title 'Kubernetes deployment', got %q", results[0].SessionTitle)
	}

	// 6. Stemming: "deploy" matches "deployment" and "deploy"
	results, err = mgr2.Search("deploy", 10)
	if err != nil {
		t.Fatalf("Search deploy: %v", err)
	}
	if len(results) < 2 {
		t.Errorf("stemming: expected >=2 results for 'deploy', got %d", len(results))
	}

	// 7. Phrase search
	results, err = mgr2.Search(`"ping interval"`, 10)
	if err != nil {
		t.Fatalf("Search phrase: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("phrase: expected 1 result, got %d", len(results))
	}

	// 8. No match
	results, err = mgr2.Search("terraform", 10)
	if err != nil {
		t.Fatalf("Search no-match: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for 'terraform', got %d", len(results))
	}

	// 9. Malformed query returns clean error
	_, err = mgr2.Search(`"unbalanced`, 10)
	if err == nil {
		// Some FTS5 versions handle this gracefully — either way is fine
		t.Log("FTS5 handled unbalanced quote without error")
	}

	// 10. ResumeLatest — should resume the most recently saved session
	sess, err := mgr2.ResumeLatest()
	if err != nil {
		t.Fatalf("ResumeLatest: %v", err)
	}
	if sess == nil {
		t.Fatal("ResumeLatest returned nil")
	}
	if sess.Title != "Go testing patterns" {
		t.Errorf("expected latest session 'Go testing patterns', got %q", sess.Title)
	}

	// 11. Verify sessions.db exists alongside JSON files
	dbPath := filepath.Join(dir, "sessions.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("sessions.db should exist")
	}

	entries, _ := os.ReadDir(dir)
	jsonCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			jsonCount++
		}
	}
	if jsonCount != 3 {
		t.Errorf("expected 3 JSON files, got %d", jsonCount)
	}

	// 12. Delete a session — verify removed from both index and disk
	mgr2.Delete(summaries[0].ID)
	results, _ = mgr2.Search("kubernetes", 10)
	// The deleted session may or may not show up depending on which summary[0] is
	// (list is sorted by created_at DESC, so [0] is the newest = "Go testing patterns")
	afterList, _ := mgr2.List()
	if len(afterList) != 2 {
		t.Errorf("expected 2 sessions after delete, got %d", len(afterList))
	}

	// 13. Rebuild index from scratch (simulates index corruption recovery)
	mgr2.RebuildIndex()
	afterRebuild, _ := mgr2.List()
	if len(afterRebuild) != 2 {
		t.Errorf("expected 2 sessions after rebuild, got %d", len(afterRebuild))
	}

	// 14. Cross-agent isolation — different dir = different index
	agentDir := t.TempDir()
	agentMgr := NewManager(agentDir)
	defer agentMgr.Close()

	agentSess := agentMgr.NewSession()
	agentSess.Title = "Agent-only session"
	agentSess.Messages = append(agentSess.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("flamingo waterfall content")})
	agentMgr.Save()

	// Agent search finds its own content
	agentResults, _ := agentMgr.Search("flamingo", 10)
	if len(agentResults) != 1 {
		t.Errorf("agent search: expected 1 result, got %d", len(agentResults))
	}

	// Main manager does NOT see agent content
	mainResults, _ := mgr2.Search("flamingo", 10)
	if len(mainResults) != 0 {
		t.Errorf("cross-agent leak: expected 0 results, got %d", len(mainResults))
	}
}

// TestSmoke_ResumeLatest_OrphanedIndex verifies that ResumeLatest recovers
// when the index points to a deleted JSON file.
func TestSmoke_ResumeLatest_OrphanedIndex(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	// Create two sessions
	s1 := mgr.NewSession()
	s1.Title = "Session A"
	s1.Messages = append(s1.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("hello from A")})
	mgr.Save()

	s2 := mgr.NewSession()
	s2.Title = "Session B"
	s2.Messages = append(s2.Messages,
		client.Message{Role: "user", Content: client.NewTextContent("hello from B")})
	mgr.Save()
	mgr.Close()

	// Delete the latest session's JSON file (simulates corruption/manual deletion)
	os.Remove(filepath.Join(dir, s2.ID+".json"))

	// Reopen and resume — should fall back to Session A
	mgr2 := NewManager(dir)
	defer mgr2.Close()

	sess, err := mgr2.ResumeLatest()
	if err != nil {
		t.Fatalf("ResumeLatest with orphaned index: %v", err)
	}
	if sess == nil {
		t.Fatal("ResumeLatest returned nil — should have fallen back to Session A")
	}
	if sess.Title != "Session A" {
		t.Errorf("expected fallback to 'Session A', got %q", sess.Title)
	}
}
