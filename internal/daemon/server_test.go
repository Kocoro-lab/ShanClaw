package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Kocoro-lab/shan/internal/config"
)

func TestServer_Health(t *testing.T) {
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", srv.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("body = %v", body)
	}
}

func TestServer_Status(t *testing.T) {
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/status", srv.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body struct {
		IsConnected bool   `json:"is_connected"`
		ActiveAgent string `json:"active_agent"`
		Uptime      int    `json:"uptime"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if body.IsConnected {
		t.Error("should not be connected")
	}
	if body.Uptime < 0 {
		t.Error("uptime should be non-negative")
	}
}

func TestServer_Shutdown(t *testing.T) {
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, nil)
	ctx, cancel := context.WithCancel(context.Background())

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	cancel()
	time.Sleep(200 * time.Millisecond)

	_, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", srv.Port()))
	if err == nil {
		t.Error("expected connection refused after shutdown")
	}
}

func TestServer_Agents_Empty(t *testing.T) {
	agentsDir := t.TempDir()
	sessDir := t.TempDir()
	deps := &ServerDeps{
		AgentsDir:    agentsDir,
		SessionCache: NewSessionCache(sessDir),
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/agents", srv.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]json.RawMessage
	json.Unmarshal(body, &parsed)
	if string(parsed["agents"]) != "[]" {
		t.Errorf("expected empty agents array, got %s", string(body))
	}
}

func TestServer_Sessions_Empty(t *testing.T) {
	sessDir := t.TempDir()
	deps := &ServerDeps{
		SessionCache: NewSessionCache(sessDir),
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/sessions", srv.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]json.RawMessage
	json.Unmarshal(body, &parsed)
	if string(parsed["sessions"]) != "[]" {
		t.Errorf("expected empty sessions array, got %s", string(body))
	}
}

func TestServer_Message_MissingText(t *testing.T) {
	deps := &ServerDeps{}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/message", srv.Port()),
		"application/json",
		strings.NewReader(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestServer_Message_AgentNotFound(t *testing.T) {
	sessDir := t.TempDir()
	deps := &ServerDeps{
		Config:       &config.Config{},
		AgentsDir:    t.TempDir(),
		SessionCache: NewSessionCache(sessDir),
	}
	c := NewClient("ws://localhost:1/x", "", func(msg MessagePayload) string { return "" }, nil)
	srv := NewServer(0, c, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/message", srv.Port()),
		"application/json",
		strings.NewReader(`{"text":"hello","agent":"nonexistent"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Agent falls back to default when not found, but RunAgent will fail
	// because deps are incomplete (no gateway, registry). 500 is expected.
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "error") {
		t.Errorf("expected error in body, got %s", string(body))
	}
}
