//go:build darwin && !ios && cgo

package koe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/executionprofile"
)

func TestDaemonClientTokenCanChangeDuringAuthorization(t *testing.T) {
	client := NewDaemonClient("http://127.0.0.1:7533")
	var wg sync.WaitGroup
	for attempt := 0; attempt < 100; attempt++ {
		wg.Add(2)
		go func(token string) {
			defer wg.Done()
			client.SetToken(token)
		}(fmt.Sprintf("token-%d", attempt))
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, client.baseURL+"/status", nil)
			if err != nil {
				t.Errorf("new request: %v", err)
				return
			}
			client.authorize(req)
		}()
	}
	wg.Wait()
}

func TestDoTaskCompleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/message" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var got DoTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		if got.Source != "koe" {
			t.Errorf("source = %q, want \"koe\"", got.Source)
		}
		if got.Agent != "finance" || got.ThreadID != "burst-1" || got.Text != "check NVDA" {
			t.Errorf("unexpected req: %+v", got)
		}
		if got.ForegroundHint == nil || got.ForegroundHint.AppName != "Mail" || got.ForegroundHint.BundleID != "com.apple.mail" {
			t.Errorf("foreground hint = %+v", got.ForegroundHint)
		}
		if got.ExecutionMode != executionprofile.ModeFull ||
			got.RequestedExecutionMode == nil || *got.RequestedExecutionMode != "fast" ||
			got.FullReason != executionprofile.FullReasonProductionIncident {
			t.Errorf("mode admission wire = %+v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"reply":          "NVDA is up two percent today.\n\nFull table omitted here.",
			"spoken_summary": "NVDA is up two percent today.",
			"session_id":     "s1", "agent": "finance",
			"deliverables": []map[string]any{{"id": "d1", "path": "/private/report.html", "filename": "report.html", "title": "NVDA report", "mime": "text/html", "byte_size": 2048}},
		})
	}))
	defer srv.Close()

	c := NewDaemonClient(srv.URL)
	requestedMode := "fast"
	out, err := c.DoTask(context.Background(), DoTaskRequest{
		Text:                   "check NVDA",
		Agent:                  "finance",
		ThreadID:               "burst-1",
		ForegroundHint:         &ForegroundHint{AppName: "Mail", BundleID: "com.apple.mail"},
		ExecutionMode:          executionprofile.ModeFull,
		RequestedExecutionMode: &requestedMode,
		FullReason:             executionprofile.FullReasonProductionIncident,
	})
	if err != nil {
		t.Fatalf("DoTask: %v", err)
	}
	if out.Kind != OutcomeCompleted {
		t.Fatalf("Kind = %v, want OutcomeCompleted", out.Kind)
	}
	if out.Reply != "NVDA is up two percent today.\n\nFull table omitted here." {
		t.Fatalf("Reply = %q", out.Reply)
	}
	if out.SpokenSummary != "NVDA is up two percent today." {
		t.Fatalf("SpokenSummary = %q", out.SpokenSummary)
	}
	if len(out.Deliverables) != 1 || out.Deliverables[0].Filename != "report.html" || out.Deliverables[0].ByteSize != 2048 {
		t.Fatalf("Deliverables = %+v", out.Deliverables)
	}
}

func TestDoTaskInjected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "injected", "route": "agent:finance:koe:burst-1"})
	}))
	defer srv.Close()

	c := NewDaemonClient(srv.URL)
	out, err := c.DoTask(context.Background(), DoTaskRequest{Text: "make it 6pm", Agent: "finance", ThreadID: "burst-1"})
	if err != nil {
		t.Fatalf("DoTask: %v", err)
	}
	if out.Kind != OutcomeInjected {
		t.Fatalf("Kind = %v, want OutcomeInjected", out.Kind)
	}
}

func TestDoTaskRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"status": "rejected", "reason": "cwd_conflict", "route": "r"})
	}))
	defer srv.Close()

	c := NewDaemonClient(srv.URL)
	out, err := c.DoTask(context.Background(), DoTaskRequest{Text: "x", Agent: "finance", ThreadID: "burst-1"})
	if err != nil {
		t.Fatalf("DoTask should not error on a structured rejection: %v", err)
	}
	if out.Kind != OutcomeRejected || out.Reason != "cwd_conflict" {
		t.Errorf("Kind=%v Reason=%q, want OutcomeRejected/cwd_conflict", out.Kind, out.Reason)
	}
}

func TestCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cancel" {
			t.Errorf("path = %s, want /cancel", r.URL.Path)
		}
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		if got["route_key"] != "agent:finance:koe:burst-1" || got["reason"] != "user_cancel" {
			t.Errorf("unexpected cancel body: %v", got)
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": "cancelled"})
	}))
	defer srv.Close()

	c := NewDaemonClient(srv.URL)
	if err := c.Cancel(context.Background(), CancelRequest{RouteKey: "agent:finance:koe:burst-1", Reason: "user_cancel"}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

func TestCancelRejectsUnknownReason(t *testing.T) {
	c := NewDaemonClient("http://127.0.0.1:0")
	err := c.Cancel(context.Background(), CancelRequest{RouteKey: "r", Reason: "bogus"})
	if err == nil {
		t.Fatal("Cancel with unknown reason should error before hitting the network")
	}
}

func TestListAgents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agents" {
			t.Errorf("path = %s, want /agents", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"agents": []map[string]any{
				{"name": "finance", "display_name": "金融分析 agent", "description": map[string]string{"en": "markets", "zh": "金融"}},
				{"name": "default", "display_name": "Kocoro"},
			},
		})
	}))
	defer srv.Close()

	c := NewDaemonClient(srv.URL)
	agents, err := c.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
	if agents[0].Slug != "finance" || agents[0].DisplayName != "金融分析 agent" {
		t.Errorf("agent[0] = %+v", agents[0])
	}
	if agents[0].Description["zh"] != "金融" {
		t.Errorf("agent[0].Description[zh] = %q, want 金融", agents[0].Description["zh"])
	}
}
