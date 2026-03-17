package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

func TestStore_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	sess := &Session{
		ID:    "test-123",
		Title: "Test session",
		CWD:   "/tmp/test",
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("hello")},
			{Role: "assistant", Content: client.NewTextContent("hi there")},
		},
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := store.Load("test-123")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.Title != "Test session" {
		t.Errorf("expected 'Test session', got %q", loaded.Title)
	}
	if len(loaded.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(loaded.Messages))
	}
}

func TestStore_List(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	store.Save(&Session{ID: "aaa", Title: "First"})
	store.Save(&Session{ID: "bbb", Title: "Second"})

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestStore_Delete(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	store.Save(&Session{ID: "del-me", Title: "Delete me"})

	if err := store.Delete("del-me"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if _, err := store.Load("del-me"); err == nil {
		t.Error("expected error loading deleted session")
	}

	// Verify file is gone
	path := filepath.Join(dir, "del-me.json")
	if fileExists(path) {
		t.Error("session file should be deleted")
	}
}

func TestStore_SaveLoadWithImageContent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	sess := &Session{
		ID:    "vision-test",
		Title: "Vision test",
		CWD:   "/tmp",
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("take a screenshot")},
			{Role: "user", Content: client.NewBlockContent([]client.ContentBlock{
				{Type: "text", Text: "Screenshot captured"},
				{Type: "image", Source: &client.ImageSource{
					Type:      "base64",
					MediaType: "image/png",
					Data:      "iVBORfake",
				}},
			})},
			{Role: "assistant", Content: client.NewTextContent("I see a desktop")},
		},
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := store.Load("vision-test")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if len(loaded.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(loaded.Messages))
	}

	// First message: plain string
	if loaded.Messages[0].Content.Text() != "take a screenshot" {
		t.Errorf("msg[0] text mismatch: %q", loaded.Messages[0].Content.Text())
	}

	// Second message: content blocks with image
	if !loaded.Messages[1].Content.HasBlocks() {
		t.Fatal("msg[1] should have blocks")
	}
	blocks := loaded.Messages[1].Content.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[1].Source == nil || blocks[1].Source.Data != "iVBORfake" {
		t.Error("image block data not preserved")
	}

	// Third message: plain string
	if loaded.Messages[2].Content.Text() != "I see a desktop" {
		t.Errorf("msg[2] text mismatch: %q", loaded.Messages[2].Content.Text())
	}
}

func TestStore_SearchIntegration(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir) // auto-creates index + rebuilds (nothing to rebuild)
	defer store.Close()

	sess := &Session{
		ID:    "int-test",
		Title: "Integration",
		CWD:   "/tmp",
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("deploy the kubernetes cluster")},
			{Role: "assistant", Content: client.NewTextContent("I'll help you deploy k8s")},
		},
	}
	store.Save(sess)

	// Search should find it
	results, err := store.Search("kubernetes", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// List should use index (fast path)
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 session, got %d", len(summaries))
	}

	// Delete should clean up index
	store.Delete("int-test")
	results, _ = store.Search("kubernetes", 10)
	if len(results) != 0 {
		t.Errorf("expected 0 results after delete, got %d", len(results))
	}
}

func TestStore_GracefulDegradation(t *testing.T) {
	dir := t.TempDir()
	store := &Store{dir: dir, index: nil} // simulate index failure

	sess := &Session{ID: "no-idx", Title: "No index", CWD: "/tmp"}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save should work without index: %v", err)
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List should fall back to JSON scan: %v", err)
	}
	if len(summaries) != 1 {
		t.Errorf("expected 1 session from JSON fallback, got %d", len(summaries))
	}

	_, err = store.Search("anything", 10)
	if err == nil {
		t.Error("Search should return error when index is nil")
	}
}

func TestStore_ListEmptyDir(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List on empty dir: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(summaries))
	}
}

func TestStore_FirstLaunchMigration(t *testing.T) {
	dir := t.TempDir()

	// Write JSON files WITHOUT an index (simulate pre-SQLite sessions)
	rawStore := &Store{dir: dir, index: nil}
	rawStore.Save(&Session{
		ID:    "legacy-1",
		Title: "Legacy session",
		CWD:   "/tmp",
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("legacy migration test")},
		},
	})

	// Now create store normally — should detect empty index and rebuild
	store := NewStore(dir)
	defer store.Close()

	// Should be searchable after migration
	results, err := store.Search("legacy", 10)
	if err != nil {
		t.Fatalf("Search after migration: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result after migration, got %d", len(results))
	}
}

func TestSessionMessageMetaSerialization(t *testing.T) {
	sess := &Session{
		ID:        "test-meta",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Title:     "Test",
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("hello")},
			{Role: "assistant", Content: client.NewTextContent("hi")},
		},
		MessageMeta: []MessageMeta{
			{Source: "slack"},
			{Source: "slack"},
		},
	}

	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}

	var loaded Session
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if len(loaded.MessageMeta) != 2 {
		t.Fatalf("expected 2 meta entries, got %d", len(loaded.MessageMeta))
	}
	if loaded.MessageMeta[0].Source != "slack" {
		t.Fatalf("expected source 'slack', got %q", loaded.MessageMeta[0].Source)
	}
}

func TestSessionMessageMetaBackwardCompat(t *testing.T) {
	// Old session JSON without message_meta should deserialize cleanly
	oldJSON := `{"id":"old","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","title":"Old","messages":[]}`

	var sess Session
	if err := json.Unmarshal([]byte(oldJSON), &sess); err != nil {
		t.Fatal(err)
	}
	if sess.MessageMeta != nil {
		t.Fatal("expected nil MessageMeta for old session")
	}
}

func TestSessionSourceAtSafety(t *testing.T) {
	sess := &Session{
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("hello")},
		},
		// No MessageMeta — simulates legacy session
	}

	// Negative index should not panic
	if sess.SourceAt(-1) != "unknown" {
		t.Fatal("expected 'unknown' for negative index")
	}

	// Out of bounds should not panic
	if sess.SourceAt(999) != "unknown" {
		t.Fatal("expected 'unknown' for out-of-bounds index")
	}

	// Valid index but no meta
	if sess.SourceAt(0) != "unknown" {
		t.Fatal("expected 'unknown' for missing meta")
	}

	// With meta
	sess.MessageMeta = []MessageMeta{{Source: "slack"}}
	if sess.SourceAt(0) != "slack" {
		t.Fatalf("expected 'slack', got %q", sess.SourceAt(0))
	}
}

func TestStore_LoadLegacyStringContent(t *testing.T) {
	dir := t.TempDir()
	legacyJSON := `{
		"id": "legacy-test",
		"title": "Legacy",
		"cwd": "/tmp",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "hi there"}
		]
	}`
	os.WriteFile(filepath.Join(dir, "legacy-test.json"), []byte(legacyJSON), 0600)

	store := NewStore(dir)
	defer store.Close()
	loaded, err := store.Load("legacy-test")
	if err != nil {
		t.Fatalf("load legacy failed: %v", err)
	}
	if loaded.Messages[0].Content.Text() != "hello" {
		t.Errorf("expected 'hello', got %q", loaded.Messages[0].Content.Text())
	}
	if loaded.Messages[1].Content.Text() != "hi there" {
		t.Errorf("expected 'hi there', got %q", loaded.Messages[1].Content.Text())
	}
}
