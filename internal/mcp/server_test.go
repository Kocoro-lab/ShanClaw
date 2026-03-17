package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

// mockTool implements agent.Tool for testing.
type mockTool struct {
	name        string
	description string
	params      map[string]any
	required    []string
	result      string
	isError     bool
	runErr      error
}

func (m *mockTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        m.name,
		Description: m.description,
		Parameters:  m.params,
		Required:    m.required,
	}
}

func (m *mockTool) Run(ctx context.Context, args string) (agent.ToolResult, error) {
	if m.runErr != nil {
		return agent.ToolResult{}, m.runErr
	}
	return agent.ToolResult{Content: m.result, IsError: m.isError}, nil
}

func (m *mockTool) RequiresApproval() bool { return false }

func newTestRegistry(tools ...agent.Tool) *agent.ToolRegistry {
	reg := agent.NewToolRegistry()
	for _, t := range tools {
		reg.Register(t)
	}
	return reg
}

func rawID(v int) *json.RawMessage {
	b, _ := json.Marshal(v)
	raw := json.RawMessage(b)
	return &raw
}

// sendRequest encodes a request as a JSON line and returns the response parsed
// from the server's output.
func sendRequest(t *testing.T, srv *Server, req Request) *Response {
	t.Helper()
	reqLine, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	reqLine = append(reqLine, '\n')

	var out bytes.Buffer
	err = srv.Serve(context.Background(), bytes.NewReader(reqLine), &out)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}

	output := strings.TrimSpace(out.String())
	if output == "" {
		return nil // no response (notification)
	}

	var resp Response
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", output, err)
	}
	return &resp
}

func TestHandleInitialize(t *testing.T) {
	srv := NewServer(newTestRegistry(), "test-server", "0.1.0", nil, nil, nil)
	resp := sendRequest(t, srv, Request{
		JSONRPC: "2.0",
		ID:      rawID(1),
		Method:  "initialize",
	})

	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultJSON, _ := json.Marshal(resp.Result)
	var result InitializeResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if result.ProtocolVersion != "2024-11-05" {
		t.Errorf("protocol version = %q, want %q", result.ProtocolVersion, "2024-11-05")
	}
	if result.ServerInfo.Name != "test-server" {
		t.Errorf("server name = %q, want %q", result.ServerInfo.Name, "test-server")
	}
	if result.ServerInfo.Version != "0.1.0" {
		t.Errorf("server version = %q, want %q", result.ServerInfo.Version, "0.1.0")
	}
	if result.Capabilities.Tools == nil {
		t.Fatal("expected tools capability, got nil")
	}
	if result.Capabilities.Tools.ListChanged {
		t.Error("expected listChanged=false")
	}
}

func TestHandleToolsList(t *testing.T) {
	tool := &mockTool{
		name:        "echo",
		description: "echoes input",
		params: map[string]any{
			"message": map[string]any{"type": "string", "description": "message to echo"},
		},
		required: []string{"message"},
	}
	srv := NewServer(newTestRegistry(tool), "test", "1.0", nil, nil, nil)
	resp := sendRequest(t, srv, Request{
		JSONRPC: "2.0",
		ID:      rawID(2),
		Method:  "tools/list",
	})

	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultJSON, _ := json.Marshal(resp.Result)
	var result ToolsListResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(result.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result.Tools))
	}
	if result.Tools[0].Name != "echo" {
		t.Errorf("tool name = %q, want %q", result.Tools[0].Name, "echo")
	}
	if result.Tools[0].Description != "echoes input" {
		t.Errorf("tool description = %q, want %q", result.Tools[0].Description, "echoes input")
	}

	var schema map[string]any
	if err := json.Unmarshal(result.Tools[0].InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
	req, ok := schema["required"].([]any)
	if !ok || len(req) != 1 || req[0] != "message" {
		t.Errorf("schema required = %v, want [message]", schema["required"])
	}
}

func TestHandleToolsListEmpty(t *testing.T) {
	srv := NewServer(newTestRegistry(), "test", "1.0", nil, nil, nil)
	resp := sendRequest(t, srv, Request{
		JSONRPC: "2.0",
		ID:      rawID(1),
		Method:  "tools/list",
	})

	resultJSON, _ := json.Marshal(resp.Result)
	var result ToolsListResult
	json.Unmarshal(resultJSON, &result)

	if result.Tools == nil {
		t.Fatal("expected non-nil tools slice")
	}
	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(result.Tools))
	}
}

func TestHandleToolCall(t *testing.T) {
	tool := &mockTool{
		name:   "greet",
		result: "hello world",
	}
	srv := NewServer(newTestRegistry(tool), "test", "1.0", nil, nil, nil)
	params, _ := json.Marshal(ToolCallParams{
		Name:      "greet",
		Arguments: json.RawMessage(`{"name":"world"}`),
	})
	resp := sendRequest(t, srv, Request{
		JSONRPC: "2.0",
		ID:      rawID(3),
		Method:  "tools/call",
		Params:  params,
	})

	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	resultJSON, _ := json.Marshal(resp.Result)
	var result ToolCallResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Type != "text" {
		t.Errorf("content type = %q, want text", result.Content[0].Type)
	}
	if result.Content[0].Text != "hello world" {
		t.Errorf("content text = %q, want %q", result.Content[0].Text, "hello world")
	}
	if result.IsError {
		t.Error("expected isError=false")
	}
}

func TestHandleToolCallError(t *testing.T) {
	tool := &mockTool{
		name:    "fail",
		result:  "error output",
		isError: true,
	}
	srv := NewServer(newTestRegistry(tool), "test", "1.0", nil, nil, nil)
	params, _ := json.Marshal(ToolCallParams{
		Name:      "fail",
		Arguments: json.RawMessage(`{}`),
	})
	resp := sendRequest(t, srv, Request{
		JSONRPC: "2.0",
		ID:      rawID(4),
		Method:  "tools/call",
		Params:  params,
	})

	resultJSON, _ := json.Marshal(resp.Result)
	var result ToolCallResult
	json.Unmarshal(resultJSON, &result)

	if !result.IsError {
		t.Error("expected isError=true")
	}
	if result.Content[0].Text != "error output" {
		t.Errorf("content = %q, want %q", result.Content[0].Text, "error output")
	}
}

func TestHandleToolCallRunError(t *testing.T) {
	tool := &mockTool{
		name:   "broken",
		runErr: errors.New("something went wrong"),
	}
	srv := NewServer(newTestRegistry(tool), "test", "1.0", nil, nil, nil)
	params, _ := json.Marshal(ToolCallParams{
		Name:      "broken",
		Arguments: json.RawMessage(`{}`),
	})
	resp := sendRequest(t, srv, Request{
		JSONRPC: "2.0",
		ID:      rawID(5),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != -32603 {
		t.Errorf("error code = %d, want -32603", resp.Error.Code)
	}
	if resp.Error.Message != "something went wrong" {
		t.Errorf("error message = %q, want %q", resp.Error.Message, "something went wrong")
	}
}

func TestHandleToolCallUnknownTool(t *testing.T) {
	srv := NewServer(newTestRegistry(), "test", "1.0", nil, nil, nil)
	params, _ := json.Marshal(ToolCallParams{
		Name:      "nonexistent",
		Arguments: json.RawMessage(`{}`),
	})
	resp := sendRequest(t, srv, Request{
		JSONRPC: "2.0",
		ID:      rawID(6),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "nonexistent") {
		t.Errorf("error message = %q, want it to contain 'nonexistent'", resp.Error.Message)
	}
}

func TestNotificationNoResponse(t *testing.T) {
	srv := NewServer(newTestRegistry(), "test", "1.0", nil, nil, nil)
	resp := sendRequest(t, srv, Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})

	if resp != nil {
		t.Fatalf("expected no response for notification, got %+v", resp)
	}
}

func TestInvalidJSON(t *testing.T) {
	srv := NewServer(newTestRegistry(), "test", "1.0", nil, nil, nil)
	var out bytes.Buffer
	input := "this is not json\n"
	err := srv.Serve(context.Background(), strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}

	var resp Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != -32700 {
		t.Errorf("error code = %d, want -32700", resp.Error.Code)
	}
}

func TestUnknownMethod(t *testing.T) {
	srv := NewServer(newTestRegistry(), "test", "1.0", nil, nil, nil)
	resp := sendRequest(t, srv, Request{
		JSONRPC: "2.0",
		ID:      rawID(7),
		Method:  "bogus/method",
	})

	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "bogus/method") {
		t.Errorf("error message = %q, want it to contain method name", resp.Error.Message)
	}
}

func TestServeMultipleRequests(t *testing.T) {
	tool := &mockTool{
		name:   "ping",
		result: "pong",
	}
	srv := NewServer(newTestRegistry(tool), "test", "1.0", nil, nil, nil)

	// Build a stream of multiple requests.
	var input bytes.Buffer
	requests := []Request{
		{JSONRPC: "2.0", ID: rawID(1), Method: "initialize"},
		{JSONRPC: "2.0", Method: "notifications/initialized"}, // notification, no response
		{JSONRPC: "2.0", ID: rawID(2), Method: "tools/list"},
	}
	callParams, _ := json.Marshal(ToolCallParams{Name: "ping", Arguments: json.RawMessage(`{}`)})
	requests = append(requests, Request{JSONRPC: "2.0", ID: rawID(3), Method: "tools/call", Params: callParams})

	for _, req := range requests {
		line, _ := json.Marshal(req)
		input.Write(line)
		input.WriteByte('\n')
	}

	var out bytes.Buffer
	err := srv.Serve(context.Background(), &input, &out)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}

	// We expect 3 responses (notification produces none).
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 response lines, got %d: %v", len(lines), lines)
	}

	// Verify each response has the correct ID.
	expectedIDs := []int{1, 2, 3}
	for i, line := range lines {
		var resp Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("unmarshal line %d: %v", i, err)
		}
		if resp.ID == nil {
			t.Fatalf("response %d has nil ID", i)
		}
		var id int
		json.Unmarshal(*resp.ID, &id)
		if id != expectedIDs[i] {
			t.Errorf("response %d ID = %d, want %d", i, id, expectedIDs[i])
		}
		if resp.Error != nil {
			t.Errorf("response %d unexpected error: %+v", i, resp.Error)
		}
	}
}

func TestServeContextCancellation(t *testing.T) {
	srv := NewServer(newTestRegistry(), "test", "1.0", nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())

	// Use a pipe so the reader blocks until we close it.
	pr, pw := io.Pipe()
	var out bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx, pr, &out)
	}()

	cancel()
	pw.Close()

	err := <-done
	// After cancellation and pipe close, Serve should return.
	// It may return ctx.Err() or nil (scanner sees EOF first).
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEmptyLinesSkipped(t *testing.T) {
	srv := NewServer(newTestRegistry(), "test", "1.0", nil, nil, nil)
	req, _ := json.Marshal(Request{JSONRPC: "2.0", ID: rawID(1), Method: "initialize"})
	input := "\n\n" + string(req) + "\n\n"

	var out bytes.Buffer
	err := srv.Serve(context.Background(), strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 response, got %d", len(lines))
	}
}
