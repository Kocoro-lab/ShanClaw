package schedule

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestCreateAndList(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "schedules.json"), dir)
	id, err := mgr.Create("ops-bot", "0 9 * * *", "check prod health")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}
	list, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d schedules, want 1", len(list))
	}
	if list[0].Agent != "ops-bot" {
		t.Errorf("agent = %q, want %q", list[0].Agent, "ops-bot")
	}
	if list[0].Cron != "0 9 * * *" {
		t.Errorf("cron = %q, want %q", list[0].Cron, "0 9 * * *")
	}
}

func TestCreateRejectsInvalidCron(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "schedules.json"), dir)
	_, err := mgr.Create("bot", "not-a-cron", "task")
	if err == nil {
		t.Fatal("expected error for invalid cron")
	}
}

func TestCreateRejectsInvalidAgentName(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "schedules.json"), dir)
	_, err := mgr.Create("../evil", "0 9 * * *", "task")
	if err == nil {
		t.Fatal("expected error for invalid agent name")
	}
}

func TestCreateAcceptsEmptyAgent(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "schedules.json"), dir)
	id, err := mgr.Create("", "0 9 * * *", "task")
	if err != nil {
		t.Fatalf("Create with empty agent: %v", err)
	}
	list, _ := mgr.List()
	if list[0].Agent != "" {
		t.Errorf("agent = %q, want empty", list[0].Agent)
	}
	_ = id
}

func TestCreateSupportsCronSyntax(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "schedules.json"), dir)
	crons := []string{
		"*/5 * * * *",
		"0 9-17 * * 1-5",
		"0 9 * * 1,3,5",
		"30 */2 * * *",
	}
	for _, c := range crons {
		_, err := mgr.Create("", c, "task")
		if err != nil {
			t.Errorf("expected valid cron %q, got error: %v", c, err)
		}
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "schedules.json"), dir)
	id, _ := mgr.Create("bot", "0 9 * * *", "task")
	err := mgr.Remove(id)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	list, _ := mgr.List()
	if len(list) != 0 {
		t.Fatalf("got %d schedules after remove, want 0", len(list))
	}
}

func TestRemoveNotFound(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "schedules.json"), dir)
	err := mgr.Remove("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent id")
	}
}

func TestUpdate(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "schedules.json"), dir)
	id, _ := mgr.Create("bot", "0 9 * * *", "old prompt")
	err := mgr.Update(id, &UpdateOpts{Prompt: strPtr("new prompt")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	list, _ := mgr.List()
	if list[0].Prompt != "new prompt" {
		t.Errorf("prompt = %q, want %q", list[0].Prompt, "new prompt")
	}
}

func TestUpdateRejectsInvalidCron(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "schedules.json"), dir)
	id, _ := mgr.Create("bot", "0 9 * * *", "task")
	bad := "not-valid"
	err := mgr.Update(id, &UpdateOpts{Cron: &bad})
	if err == nil {
		t.Fatal("expected error for invalid cron update")
	}
}

func TestEnableDisable(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "schedules.json"), dir)
	id, _ := mgr.Create("bot", "0 9 * * *", "task")
	if err := mgr.Update(id, &UpdateOpts{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	list, _ := mgr.List()
	if list[0].Enabled {
		t.Error("expected disabled")
	}
}

func TestConcurrentCreates(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "schedules.json"), dir)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Create may return plist sync errors (launchctl fails in test env)
			// but the schedule entry should still be written to the index.
			mgr.Create("bot", "0 9 * * *", "task")
		}()
	}
	wg.Wait()
	list, _ := mgr.List()
	if len(list) != 10 {
		t.Errorf("got %d schedules, want 10", len(list))
	}
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
