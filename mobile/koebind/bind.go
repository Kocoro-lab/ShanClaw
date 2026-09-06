// Package koebind exposes the Koe front brain to iOS through gomobile.
//
// Everything exported here must be gomobile-safe: signed ints, floats, string,
// bool, []byte, error, and interfaces/structs declared in THIS package. No maps,
// no struct slices, no multi-return. gobind does not skip unsupported exported
// API — it crashes with a nil dereference — which is why the brain package is
// never bound directly and this wrapper stays deliberately narrow.
package koebind

import (
	"os"

	"github.com/Kocoro-lab/ShanClaw/internal/koe"
)

// SetBargeIn allows the user to interrupt Kocoro by talking over it. It arms
// the same env gate cmd/koe.go's --barge-in flag arms, so the shared brain's
// talk-over path (server-VAD speech_started → pause playback → native floor
// judge) behaves identically on both platforms. Call it before NewBridge.
//
// It must be a Go-side setter: the Go runtime snapshots the process environment
// at init, so a setenv from Swift after the framework loads is invisible to
// os.Getenv.
func SetBargeIn(enabled bool) {
	v := "0"
	if enabled {
		v = "1"
	}
	os.Setenv("KOE_VPIO_BARGE_IN", v)
}

// AudioSink is implemented in Swift by the app's audio layer. gomobile turns it
// into an Objective-C protocol.
//
// These are exactly the calls the brain makes into audio, and every one of them
// is boolean state — no audio samples cross this boundary, which is what keeps
// the bridge off the realtime path.
type AudioSink interface {
	SetSpeaking(s bool)
	SetPlaybackEnabled(s bool)
	SetPlaybackPaused(paused bool)
	DropCapture() bool
	UserMicOff() bool
	SetUserMicOff(off bool)
	UserMicSticky() bool
	PlaybackIdle() bool
}

// EventSender is implemented in Swift: it puts one Realtime client event onto
// the WebRTC data channel. The brain never owns a socket.
type EventSender interface {
	Send(payloadJSON string) error
}

// ControlHost is implemented in Swift: the brain asking the app's UI to act
// (show/hide/new_conversation/open_settings). Returning an error declines the
// action; the reason travels back to the model as a failed tool result, so a
// reduced shell degrades honestly — the iOS app accepts only "show" (the app
// is already on screen) and declines the rest with a spoken-word reason.
// gomobile surfaces the error return as a throwing method in Swift.
type ControlHost interface {
	ControlApp(action string) error
}

// TaskBackend is implemented in Swift: it executes delegated work by relaying
// to Shannon Cloud's remote-run channel (the paired Mac's daemon runs the
// task). Strings in, strings out — gobind supports neither maps nor struct
// slices. Calls may block: the brain only ever invokes DoTask from its
// detached do_task goroutine, never from the event loop.
//
// DoTask receives a DoTaskRequest JSON extended with an opaque route_key and
// must return a /message-shaped response body. Cancel receives a CancelRequest
// JSON carrying the same route_key. ListAgents returns {"agents":[...]}.
type TaskBackend interface {
	DoTask(requestJSON string) (string, error)
	Cancel(requestJSON string) error
	ListAgents() (string, error)
}

// CallHost is implemented in Swift: the brain telling the app the call must
// end — the model called the end_call voice tool, or the ASR dismiss-phrase
// backstop fired (it stays disarmed when no CallHost is supplied). The brain
// has already stopped its output and entered its local terminal when EndCall
// arrives; the app closes transport and audio, exactly what its own hang-up
// button does. Fired from a fresh goroutine, never the event loop.
type CallHost interface {
	EndCall()
}

// UsageReporter is implemented in Swift: it relays one turn's billing report
// to Shannon Cloud's usage-ingest endpoint (POST /api/v1/usage/realtime). The
// string is the brain's verbatim {provider, model, response_id, usage} JSON —
// the host posts it unparsed, exactly as the daemon forwards the same bytes
// for macOS, so Cloud sees one shape from every platform. Called from a fresh
// goroutine, never the event loop; a host that supplies none is never billed
// for its realtime turns.
type UsageReporter interface {
	ReportUsage(usageJSON string)
}

// sinkAdapter bridges the Swift-facing interface to the brain's own. They differ
// only in that koe keeps dropCapture unexported on its internal interface.
type sinkAdapter struct{ s AudioSink }

func (a sinkAdapter) SetSpeaking(v bool)        { a.s.SetSpeaking(v) }
func (a sinkAdapter) SetPlaybackEnabled(v bool) { a.s.SetPlaybackEnabled(v) }
func (a sinkAdapter) SetPlaybackPaused(v bool)  { a.s.SetPlaybackPaused(v) }
func (a sinkAdapter) DropCapture() bool         { return a.s.DropCapture() }
func (a sinkAdapter) UserMicOff() bool          { return a.s.UserMicOff() }
func (a sinkAdapter) SetUserMicOff(v bool)      { a.s.SetUserMicOff(v) }
func (a sinkAdapter) UserMicSticky() bool       { return a.s.UserMicSticky() }
func (a sinkAdapter) PlaybackIdle() bool        { return a.s.PlaybackIdle() }

// Bridge is the single bound object; the app holds one per call.
type Bridge struct {
	session *koe.Session
}

// NewBridge starts a call's front brain against Swift-supplied audio and
// transport. backendURL is Shannon Cloud on iOS, where there is no local
// daemon. model is what the mint response pinned — it stamps usage reports,
// which Cloud rejects without a model.
func NewBridge(
	burstID string,
	boundAgent string,
	backendURL string,
	sink AudioSink,
	sender EventSender,
	host ControlHost,
	taskBackend TaskBackend,
	callHost CallHost,
	model string,
	usageReporter UsageReporter,
) *Bridge {
	cfg := koe.SessionConfig{
		BurstID:    burstID,
		BoundAgent: boundAgent,
		BackendURL: backendURL,
		Model:      model,
		// iOS WebRTC captures through Apple's VPIO voice processing, so the
		// uplink is already echo-cancelled — the precondition for the floor
		// gate. Without this the brain treats the host as half-duplex and
		// nativeFloorEnabled() stays false regardless of SetBargeIn.
		FullDuplexAEC: true,
	}
	// gomobile hands over a non-nil interface wrapping a nil Objective-C object
	// when Swift passes nil, so each hook is guarded rather than assumed present.
	if sink != nil {
		cfg.Audio = sinkAdapter{s: sink}
	}
	if sender != nil {
		cfg.Send = sender.Send
	}
	if host != nil {
		cfg.ControlApp = host.ControlApp
	}
	if taskBackend != nil {
		cfg.TaskBackend = taskBackend
	}
	if callHost != nil {
		cfg.OnEndCall = callHost.EndCall
	}
	if usageReporter != nil {
		cfg.OnUsage = usageReporter.ReportUsage
	}
	return &Bridge{session: koe.NewSession(cfg)}
}

// HandleEvent feeds one Realtime server event, raw JSON straight off the data
// channel. This is the front brain's only input.
//
// MUST NOT be called concurrently. The brain's event path writes plain,
// non-atomic state and is safe only because a transport delivers serially.
// Deliver from ONE serial queue — an RTCDataChannel delegate dispatched onto a
// concurrent queue corrupts that state with no visible error and no crash.
func (b *Bridge) HandleEvent(raw []byte) { b.session.HandleEvent(raw) }

// Close ends the call's brain: it stops the result-delivery worker and retires
// the call's voice scope, so a task finishing after hang-up is not spoken into
// a dead transport. Call it exactly when the app tears the call down.
func (b *Bridge) Close() { b.session.Close() }

// BurstID identifies this call's task lineage.
func (b *Bridge) BurstID() string { return b.session.BurstID() }

// SentEventCount is how many client events the host has actually accepted from
// the brain — the cheapest way for the app to assert the brain reacted at all.
// An event that failed to encode, or that the host's Send rejected, is not
// counted, so this is evidence of delivery rather than of intent.
func (b *Bridge) SentEventCount() int64 { return b.session.SentEventCount() }

// SendSessionUpdate configures the realtime session — instructions, the seven
// voice tools, turn detection, voice. Call it the moment the event channel
// opens; a call that skips it connects and then does nothing at all.
func (b *Bridge) SendSessionUpdate(persona string, voice string, fullDuplexAEC bool) error {
	return b.session.SendSessionUpdate(persona, voice, fullDuplexAEC)
}

// DefaultPersona is the mobile spoken persona for one reply language ("" mirrors
// the user's utterance): the same personality, tool discipline, and stop/cancel/
// end-call vocabulary macOS composes, with host guidance adjusted to the iPhone
// shell, plus the concurrent-tasks section when the task ledger is enabled.
// Pass the result to SendSessionUpdate — a call configured with an empty persona
// runs with tools but no personality, which is how the first iOS calls shipped.
func DefaultPersona(language string) string {
	return koe.AppendTaskLedgerPersona(koe.SpokenPersona(language, koe.HostMobile))
}
