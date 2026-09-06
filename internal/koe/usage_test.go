//go:build darwin && !ios && cgo

package koe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

func TestSendRealtimeUsage(t *testing.T) {
	var gotBody []byte
	var gotPrincipal string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/koe/realtime/usage" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotPrincipal = r.Header.Get(client.RealtimeUsagePrincipalHeader)
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cost_usd":0.01}`))
	}))
	defer srv.Close()

	relay := NewDaemonClient(srv.URL)
	err := relay.SendRealtimeUsageWithPrincipal(context.Background(), json.RawMessage(`{"model":"m","response_id":"r1"}`), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("SendRealtimeUsage: %v", err)
	}
	if !strings.Contains(string(gotBody), `"response_id":"r1"`) {
		t.Errorf("daemon did not receive the usage body; got %s", gotBody)
	}
	if gotPrincipal != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("daemon did not receive usage principal header: %q", gotPrincipal)
	}
}

// TestSendRealtimeUsageRetriesOn503 verifies S2b: Cloud returns 503 when a usage
// report fails to persist transiently (daemon forwards it verbatim), so the relay
// retries and succeeds once the daemon recovers.
func TestSendRealtimeUsageRetriesOn503(t *testing.T) {
	t.Setenv("KOE_USAGE_RELAY_BACKOFF_MS", "1") // keep the test fast
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable) // transient persist failure
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cost_usd":0.01}`))
	}))
	defer srv.Close()

	if err := NewDaemonClient(srv.URL).SendRealtimeUsage(context.Background(), json.RawMessage(`{"model":"m"}`)); err != nil {
		t.Fatalf("SendRealtimeUsage should succeed after retrying 503: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 attempts (503, 503, 200); got %d", got)
	}
}

// TestSendRealtimeUsageDoesNotRetryOn400 verifies a 4xx (bad body / auth) is
// permanent — retrying it just re-sends a request Cloud will reject identically.
func TestSendRealtimeUsageDoesNotRetryOn400(t *testing.T) {
	t.Setenv("KOE_USAGE_RELAY_BACKOFF_MS", "1")
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	if err := NewDaemonClient(srv.URL).SendRealtimeUsage(context.Background(), json.RawMessage(`{"model":"m"}`)); err == nil {
		t.Fatal("SendRealtimeUsage should return an error on 400")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("4xx must not be retried; expected 1 attempt, got %d", got)
	}
}

func TestExchangeSDPViaDaemon(t *testing.T) {
	var got map[string]string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/koe/realtime/sdp" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"qwen","model":"qwen3.5-omni-flash-realtime","answer_sdp":"v=0\r\n","usage_principal":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`))
	}))
	defer srv.Close()

	client := NewDaemonClient(srv.URL)
	client.SetToken("  robot-backbrain-token\n")
	answer, principal, err := client.ExchangeSDPViaDaemonWithPrincipal(
		context.Background(), "qwen", "qwen3.5-omni-flash-realtime", "v=0\r\n",
	)
	if err != nil {
		t.Fatalf("ExchangeSDPViaDaemon: %v", err)
	}
	if answer != "v=0\r\n" {
		t.Errorf("answer = %q", answer)
	}
	if principal != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("usage principal = %q", principal)
	}
	if gotAuth != "Bearer robot-backbrain-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if got["provider"] != "qwen" || got["model"] != "qwen3.5-omni-flash-realtime" || got["offer_sdp"] != "v=0\r\n" {
		t.Errorf("request = %#v", got)
	}
}

func TestExchangeSDPViaDaemonPreservesErrorStatusAndCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"detail":{"code":"provider_auth_failed"}}`))
	}))
	defer srv.Close()

	_, err := NewDaemonClient(srv.URL).ExchangeSDPViaDaemon(context.Background(), "qwen", "m", "v=0\r\n")
	var bootstrapErr *RealtimeBootstrapError
	if !errors.As(err, &bootstrapErr) {
		t.Fatalf("error = %T %v, want RealtimeBootstrapError", err, err)
	}
	if bootstrapErr.StatusCode != http.StatusBadGateway || !strings.Contains(bootstrapErr.Body, "provider_auth_failed") {
		t.Errorf("bootstrap error = %#v", bootstrapErr)
	}
}

func TestReportUsageBuildsBillingBody(t *testing.T) {
	gotUsage := make(chan json.RawMessage, 1)
	h := &eventHandler{provider: "openai", model: "gpt-realtime-2.1-mini", onUsage: func(b json.RawMessage) { gotUsage <- b }}
	if !h.reportUsage([]byte(`{"type":"response.done","response":{"id":"resp_1","usage":{"total_tokens":42,"output_token_details":{"audio_tokens":30}}}}`)) {
		t.Fatal("reportUsage rejected a response.done carrying usage")
	}
	var got json.RawMessage
	select {
	case got = <-gotUsage:
	case <-time.After(time.Second):
		t.Fatal("onUsage was not fired for a response.done carrying usage")
	}
	s := string(got)
	if !strings.Contains(s, `"response_id":"resp_1"`) ||
		!strings.Contains(s, `"provider":"openai"`) ||
		!strings.Contains(s, `"model":"gpt-realtime-2.1-mini"`) ||
		!strings.Contains(s, `"total_tokens":42`) {
		t.Errorf("billing body missing fields: %s", s)
	}
}

func TestReportUsagePreservesQwenPluralTokenDetails(t *testing.T) {
	gotUsage := make(chan json.RawMessage, 1)
	h := &eventHandler{provider: "qwen", model: "qwen3.5-omni-flash-realtime", onUsage: func(b json.RawMessage) { gotUsage <- b }}
	if !h.reportUsage([]byte(`{"type":"response.done","response":{"id":"resp_qwen","usage":{"input_tokens_details":{"audio_tokens":12},"output_tokens_details":{"audio_tokens":34}}}}`)) {
		t.Fatal("reportUsage rejected a response.done carrying usage")
	}
	var got json.RawMessage
	select {
	case got = <-gotUsage:
	case <-time.After(time.Second):
		t.Fatal("onUsage was not fired for a response.done carrying usage")
	}

	var body map[string]any
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatalf("billing body: %v", err)
	}
	if body["provider"] != "qwen" || body["model"] != "qwen3.5-omni-flash-realtime" {
		t.Fatalf("provider/model = %#v/%#v", body["provider"], body["model"])
	}
	usage, ok := body["usage"].(map[string]any)
	if !ok || usage["input_tokens_details"] == nil || usage["output_tokens_details"] == nil {
		t.Fatalf("Qwen plural token details were not preserved: %#v", body)
	}
}

func TestReportUsageSkipsWhenNoUsage(t *testing.T) {
	fired := false
	h := &eventHandler{onUsage: func(json.RawMessage) { fired = true }}
	h.reportUsage([]byte(`{"type":"response.done","response":{"id":"resp_1"}}`)) // no usage field
	if fired {
		t.Error("onUsage fired for a response.done with no usage")
	}
}
