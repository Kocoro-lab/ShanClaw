package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Kocoro-lab/shan/internal/client"
)

func TestStore_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

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
