package context

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

// TestConsolidateMemory_LLMQuality calls the real gateway to verify the
// consolidation prompt produces quality output from realistic duplicated input.
//
// Run with: go test ./internal/context/ -run TestConsolidateMemory_LLMQuality -v
// Requires: SHANNON_ENDPOINT and SHANNON_API_KEY env vars (or ~/.shannon/config.yaml)
func TestConsolidateMemory_LLMQuality(t *testing.T) {
	endpoint := os.Getenv("SHANNON_ENDPOINT")
	apiKey := os.Getenv("SHANNON_API_KEY")

	if endpoint == "" {
		endpoint = "https://api-dev.shannon.run"
	}
	if apiKey == "" {
		apiKey = os.Getenv("SHANNON_API_KEY")
	}
	if apiKey == "" {
		t.Skip("Skipping LLM quality test: no API key (set SHANNON_API_KEY)")
	}

	// Build realistic input: 13 auto-persisted files with heavy duplication
	var input strings.Builder

	// Inline auto section from MEMORY.md
	input.WriteString("## Auto-persisted (2026-03-01 10:00)\n\n")
	input.WriteString("- Auth tokens expire after 1 hour\n")
	input.WriteString("- Database connection pool max is 20\n")
	input.WriteString("- Slack webhook URL is stored in 1Password vault \"Ops\"\n")
	input.WriteString("- Deploy target is us-west-2 EKS cluster\n\n")

	// 13 detail files with duplicates, variations, and stale entries
	entries := []struct {
		date    string
		facts   []string
	}{
		{"2026-03-01", []string{
			"Auth tokens expire after 1 hour",
			"Database pool max is 20 connections",
			"User prefers verbose logging during debugging",
			"Deployed v1.2.1 to staging",
		}},
		{"2026-03-02", []string{
			"Auth tokens expire after 1 hour (session management critical)",
			"Database connection pool max is 20",
			"Found race condition in session handler — fixed with mutex",
			"Deployed v1.2.2 to staging",
		}},
		{"2026-03-03", []string{
			"Auth tokens expire hourly — refresh logic in auth/middleware.go",
			"Database pool max 20, consider increasing to 50 for prod",
			"User asked about Kubernetes HPA for API pods",
			"Redis cache TTL is 5 minutes for session data",
		}},
		{"2026-03-04", []string{
			"Auth token expiry is 1 hour",
			"DB pool max 20 connections",
			"Kubernetes HPA configured: min 2, max 10, target CPU 70%",
			"Deployed v1.2.3 to staging",
		}},
		{"2026-03-05", []string{
			"Auth tokens: 1 hour expiry, refresh via /auth/refresh endpoint",
			"Database pool: 20 connections max",
			"Monitoring dashboard at grafana.internal/d/api-health",
			"User mentioned meeting with security team on March 10",
		}},
		{"2026-03-06", []string{
			"Auth tokens expire after 1 hour",
			"DB connection pool max is 20",
			"CI/CD pipeline takes ~8 minutes for full test suite",
			"Deployed v1.2.4 to staging — rollback needed due to migration bug",
		}},
		{"2026-03-07", []string{
			"Auth token TTL: 1 hour",
			"Database pool: 20 max connections",
			"Migration bug in v1.2.4 fixed — column type mismatch in orders table",
			"Deployed v1.2.5 to staging (fix for migration)",
		}},
		{"2026-03-08", []string{
			"Auth tokens: 1h expiry",
			"DB pool 20 max",
			"User prefers dark mode for all UIs",
			"Slack channel #ops-alerts for production incidents",
		}},
		{"2026-03-09", []string{
			"Auth token expiry: 1 hour",
			"Database max connections: 20",
			"Started work on payment integration — Stripe API",
			"API rate limit is 1000 req/min per API key",
		}},
		{"2026-03-10", []string{
			"Auth tokens expire in 1 hour",
			"DB pool max 20",
			"Security review completed — no critical findings",
			"Payment integration: using Stripe checkout sessions",
		}},
		{"2026-03-11", []string{
			"Auth: 1 hour token expiry",
			"Database: 20 connection pool max",
			"Stripe webhook endpoint: /api/webhooks/stripe",
			"Test coverage at 78% — target is 85%",
		}},
		{"2026-03-12", []string{
			"Auth token TTL is 1 hour (unchanged)",
			"DB pool: 20 connections",
			"Payment flow working end-to-end in staging",
			"Deployed v1.3.0 to staging with Stripe integration",
		}},
		{"2026-03-13", []string{
			"Auth tokens: 1h",
			"DB: 20 max connections",
			"v1.3.0 promoted to production",
			"User mentioned next sprint focuses on analytics dashboard",
		}},
	}

	for _, e := range entries {
		fmt.Fprintf(&input, "# Auto-persisted Learnings (%s)\n\n", e.date)
		for _, fact := range e.facts {
			fmt.Fprintf(&input, "- %s\n", fact)
		}
		input.WriteString("\n")
	}

	t.Logf("Input to LLM (%d chars, %d lines):\n%s", input.Len(),
		strings.Count(input.String(), "\n"), input.String())

	// Call real gateway
	gw := client.NewGatewayClient(endpoint, apiKey)

	req := client.CompletionRequest{
		Messages: []client.Message{
			{Role: "system", Content: client.NewTextContent(consolidatePrompt)},
			{Role: "user", Content: client.NewTextContent(input.String())},
		},
		ModelTier:   "small",
		Temperature: 0.2,
		MaxTokens:   2000,
	}

	resp, err := gw.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("LLM call failed: %v", err)
	}

	result := strings.TrimSpace(resp.OutputText)
	t.Logf("\n=== LLM Consolidation Output ===\n%s\n===", result)

	// Quality checks
	lines := strings.Split(result, "\n")
	bulletCount := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			bulletCount++
		}
	}

	t.Logf("Output: %d lines, %d bullets", len(lines), bulletCount)

	// Should deduplicate: "auth tokens expire after 1 hour" appears 13 times
	// in the input but should appear at most once in output
	authCount := 0
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "auth") && strings.Contains(lower, "token") {
			authCount++
		}
	}
	if authCount > 2 {
		t.Errorf("Auth token fact appears %d times — should be deduplicated to 1-2 mentions", authCount)
	}

	// Should deduplicate: "database pool max 20" appears 13 times
	dbCount := 0
	for _, line := range lines {
		lower := strings.ToLower(line)
		if (strings.Contains(lower, "database") || strings.Contains(lower, "db")) &&
			strings.Contains(lower, "20") {
			dbCount++
		}
	}
	if dbCount > 2 {
		t.Errorf("DB pool fact appears %d times — should be deduplicated to 1-2 mentions", dbCount)
	}

	// Should be under 100 lines (prompt says "target under 100 lines")
	if len(lines) > 100 {
		t.Errorf("Output has %d lines — should be under 100", len(lines))
	}

	// Should NOT be NONE (there are real facts worth keeping)
	if strings.EqualFold(result, "NONE") {
		t.Error("LLM returned NONE — there are clearly facts worth keeping")
	}

	// Should contain key non-duplicate facts
	mustContainAny := []struct {
		desc     string
		keywords []string
	}{
		{"Stripe/payment integration", []string{"stripe", "payment"}},
		{"Kubernetes/HPA", []string{"kubernetes", "hpa", "pod scaling"}},
		{"Grafana dashboard", []string{"grafana", "dashboard", "monitoring"}},
		{"Slack ops channel", []string{"slack", "ops-alerts", "#ops"}},
	}

	for _, check := range mustContainAny {
		found := false
		lower := strings.ToLower(result)
		for _, kw := range check.keywords {
			if strings.Contains(lower, kw) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Missing important fact: %s (looked for: %v)", check.desc, check.keywords)
		}
	}
}
