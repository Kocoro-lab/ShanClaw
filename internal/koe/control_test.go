//go:build darwin && !ios && cgo

package koe

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// subscriberCount is a test-only accessor (compiled only in tests).
func (s *ControlServer) subscriberCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subscribers)
}

func TestControlServerAuthToken(t *testing.T) {
	started := make(chan struct{}, 1)
	s := NewControlServer(func(StartCallRequest) { started <- struct{}{} }, nil, nil)
	s.SetMicHandler(func(bool) error { return nil })
	s.SetToken("secret-token")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/call/start", "{}"},
		{http.MethodPost, "/call/end", "{}"},
		{http.MethodPost, "/call/interrupt", "{}"},
		{http.MethodPost, "/call/mic", `{"mic":"off"}`},
		{http.MethodGet, "/events", ""},
	}
	for _, tt := range tests {
		req, _ := http.NewRequest(tt.method, srv.URL+tt.path, strings.NewReader(tt.body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s without token: %v", tt.method, tt.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "unauthorized") {
			t.Fatalf("%s %s without token = %d %s, want 401 unauthorized", tt.method, tt.path, resp.StatusCode, body)
		}

		req, _ = http.NewRequest(tt.method, srv.URL+tt.path, strings.NewReader(tt.body))
		req.Header.Set("Authorization", "Bearer wrong-token")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s with wrong token: %v", tt.method, tt.path, err)
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "unauthorized") {
			t.Fatalf("%s %s with wrong token = %d %s, want 401 unauthorized", tt.method, tt.path, resp.StatusCode, body)
		}

		req, _ = http.NewRequest(tt.method, srv.URL+tt.path, strings.NewReader(tt.body))
		req.Header.Set("Authorization", "Bearer secret-token")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s with token: %v", tt.method, tt.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s with token = %d, want 200", tt.method, tt.path, resp.StatusCode)
		}
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("authorized /call/start did not invoke onStart")
	}
	select {
	case <-started:
		t.Fatal("unauthorized /call/start invoked onStart")
	default:
	}
}

func TestControlServerEmptyTokenAllowsRequests(t *testing.T) {
	s := NewControlServer(nil, nil, nil)
	s.SetToken("")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/call/end", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /call/end: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty token status=%d, want 200", resp.StatusCode)
	}
}

func TestControlServerStartEnd(t *testing.T) {
	started := make(chan struct{}, 1)
	var ended, interrupted bool
	s := NewControlServer(
		func(StartCallRequest) { started <- struct{}{} },
		func() { ended = true },
		func() { interrupted = true },
	)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/call/start", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /call/start: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Error("POST /call/start did not invoke onStart")
	}
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("start response = %s", body)
	}

	resp2, err := http.Post(srv.URL+"/call/end", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /call/end: %v", err)
	}
	resp2.Body.Close()
	if !ended {
		t.Error("POST /call/end did not invoke onEnd")
	}

	resp3, err := http.Post(srv.URL+"/call/interrupt", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /call/interrupt: %v", err)
	}
	resp3.Body.Close()
	if !interrupted {
		t.Error("POST /call/interrupt did not invoke onInterrupt")
	}
}

func TestControlServerStartDoesNotBlockHTTP(t *testing.T) {
	entered := make(chan struct{}, 1)
	block := make(chan struct{})
	s := NewControlServer(func(StartCallRequest) {
		entered <- struct{}{}
		<-block
	}, nil, nil)
	defer close(block)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Post(srv.URL+"/call/start", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /call/start should return while start work continues: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("start response = %s", body)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("onStart was not invoked")
	}
}

func TestControlServerStartCarriesContext(t *testing.T) {
	got := make(chan StartCallRequest, 1)
	s := NewControlServer(func(req StartCallRequest) { got <- req }, nil, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	body := `{"command":"start_call","cwd":"/Users/hu/project","foreground_hint":{"pid":123,"app_name":"Mail","bundle_id":"com.apple.mail"}}`
	resp, err := http.Post(srv.URL+"/call/start", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /call/start: %v", err)
	}
	resp.Body.Close()

	req := <-got
	if req.CWD != "/Users/hu/project" || req.ForegroundHint == nil {
		t.Fatalf("start context = %+v", req)
	}
	if req.ForegroundHint.PID != 123 || req.ForegroundHint.AppName != "Mail" || req.ForegroundHint.BundleID != "com.apple.mail" {
		t.Fatalf("foreground hint = %+v", req.ForegroundHint)
	}
}

func TestControlServerSSEDelivers(t *testing.T) {
	s := NewControlServer(nil, nil, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	// Wait for the subscriber to register, then emit each event variant.
	deadline := time.Now().Add(2 * time.Second)
	for s.subscriberCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	s.EmitVoiceState("listening")
	s.EmitControlApp("open_settings")
	s.EmitCallState("on_call")

	want := []string{
		`{"type":"voice_state","state":"listening"}`,
		`{"type":"control_app","action":"open_settings"}`,
		`{"type":"call_state","state":"on_call"}`,
	}
	br := bufio.NewReader(resp.Body)
	var got []string
	readDeadline := time.Now().Add(3 * time.Second)
	for len(got) < len(want) && time.Now().Before(readDeadline) {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			got = append(got, strings.TrimSpace(data))
		}
	}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Errorf("SSE line %d = %q, want %q (all got: %v)", i, safeIdx(got, i), w, got)
		}
	}
}

// Pins the EXACT bytes Desktop's decoder reads. The Swift side asserts the same
// three lines in KoeControlProtocolTests, so the contract is checked from both
// ends rather than assumed — which is what was missing when a failed call and a
// hang-up were the same event on the wire.
func TestControlServerCallFailedCarriesReason(t *testing.T) {
	s := NewControlServer(nil, nil, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for s.subscriberCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	s.EmitCallFailed(CallFailureAccountRequired)
	// An empty reason must fall back to a plain `ended`: a hang-up the user
	// asked for can never be reported to them as a failure.
	s.EmitCallFailed("")
	s.EmitCallState("ended")

	want := []string{
		`{"type":"call_state","state":"ended","reason":"account_required"}`,
		`{"type":"call_state","state":"ended"}`,
		`{"type":"call_state","state":"ended"}`,
	}
	br := bufio.NewReader(resp.Body)
	var got []string
	readDeadline := time.Now().Add(3 * time.Second)
	for len(got) < len(want) && time.Now().Before(readDeadline) {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			got = append(got, strings.TrimSpace(data))
		}
	}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Errorf("SSE line %d = %q, want %q (all got: %v)", i, safeIdx(got, i), w, got)
		}
	}
}

func safeIdx(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<missing>"
}

func TestCallMicEndpoint(t *testing.T) {
	var got []bool
	s := NewControlServer(nil, nil, nil)
	s.SetMicHandler(func(off bool) error {
		got = append(got, off)
		if off && len(got) > 1 {
			return ErrNoTaskPending
		}
		return nil
	})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/call/mic", "application/json", strings.NewReader(`{"mic":"off"}`))
	if err != nil {
		t.Fatalf("POST /call/mic: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(got) != 1 || got[0] != true {
		t.Fatalf("mic off: status=%d handler calls=%v", resp.StatusCode, got)
	}

	resp2, _ := http.Post(srv.URL+"/call/mic", "application/json", strings.NewReader(`{"mic":"bogus"}`))
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("bogus mic value: status=%d, want 400", resp2.StatusCode)
	}

	resp3, _ := http.Post(srv.URL+"/call/mic", "application/json", strings.NewReader(`{"mic":"off"}`))
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusConflict || !strings.Contains(string(body3), "no_task_pending") {
		t.Fatalf("rejected mic off: status=%d body=%s, want 409 no_task_pending", resp3.StatusCode, body3)
	}
}

func TestVoiceStateSnapshotStamped(t *testing.T) {
	s := NewControlServer(nil, nil, nil)
	taskPending, micOff := true, true
	s.SetSnapshotProviders(func() bool { return taskPending }, func() bool { return micOff })
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for s.subscriberCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	s.EmitVoiceState("thinking")
	taskPending, micOff = false, false
	s.ReemitVoiceState()
	s.EmitVoiceLevel("listening", 0.25)

	want := []string{
		`{"type":"voice_state","state":"thinking","task_pending":true,"mic":"off"}`,
		`{"type":"voice_state","state":"thinking"}`,
		`{"type":"voice_state","state":"listening","level":0.25}`,
	}
	br := bufio.NewReader(resp.Body)
	var gotLines []string
	readDeadline := time.Now().Add(3 * time.Second)
	for len(gotLines) < len(want) && time.Now().Before(readDeadline) {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			gotLines = append(gotLines, strings.TrimSpace(data))
		}
	}
	for i, w := range want {
		if i >= len(gotLines) || gotLines[i] != w {
			t.Errorf("SSE line %d = %q, want %q (all: %v)", i, safeIdx(gotLines, i), w, gotLines)
		}
	}
}
