package agent

import (
	"fmt"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

func TestIsRetryableLLMError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"nil", nil, false},
		// Typed APIError (primary path)
		{"typed 429", &client.APIError{StatusCode: 429, Body: "rate limit"}, true},
		{"typed 500", &client.APIError{StatusCode: 500, Body: "internal"}, true},
		{"typed 502", &client.APIError{StatusCode: 502, Body: "bad gateway"}, true},
		{"typed 503", &client.APIError{StatusCode: 503}, true},
		{"typed 529", &client.APIError{StatusCode: 529, Body: "overloaded"}, true},
		{"typed 400", &client.APIError{StatusCode: 400, Body: "invalid"}, false},
		{"typed 401", &client.APIError{StatusCode: 401, Body: "unauthorized"}, false},
		{"typed 403", &client.APIError{StatusCode: 403, Body: "forbidden"}, false},
		// Wrapped typed APIError (errors.As unwraps)
		{"wrapped 429", fmt.Errorf("LLM call failed: %w", &client.APIError{StatusCode: 429}), true},
		{"wrapped 400", fmt.Errorf("LLM call failed: %w", &client.APIError{StatusCode: 400}), false},
		// Network/stream errors (string-matched)
		{"network timeout", fmt.Errorf("request failed: context deadline exceeded"), true},
		{"connection reset", fmt.Errorf("request failed: connection reset"), true},
		{"stream read error", fmt.Errorf("stream read error: unexpected EOF"), true},
		{"stream ended early", fmt.Errorf("stream ended without done event"), true},
		// Non-retryable
		{"marshal error", fmt.Errorf("marshal request: json error"), false},
		{"decode error", fmt.Errorf("decode response: unexpected EOF"), false},
		{"generic error", fmt.Errorf("something unexpected"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableLLMError(tt.err)
			if got != tt.retryable {
				t.Errorf("isRetryableLLMError(%v) = %v, want %v", tt.err, got, tt.retryable)
			}
		})
	}
}

func TestClassifyLLMError(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		expect string
	}{
		{"nil", nil, "unknown"},
		{"rate limit", &client.APIError{StatusCode: 429}, "rate limited"},
		{"overloaded", &client.APIError{StatusCode: 529}, "API overloaded"},
		{"server 500", &client.APIError{StatusCode: 500}, "server error"},
		{"server 502", &client.APIError{StatusCode: 502}, "server error"},
		{"server 503", &client.APIError{StatusCode: 503}, "server error"},
		{"bad request", &client.APIError{StatusCode: 400}, "HTTP 400"},
		{"timeout", fmt.Errorf("request failed: context deadline exceeded"), "request timeout"},
		{"connection reset", fmt.Errorf("request failed: connection reset"), "connection error"},
		{"stream error", fmt.Errorf("stream read error: unexpected EOF"), "stream interrupted"},
		{"generic", fmt.Errorf("something else"), "transient error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyLLMError(tt.err)
			if got != tt.expect {
				t.Errorf("classifyLLMError(%v) = %q, want %q", tt.err, got, tt.expect)
			}
		})
	}
}
