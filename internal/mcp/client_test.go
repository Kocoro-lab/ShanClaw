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
