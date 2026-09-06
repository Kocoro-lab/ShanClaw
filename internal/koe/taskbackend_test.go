package koe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeExternalTaskBackend stands in for the host task relay (Swift on iOS).
// It records the JSON the brain sent so tests can assert the wire shape.
type fakeExternalTaskBackend struct {
	doTaskReq  string
	doTaskResp string
	doTaskErr  error
	cancelReq  string
	agentsJSON string
	agentsErr  error
	listedCh   chan struct{}
}

func (f *fakeExternalTaskBackend) DoTask(requestJSON string) (string, error) {
	f.doTaskReq = requestJSON
	return f.doTaskResp, f.doTaskErr
}

func (f *fakeExternalTaskBackend) Cancel(requestJSON string) error {
	f.cancelReq = requestJSON
	return nil
}

func (f *fakeExternalTaskBackend) ListAgents() (string, error) {
	if f.listedCh != nil {
		defer close(f.listedCh)
	}
	return f.agentsJSON, f.agentsErr
}

// The Go-side adapter owns route-key computation: Swift receives it as an
// opaque correlation string in the DoTask JSON and hands it back on Cancel,
// so the key format never leaks across the bridge.
func TestExternalBackendDoTaskWireShape(t *testing.T) {
	fake := &fakeExternalTaskBackend{doTaskResp: `{"reply":"sunny","session_id":"s1","agent":"writer"}`}
	backend := newExternalBackend(fake)

	out, err := backend.DoTask(context.Background(), DoTaskRequest{
		Text: "check tomorrow's weather", Source: "koe", Agent: "writer", ThreadID: "burst-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Kind != OutcomeCompleted || out.Reply != "sunny" || out.SessionID != "s1" || out.Agent != "writer" {
		t.Fatalf("outcome not parsed via shared parser: %+v", out)
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(fake.doTaskReq), &sent); err != nil {
		t.Fatalf("request is not JSON: %v; raw=%s", err, fake.doTaskReq)
	}
	if sent["text"] != "check tomorrow's weather" || sent["agent"] != "writer" || sent["thread_id"] != "burst-1" {
		t.Fatalf("request fields missing: %v", sent)
	}
	if sent["route_key"] != "agent:writer:koe:burst-1" {
		t.Fatalf("route_key = %v, want agent:writer:koe:burst-1", sent["route_key"])
	}
}

func TestExternalBackendDoTaskDefaultAgentRouteKey(t *testing.T) {
	fake := &fakeExternalTaskBackend{doTaskResp: `{"reply":"ok"}`}
	backend := newExternalBackend(fake)
	if _, err := backend.DoTask(context.Background(), DoTaskRequest{Text: "t", Source: "koe", ThreadID: "burst-2"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(fake.doTaskReq), &sent); err != nil {
		t.Fatalf("request is not JSON: %v", err)
	}
	if sent["route_key"] != "default:koe:burst-2" {
		t.Fatalf("route_key = %v, want default:koe:burst-2", sent["route_key"])
	}
}

func TestExternalBackendDoTaskTransportError(t *testing.T) {
	fake := &fakeExternalTaskBackend{doTaskErr: errors.New("mac offline")}
	backend := newExternalBackend(fake)
	if _, err := backend.DoTask(context.Background(), DoTaskRequest{Text: "t", Source: "koe", ThreadID: "b"}); err == nil || !strings.Contains(err.Error(), "mac offline") {
		t.Fatalf("want host error propagated, got %v", err)
	}
}

func TestExternalBackendCancelPassesRouteKeyAndReason(t *testing.T) {
	fake := &fakeExternalTaskBackend{}
	backend := newExternalBackend(fake)
	err := backend.Cancel(context.Background(), CancelRequest{RouteKey: "agent:writer:koe:burst-1", Reason: "user_cancel"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(fake.cancelReq), &sent); err != nil {
		t.Fatalf("cancel request is not JSON: %v", err)
	}
	if sent["route_key"] != "agent:writer:koe:burst-1" || sent["reason"] != "user_cancel" {
		t.Fatalf("cancel fields missing: %v", sent)
	}
}

// The host-relayed path validates the cancel reason exactly like DaemonClient
// does. Both share normalizeCancelReason for the same reason both share
// parseMessageResponse: a cancel crossing the gomobile bridge must not be able
// to carry a reason the local path would have refused, or the two platforms
// disagree about what a valid cancel is.
func TestExternalBackendCancelRejectsAnUnknownReason(t *testing.T) {
	fake := &fakeExternalTaskBackend{}
	backend := newExternalBackend(fake)

	err := backend.Cancel(context.Background(), CancelRequest{RouteKey: "agent:x:koe:b1", Reason: "nope"})
	if err == nil {
		t.Fatal("an unknown cancel reason must be refused before it crosses the bridge")
	}
	if fake.cancelReq != "" {
		t.Fatalf("a refused cancel still reached the host: %s", fake.cancelReq)
	}
	// The message lists the whole accepted set, sibling_error included — the
	// daemon's own 400 text omits it, and that omission is exactly what a
	// hand-written duplicate would copy.
	if !strings.Contains(err.Error(), "sibling_error") {
		t.Fatalf("error text does not list the full accepted set: %v", err)
	}
}

func TestExternalBackendListAgentsDecodes(t *testing.T) {
	fake := &fakeExternalTaskBackend{agentsJSON: `{"agents":[{"name":"writer","display_name":"Writer","description":{"en":"writes things"}}]}`}
	backend := newExternalBackend(fake)
	agents, err := backend.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agents) != 1 || agents[0].Slug != "writer" || agents[0].DisplayName != "Writer" || agents[0].Description["en"] != "writes things" {
		t.Fatalf("agents not decoded: %+v", agents)
	}
}

// parseMessageResponse is the one decoder both backends share: DaemonClient
// (macOS) and externalTaskBackend (iOS) must map a /message-shaped body to the
// same DoTaskOutcome, or the two platforms drift silently.
func TestParseMessageResponseCompleted(t *testing.T) {
	raw := []byte(`{
		"reply": "done: 23°C tomorrow",
		"spoken_summary": "Tokyo is 23 degrees tomorrow",
		"session_id": "sess-1",
		"agent": "default",
		"partial": true,
		"failure_code": "soft_force_stop",
		"deliverables": [{"id":"d1","filename":"weather.md","byte_size":42}]
	}`)
	out, err := parseMessageResponse(raw, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Kind != OutcomeCompleted {
		t.Fatalf("Kind = %v, want OutcomeCompleted", out.Kind)
	}
	if out.Reply != "done: 23°C tomorrow" || out.SpokenSummary != "Tokyo is 23 degrees tomorrow" {
		t.Fatalf("reply/summary not mapped: %+v", out)
	}
	if out.SessionID != "sess-1" || out.Agent != "default" || !out.Partial || out.FailureCode != "soft_force_stop" {
		t.Fatalf("session/agent/partial/failure not mapped: %+v", out)
	}
	if len(out.Deliverables) != 1 || out.Deliverables[0].Filename != "weather.md" || out.Deliverables[0].ByteSize != 42 {
		t.Fatalf("deliverables not mapped: %+v", out.Deliverables)
	}
}

func TestParseMessageResponseInjectedStatuses(t *testing.T) {
	for _, status := range []string{"injected", "retracted_before_delivery"} {
		out, err := parseMessageResponse([]byte(`{"status":"`+status+`","route":"agent:x:koe:b"}`), 200)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", status, err)
		}
		if out.Kind != OutcomeInjected || out.Route != "agent:x:koe:b" {
			t.Fatalf("%s: got %+v, want Injected on route", status, out)
		}
	}
}

func TestParseMessageResponseRejected(t *testing.T) {
	out, err := parseMessageResponse([]byte(`{"status":"rejected","route":"default:koe:b","reason":"queue_full"}`), 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Kind != OutcomeRejected || out.Reason != "queue_full" || out.Route != "default:koe:b" {
		t.Fatalf("got %+v, want structured rejection", out)
	}
}

func TestParseMessageResponseErrorBody(t *testing.T) {
	_, err := parseMessageResponse([]byte(`{"error":"agent exploded"}`), 500)
	if err == nil || !strings.Contains(err.Error(), "agent exploded") || !strings.Contains(err.Error(), "500") {
		t.Fatalf("want error carrying body message and status, got %v", err)
	}
}

// The exact iOS incident shape: a plain-text 404 from a host that does not
// speak the protocol must surface as a decode error naming the status, never
// as a silent zero-value outcome.
func TestParseMessageResponseMalformedBody(t *testing.T) {
	_, err := parseMessageResponse([]byte("404 page not found"), 404)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want decode error naming status 404, got %v", err)
	}
}

// A session built over a host task backend must populate its agent registry
// from that backend off the critical path: named-agent dispatch starts working
// once the fetch lands, and session construction never blocks on it.
func TestSessionTaskBackendPopulatesResolverAsync(t *testing.T) {
	fake := &fakeExternalTaskBackend{
		agentsJSON: `{"agents":[{"name":"writer","display_name":"Writer","description":{"en":"writes"}}]}`,
		listedCh:   make(chan struct{}),
	}
	s := NewSession(SessionConfig{BurstID: "burst-resolver", Audio: &fakeExternalAudio{}, TaskBackend: fake})

	select {
	case <-fake.listedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("session never asked the backend for agents")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		res := s.handler.disp.res().Resolve("writer")
		if res.Status == ResolveResolved && res.Slug == "writer" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("resolver never learned the registry: %+v", res)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The agent registry arrives asynchronously on iOS (ListAgents may block up to
// 30s, so it must never sit on the call-start critical path). A swap installed
// after construction has to become visible to later dispatches.
func TestDispatcherSetResolverSwapsRegistry(t *testing.T) {
	d := NewDispatcher(nil, NewAgentResolver(nil, nil), NewCallState("burst-swap", ""), nil)
	if got := d.res().Resolve("writer"); got.Status != ResolveNotFound {
		t.Fatalf("empty registry resolved %q: %+v", "writer", got)
	}
	d.SetResolver(NewAgentResolver([]AgentSummary{{Slug: "writer", DisplayName: "Writer"}}, nil))
	res := d.res().Resolve("writer")
	if res.Status != ResolveResolved || res.Slug != "writer" {
		t.Fatalf("swap not visible to dispatch: %+v", res)
	}
}
