package koe

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// Being unable to reach the LOCAL relay says nothing about any provider — both
// ride that same hop. Measured 2026-08-27: koe outliving a daemon restart by
// three seconds opened the 5-minute circuit and pinned every later call onto a
// provider the backend had not configured, with OpenAI healthy throughout.
func TestAutoFallbackIgnoresLocalRelayFailure(t *testing.T) {
	relay := &DaemonRelayError{
		Err: errors.New(`Post "http://127.0.0.1:7533/koe/realtime/mint": dial tcp 127.0.0.1:7533: connect: connection refused`),
	}
	err := OpenAIMintError(relay)
	if AutoFallbackEligible(err) {
		t.Fatal("a local relay failure must not count as an OpenAI provider failure")
	}
}

// The guard must not swallow real provider trouble: a genuine network failure
// reaching OpenAI is still a fallback-eligible provider failure.
func TestAutoFallbackStillEligibleForRealProviderFailure(t *testing.T) {
	err := OpenAIMintError(&RealtimeBootstrapError{StatusCode: 502, Body: "upstream boom"})
	if !AutoFallbackEligible(err) {
		t.Fatal("a 5xx from Cloud must remain fallback-eligible")
	}
}

func TestIsProviderNotConfigured(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "the observed 503 body",
			err: fmt.Errorf("qwen realtime sdp_exchange: %w", &RealtimeBootstrapError{
				StatusCode: 503,
				Body:       `{"detail":{"code":"provider_not_configured","message":"Qwen WebRTC is not configured"}}`,
			}),
			want: true,
		},
		{
			name: "a plain 503 is temporary, not unconfigured",
			err:  fmt.Errorf("qwen: %w", &RealtimeBootstrapError{StatusCode: 503, Body: "service unavailable"}),
			want: false,
		},
		{name: "nil", err: nil, want: false},
		{name: "unrelated", err: errors.New("boom"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsProviderNotConfigured(tc.err); got != tc.want {
				t.Fatalf("IsProviderNotConfigured() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFallbackHealthTTLAndClear(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	f := NewFallbackHealth(10 * time.Minute)
	f.now = func() time.Time { return now }

	if f.Unavailable() {
		t.Fatal("a fresh cache must report available")
	}

	f.RecordUnavailable()
	if !f.Unavailable() {
		t.Fatal("expected unavailable right after recording")
	}

	// The TTL is deliberate, not permanent: a deployment can gain the provider
	// through a config change, and a process that never forgot would never
	// notice.
	now = now.Add(9 * time.Minute)
	if !f.Unavailable() {
		t.Fatal("must still be unavailable inside the TTL")
	}
	now = now.Add(2 * time.Minute)
	if f.Unavailable() {
		t.Fatal("must expire after the TTL")
	}

	// A success clears it immediately rather than waiting the TTL out.
	f.RecordUnavailable()
	f.RecordAvailable()
	if f.Unavailable() {
		t.Fatal("a successful connect must clear the cache at once")
	}
}

// A nil cache must behave as "available" so a connector built without one
// cannot change provider selection.
func TestFallbackHealthNilIsAvailable(t *testing.T) {
	var f *FallbackHealth
	if f.Unavailable() {
		t.Fatal("nil FallbackHealth must report available")
	}
	f.RecordUnavailable() // must not panic
	f.RecordAvailable()
}

// Jitter must stay inside its band and never collapse a backoff into a hot loop.
func TestJitterStaysInBandAndAboveOneSecond(t *testing.T) {
	for _, base := range []time.Duration{5 * time.Second, 60 * time.Second} {
		lo := time.Duration(float64(base) * (1 - WarmRetryJitterFraction))
		hi := time.Duration(float64(base) * (1 + WarmRetryJitterFraction))
		for i := 0; i < 500; i++ {
			got := JitterWarmRetryDelay(base)
			if got < time.Second {
				t.Fatalf("base %v: jittered to %v, below the 1s floor", base, got)
			}
			if got < lo || got > hi {
				t.Fatalf("base %v: jittered to %v, outside [%v, %v]", base, got, lo, hi)
			}
		}
	}
	if got := JitterWarmRetryDelay(0); got != 0 {
		t.Fatalf("JitterWarmRetryDelay(0) = %v, want 0 (paused means no timer)", got)
	}
}
