package session

import (
	"strings"
	"testing"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

func TestIndex_OpenClose(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestIndex_UpsertAndList(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer idx.Close()

	now := time.Now().Truncate(time.Second)
	sess := &Session{
		ID:        "sess-1",
		Title:     "First session",
		CWD:       "/tmp/test",
		CreatedAt: now,
		UpdatedAt: now,
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("hello world")},
			{Role: "assistant", Content: client.NewTextContent("hi there")},
		},
	}

	if err := idx.UpsertSession(sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	summaries, err := idx.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 session, got %d", len(summaries))
	}
	if summaries[0].ID != "sess-1" {
		t.Errorf("expected ID 'sess-1', got %q", summaries[0].ID)
	}
	if summaries[0].Title != "First session" {
		t.Errorf("expected title 'First session', got %q", summaries[0].Title)
	}
	if summaries[0].MsgCount != 2 {
		t.Errorf("expected 2 messages, got %d", summaries[0].MsgCount)
	}
}

func TestIndex_ListOrder(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer idx.Close()

	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	if err := idx.UpsertSession(&Session{
		ID: "old", Title: "Old session", CreatedAt: older, UpdatedAt: older,
	}); err != nil {
		t.Fatal(err)
	}
	if err := idx.UpsertSession(&Session{
		ID: "new", Title: "New session", CreatedAt: newer, UpdatedAt: newer,
	}); err != nil {
		t.Fatal(err)
	}

	summaries, err := idx.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2, got %d", len(summaries))
	}
	if summaries[0].ID != "new" {
		t.Errorf("expected newest first, got %q", summaries[0].ID)
	}
	if summaries[1].ID != "old" {
		t.Errorf("expected oldest second, got %q", summaries[1].ID)
	}
}

func TestIndex_Search(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer idx.Close()

	now := time.Now().Truncate(time.Second)
	sess := &Session{
		ID: "search-1", Title: "Search test", CreatedAt: now, UpdatedAt: now,
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("tell me about websocket reconnect logic")},
			{Role: "assistant", Content: client.NewTextContent("the reconnect uses exponential backoff")},
		},
	}
	if err := idx.UpsertSession(sess); err != nil {
		t.Fatal(err)
	}

	results, err := idx.Search("websocket", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if results[0].SessionID != "search-1" {
		t.Errorf("expected session 'search-1', got %q", results[0].SessionID)
	}
	if results[0].SessionTitle != "Search test" {
		t.Errorf("expected title 'Search test', got %q", results[0].SessionTitle)
	}
	if results[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", results[0].Role)
	}
	if !strings.Contains(results[0].Snippet, "websocket") {
		t.Errorf("snippet should contain 'websocket', got %q", results[0].Snippet)
	}
}

func TestIndex_SearchStemming(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer idx.Close()

	now := time.Now().Truncate(time.Second)
	sess := &Session{
		ID: "stem-1", Title: "Stemming", CreatedAt: now, UpdatedAt: now,
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("the server is running on port 8080")},
		},
	}
	if err := idx.UpsertSession(sess); err != nil {
		t.Fatal(err)
	}

	results, err := idx.Search("run", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected stemmed match for 'run' -> 'running'")
	}
}

func TestIndex_SearchNoResults(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer idx.Close()

	now := time.Now().Truncate(time.Second)
	if err := idx.UpsertSession(&Session{
		ID: "no-match", Title: "No match", CreatedAt: now, UpdatedAt: now,
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("hello world")},
		},
	}); err != nil {
		t.Fatal(err)
	}

	results, err := idx.Search("xyzzynonexistent", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestIndex_SearchPhraseQuery(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer idx.Close()

	now := time.Now().Truncate(time.Second)
	if err := idx.UpsertSession(&Session{
		ID: "phrase-1", Title: "Phrase test", CreatedAt: now, UpdatedAt: now,
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("fix the websocket reconnect issue")},
			{Role: "user", Content: client.NewTextContent("websocket is fine but reconnect is broken elsewhere")},
		},
	}); err != nil {
		t.Fatal(err)
	}

	results, err := idx.Search(`"websocket reconnect"`, 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected phrase match")
	}
	// The phrase "websocket reconnect" only appears adjacent in msg_index 0
	if results[0].MsgIndex != 0 {
		t.Errorf("expected msg_index 0 for phrase match, got %d", results[0].MsgIndex)
	}
}

func TestIndex_SearchMalformed(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer idx.Close()

	// Insert data so FTS table is non-empty (empty FTS skips MATCH evaluation)
	now := time.Now().Truncate(time.Second)
	if err := idx.UpsertSession(&Session{
		ID: "mal-1", Title: "Malformed", CreatedAt: now, UpdatedAt: now,
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("some content")},
		},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = idx.Search(`"unbalanced`, 20)
	if err == nil {
		t.Fatal("expected error for malformed query")
	}
	if !strings.Contains(err.Error(), "invalid search query") {
		t.Errorf("expected clean error message, got: %v", err)
	}
}

func TestIndex_Delete(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer idx.Close()

	now := time.Now().Truncate(time.Second)
	sess := &Session{
		ID: "del-1", Title: "Delete me", CreatedAt: now, UpdatedAt: now,
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("unique deletable content")},
		},
	}
	if err := idx.UpsertSession(sess); err != nil {
		t.Fatal(err)
	}

	if err := idx.DeleteSession("del-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	// Verify gone from list
	summaries, err := idx.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 0 {
		t.Errorf("expected 0 sessions after delete, got %d", len(summaries))
	}

	// Verify gone from FTS
	results, err := idx.Search("deletable", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 FTS results after delete, got %d", len(results))
	}
}

func TestIndex_UpsertUpdatesExisting(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer idx.Close()

	now := time.Now().Truncate(time.Second)

	// First upsert
	if err := idx.UpsertSession(&Session{
		ID: "upd-1", Title: "Original", CreatedAt: now, UpdatedAt: now,
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("original content alpha")},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Second upsert with updated title and new messages
	if err := idx.UpsertSession(&Session{
		ID: "upd-1", Title: "Updated", CreatedAt: now, UpdatedAt: now.Add(time.Minute),
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("original content alpha")},
			{Role: "assistant", Content: client.NewTextContent("new content bravo")},
		},
	}); err != nil {
		t.Fatal(err)
	}

	summaries, err := idx.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 session, got %d", len(summaries))
	}
	if summaries[0].Title != "Updated" {
		t.Errorf("expected title 'Updated', got %q", summaries[0].Title)
	}
	if summaries[0].MsgCount != 2 {
		t.Errorf("expected 2 messages, got %d", summaries[0].MsgCount)
	}

	// Both terms should be searchable
	r1, err := idx.Search("alpha", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1) == 0 {
		t.Error("expected 'alpha' to still be searchable")
	}

	r2, err := idx.Search("bravo", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2) == 0 {
		t.Error("expected 'bravo' to be searchable after upsert")
	}
}

func TestIndex_Rebuild(t *testing.T) {
	dir := t.TempDir()
	store := &Store{dir: dir}

	// Create sessions via Store (JSON files)
	now := time.Now().Truncate(time.Second)
	s1 := &Session{
		ID: "rb-1", Title: "Rebuild one", CreatedAt: now, UpdatedAt: now,
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("rebuild test alpha")},
		},
	}
	s2 := &Session{
		ID: "rb-2", Title: "Rebuild two", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("rebuild test bravo")},
		},
	}
	if err := store.Save(s1); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(s2); err != nil {
		t.Fatal(err)
	}

	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	if err := idx.Rebuild(store); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	summaries, err := idx.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 sessions after rebuild, got %d", len(summaries))
	}

	// Verify FTS works
	results, err := idx.Search("bravo", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected FTS results after rebuild")
	}
}

func TestIndex_LatestUpdated(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	if err := idx.UpsertSession(&Session{
		ID: "old", Title: "Old", CreatedAt: t1, UpdatedAt: t1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := idx.UpsertSession(&Session{
		ID: "new", Title: "New", CreatedAt: t1, UpdatedAt: t2,
	}); err != nil {
		t.Fatal(err)
	}

	id, err := idx.LatestUpdatedID()
	if err != nil {
		t.Fatalf("LatestUpdatedID: %v", err)
	}
	if id != "new" {
		t.Errorf("expected 'new', got %q", id)
	}
}

func TestIndex_SearchFTSSyntaxError(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer idx.Close()

	// Insert data so FTS actually runs the query
	idx.UpsertSession(&Session{
		ID: "data", Title: "Data", CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Messages: []client.Message{
			{Role: "user", Content: client.NewTextContent("some data")},
		},
	})

	// Various malformed queries
	badQueries := []string{
		`"unbalanced`,
		`AND`,
		`OR OR`,
		`NOT`,
	}
	for _, q := range badQueries {
		_, err := idx.Search(q, 10)
		if err == nil {
			continue // some "bad" queries may be valid in FTS5, that's OK
		}
		// If it errors, the message should be clean (not raw sqlite)
		if !strings.Contains(err.Error(), "invalid search query") {
			t.Errorf("query %q: expected clean error, got: %v", q, err)
		}
	}
}

func TestIndex_IsEmpty(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	empty, err := idx.IsEmpty()
	if err != nil {
		t.Fatal(err)
	}
	if !empty {
		t.Error("expected empty index")
	}

	now := time.Now().Truncate(time.Second)
	if err := idx.UpsertSession(&Session{
		ID: "x", Title: "X", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	empty, err = idx.IsEmpty()
	if err != nil {
		t.Fatal(err)
	}
	if empty {
		t.Error("expected non-empty index after insert")
	}
}
