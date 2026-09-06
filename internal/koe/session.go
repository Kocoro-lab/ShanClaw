package koe

import (
	"context"
	"encoding/json"
	"sync/atomic"
)

// SessionConfig describes one voice call for an out-of-process driver.
//
// Every field is supplied by the host application because none of it exists on
// the platform the host runs on: on iOS the audio layer is Swift, the transport
// is a WebRTC data channel, and there is no local daemon to reach.
type SessionConfig struct {
	// BurstID scopes the call's task ledger and result mailbox.
	BurstID string
	// BoundAgent is the agent this call is pinned to; empty means default.
	BoundAgent string
	// BackendURL is where do_task/cancel/agent-listing go. On macOS that is the
	// local daemon; on iOS it is Shannon Cloud.
	BackendURL string
	// Audio owns the microphone and speaker. See ExternalAudio.
	Audio ExternalAudio
	// Send delivers one Realtime client event, already JSON-encoded, to the
	// transport. The brain never owns a socket.
	Send func(payloadJSON string) error
	// ControlApp asks the host UI to act (show/hide/new_conversation/open_settings).
	// Optional. A non-nil error is the host declining the action; it reaches the
	// model as a failed tool result with this reason, so a host with a reduced
	// shell (the iOS app) can degrade honestly instead of faking success or
	// falling back to the generic "not wired" of a missing hook.
	ControlApp func(action string) error
	// TaskBackend executes delegated work (do_task/cancel/agent listing) through
	// the host application. Optional; nil keeps the DaemonClient path against
	// BackendURL. On iOS the host relays to Shannon Cloud's remote-run channel —
	// BackendURL alone is a dead end there because Cloud does not speak the
	// daemon's local /message protocol.
	TaskBackend ExternalTaskBackend
	// FullDuplexAEC declares that the host's audio path has echo cancellation
	// and keeps the mic live while Kocoro speaks (iOS WebRTC runs Apple's VPIO
	// voice processing). It arms the same runtime floor gate webrtc.go arms on
	// macOS; without it nativeFloorEnabled() is false and barge-in cannot work
	// no matter what the session.update advertised.
	FullDuplexAEC bool
	// OnEndCall tears the call down when the brain accepts a hang-up — the
	// end_call voice tool or the ASR dismiss-phrase backstop (which stays
	// disarmed while this is nil). The brain has already stopped its output and
	// entered its local terminal when this fires; the host closes transport and
	// audio. macOS wires the equivalent closure in cmd/koe.go. Optional, but a
	// host that leaves it nil cannot be hung up by voice.
	OnEndCall func()
	// Model is the realtime model the call runs on, stamped into each usage
	// report. Cloud rejects a report with an empty model, and only webrtc.go's
	// Connect (the macOS path) ever set the handler's copy — a façade host must
	// supply it here, from the authoritative mint response.
	Model string
	// OnUsage receives one billing report per completed response — the brain's
	// verbatim {provider, model, response_id, usage} JSON. The host relays it
	// unparsed to Cloud's usage-ingest endpoint, exactly as macOS forwards the
	// same bytes through the daemon (cmd/koe.go onUsage → POST
	// /koe/realtime/usage → Cloud). Fired from a fresh goroutine, never the
	// event loop. Optional, but a host that leaves it nil is never billed for
	// its realtime turns.
	OnUsage func(usageJSON string)
}

// Session is the exported handle on the front brain.
//
// The brain's own types are unexported on purpose — they are internal to the
// event loop. This façade exists so a host outside package koe (the iOS gomobile
// binding) can drive the SAME logic macOS runs, rather than reimplementing
// turn-taking, the hang-up vocabulary, the task ledger and the result mailbox in
// another language, where they would drift silently.
type Session struct {
	handler   *eventHandler
	state     *CallState
	send      func(any) error
	sendCount int64
	cancel    context.CancelFunc
}

// NewSession wires the front brain against host-supplied audio and transport.
func NewSession(cfg SessionConfig) *Session {
	s := &Session{}
	s.state = NewCallState(cfg.BurstID, cfg.BoundAgent)

	var client Backend
	if cfg.TaskBackend != nil {
		client = newExternalBackend(cfg.TaskBackend)
	} else {
		client = NewDaemonClient(cfg.BackendURL)
	}
	resolver := NewAgentResolver(nil, nil)

	var controlApp ControlAppFunc
	if cfg.ControlApp != nil {
		controlApp = func(_ context.Context, action string) error {
			return cfg.ControlApp(action)
		}
	}
	disp := NewDispatcher(client, resolver, s.state, controlApp)
	if cfg.TaskBackend != nil {
		// Registry fetch stays off the call-start critical path (ListAgents can
		// block up to 30s — same rule as cmd/koe.go's deferred resolverHolder).
		// Until it lands, named agents are unresolved, exactly like an empty
		// registry; a fetch error keeps that state.
		go func() {
			agents, err := client.ListAgents(context.Background())
			if err != nil || len(agents) == 0 {
				return
			}
			disp.SetResolver(NewAgentResolver(agents, NoopSemanticMatcher{}))
		}()
	}

	sendFn := func(v any) error {
		if cfg.Send == nil {
			return nil
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if err := cfg.Send(string(raw)); err != nil {
			return err
		}
		// Counted only once the host has actually taken the event. This counter is
		// what koebind tells an iOS host to assert the brain reacted at all, so
		// counting attempts would let a marshal failure — or a host that never
		// wired Send — read as proof of a working brain.
		atomic.AddInt64(&s.sendCount, 1)
		return nil
	}

	s.send = sendFn
	s.handler = newEventHandler(disp, s.state, NewExternalAudioController(cfg.Audio), sendFn)
	s.handler.onEndCall = cfg.OnEndCall
	s.handler.fullDuplexAEC = cfg.FullDuplexAEC
	s.handler.model = cfg.Model
	if cfg.OnUsage != nil {
		onUsage := cfg.OnUsage
		// Detach the relay from the event loop — the same fire-and-forget
		// cmd/koe.go's onUsage closure does; the host's HTTP retries may block.
		s.handler.onUsage = func(usage json.RawMessage) {
			go onUsage(string(usage))
		}
	}
	// The mailbox delivery worker. macOS starts it in Connect (webrtc.go); the
	// façade owns it here because the host owns the transport. Without it a
	// completed do_task result stays parked in the mailbox forever — never
	// injected, never spoken — and the model keeps answering "no news yet"
	// about work the daemon finished long ago (2026-08-21 iOS device log).
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.handler.runResponseSender(ctx)
	return s
}

// Close ends the call's delivery scope: it stops the mailbox worker and retires
// the burst so results landing after hang-up are dropped from voice delivery
// (the daemon session still holds the durable report — same contract as
// cmd/koe.go's RetireBurst on call end). Safe to call more than once.
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.handler.resultMailbox.RetireBurst(s.state.BurstID())
}

// HandleEvent feeds one Realtime server event to the brain. `raw` is the JSON
// exactly as it arrived on the transport — the brain parses it itself, so no
// host-side interpretation can diverge between platforms.
//
// MUST NOT be called concurrently. The event path writes plain, non-atomic
// handler fields (responseDoneAt, floorPausedAt, the turn ledger's bookkeeping)
// and is safe only because every transport delivers serially — pion's OnMessage
// on macOS, a single serial queue on iOS. Nothing in the type system stops a
// host from dispatching its data-channel callback onto a concurrent queue, and
// the resulting corruption has no visible error.
func (s *Session) HandleEvent(raw []byte) {
	s.handler.handleEvent(context.Background(), raw)
}

// BurstID identifies this call's task lineage.
func (s *Session) BurstID() string { return s.state.BurstID() }

// SentEventCount is how many client events the host has actually accepted from
// the brain; a marshal failure or a rejected send is not counted. Useful for
// asserting the brain actually reacted, which a silent audio path cannot show.
func (s *Session) SentEventCount() int64 { return atomic.LoadInt64(&s.sendCount) }

// SendSessionUpdate configures the realtime session: spoken instructions, the
// seven voice tools, turn detection, and the output voice.
//
// It MUST be sent as soon as the event channel opens. Without it the model has
// no tools and no instructions, so the call connects, the audio path runs, and
// nothing whatsoever happens — which is precisely how the first iOS call failed.
// The macOS path does the same thing from its data channel's OnOpen.
//
// `persona` is a parameter rather than a package constant because the caller
// assembles it: SpokenPersona/AppendTaskLedgerPersona in persona.go hold the
// shared text and each host layers its own runtime context on top. A host that
// passes "" still gets a functioning call with tools and turn-taking, but no
// personality at all.
func (s *Session) SendSessionUpdate(persona, voice string, fullDuplexAEC bool) error {
	if s.send == nil {
		return nil
	}
	return s.send(sessionConfig(persona, voice, fullDuplexAEC))
}
