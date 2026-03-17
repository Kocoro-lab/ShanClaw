package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

type HTTPTool struct{}

type httpArgs struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

func (t *HTTPTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "http",
		Description: "Make an HTTP request. Returns status code, response headers, and body (truncated to 10KB).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":     map[string]any{"type": "string", "description": "Request URL"},
				"method":  map[string]any{"type": "string", "description": "HTTP method (default: GET)"},
				"headers": map[string]any{"type": "object", "description": "Request headers as key-value pairs"},
				"body":    map[string]any{"type": "string", "description": "Request body"},
				"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (default: 30)"},
			},
		},
		Required: []string{"url"},
	}
}

func (t *HTTPTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args httpArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	method := args.Method
	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(method)

	timeout := 30 * time.Second
	if args.Timeout > 0 {
		timeout = time.Duration(args.Timeout) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var bodyReader io.Reader
	if args.Body != "" {
		bodyReader = strings.NewReader(args.Body)
	}

	req, err := http.NewRequestWithContext(ctx, method, args.URL, bodyReader)
	if err != nil {
		return agent.ValidationError(fmt.Sprintf("error creating request: %v", err)), nil
	}

	for k, v := range args.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return agent.TransientError(fmt.Sprintf("request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10240))
	if err != nil {
		return agent.TransientError(fmt.Sprintf("error reading response body: %v", err)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Status: %d %s\n\nHeaders:\n", resp.StatusCode, resp.Status)
	for k, vals := range resp.Header {
		for _, v := range vals {
			fmt.Fprintf(&sb, "  %s: %s\n", k, v)
		}
	}
	fmt.Fprintf(&sb, "\nBody:\n%s", string(body))

	return agent.ToolResult{Content: sb.String()}, nil
}

func (t *HTTPTool) RequiresApproval() bool { return true }

func (t *HTTPTool) IsSafeArgs(argsJSON string) bool {
	var args httpArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return false
	}

	method := strings.ToUpper(args.Method)
	if method == "" {
		method = "GET"
	}
	if method != "GET" {
		return false
	}

	parsed, err := url.Parse(args.URL)
	if err != nil {
		return false
	}

	host := parsed.Hostname()
	return host == "localhost" || host == "127.0.0.1"
}
