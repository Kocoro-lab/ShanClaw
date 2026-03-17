package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
	"github.com/Kocoro-lab/ShanClaw/internal/audit"
	"github.com/Kocoro-lab/ShanClaw/internal/hooks"
	"github.com/Kocoro-lab/ShanClaw/internal/permissions"
)

// JSON-RPC 2.0 types.

type Request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  any         `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP protocol types.

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
}

type Capabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type ToolsListResult struct {
	Tools []ToolDef `json:"tools"`
}

type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Server is a lightweight MCP server that exposes a ToolRegistry over
// JSON-RPC 2.0 via stdio.
type Server struct {
	tools       *agent.ToolRegistry
	name        string
	version     string
	permissions *permissions.PermissionsConfig
	auditor     *audit.AuditLogger
	hookRunner  *hooks.HookRunner
}

// NewServer creates a new MCP server backed by the given tool registry.
func NewServer(tools *agent.ToolRegistry, name, version string, perms *permissions.PermissionsConfig, auditor *audit.AuditLogger, hookRunner *hooks.HookRunner) *Server {
	return &Server{
		tools:       tools,
		name:        name,
		version:     version,
		permissions: perms,
		auditor:     auditor,
		hookRunner:  hookRunner,
	}
}

// Serve reads JSON-RPC requests from reader (one per line) and writes
// responses to writer. It blocks until the reader is closed or the
// context is cancelled.
func (s *Server) Serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	enc := json.NewEncoder(writer)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			enc.Encode(errorResponse(nil, -32700, "parse error"))
			continue
		}

		// Notifications have no ID and must not receive a response.
		if req.ID == nil {
			continue
		}

		var resp Response
		switch req.Method {
		case "initialize":
			resp = s.handleInitialize(req.ID)
		case "tools/list":
			resp = s.handleToolsList(req.ID)
		case "tools/call":
			resp = s.handleToolCall(ctx, req.ID, req.Params)
		default:
			resp = errorResponse(req.ID, -32601, "method not found: "+req.Method)
		}

		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) handleInitialize(id *json.RawMessage) Response {
	return Response{
		JSONRPC: "2.0",
		ID:      id,
		Result: InitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities: Capabilities{
				Tools: &ToolsCapability{ListChanged: false},
			},
			ServerInfo: ServerInfo{Name: s.name, Version: s.version},
		},
	}
}

func (s *Server) handleToolsList(id *json.RawMessage) Response {
	var tools []ToolDef
	for _, t := range s.tools.All() {
		info := t.Info()
		schema := map[string]any{
			"type":       "object",
			"properties": info.Parameters,
		}
		if len(info.Required) > 0 {
			schema["required"] = info.Required
		}
		schemaJSON, _ := json.Marshal(schema)
		tools = append(tools, ToolDef{
			Name:        info.Name,
			Description: info.Description,
			InputSchema: schemaJSON,
		})
	}
	if tools == nil {
		tools = []ToolDef{}
	}
	return Response{JSONRPC: "2.0", ID: id, Result: ToolsListResult{Tools: tools}}
}

func (s *Server) handleToolCall(ctx context.Context, id *json.RawMessage, params json.RawMessage) Response {
	var p ToolCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return errorResponse(id, -32602, "invalid params: "+err.Error())
	}

	tool, ok := s.tools.Get(p.Name)
	if !ok {
		return errorResponse(id, -32602, "unknown tool: "+p.Name)
	}

	argsStr := string(p.Arguments)

	// Permission check: MCP has no interactive TTY, so "ask" → deny (conservative)
	if s.permissions != nil {
		decision, reason := permissions.CheckToolCall(p.Name, argsStr, s.permissions)
		if decision == "deny" {
			s.logAudit(p.Name, argsStr, "denied by permission policy: "+reason, "deny", 0)
			return errorResponse(id, -32603, "tool call denied by permission policy")
		}
		if decision == "ask" {
			s.logAudit(p.Name, argsStr, "denied (requires approval, no TTY): "+reason, "deny", 0)
			return errorResponse(id, -32603, "tool call requires approval (not available in MCP mode)")
		}
	} else {
		// No permissions config: only allow tools that don't require approval
		if tool.RequiresApproval() {
			s.logAudit(p.Name, argsStr, "denied (requires approval, no permissions config)", "deny", 0)
			return errorResponse(id, -32603, "tool call requires approval (not available in MCP mode)")
		}
	}

	// Pre-tool-use hook
	if s.hookRunner != nil {
		hookDecision, hookReason, hookErr := s.hookRunner.RunPreToolUse(ctx, p.Name, argsStr, "")
		if hookErr != nil {
			fmt.Fprintf(io.Discard, "[hooks] pre-tool-use error: %v\n", hookErr)
		}
		if hookDecision == "deny" {
			s.logAudit(p.Name, argsStr, "denied by hook: "+hookReason, "deny", 0)
			return errorResponse(id, -32603, "tool call denied by hook: "+hookReason)
		}
	}

	startTime := time.Now()
	result, err := tool.Run(ctx, argsStr)
	elapsed := time.Since(startTime)
	if err != nil {
		return errorResponse(id, -32603, err.Error())
	}

	// Post-tool-use hook
	if s.hookRunner != nil {
		_ = s.hookRunner.RunPostToolUse(ctx, p.Name, argsStr, result.Content, "")
	}

	s.logAudit(p.Name, argsStr, result.Content, "allow", elapsed.Milliseconds())

	return Response{
		JSONRPC: "2.0",
		ID:      id,
		Result: ToolCallResult{
			Content: []ContentBlock{{Type: "text", Text: result.Content}},
			IsError: result.IsError,
		},
	}
}

func (s *Server) logAudit(toolName, argsStr, outputSummary, decision string, durationMs int64) {
	if s.auditor == nil {
		return
	}
	s.auditor.Log(audit.AuditEntry{
		Timestamp:     time.Now(),
		SessionID:     "mcp",
		ToolName:      toolName,
		InputSummary:  argsStr,
		OutputSummary: outputSummary,
		Decision:      decision,
		Approved:      decision == "allow",
		DurationMs:    durationMs,
	})
}

func errorResponse(id *json.RawMessage, code int, message string) Response {
	return Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: message},
	}
}
