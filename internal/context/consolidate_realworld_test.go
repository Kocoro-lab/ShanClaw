package context

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

// TestConsolidateMemory_RealWorld exercises the full consolidation flow against
// a realistic memory directory with 13 auto files, mixed MEMORY.md content,
// and a mock LLM that returns a deduplicated result.
// Run with: go test ./internal/context/ -run TestConsolidateMemory_RealWorld -v
func TestConsolidateMemory_RealWorld(t *testing.T) {
	dir := t.TempDir()

	// 1. Create MEMORY.md with user-written + inline auto-persisted content
	memory := `# Agent Memory

- User prefers dark mode for all UIs
- Deploy target is always us-west-2
- Never use force push on main

## Auto-persisted (2026-03-01 10:00)

- Found that auth tokens expire hourly
- Database connection pool max is 20
- Slack webhook URL is in 1Password vault "Ops"

- [2026-03-05] See [auto-2026-03-05-aaa111.md](auto-2026-03-05-aaa111.md) for details
- [2026-03-08] See [auto-2026-03-08-bbb222.md](auto-2026-03-08-bbb222.md) for details
`
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(memory), 0644)

	// 2. Create 13 auto-*.md detail files with realistic content (lots of duplicates)
	for i := 1; i <= 13; i++ {
		name := fmt.Sprintf("auto-2026-03-%02d-%06x.md", i, i)
		content := fmt.Sprintf(`# Auto-persisted Learnings (2026-03-%02d)

- Auth tokens expire after 1 hour (critical for session management)
- Database pool max is 20 connections
- User asked about Kubernetes pod scaling on March %d
- Deployed version v1.2.%d to staging
`, i, i, i)
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
	}

	// Pre-flight checks
	autoFiles, _ := filepath.Glob(filepath.Join(dir, "auto-*.md"))
	t.Logf("Auto files before: %d", len(autoFiles))
	if len(autoFiles) != 13 {
		t.Fatalf("expected 13 auto files, got %d", len(autoFiles))
	}

	// 3. Mock completer simulating LLM dedup output
	mock := &mockCompleter{
		response: &client.CompletionResponse{
			OutputText: `- Auth tokens expire after 1 hour (critical for session management)
- Database connection pool max is 20 connections
- Slack webhook URL is in 1Password vault "Ops"
- Various staging deployments (v1.2.x series) happened March 1-13
- User asked about Kubernetes pod scaling across multiple sessions`,
		},
	}

	// 4. Run consolidation
	err := ConsolidateMemory(context.Background(), mock, dir)
	if err != nil {
		t.Fatalf("ConsolidateMemory failed: %v", err)
	}

	// 5. Verify: auto files deleted
	autoFilesAfter, _ := filepath.Glob(filepath.Join(dir, "auto-*.md"))
	if len(autoFilesAfter) != 0 {
		t.Errorf("expected 0 auto files after consolidation, got %d", len(autoFilesAfter))
	}

	// 6. Verify: MEMORY.md content
	data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	result := string(data)
	t.Logf("MEMORY.md after consolidation:\n%s", result)

	// User content preserved verbatim
	for _, expected := range []string{
		"User prefers dark mode",
		"Deploy target is always us-west-2",
		"Never use force push on main",
	} {
		if !strings.Contains(result, expected) {
			t.Errorf("user content missing: %q", expected)
		}
	}

	// Old auto sections removed
	if strings.Contains(result, "## Auto-persisted") {
		t.Error("old Auto-persisted header should be replaced")
	}
	if strings.Contains(result, "See [auto-") {
		t.Error("pointer lines should be removed")
	}

	// Consolidated section present
	if !strings.Contains(result, "## Auto-consolidated") {
		t.Error("should have Auto-consolidated header")
	}
	if !strings.Contains(result, "Auth tokens expire") {
		t.Error("consolidated content should include deduped auth token fact")
	}

	// LLM input included inline auto content from MEMORY.md
	llmInput := mock.lastReq.Messages[1].Content.Text()
	if !strings.Contains(llmInput, "Found that auth tokens expire hourly") {
		t.Error("LLM input should include inline auto-persisted content from MEMORY.md")
	}
	if !strings.Contains(llmInput, "Deployed version v1.2.1") {
		t.Error("LLM input should include content from auto-*.md detail files")
	}

	// Marker file exists
	if _, err := os.Stat(filepath.Join(dir, ".memory_gc")); os.IsNotExist(err) {
		t.Error(".memory_gc marker should exist after consolidation")
	}

	t.Log("Real-world consolidation test passed.")
}
