package koe

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Kocoro-lab/ShanClaw/internal/executionprofile"
)

// Backend executes delegated work for the voice brain: do_task delegation,
// route cancellation, and the agent registry for name resolution.
//
// macOS keeps *DaemonClient (the local daemon, implicit interface match — no
// change to link.go). iOS supplies an externalTaskBackend that relays through
// the host application to Shannon Cloud's remote-run channel. The brain never
// holds a raw backend URL beyond constructing the default client.
type Backend interface {
	DoTask(ctx context.Context, req DoTaskRequest) (DoTaskOutcome, error)
	Cancel(ctx context.Context, req CancelRequest) error
	ListAgents(ctx context.Context) ([]AgentSummary, error)
}

var _ Backend = (*DaemonClient)(nil)

// ExternalTaskBackend is the gomobile-safe seam a host application implements
// to execute delegated work (Swift relays it to Shannon Cloud's remote-run
// channel on iOS). Strings in, strings out — gobind supports neither maps nor
// struct slices. Calls may block: DoTask is only ever invoked from the
// detached do_task goroutine, never from the brain's event loop.
//
// DoTask receives a DoTaskRequest JSON extended with route_key and must return
// a /message-shaped response body. Cancel receives a CancelRequest JSON.
// ListAgents returns the GET /agents body ({"agents":[...]}).
type ExternalTaskBackend interface {
	DoTask(requestJSON string) (string, error)
	Cancel(requestJSON string) error
	ListAgents() (string, error)
}

// externalBackend adapts an ExternalTaskBackend to Backend. It owns route-key
// computation so the key format never leaks across the bridge: the host sees
// an opaque correlation string on DoTask and hands it back on Cancel.
type externalBackend struct {
	ext ExternalTaskBackend
}

func newExternalBackend(ext ExternalTaskBackend) Backend { return &externalBackend{ext: ext} }

func (b *externalBackend) DoTask(_ context.Context, req DoTaskRequest) (DoTaskOutcome, error) {
	body, err := json.Marshal(struct {
		DoTaskRequest
		RouteKey string `json:"route_key"`
	}{DoTaskRequest: req, RouteKey: burstRouteKey(req.Agent, req.ThreadID)})
	if err != nil {
		return DoTaskOutcome{}, err
	}
	raw, err := b.ext.DoTask(string(body))
	if err != nil {
		return DoTaskOutcome{}, err
	}
	return parseMessageResponse([]byte(raw), 0)
}

func (b *externalBackend) Cancel(_ context.Context, req CancelRequest) error {
	reason, err := normalizeCancelReason(req.Reason)
	if err != nil {
		return err
	}
	req.Reason = reason
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return b.ext.Cancel(string(body))
}

func (b *externalBackend) ListAgents(_ context.Context) ([]AgentSummary, error) {
	raw, err := b.ext.ListAgents()
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Agents []AgentSummary `json:"agents"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("decode agents listing: %w", err)
	}
	return parsed.Agents, nil
}

// parseMessageResponse maps one /message-shaped response body to a DoTaskOutcome.
//
// It is shared by both backends — DaemonClient (macOS, local daemon) and
// externalTaskBackend (iOS, host-supplied relay) — so delegation outcome
// semantics cannot drift between platforms. statusCode is only for error text;
// outcome discrimination rides the body's status field, matching the daemon.
func parseMessageResponse(raw []byte, statusCode int) (DoTaskOutcome, error) {
	var parsed struct {
		Reply         string                `json:"reply"`
		SpokenSummary string                `json:"spoken_summary"`
		SessionID     string                `json:"session_id"`
		Agent         string                `json:"agent"`
		Partial       bool                  `json:"partial"`
		FailureCode   string                `json:"failure_code"`
		Deliverables  []Deliverable         `json:"deliverables"`
		Status        string                `json:"status"`
		Route         string                `json:"route"`
		Reason        string                `json:"reason"`
		Error         string                `json:"error"`
		ExecutionRun  *executionprofile.Run `json:"execution_run"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return DoTaskOutcome{}, fmt.Errorf("decode POST /message response (status %d): %w; body=%s", statusCode, err, string(raw))
	}
	if parsed.Error != "" {
		return DoTaskOutcome{}, fmt.Errorf("daemon error (status %d): %s", statusCode, parsed.Error)
	}

	switch parsed.Status {
	case "":
		return DoTaskOutcome{
			Kind: OutcomeCompleted, Reply: parsed.Reply, SpokenSummary: parsed.SpokenSummary, SessionID: parsed.SessionID,
			Agent: parsed.Agent, Partial: parsed.Partial, FailureCode: parsed.FailureCode,
			Deliverables: append([]Deliverable(nil), parsed.Deliverables...),
			ExecutionRun: parsed.ExecutionRun,
		}, nil
	case "injected", "retracted_before_delivery":
		return DoTaskOutcome{Kind: OutcomeInjected, Route: parsed.Route}, nil
	default: // "rejected" (and any future status) → treat as a structured rejection
		return DoTaskOutcome{Kind: OutcomeRejected, Route: parsed.Route, Reason: parsed.Reason}, nil
	}
}
