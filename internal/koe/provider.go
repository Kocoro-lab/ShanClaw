package koe

// Realtime provider identity, model allowlist, connect-error taxonomy and the
// OpenAI health circuit. Deliberately untagged, same rule as audio_level.go:
// realtime.go is built for iOS via gomobile and references ProviderQwen, so
// keeping these behind `darwin && cgo` broke `go build ./...` for linux,
// windows and darwin-without-cgo — the exact matrix ci.yml gates on. Nothing
// here touches cgo; it is pure Go and compiles everywhere the brain does.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type RealtimeProvider string

const (
	ProviderAuto   RealtimeProvider = "auto"
	ProviderOpenAI RealtimeProvider = "openai"
	ProviderQwen   RealtimeProvider = "qwen"

	DefaultOpenAIRealtimeModel = "gpt-realtime-2.1"
	DefaultQwenRealtimeModel   = "qwen3.5-omni-flash-realtime"
	DefaultOpenAIRealtimeVoice = "marin"
	DefaultQwenRealtimeVoice   = "Tina"
)

var realtimeModels = map[RealtimeProvider]map[string]struct{}{
	ProviderOpenAI: {
		"gpt-realtime-2.1":      {},
		"gpt-realtime-2.1-mini": {},
	},
	ProviderQwen: {
		"qwen3.5-omni-flash-realtime": {},
		"qwen3.5-omni-plus-realtime":  {},
	},
}

func ParseRealtimeProvider(raw string) (RealtimeProvider, error) {
	p := RealtimeProvider(strings.ToLower(strings.TrimSpace(raw)))
	if p == "" {
		return ProviderAuto, nil
	}
	switch p {
	case ProviderAuto, ProviderOpenAI, ProviderQwen:
		return p, nil
	default:
		return "", fmt.Errorf("invalid realtime provider %q (want auto, openai, or qwen)", raw)
	}
}

func ValidateRealtimeModel(provider RealtimeProvider, model string) error {
	if _, ok := realtimeModels[provider][model]; ok {
		return nil
	}
	return fmt.Errorf("realtime model %q is not available for provider %q", model, provider)
}

// RealtimeConnectError identifies which part of the pre-media WebRTC bootstrap
// failed. Auto mode only falls back for provider/network availability failures;
// credentials, quota, and rejected session configuration remain visible.
type RealtimeConnectError struct {
	Provider RealtimeProvider
	Stage    string
	Err      error
}

func (e *RealtimeConnectError) Error() string {
	return fmt.Sprintf("%s realtime %s: %v", e.Provider, e.Stage, e.Err)
}

func (e *RealtimeConnectError) Unwrap() error { return e.Err }

func connectError(provider RealtimeProvider, stage string, err error) error {
	if err == nil {
		return nil
	}
	return &RealtimeConnectError{Provider: provider, Stage: stage, Err: err}
}

func OpenAIMintError(err error) error {
	return connectError(ProviderOpenAI, "mint", err)
}

// AutoFallbackEligible reports whether an OpenAI failure is safe to route to
// Qwen before a session becomes ready. An SDP POST timeout has unknown OpenAI
// outcome, so the caller must not retry OpenAI; it may start one Qwen session.
//
// The >=500 gate is coarser than it looks: Cloud's mint endpoint folds every
// upstream OpenAI failure — auth and config rejections included — into 502, so
// Cloud-side OpenAI failures fall back by design (voice degrades to Qwen
// instead of dying with the Cloud-held credential). Only gateway-authored 4xx
// (client auth, quota denial) stay terminal here.
func AutoFallbackEligible(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	// Being unable to REACH the local daemon relay is not evidence about any
	// provider: both mint and exchange SDP through that same localhost hop, so
	// falling back cannot help and recording it against OpenAI's health is
	// simply wrong. Measured 2026-08-27: a three-second window where koe
	// outlived a daemon restart opened the 5-minute circuit and pinned every
	// call onto a provider the backend had not configured, with OpenAI healthy
	// throughout. Checked FIRST because the underlying error is a net.Error and
	// would otherwise match the transport arm below.
	var relayErr *DaemonRelayError
	if errors.As(err, &relayErr) {
		return false
	}
	var ce *RealtimeConnectError
	if !errors.As(err, &ce) || ce.Provider != ProviderOpenAI {
		return false
	}
	switch ce.Stage {
	case "ice", "data_channel", "session_ready_timeout":
		return true
	case "mint", "sdp_exchange":
		var bootstrapErr *RealtimeBootstrapError
		if errors.As(ce.Err, &bootstrapErr) {
			if bootstrapErr.StatusCode == http.StatusServiceUnavailable &&
				strings.Contains(bootstrapErr.Body, "cloud not configured") {
				// The daemon's own signed-out 503. Qwen bootstrap rides the same
				// daemon relay, so falling back cannot succeed — and opening the
				// OpenAI circuit on it keeps Auto off OpenAI for the whole
				// cooldown after the user signs back in.
				return false
			}
			return bootstrapErr.StatusCode >= http.StatusInternalServerError
		}
		var netErr net.Error
		return errors.As(ce.Err, &netErr) || errors.Is(ce.Err, context.DeadlineExceeded)
	default:
		return false
	}
}

// OpenAICircuit is a process-local negative health cache. It removes a known
// unavailable first hop from subsequent Auto calls during the cooldown without
// changing forced-provider behavior or switching an active call mid-session.
type OpenAICircuit struct {
	mu        sync.Mutex
	openUntil time.Time
	cooldown  time.Duration
	now       func() time.Time
}

func NewOpenAICircuit(cooldown time.Duration) *OpenAICircuit {
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}
	return &OpenAICircuit{cooldown: cooldown, now: time.Now}
}

func (c *OpenAICircuit) Skip() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now().Before(c.openUntil)
}

func (c *OpenAICircuit) RecordFailure(err error) {
	if !AutoFallbackEligible(err) {
		return
	}
	c.mu.Lock()
	c.openUntil = c.now().Add(c.cooldown)
	c.mu.Unlock()
}

func (c *OpenAICircuit) RecordSuccess() {
	c.mu.Lock()
	c.openUntil = time.Time{}
	c.mu.Unlock()
}

// IsProviderNotConfigured reports whether Cloud refused a provider because it
// is not configured on that deployment, as opposed to being temporarily down.
//
// Distinguishing them matters for Auto mode: an unconfigured fallback is not a
// destination at all, so trading a working primary for it — and opening the
// primary's circuit to make that trade stick — is a strict downgrade. Observed
// as HTTP 503 {"code":"provider_not_configured"} from a dev deployment with no
// Qwen WebRTC (30 occurrences, 2026-08-27).
func IsProviderNotConfigured(err error) bool {
	if err == nil {
		return false
	}
	var bootstrap *RealtimeBootstrapError
	if errors.As(err, &bootstrap) && strings.Contains(bootstrap.Body, "provider_not_configured") {
		return true
	}
	return strings.Contains(err.Error(), "provider_not_configured")
}

// FallbackHealth is a process-local negative cache for Auto mode's fallback
// target, mirroring OpenAICircuit's shape on the other side of the decision.
//
// It exists so a fallback the backend has told us it cannot serve stops being
// treated as an escape route. The TTL is deliberate rather than permanent: a
// deployment can gain the provider through a config change, and a process that
// remembered "unavailable" forever would never notice. A successful connect
// clears it immediately.
type FallbackHealth struct {
	mu              sync.Mutex
	unavailableTill time.Time
	ttl             time.Duration
	now             func() time.Time
}

func NewFallbackHealth(ttl time.Duration) *FallbackHealth {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &FallbackHealth{ttl: ttl, now: time.Now}
}

// RecordUnavailable marks the fallback unusable for the TTL.
func (f *FallbackHealth) RecordUnavailable() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.unavailableTill = f.now().Add(f.ttl)
	f.mu.Unlock()
}

// RecordAvailable clears the cache — the fallback just worked.
func (f *FallbackHealth) RecordAvailable() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.unavailableTill = time.Time{}
	f.mu.Unlock()
}

// Unavailable reports whether the fallback is known unusable right now.
func (f *FallbackHealth) Unavailable() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now().Before(f.unavailableTill)
}
