package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePinchtab returns a test server that mimics pinchtab's HTTP API.
func fakePinchtab(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/navigate", func(w http.ResponseWriter, r *http.Request) {
		var req ptNavigateReq
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(ptNavigateResp{
			TabID: "tab_test123",
			URL:   req.URL,
			Title: "Test Page",
		})
	})

	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("filter")
		nodes := []ptSnapshotNode{
			{Ref: "e0", Role: "link", Name: "Home", Depth: 0},
			{Ref: "e1", Role: "button", Name: "Submit", Depth: 0},
			{Ref: "e2", Role: "textbox", Name: "Search", Depth: 0, Value: ""},
		}
		if filter == "interactive" {
			// same for this mock
		}
		json.NewEncoder(w).Encode(ptSnapshotResp{
			URL:   "https://example.com",
			Title: "Test Page",
			Nodes: nodes,
			Count: len(nodes),
		})
	})

	mux.HandleFunc("/find", func(w http.ResponseWriter, r *http.Request) {
		var req ptFindReq
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(ptFindResp{
			BestRef:    "e1",
			Confidence: "high",
			Score:      0.95,
			Matches: []ptFindMatch{
				{Ref: "e1", Score: 0.95, Role: "button", Name: "Submit"},
			},
		})
	})

	mux.HandleFunc("/action", func(w http.ResponseWriter, r *http.Request) {
		var req ptActionReq
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(ptActionResp{
			Success: true,
			Result:  map[string]any{"clicked": true, "kind": req.Kind},
		})
	})

	mux.HandleFunc("/text", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ptTextResp{
			URL:   "https://example.com",
			Title: "Test Page",
			Text:  "Hello from the test page.",
		})
	})

	mux.HandleFunc("/evaluate", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ptEvalResp{Result: "Test Page"})
	})

	mux.HandleFunc("/screenshot", func(w http.ResponseWriter, r *http.Request) {
		// Return a minimal valid JPEG (SOI + EOI markers)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0xD9})
	})

	mux.HandleFunc("/shutdown", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "shutting down"})
	})

	return httptest.NewServer(mux)
}

// newToolWithFakePinchtab creates a BrowserTool pre-wired to a fake pinchtab server.
func newToolWithFakePinchtab(t *testing.T, srv *httptest.Server) *BrowserTool {
	t.Helper()
	pt := &pinchtabClient{
		base: srv.URL,
		http: srv.Client(),
	}
	return &BrowserTool{
		backend: backendPinchtab,
		pt:      pt,
	}
}

// --- Test 1: snapshot/find on pinchtab path ---

func TestPinchtab_Snapshot(t *testing.T) {
	srv := fakePinchtab(t)
	defer srv.Close()
	tool := newToolWithFakePinchtab(t, srv)
	defer tool.Cleanup()

	// Navigate first to get a tabID
	result, err := tool.Run(context.Background(), `{"action":"navigate","url":"https://example.com"}`)
	if err != nil {
		t.Fatalf("navigate error: %v", err)
	}
	if result.IsError {
		t.Fatalf("navigate failed: %s", result.Content)
	}
	if !contains(result.Content, "tab_test123") {
		t.Errorf("expected tabID in output, got: %s", result.Content)
	}

	// Snapshot
	result, err = tool.Run(context.Background(), `{"action":"snapshot","filter":"interactive"}`)
	if err != nil {
		t.Fatalf("snapshot error: %v", err)
	}
	if result.IsError {
		t.Fatalf("snapshot failed: %s", result.Content)
	}

	// Should contain element refs
	if !contains(result.Content, "[e0]") {
		t.Errorf("expected ref [e0] in snapshot, got: %s", result.Content)
	}
	if !contains(result.Content, "[e1]") {
		t.Errorf("expected ref [e1] in snapshot, got: %s", result.Content)
	}
	if !contains(result.Content, "button") {
		t.Errorf("expected role 'button' in snapshot, got: %s", result.Content)
	}
	if !contains(result.Content, "Elements: 3") {
		t.Errorf("expected element count in snapshot, got: %s", result.Content)
	}
}

func TestPinchtab_Find(t *testing.T) {
	srv := fakePinchtab(t)
	defer srv.Close()
	tool := newToolWithFakePinchtab(t, srv)
	defer tool.Cleanup()

	result, err := tool.Run(context.Background(), `{"action":"find","query":"submit button"}`)
	if err != nil {
		t.Fatalf("find error: %v", err)
	}
	if result.IsError {
		t.Fatalf("find failed: %s", result.Content)
	}
	if !contains(result.Content, "e1") {
		t.Errorf("expected best ref e1, got: %s", result.Content)
	}
	if !contains(result.Content, "high") {
		t.Errorf("expected confidence 'high', got: %s", result.Content)
	}
}

func TestPinchtab_ClickByRef(t *testing.T) {
	srv := fakePinchtab(t)
	defer srv.Close()
	tool := newToolWithFakePinchtab(t, srv)
	defer tool.Cleanup()

	result, err := tool.Run(context.Background(), `{"action":"click","ref":"e1"}`)
	if err != nil {
		t.Fatalf("click error: %v", err)
	}
	if result.IsError {
		t.Fatalf("click failed: %s", result.Content)
	}
	if !contains(result.Content, "Clicked: e1") {
		t.Errorf("expected 'Clicked: e1', got: %s", result.Content)
	}
}

func TestPinchtab_ClickWithKey(t *testing.T) {
	srv := fakePinchtab(t)
	defer srv.Close()
	tool := newToolWithFakePinchtab(t, srv)
	defer tool.Cleanup()

	// click with key should dispatch as "press" kind
	result, err := tool.Run(context.Background(), `{"action":"click","ref":"e2","key":"Enter"}`)
	if err != nil {
		t.Fatalf("click+key error: %v", err)
	}
	if result.IsError {
		t.Fatalf("click+key failed: %s", result.Content)
	}
}

func TestPinchtab_ClickWithValue(t *testing.T) {
	srv := fakePinchtab(t)
	defer srv.Close()
	tool := newToolWithFakePinchtab(t, srv)
	defer tool.Cleanup()

	// click with value should dispatch as "select" kind
	result, err := tool.Run(context.Background(), `{"action":"click","ref":"e2","value":"option1"}`)
	if err != nil {
		t.Fatalf("click+value error: %v", err)
	}
	if result.IsError {
		t.Fatalf("click+value failed: %s", result.Content)
	}
}

func TestPinchtab_ScreenshotFeedsVision(t *testing.T) {
	srv := fakePinchtab(t)
	defer srv.Close()
	tool := newToolWithFakePinchtab(t, srv)
	defer tool.Cleanup()

	result, err := tool.Run(context.Background(), `{"action":"screenshot"}`)
	if err != nil {
		t.Fatalf("screenshot error: %v", err)
	}
	if result.IsError {
		t.Fatalf("screenshot failed: %s", result.Content)
	}
	if len(result.Images) == 0 {
		t.Error("expected screenshot to populate Images for vision loop")
	}
	if result.Images[0].MediaType != "image/jpeg" {
		t.Errorf("expected image/jpeg, got: %s", result.Images[0].MediaType)
	}
}

// --- Test 2: fallback-to-chromedp transition ---

func TestPinchtab_SnapshotFallbackError(t *testing.T) {
	// No pinchtab server — tool should be on chromedp backend.
	// snapshot/find should return clear errors, not panic.
	tool := &BrowserTool{backend: backendChromedp}

	result, err := tool.Run(context.Background(), `{"action":"snapshot"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error for snapshot on chromedp fallback")
	}
	if !contains(result.Content, "requires pinchtab") {
		t.Errorf("expected pinchtab-required message, got: %s", result.Content)
	}
}

func TestPinchtab_FindFallbackError(t *testing.T) {
	tool := &BrowserTool{backend: backendChromedp}

	result, err := tool.Run(context.Background(), `{"action":"find","query":"search"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error for find on chromedp fallback")
	}
	if !contains(result.Content, "requires pinchtab") {
		t.Errorf("expected pinchtab-required message, got: %s", result.Content)
	}
}

func TestPinchtab_FallbackTransition_ClearsTabID(t *testing.T) {
	// Simulate: pinchtab was running with a tabID, then goes unhealthy.
	// On next ensureBackend, tabID should be cleared.
	srv := fakePinchtab(t)
	tool := newToolWithFakePinchtab(t, srv)
	tool.tabID = "tab_old_stale"

	// Kill the fake server to simulate pinchtab dying
	srv.Close()

	// ensureBackend should detect unhealthy, clear tabID, fall to chromedp attempt
	// (which will also fail in test env since no Chrome — that's ok, we test the tabID clear)
	_ = tool.ensureBackend(context.Background())

	if tool.tabID != "" {
		t.Errorf("expected tabID to be cleared after pinchtab failure, got: %q", tool.tabID)
	}
}

// --- Test 3: close after pinchtab terminates mid-run ---

func TestPinchtab_CloseAfterServerDies(t *testing.T) {
	srv := fakePinchtab(t)
	tool := newToolWithFakePinchtab(t, srv)
	tool.tabID = "tab_test123"

	// Simulate pinchtab dying mid-run
	srv.Close()

	// close should not panic, should report success
	result, err := tool.Run(context.Background(), `{"action":"close"}`)
	if err != nil {
		t.Fatalf("close error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected clean close, got error: %s", result.Content)
	}

	// Backend should be reset
	if tool.backend != backendNone {
		t.Errorf("expected backendNone after close, got: %d", tool.backend)
	}
	if tool.tabID != "" {
		t.Errorf("expected tabID cleared after close, got: %q", tool.tabID)
	}
}

func TestPinchtab_CloseWhenPinchtabNeverStarted(t *testing.T) {
	// BrowserTool with pinchtab client that was never connected
	tool := &BrowserTool{
		pt: newPinchtabClient(),
	}

	result, err := tool.Run(context.Background(), `{"action":"close"}`)
	if err != nil {
		t.Fatalf("close error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected clean close, got error: %s", result.Content)
	}
	if !contains(result.Content, "not running") {
		t.Errorf("expected 'not running', got: %s", result.Content)
	}
}

// --- Test: new params in Info ---

func TestBrowser_InfoNewParams(t *testing.T) {
	tool := &BrowserTool{}
	info := tool.Info()
	props := info.Parameters["properties"].(map[string]any)

	newParams := []string{"ref", "query", "filter", "key", "value"}
	for _, p := range newParams {
		if _, exists := props[p]; !exists {
			t.Errorf("expected parameter %q in properties", p)
		}
	}

	if !strings.Contains(info.Description, "snapshot") {
		t.Error("expected description to mention snapshot")
	}
	if !strings.Contains(info.Description, "find") {
		t.Error("expected description to mention find")
	}
}
