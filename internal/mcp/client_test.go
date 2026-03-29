package mcp

import (
	"context"
	"testing"
	"time"
)

func TestCachedTools_ReturnsEmpty(t *testing.T) {
	mgr := NewClientManager()
	tools := mgr.CachedTools("nonexistent")
	if len(tools) != 0 {
		t.Errorf("expected empty, got %d tools", len(tools))
	}
}

func TestProbeTransport_NoClient(t *testing.T) {
	mgr := NewClientManager()
	err := mgr.ProbeTransport(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for unknown server")
	}
}

func TestReconnect_NoConfig(t *testing.T) {
	mgr := NewClientManager()
	_, err := mgr.Reconnect(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for unknown server config")
	}
}

func TestSetSupervised_DisablesInlineReconnect(t *testing.T) {
	mgr := NewClientManager()
	mgr.SetSupervised(true)
	mgr.mu.Lock()
	supervised := mgr.supervised
	mgr.mu.Unlock()
	if !supervised {
		t.Error("expected supervised=true")
	}
}

func TestIsPlaywrightCDPMode(t *testing.T) {
	cfg := MCPServerConfig{Args: []string{"--cdp-endpoint", "http://localhost:9222"}}
	if !IsPlaywrightCDPMode(cfg) {
		t.Fatal("expected CDP mode to be detected")
	}

	cfg.Args = []string{"--browser", "chrome"}
	if IsPlaywrightCDPMode(cfg) {
		t.Fatal("expected CDP mode to be false when flag is absent")
	}
}

func TestNormalizePlaywrightCDPConfig_MigratesLegacyLocalhostPort(t *testing.T) {
	cfg := MCPServerConfig{Args: []string{"--cdp-endpoint", "http://localhost:9222", "--caps", "vision,pdf"}}
	normalized := NormalizePlaywrightCDPConfig(cfg)
	want := "http://127.0.0.1:9223"
	if normalized.Args[1] != want {
		t.Fatalf("expected %s, got %s", want, normalized.Args[1])
	}
}

func TestNormalizePlaywrightCDPConfig_PreservesCustomEndpoint(t *testing.T) {
	cfg := MCPServerConfig{Args: []string{"--cdp-endpoint", "http://127.0.0.1:9333"}}
	normalized := NormalizePlaywrightCDPConfig(cfg)
	if normalized.Args[1] != "http://127.0.0.1:9333" {
		t.Fatalf("expected custom endpoint to be preserved, got %s", normalized.Args[1])
	}
}

func TestPlaywrightCDPPort(t *testing.T) {
	cfg := MCPServerConfig{Args: []string{"--cdp-endpoint", "http://127.0.0.1:9333"}}
	if got := PlaywrightCDPPort(cfg); got != 9333 {
		t.Fatalf("expected port 9333, got %d", got)
	}

	cfg = MCPServerConfig{Args: []string{"--cdp-endpoint", "http://localhost:9222"}}
	cfg = NormalizePlaywrightCDPConfig(cfg)
	if got := PlaywrightCDPPort(cfg); got != DefaultCDPPort {
		t.Fatalf("expected default dedicated port %d, got %d", DefaultCDPPort, got)
	}
}

func TestPlaywrightCDPPortDefaultsWhenEndpointValueIsMissing(t *testing.T) {
	cfg := MCPServerConfig{Args: []string{"--cdp-endpoint"}}
	if got := PlaywrightCDPPort(cfg); got != DefaultCDPPort {
		t.Fatalf("expected default dedicated port %d, got %d", DefaultCDPPort, got)
	}
}

func TestConnectAll_StoresConfigOnFailure(t *testing.T) {
	mgr := NewClientManager()
	servers := map[string]MCPServerConfig{
		"bad": {Command: "/nonexistent/binary"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := mgr.ConnectAll(ctx, servers)
	if err == nil {
		t.Fatal("expected error for bad command")
	}
	mgr.mu.Lock()
	_, hasCfg := mgr.configs["bad"]
	mgr.mu.Unlock()
	if !hasCfg {
		t.Error("expected config to be stored for failed server")
	}
}
