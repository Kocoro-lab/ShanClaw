package koebind

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCallHost struct{ ended chan struct{} }

func (f *fakeCallHost) EndCall() { f.ended <- struct{}{} }

// The brain's hang-up must cross the binding: a Swift host that supplies a
// CallHost gets EndCall when the model calls the end_call voice tool. Without
// this the brain enters its local terminal and the iOS call runs forever.
func TestBridgeEndCallReachesCallHost(t *testing.T) {
	host := &fakeCallHost{ended: make(chan struct{}, 1)}
	b := NewBridge("burst-bind-end", "", "", nil, nil, nil, nil, host, "", nil)

	b.HandleEvent([]byte(`{"type":"input_audio_buffer.committed"}`))
	b.HandleEvent([]byte(`{"type":"response.created","response":{"id":"end-response"}}`))
	b.HandleEvent([]byte(`{"type":"response.function_call_arguments.done","response_id":"end-response","name":"end_call","call_id":"c1","arguments":"{}"}`))

	select {
	case <-host.ended:
	case <-time.After(2 * time.Second):
		t.Fatal("end_call never crossed the binding to CallHost.EndCall")
	}
}

type recordingSink struct {
	mu     sync.Mutex
	calls  []string
	speaks bool
}

func (r *recordingSink) record(c string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}
func (r *recordingSink) has(c string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, got := range r.calls {
		if got == c {
			return true
		}
	}
	return false
}
func (r *recordingSink) SetSpeaking(s bool) {
	r.mu.Lock()
	r.speaks = s
	r.mu.Unlock()
	r.record("SetSpeaking")
}
func (r *recordingSink) SetPlaybackEnabled(bool) { r.record("SetPlaybackEnabled") }
func (r *recordingSink) SetPlaybackPaused(p bool) {
	if p {
		r.record("SetPlaybackPaused(true)")
	} else {
		r.record("SetPlaybackPaused(false)")
	}
}
func (r *recordingSink) DropCapture() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.speaks
}
func (r *recordingSink) UserMicOff() bool    { return false }
func (r *recordingSink) SetUserMicOff(bool)  {}
func (r *recordingSink) UserMicSticky() bool { return false }
func (r *recordingSink) PlaybackIdle() bool  { return false }

// Talk-over must cross the binding as a native-floor pause: with barge-in
// enabled, the user speaking over Kocoro pauses playback (a resumable hold for
// the floor judge) instead of being ignored. This is the iOS barge-in wiring —
// SetBargeIn arms the same env gate cmd/koe.go's --barge-in arms, and NewBridge
// must declare the host full-duplex or the floor gate stays cold.
func TestBargeInPausesPlaybackAcrossBinding(t *testing.T) {
	SetBargeIn(true)
	t.Cleanup(func() { SetBargeIn(false) })

	sink := &recordingSink{}
	b := NewBridge("burst-bind-floor", "", "", sink, nil, nil, nil, nil, "", nil)

	b.HandleEvent([]byte(`{"type":"response.created","response":{"id":"resp-floor"}}`))
	b.HandleEvent([]byte(`{"type":"output_audio_buffer.started","response_id":"resp-floor"}`))
	b.HandleEvent([]byte(`{"type":"input_audio_buffer.speech_started"}`))

	if !sink.has("SetPlaybackPaused(true)") {
		t.Fatalf("talk-over with barge-in on must pause playback for the floor judge; sink saw %v", sink.calls)
	}
}

// Without SetBargeIn the binding must keep the half-duplex default: talk-over
// while speaking is impossible (mic gated), so a stray speech_started must not
// pause anything.
func TestBargeInOffKeepsHalfDuplex(t *testing.T) {
	SetBargeIn(false)

	sink := &recordingSink{}
	b := NewBridge("burst-bind-halfduplex", "", "", sink, nil, nil, nil, nil, "", nil)

	b.HandleEvent([]byte(`{"type":"response.created","response":{"id":"resp-hd"}}`))
	b.HandleEvent([]byte(`{"type":"output_audio_buffer.started","response_id":"resp-hd"}`))
	b.HandleEvent([]byte(`{"type":"input_audio_buffer.speech_started"}`))

	if sink.has("SetPlaybackPaused(true)") {
		t.Fatalf("barge-in off must not pause playback on speech_started; sink saw %v", sink.calls)
	}
}

type recordingUsageReporter struct{ reports chan string }

func (r *recordingUsageReporter) ReportUsage(usageJSON string) { r.reports <- usageJSON }

// A turn's usage must cross the binding with the model stamped in: Cloud
// rejects a report without model/response_id, and on iOS only the host knows
// the model (from the mint response). The report body itself is the brain's
// verbatim JSON — the host relays it unparsed.
func TestBridgeUsageCrossesBinding(t *testing.T) {
	rep := &recordingUsageReporter{reports: make(chan string, 1)}
	b := NewBridge("burst-bind-usage", "", "", nil, nil, nil, nil, nil, "gpt-realtime-test", rep)

	b.HandleEvent([]byte(`{"type":"response.done","response":{"id":"resp-u1","status":"completed","usage":{"total_tokens":7}}}`))

	select {
	case got := <-rep.reports:
		for _, want := range []string{`"model":"gpt-realtime-test"`, `"response_id":"resp-u1"`, `"total_tokens":7`} {
			if !strings.Contains(got, want) {
				t.Fatalf("usage report missing %s; body=%s", want, got)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("response.done usage never crossed the binding to UsageReporter.ReportUsage")
	}
}

type recordingSender struct {
	mu   sync.Mutex
	sent []string
}

func (r *recordingSender) Send(payloadJSON string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, payloadJSON)
	return nil
}

func (r *recordingSender) waitFor(t *testing.T, markers ...string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, payload := range r.sent {
			ok := true
			for _, m := range markers {
				if !strings.Contains(payload, m) {
					ok = false
					break
				}
			}
			if ok {
				r.mu.Unlock()
				return
			}
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("no sent payload contained %q; sent: %v", markers, r.sent)
}

// declineControlHost is the honest-degradation host the iOS app implements:
// "show" succeeds (the app is already on screen), everything else is declined
// with a reason the model can speak.
type declineControlHost struct{}

func (declineControlHost) ControlApp(action string) error {
	if action == "show" {
		return nil
	}
	return fmt.Errorf("on iPhone the user does that by hand in the app")
}

// A declined UI action must cross the binding as a failed tool result carrying
// the host's reason — not the generic "not wired", and never a fake success.
func TestBridgeControlAppDeclineCrossesBinding(t *testing.T) {
	sender := &recordingSender{}
	b := NewBridge("burst-bind-ctl", "", "", nil, sender, declineControlHost{}, nil, nil, "", nil)

	b.HandleEvent([]byte(`{"type":"input_audio_buffer.committed"}`))
	b.HandleEvent([]byte(`{"type":"response.created","response":{"id":"ctl-resp"}}`))
	b.HandleEvent([]byte(`{"type":"response.function_call_arguments.done","response_id":"ctl-resp","name":"control_app","call_id":"ctl-1","arguments":"{\"action\":\"open_settings\"}"}`))

	sender.waitFor(t, "function_call_output", "failed", "by hand in the app")
}

// The action the iOS host does honor keeps reporting plain success.
func TestBridgeControlAppShowSucceeds(t *testing.T) {
	sender := &recordingSender{}
	b := NewBridge("burst-bind-ctl-ok", "", "", nil, sender, declineControlHost{}, nil, nil, "", nil)

	b.HandleEvent([]byte(`{"type":"input_audio_buffer.committed"}`))
	b.HandleEvent([]byte(`{"type":"response.created","response":{"id":"ctl-resp-2"}}`))
	b.HandleEvent([]byte(`{"type":"response.function_call_arguments.done","response_id":"ctl-resp-2","name":"control_app","call_id":"ctl-2","arguments":"{\"action\":\"show\"}"}`))

	sender.waitFor(t, "function_call_output", `\"status\":\"ok\"`)
}

// DefaultPersona is the mobile spoken persona: same shared personality and
// tool discipline as macOS, host guidance adjusted to the iPhone shell, task
// ledger section included exactly as the desktop composition appends it.
func TestDefaultPersonaMobileVariant(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	p := DefaultPersona("")
	if p == "" {
		t.Fatal("DefaultPersona returned empty")
	}
	if strings.Contains(p, "Option key") {
		t.Error("mobile persona tells the user to press the Option key")
	}
	for _, want := range []string{"iPhone", "# Personality and Tone", "# Concurrent Tasks", "control_app"} {
		if !strings.Contains(p, want) {
			t.Errorf("DefaultPersona missing %q", want)
		}
	}
	if zh := DefaultPersona("zh"); !strings.Contains(zh, "Always reply in Chinese") {
		t.Error("DefaultPersona(zh) not pinned to Chinese")
	}
}
