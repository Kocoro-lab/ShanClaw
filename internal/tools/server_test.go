package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

func TestServerTool_Info(t *testing.T) {
	schema := client.ServerToolSchema{
		Name:        "web_search",
		Description: "Search the web for information",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
	}
	tool := NewServerTool(schema, nil)
	info := tool.Info()

	if info.Name != "web_search" {
		t.Errorf("expected name web_search, got %s", info.Name)
	}
	if info.Description != "Search the web for information" {
		t.Errorf("unexpected description: %s", info.Description)
	}
}

func TestServerTool_RequiresApproval(t *testing.T) {
	tool := NewServerTool(client.ServerToolSchema{Name: "test"}, nil)
	if tool.RequiresApproval() {
		t.Error("server tools should not require approval")
	}
}

// toolExecResp builds a mock tool execute response matching the gateway format.
func toolExecResp(success bool, output any, errMsg *string) client.ToolExecuteResponse {
	var raw json.RawMessage
	if output != nil {
		raw, _ = json.Marshal(output)
	}
	return client.ToolExecuteResponse{
		Success: success,
		Output:  raw,
		Error:   errMsg,
	}
}

func strPtr(s string) *string { return &s }

func TestServerTool_Run(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toolExecResp(true, map[string]any{"results": []string{"result1"}}, nil))
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	schema := client.ServerToolSchema{Name: "web_search", Description: "Search"}
	tool := NewServerTool(schema, gw)

	result, err := tool.Run(context.Background(), `{"query":"test"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "result1") {
		t.Errorf("expected output to contain 'result1', got %q", result.Content)
	}
}

func TestServerTool_Run_EmptyArgs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toolExecResp(true, map[string]any{"status": "ok"}, nil))
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	tool := NewServerTool(client.ServerToolSchema{Name: "ping"}, gw)

	result, err := tool.Run(context.Background(), "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "ok") {
		t.Errorf("expected output to contain 'ok', got %q", result.Content)
	}
}

func TestServerTool_Run_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toolExecResp(false, nil, strPtr("Required parameter 'query' is missing")))
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	tool := NewServerTool(client.ServerToolSchema{Name: "failing"}, gw)

	result, err := tool.Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true")
	}
	if !strings.Contains(result.Content, "missing") {
		t.Errorf("expected error about missing param, got %q", result.Content)
	}
}

func TestServerTool_Run_NullOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toolExecResp(true, nil, nil))
	}))
	defer server.Close()

	gw := client.NewGatewayClient(server.URL, "")
	tool := NewServerTool(client.ServerToolSchema{Name: "noop"}, gw)

	result, err := tool.Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "no output" {
		t.Errorf("expected 'no output', got %q", result.Content)
	}
}

func TestServerTool_Run_InvalidJSON(t *testing.T) {
	tool := NewServerTool(client.ServerToolSchema{Name: "test"}, nil)
	result, err := tool.Run(context.Background(), "not json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid JSON")
	}
}
