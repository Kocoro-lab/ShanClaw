// Package koe is the voice front-brain's process-local library: the HTTP link to
// the daemon back-brain, the agent name-resolution ladder, and the voice-tool
// schemas. It talks to the daemon over localhost JSON and never imports
// internal/daemon — the contract is the wire, not shared Go types.
package koe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
	"github.com/Kocoro-lab/ShanClaw/internal/executionprofile"
)

// DaemonClient is a localhost HTTP client for the daemon back-brain.
type DaemonClient struct {
	baseURL string
	// doTaskClient has NO timeout: a back-brain turn can run for minutes, so the
	// caller controls the lifetime via context (the Koe-process context, never the
	// realtime call's). controlClient is genuinely bounded — cancel/list are fast
	// localhost calls, 30s is a safety net against a wedged daemon; do_task stays
	// unbounded.
	doTaskClient  *http.Client
	controlClient *http.Client
	tokenMu       sync.RWMutex
	token         string
}

// SetToken sets the optional bearer attached to daemon requests. It is read by
// the CLI from an environment variable so remote front brains do not expose the
// secret in their process arguments.
func (c *DaemonClient) SetToken(token string) {
	c.tokenMu.Lock()
	c.token = strings.TrimSpace(token)
	c.tokenMu.Unlock()
}

func (c *DaemonClient) authorize(req *http.Request) {
	c.tokenMu.RLock()
	token := c.token
	c.tokenMu.RUnlock()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// NewDaemonClient builds a client against e.g. "http://127.0.0.1:7533".
func NewDaemonClient(baseURL string) *DaemonClient {
	return &DaemonClient{
		baseURL:       strings.TrimRight(baseURL, "/"),
		doTaskClient:  &http.Client{Timeout: 0},                // unbounded; ctx-controlled
		controlClient: &http.Client{Timeout: 30 * time.Second}, // safety net for fast cancel/list
	}
}

// MintViaDaemon asks the daemon to mint an OpenAI Realtime ephemeral client
// secret on Koe's behalf (the via-daemon design — the front brain holds no
// long-lived credential; the daemon mints through Cloud with its own key). It
// returns the ephemeral "value" (ek_...). This is the production mint path that
// replaces C-minimal's direct dev-key mint. A fast localhost call → controlClient.
func (c *DaemonClient) MintViaDaemon(ctx context.Context, model string) (string, error) {
	value, _, err := c.MintViaDaemonWithPrincipal(ctx, model)
	return value, err
}

// MintViaDaemonWithPrincipal returns the daemon-issued opaque account binding
// captured in the same bootstrap response as the ephemeral secret. Callers
// that keep a realtime session alive must carry this value with that session;
// reading the client's current principal later could cross an account switch.
func (c *DaemonClient) MintViaDaemonWithPrincipal(ctx context.Context, model string) (string, string, error) {
	body, _ := json.Marshal(map[string]any{"model": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/koe/realtime/mint", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	resp, err := c.controlClient.Do(req)
	if err != nil {
		// Typed so provider selection can tell "the local relay is down" from
		// "Cloud said no" — see DaemonRelayError.
		return "", "", &DaemonRelayError{Err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", &RealtimeBootstrapError{StatusCode: resp.StatusCode, Body: string(raw)}
	}
	var mint struct {
		Value          string `json:"value"`
		UsagePrincipal string `json:"usage_principal"`
	}
	if err := json.Unmarshal(raw, &mint); err != nil || mint.Value == "" {
		return "", "", fmt.Errorf("daemon mint parse failed: %v (body %d bytes)", err, len(raw))
	}
	return mint.Value, strings.TrimSpace(mint.UsagePrincipal), nil
}

// ExchangeSDPViaDaemon sends a WebRTC offer through daemon→Cloud so Qwen's
// long-lived API key never enters the Koe process.
func (c *DaemonClient) ExchangeSDPViaDaemon(ctx context.Context, provider, model, offerSDP string) (string, error) {
	answer, _, err := c.ExchangeSDPViaDaemonWithPrincipal(ctx, provider, model, offerSDP)
	return answer, err
}

// ExchangeSDPViaDaemonWithPrincipal returns the answer and the opaque account
// binding from the same daemon SDP response. The binding is the lease for the
// resulting realtime session and must not be replaced with a later account.
func (c *DaemonClient) ExchangeSDPViaDaemonWithPrincipal(ctx context.Context, provider, model, offerSDP string) (string, string, error) {
	body, _ := json.Marshal(map[string]string{
		"provider": provider, "model": model, "offer_sdp": offerSDP,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/koe/realtime/sdp", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	resp, err := c.controlClient.Do(req)
	if err != nil {
		// Typed so provider selection can tell "the local relay is down" from
		// "Cloud said no" — see DaemonRelayError.
		return "", "", &DaemonRelayError{Err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", &RealtimeBootstrapError{StatusCode: resp.StatusCode, Body: string(raw)}
	}
	var out struct {
		AnswerSDP      string `json:"answer_sdp"`
		UsagePrincipal string `json:"usage_principal"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || strings.TrimSpace(out.AnswerSDP) == "" {
		return "", "", fmt.Errorf("daemon SDP parse failed: %v (body %d bytes)", err, len(raw))
	}
	return out.AnswerSDP, strings.TrimSpace(out.UsagePrincipal), nil
}

type RealtimeBootstrapError struct {
	StatusCode int
	Body       string
}

func (e *RealtimeBootstrapError) Error() string {
	return fmt.Sprintf("realtime bootstrap failed: HTTP %d: %s", e.StatusCode, e.Body)
}

// DaemonRelayError marks a failure to REACH the local daemon relay, as opposed
// to a failure the relay reported back from Cloud.
//
// The distinction is load-bearing for provider selection. Both providers mint
// and exchange SDP through this same localhost relay, so being unable to reach
// it says nothing whatsoever about OpenAI — and falling back to Qwen cannot
// help, because Qwen rides the identical dead path. Measured 2026-08-27: a
// three-second window where koe outlived a daemon restart was classified as
// "OpenAI unavailable", opened the 5-minute circuit, and pinned every
// subsequent call onto a provider the backend had not even configured. Voice
// stayed broken for five minutes with OpenAI healthy the whole time.
type DaemonRelayError struct{ Err error }

func (e *DaemonRelayError) Error() string {
	return "daemon relay unreachable: " + e.Err.Error()
}

func (e *DaemonRelayError) Unwrap() error { return e.Err }

// FetchPersona pulls the small-tier-distilled spoken-persona context (who the
// user is, how to address them — derived from the user's instructions + memory)
// from the daemon, to append to Koe's base persona before the session.update.
// Best-effort: an empty result or any error means Koe uses its base persona only,
// never blocking the call.
func (c *DaemonClient) FetchPersona(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/koe/persona", nil)
	if err != nil {
		return "", err
	}
	c.authorize(req)
	resp, err := c.controlClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("daemon persona failed: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Persona string `json:"persona"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.Persona, nil
}

// realtimeUsageMaxAttempts bounds retries on the usage relay. WORKLOAD: the Cloud
// usage-ingest endpoint returns 503 (retryable) when a realtime usage report fails
// to persist transiently, and the daemon propagates that status back to Koe; each
// voice turn emits exactly one usage report, so dropping it on the first 503
// silently under-bills that turn. SYMPTOM if too low: a transient daemon/filesystem blip
// drops a turn's usage; if too high: a persistent local outage delays the
// synchronous durable handoff longer before giving up. OVERRIDE: KOE_USAGE_RELAY_MAX_ATTEMPTS.
const realtimeUsageMaxAttempts = 3

// realtimeUsageRetryBackoffMS is the base backoff between usage-relay attempts
// (multiplied by the attempt index, so 200ms then 400ms for the default 3 attempts).
// OVERRIDE: KOE_USAGE_RELAY_BACKOFF_MS.
const realtimeUsageRetryBackoffMS = 200

// SendRealtimeUsage reports a realtime usage record (model, response_id, token
// details — built from a response.done event) to the daemon's durable outbox.
// The daemon returns after local persistence; Cloud delivery is asynchronous.
// A handoff failure is reported to logs but never interrupts the conversation.
// Koe never sees pricing.
//
// Cloud returns 503 when the usage report fails to persist transiently (the daemon
// forwards that status verbatim), so a 5xx or a transport error is retried with a
// short backoff up to realtimeUsageMaxAttempts. A 4xx (bad body / auth) is permanent
// and returns immediately without retry.
func (c *DaemonClient) SendRealtimeUsage(ctx context.Context, usage json.RawMessage) error {
	return c.sendRealtimeUsage(ctx, usage, "")
}

// SendRealtimeUsageWithPrincipal performs the durable handoff with the
// session's captured bootstrap principal. Keeping this explicit avoids an
// account switch between bootstrap and response.done silently rebinding an
// older realtime session to the new account.
func (c *DaemonClient) SendRealtimeUsageWithPrincipal(ctx context.Context, usage json.RawMessage, principal string) error {
	return c.sendRealtimeUsage(ctx, usage, principal)
}

func (c *DaemonClient) sendRealtimeUsage(ctx context.Context, usage json.RawMessage, principal string) error {
	attempts := koeEnvInt("KOE_USAGE_RELAY_MAX_ATTEMPTS", realtimeUsageMaxAttempts)
	backoff := time.Duration(koeEnvInt("KOE_USAGE_RELAY_BACKOFF_MS", realtimeUsageRetryBackoffMS)) * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		status, err := c.postRealtimeUsage(ctx, usage, principal)
		switch {
		case err != nil:
			lastErr = err // transport error — retryable
		case status == http.StatusOK:
			return nil
		case status >= 500:
			lastErr = fmt.Errorf("daemon usage relay: HTTP %d", status) // transient (incl. 503) — retryable
		default:
			return fmt.Errorf("daemon usage relay: HTTP %d", status) // 4xx — permanent, do not retry
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt < attempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff * time.Duration(attempt)):
			}
		}
	}
	return lastErr
}

// postRealtimeUsage performs one POST /koe/realtime/usage attempt, returning the
// HTTP status (0 on transport error) so SendRealtimeUsage can gate its retry on 5xx.
func (c *DaemonClient) postRealtimeUsage(ctx context.Context, usage json.RawMessage, principal string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/koe/realtime/usage", bytes.NewReader(usage))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if principal = strings.TrimSpace(principal); principal != "" {
		req.Header.Set(client.RealtimeUsagePrincipalHeader, principal)
	}
	c.authorize(req)
	resp, err := c.controlClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// DoTaskRequest is the subset of the daemon's POST /message body that Koe sends.
// Source is always "koe". ThreadID is the per-call burst id; Agent is the
// resolved slug ("" = daemon default).
type DoTaskRequest struct {
	Text           string          `json:"text"`
	Source         string          `json:"source"`
	Agent          string          `json:"agent,omitempty"`
	ThreadID       string          `json:"thread_id,omitempty"`
	CWD            string          `json:"cwd,omitempty"`
	ForegroundHint *ForegroundHint `json:"foreground_hint,omitempty"`
	// ExecutionMode is the locally admitted mode used by the voice ledger.
	// The daemon independently recomputes it from the raw selector fields.
	ExecutionMode          executionprofile.Mode       `json:"execution_mode"`
	RequestedExecutionMode *string                     `json:"requested_execution_mode"`
	FullReason             executionprofile.FullReason `json:"full_reason"`
	InheritedMode          executionprofile.Mode       `json:"inherited_execution_mode,omitempty"`
	LogicalTaskID          string                      `json:"logical_task_id,omitempty"`
	ExecutionRunID         string                      `json:"execution_run_id,omitempty"`
	ParentRunID            string                      `json:"parent_run_id,omitempty"`
}

// ForegroundHint mirrors daemon.RunAgentRequest.foreground_hint without importing
// internal/daemon into the Koe package. Desktop sends it on /call/start so a
// spoken "this window/app" task can use the same AX/screenshot targeting path as
// the quick panel.
type ForegroundHint struct {
	PID      int    `json:"pid,omitempty"`
	AppName  string `json:"app_name,omitempty"`
	BundleID string `json:"bundle_id,omitempty"`
}

// OutcomeKind discriminates the polymorphic POST /message response.
type OutcomeKind int

const (
	OutcomeCompleted OutcomeKind = iota // a RunAgentResult with a reply
	OutcomeInjected                     // follow-up absorbed into a live run
	OutcomeRejected                     // queue_full / active_run_not_ready / cwd_conflict
)

// DoTaskOutcome carries exactly one meaningful payload, keyed by Kind.
type DoTaskOutcome struct {
	Kind          OutcomeKind
	Reply         string        // Completed
	SpokenSummary string        // Completed; compatibility projection for older daemons
	Deliverables  []Deliverable // Completed; validated output metadata, never file bytes
	SessionID     string        // Completed
	Agent         string        // Completed
	Partial       bool          // Completed (soft force-stop)
	FailureCode   string        // Completed (soft)
	Route         string        // Injected / Rejected
	Reason        string        // Rejected (queue_full|active_run_not_ready|cwd_conflict)
	ExecutionRun  *executionprofile.Run
}

// Deliverable is the voice-safe subset of a daemon-validated deliverable. Local
// paths are deliberately excluded: Realtime only needs enough metadata to tell
// the user what was produced and where the full result is available.
type Deliverable struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Title    string `json:"title,omitempty"`
	MIME     string `json:"mime,omitempty"`
	ByteSize int64  `json:"byte_size"`
}

// DoTask POSTs a delegated task and blocks until the back-brain turn completes
// (or the follow-up is injected/rejected). It returns an error only for transport
// failures — a structured rejection is a normal OutcomeRejected, not an error.
func (c *DaemonClient) DoTask(ctx context.Context, req DoTaskRequest) (DoTaskOutcome, error) {
	if req.Source == "" {
		req.Source = "koe"
	}
	body, err := json.Marshal(req)
	if err != nil {
		return DoTaskOutcome{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/message", bytes.NewReader(body))
	if err != nil {
		return DoTaskOutcome{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.authorize(httpReq)
	resp, err := c.doTaskClient.Do(httpReq)
	if err != nil {
		return DoTaskOutcome{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return DoTaskOutcome{}, err
	}
	return parseMessageResponse(raw, resp.StatusCode)
}

// cancelReasons mirrors agenttypes.ParseCancelReason on the daemon (server.go:898).
// Validating client-side avoids a guaranteed 400 round-trip. The daemon accepts
// five reasons (the fifth, sibling_error, is missing from its own 400 message
// string but accepted by ParseCancelReason) — keep this list complete. The
// default is the first entry, and the error text is built from the list, so the
// accepted set and the message it prints cannot drift apart the way the
// daemon's did.
var cancelReasons = []string{"user_cancel", "interrupt", "background", "idle_timeout", "sibling_error"}

// normalizeCancelReason applies the empty-string default and rejects anything
// the daemon would 400.
//
// Shared by DaemonClient.Cancel and the host-relayed externalBackend for the
// same reason parseMessageResponse is shared: a cancel crossing the gomobile
// bridge must not be able to carry a reason the local path would have refused,
// or iOS and macOS disagree about what a valid cancel even is.
func normalizeCancelReason(reason string) (string, error) {
	if reason == "" {
		return cancelReasons[0], nil
	}
	if slices.Contains(cancelReasons, reason) {
		return reason, nil
	}
	return "", fmt.Errorf("unknown cancel reason %q (want %s)", reason, strings.Join(cancelReasons, "|"))
}

// CancelRequest cancels the in-flight run on a route. RouteKey is the burst key
// (agent:<bound>:koe:<burst-id>). RestoreLast asks the daemon to slice the
// session back to before this run.
type CancelRequest struct {
	RouteKey    string `json:"route_key,omitempty"`
	Reason      string `json:"reason,omitempty"`
	RestoreLast bool   `json:"restore_last,omitempty"`
}

// Cancel POSTs /cancel. Returns an error for an unknown reason (caught locally),
// transport failure, or a non-2xx daemon response.
func (c *DaemonClient) Cancel(ctx context.Context, req CancelRequest) error {
	reason, err := normalizeCancelReason(req.Reason)
	if err != nil {
		return err
	}
	req.Reason = reason
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/cancel", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.authorize(httpReq)
	resp, err := c.controlClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cancel failed (status %d): %s", resp.StatusCode, string(raw))
	}
	return nil
}

// AgentSummary is the subset of GET /agents the resolver needs. Description is
// the localized blurb (locale → text); the resolver flattens it before matching.
type AgentSummary struct {
	Slug        string            `json:"name"`
	DisplayName string            `json:"display_name"`
	Description map[string]string `json:"description"`
}

// ListAgents fetches the daemon's agent registry for name resolution.
func (c *DaemonClient) ListAgents(ctx context.Context) ([]AgentSummary, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/agents", nil)
	if err != nil {
		return nil, err
	}
	c.authorize(httpReq)
	resp, err := c.controlClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list agents failed (status %d): %s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		Agents []AgentSummary `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return parsed.Agents, nil
}
