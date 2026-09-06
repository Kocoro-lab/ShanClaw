//go:build darwin && !ios && cgo

package koe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMintViaDaemon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/koe/realtime/mint" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var got struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got.Model != "gpt-realtime-2.1-mini" {
			t.Errorf("model = %q, want the pinned realtime model", got.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		// Mirror the gateway's forwarded shape (value + top-level expires_at).
		_ = json.NewEncoder(w).Encode(map[string]any{"value": "ek_relay123", "expires_at": 1782380235, "usage_principal": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	}))
	defer srv.Close()

	client := NewDaemonClient(srv.URL)
	ek, principal, err := client.MintViaDaemonWithPrincipal(context.Background(), "gpt-realtime-2.1-mini")
	if err != nil {
		t.Fatalf("MintViaDaemon: %v", err)
	}
	if ek != "ek_relay123" {
		t.Errorf("ek = %q, want ek_relay123", ek)
	}
	if principal != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("usage principal = %q", principal)
	}
}

func TestMintViaDaemonForwardsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"cloud not configured"}`))
	}))
	defer srv.Close()

	if _, err := NewDaemonClient(srv.URL).MintViaDaemon(context.Background(), "m"); err == nil {
		t.Error("expected an error when the daemon relay returns non-200")
	}
}
