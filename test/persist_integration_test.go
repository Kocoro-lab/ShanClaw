package test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kocoro-lab/shan/internal/client"
	"github.com/Kocoro-lab/shan/internal/config"
	ctxwin "github.com/Kocoro-lab/shan/internal/context"
)

// TestPersistLearningsIntegration tests PersistLearnings with a real LLM call.
// Requires SHANNON_API_KEY or valid config. Skip if not available.
func TestPersistLearningsIntegration(t *testing.T) {
	cfg, err := config.Load()
	if err != nil || cfg.APIKey == "" {
		t.Skip("skipping: no API key configured")
	}

	gw := client.NewGatewayClient(cfg.Endpoint, cfg.APIKey)

	// Simulate a conversation with content worth remembering
	messages := []client.Message{
		{Role: "system", Content: client.NewTextContent("You are an assistant.")},
		{Role: "user", Content: client.NewTextContent("I prefer Go over Python for backend services. Also, our deployment pipeline uses ArgoCD with Helm charts. The staging cluster is at k8s-staging.internal.company.com.")},
		{Role: "assistant", Content: client.NewTextContent("Got it! I'll keep those preferences in mind. Go for backend, ArgoCD+Helm for deployments, and staging at k8s-staging.internal.company.com.")},
		{Role: "user", Content: client.NewTextContent("One more thing - never use fmt.Println in production code, always use structured logging with zerolog.")},
		{Role: "assistant", Content: client.NewTextContent("Understood - zerolog for structured logging, no fmt.Println in production.")},
	}

	t.Run("extracts and writes learnings", func(t *testing.T) {
		dir := t.TempDir()

		err := ctxwin.PersistLearnings(context.Background(), gw, messages, dir)
		if err != nil {
			t.Fatalf("PersistLearnings failed: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
		if err != nil {
			t.Fatalf("MEMORY.md not created: %v", err)
		}

		content := string(data)
		t.Logf("MEMORY.md content:\n%s", content)

		// Should contain at least some of the key facts
		if !strings.Contains(strings.ToLower(content), "go") && !strings.Contains(strings.ToLower(content), "argocd") && !strings.Contains(strings.ToLower(content), "zerolog") {
			t.Error("should contain at least one extracted learning (Go, ArgoCD, or zerolog)")
		}
	})

	t.Run("avoids duplicating existing memory", func(t *testing.T) {
		dir := t.TempDir()

		// Pre-seed with existing memory
		existing := "# Memory\n\n- User prefers Go over Python for backend\n- Deployment uses ArgoCD with Helm\n"
		os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(existing), 0644)

		err := ctxwin.PersistLearnings(context.Background(), gw, messages, dir)
		if err != nil {
			t.Fatalf("PersistLearnings failed: %v", err)
		}

		data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
		content := string(data)
		t.Logf("MEMORY.md content (with existing):\n%s", content)

		// Count occurrences of "Go" preference — should not be duplicated heavily
		goCount := strings.Count(strings.ToLower(content), "go over python")
		if goCount > 1 {
			t.Errorf("should not duplicate existing memory, found 'go over python' %d times", goCount)
		}
	})

	t.Run("overflows to detail file when near limit", func(t *testing.T) {
		dir := t.TempDir()

		// Create a large MEMORY.md near the limit
		var lines []string
		for i := 0; i < 148; i++ {
			lines = append(lines, "- existing fact line")
		}
		os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(strings.Join(lines, "\n")), 0644)

		err := ctxwin.PersistLearnings(context.Background(), gw, messages, dir)
		if err != nil {
			t.Fatalf("PersistLearnings failed: %v", err)
		}

		// Check for detail file
		entries, _ := os.ReadDir(dir)
		var detailFiles []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "auto-") {
				detailFiles = append(detailFiles, e.Name())
			}
		}

		if len(detailFiles) == 0 {
			t.Error("should have created a detail file when MEMORY.md is near limit")
		} else {
			t.Logf("Detail file created: %s", detailFiles[0])
			detailData, _ := os.ReadFile(filepath.Join(dir, detailFiles[0]))
			t.Logf("Detail file content:\n%s", string(detailData))
		}

		// MEMORY.md should have a pointer to the detail file
		data, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
		content := string(data)
		if len(detailFiles) > 0 && !strings.Contains(content, detailFiles[0]) {
			t.Error("MEMORY.md should reference the detail file")
		}
	})
}
