package koe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

// Every literal below is a real line lifted from koe.log, not a constructed
// example — the point of the classifier is to be right about what actually
// reaches it.
func TestClassifyCallFailureFromObservedErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			// THE one the user hit: 25,685 times over two days. It arrives
			// wrapped in a 502, so this also pins the ordering rule — a
			// status-first classifier would call it backend_unavailable and
			// send the user looking for an outage that is not happening.
			name: "usage principal wrapped in 502",
			err: fmt.Errorf("openai realtime mint: %w", &RealtimeBootstrapError{
				StatusCode: 502,
				Body:       `{"error":"mint relay failed: realtime usage principal unavailable"}`,
			}),
			want: CallFailureAccountRequired,
		},
		{
			name: "qwen sdp relay, same cause",
			err: fmt.Errorf("qwen realtime sdp_exchange: %w", &RealtimeBootstrapError{
				StatusCode: 502,
				Body:       `{"error":"SDP relay failed: realtime usage principal unavailable"}`,
			}),
			want: CallFailureAccountRequired,
		},
		{
			name: "mint service unavailable",
			err: fmt.Errorf("mint failed: %w", &RealtimeBootstrapError{
				StatusCode: 502,
				Body:       `{"error":"mint failed: {\"error\":{\"message\":\"Mint service unavailable\"}}"}`,
			}),
			want: CallFailureBackendUnavailable,
		},
		{
			name: "route absent on this gateway",
			err: fmt.Errorf("mint failed: %w", &RealtimeBootstrapError{
				StatusCode: 404, Body: "404 page not found\n",
			}),
			want: CallFailureBackendUnavailable,
		},
		{
			name: "dns failure reaching cloud",
			err:  errors.New(`mint failed: daemon mint failed: HTTP 502: {"error":"mint relay failed: request failed: Post \"https://api-dev.shannon.run/v1/realtime/client_secrets\": dial tcp: lookup api-dev.shannon.run: no such host"}`),
			want: CallFailureNetwork,
		},
		{
			name: "openai unreachable",
			err:  errors.New(`connect failed: Post "https://api.openai.com/v1/realtime/calls": dial tcp [2a03:2880::]:443: i/o timeout`),
			want: CallFailureNetwork,
		},
		{
			name: "daemon relay timed out",
			err:  errors.New(`mint failed: Post "http://127.0.0.1:7533/koe/realtime/mint": context deadline exceeded`),
			want: CallFailureNetwork,
		},
		{
			name: "daemon not up yet",
			err:  errors.New(`mint failed: Post "http://127.0.0.1:7533/koe/realtime/mint": dial tcp 127.0.0.1:7533: connect: connection refused`),
			want: CallFailureNetwork,
		},
		{
			name: "typed net.Error still classifies",
			err:  fmt.Errorf("connect failed: %w", &net.DNSError{Err: "no such host", Name: "x"}),
			want: CallFailureNetwork,
		},
		{
			name: "typed deadline still classifies",
			err:  fmt.Errorf("connect failed: %w", context.DeadlineExceeded),
			want: CallFailureNetwork,
		},
		{
			name: "unauthorized is an account problem",
			err:  fmt.Errorf("mint: %w", &RealtimeBootstrapError{StatusCode: 401, Body: "unauthorized"}),
			want: CallFailureAccountRequired,
		},
		{
			name: "quota is its own message",
			err:  fmt.Errorf("mint: %w", &RealtimeBootstrapError{StatusCode: 429, Body: "rate limited"}),
			want: CallFailureQuotaExceeded,
		},
		{
			name: "unclassified still reports something",
			err:  errors.New("session config timeout after 10s"),
			want: CallFailureUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyCallFailure(tc.err); got != tc.want {
				t.Fatalf("ClassifyCallFailure() = %q, want %q\n  err: %v", got, tc.want, tc.err)
			}
		})
	}
}

// A normal hang-up carries no failure, and must not be dressed up as one:
// Desktop shows a banner for any non-empty reason.
func TestClassifyCallFailureNilIsEmpty(t *testing.T) {
	if got := ClassifyCallFailure(nil); got != "" {
		t.Fatalf("ClassifyCallFailure(nil) = %q, want empty", got)
	}
}
