//go:build darwin && !ios && cgo

package koe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchPersona(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/koe/persona" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"persona":"The user prefers Chinese and goes by Shenghao."}`))
	}))
	defer srv.Close()

	got, err := NewDaemonClient(srv.URL).FetchPersona(context.Background())
	if err != nil {
		t.Fatalf("FetchPersona: %v", err)
	}
	if got != "The user prefers Chinese and goes by Shenghao." {
		t.Errorf("persona = %q", got)
	}
}

func TestFetchPersonaEmptyIsNotAnError(t *testing.T) {
	// The daemon fail-soft returns {"persona":""} when there's no usable context;
	// Koe must treat that as "use base persona only", not an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"persona":""}`))
	}))
	defer srv.Close()

	got, err := NewDaemonClient(srv.URL).FetchPersona(context.Background())
	if err != nil {
		t.Fatalf("FetchPersona: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty persona, got %q", got)
	}
}
