package koe

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeExternalAudio stands in for the host audio layer (Swift on iOS). It records
// what the brain asked for, so these tests can assert the brain drives the audio
// gates without a real device.
type fakeExternalAudio struct {
	mu       sync.Mutex
	calls    []string
	speaking bool
}

func (f *fakeExternalAudio) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeExternalAudio) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeExternalAudio) SetSpeaking(s bool) {
	f.mu.Lock()
	f.speaking = s
	f.mu.Unlock()
	f.record("SetSpeaking")
}
func (f *fakeExternalAudio) SetPlaybackEnabled(bool) { f.record("SetPlaybackEnabled") }
func (f *fakeExternalAudio) SetPlaybackPaused(bool)  { f.record("SetPlaybackPaused") }
func (f *fakeExternalAudio) DropCapture() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.speaking
}
func (f *fakeExternalAudio) SetUserMicOff(bool)  { f.record("SetUserMicOff") }
func (f *fakeExternalAudio) UserMicOff() bool    { return false }
func (f *fakeExternalAudio) UserMicSticky() bool { return false }
func (f *fakeExternalAudio) PlaybackIdle() bool  { return true }

func contains(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

// The façade must actually reach the brain: a response starting has to gate the
// microphone, or the model hears its own voice and answers itself.
func TestSessionDrivesHostAudioOnResponseStart(t *testing.T) {
	audio := &fakeExternalAudio{}
	s := NewSession(SessionConfig{BurstID: "burst-facade", Audio: audio})

	s.HandleEvent([]byte(`{"type":"response.created","response":{"id":"resp-1"}}`))

	calls := audio.snapshot()
	if !contains(calls, "SetSpeaking") {
		t.Fatalf("response.created must gate the mic; host saw %v", calls)
	}
	if !contains(calls, "SetPlaybackEnabled") {
		t.Fatalf("response.created must open playback; host saw %v", calls)
	}
}

// A host that supplies no audio layer must not crash the brain — the same
// contract the nil audio path has had since before AudioController existed.
func TestSessionWithoutAudioIsInert(t *testing.T) {
	s := NewSession(SessionConfig{BurstID: "burst-no-audio"})
	s.HandleEvent([]byte(`{"type":"response.created","response":{"id":"resp-1"}}`))
	s.HandleEvent([]byte(`{"type":"response.done","response":{"id":"resp-1","status":"completed"}}`))
}

// Malformed transport payloads must be tolerated, not fatal: the brain sits
// directly on a network-fed data channel.
func TestSessionToleratesGarbageEvents(t *testing.T) {
	audio := &fakeExternalAudio{}
	s := NewSession(SessionConfig{BurstID: "burst-garbage", Audio: audio})

	s.HandleEvent([]byte(`not json at all`))
	s.HandleEvent([]byte(`{}`))
	s.HandleEvent([]byte(`{"type":"totally.unknown.event"}`))
	s.HandleEvent(nil)

	// Still functional afterwards.
	s.HandleEvent([]byte(`{"type":"response.created","response":{"id":"resp-2"}}`))
	if !contains(audio.snapshot(), "SetSpeaking") {
		t.Fatal("brain stopped responding after malformed input")
	}
}

// BurstID scopes the task ledger; the host reads it back to correlate work.
func TestSessionExposesBurstID(t *testing.T) {
	s := NewSession(SessionConfig{BurstID: "burst-xyz", Audio: &fakeExternalAudio{}})
	if got := s.BurstID(); got != "burst-xyz" {
		t.Fatalf("BurstID() = %q, want %q", got, "burst-xyz")
	}
}

type scriptedTaskBackend struct{ response string }

func (b scriptedTaskBackend) DoTask(string) (string, error) { return b.response, nil }
func (b scriptedTaskBackend) Cancel(string) error           { return nil }
func (b scriptedTaskBackend) ListAgents() (string, error)   { return `{"agents":[]}`, nil }

// A completed do_task must reach the model through the façade: macOS starts the
// mailbox delivery worker in Connect, so the façade owns starting it here. The
// 2026-08-21 device log showed the failure mode when it is missing — the result
// sits in the mailbox forever, and the model keeps answering "no news yet"
// about work the daemon finished minutes ago.
func TestSessionDeliversCompletedTaskResult(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	s := NewSession(SessionConfig{
		BurstID: "burst-deliver",
		Audio:   &fakeExternalAudio{},
		Send: func(payload string) error {
			mu.Lock()
			sent = append(sent, payload)
			mu.Unlock()
			return nil
		},
		TaskBackend: scriptedTaskBackend{response: `{"reply":"Haidian is 31C","session_id":"sess-1"}`},
	})

	s.HandleEvent([]byte(`{"type":"input_audio_buffer.committed","item_id":"item-user-1"}`))
	s.HandleEvent([]byte(`{"type":"response.created","response":{"id":"resp-1"}}`))
	s.HandleEvent([]byte(`{"type":"response.function_call_arguments.done","response_id":"resp-1","call_id":"call-1","name":"do_task","arguments":"{\"task\":\"check the weather\"}"}`))
	s.HandleEvent([]byte(`{"type":"response.done","response":{"id":"resp-1","status":"completed"}}`))

	deadline := time.Now().Add(5 * time.Second)
	injected, continued := false, false
	acked := 0
	for time.Now().Before(deadline) {
		mu.Lock()
		injected, continued = false, false
		creates := 0
		for _, payload := range sent {
			if strings.Contains(payload, `"type":"response.create"`) {
				creates++
			}
			if strings.Contains(payload, "conversation.item.create") && strings.Contains(payload, "Haidian is 31C") {
				injected = true
				continue
			}
			if injected && strings.Contains(payload, `"type":"response.create"`) {
				continued = true
			}
		}
		mu.Unlock()
		if continued {
			break
		}
		// Acknowledge each outbound response.create the way the provider would:
		// created, then a completed done, so the response slot frees up again.
		for acked < creates {
			acked++
			id := fmt.Sprintf("resp-synth-%d", acked)
			s.HandleEvent([]byte(`{"type":"response.created","response":{"id":"` + id + `"}}`))
			s.HandleEvent([]byte(`{"type":"response.done","response":{"id":"` + id + `","status":"completed"}}`))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !injected {
		t.Fatal("completed task result was never injected into the conversation")
	}
	if !continued {
		t.Fatal("no response.create followed the injected task result — the model was never asked to speak it")
	}
}

// A typed-nil host implementation must normalize to "no audio attached" rather
// than becoming a live-looking interface over a nil pointer.
func TestSessionNormalizesTypedNilAudio(t *testing.T) {
	var typedNil *fakeExternalAudio
	s := NewSession(SessionConfig{BurstID: "burst-typed-nil", Audio: typedNil})
	// Would panic on first audio call if the normalization were missing.
	s.HandleEvent([]byte(`{"type":"response.created","response":{"id":"resp-1"}}`))
}

// end_call must reach the host through the façade: on iOS the host's teardown
// IS the hang-up. macOS wires onEndCall in cmd/koe.go (goodbye earcon + close);
// a façade host with no hook gets a brain that enters its local terminal while
// the call's transport and audio keep running forever — "再见" then silence,
// with the call screen still up (2026-08-21 assessment, outstanding item #2).
// The same hook also arms the ASR dismiss-phrase backstop, which is skipped
// entirely while onEndCall is nil.
func TestSessionEndCallReachesHost(t *testing.T) {
	ended := make(chan struct{}, 1)
	s := NewSession(SessionConfig{
		BurstID:   "burst-end-host",
		Audio:     &fakeExternalAudio{},
		OnEndCall: func() { ended <- struct{}{} },
	})

	s.HandleEvent([]byte(`{"type":"input_audio_buffer.committed"}`))
	s.HandleEvent([]byte(`{"type":"response.created","response":{"id":"end-response"}}`))
	s.HandleEvent([]byte(`{"type":"response.function_call_arguments.done","response_id":"end-response","name":"end_call","call_id":"c1","arguments":"{}"}`))

	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("end_call never reached the host's OnEndCall")
	}
}

// The façade must arm the runtime floor gate from its config. Only the macOS
// path (webrtc.go) ever set handler.fullDuplexAEC, so an iOS host that enabled
// barge-in via env still had nativeFloorEnabled() == false — speech_started
// during playback fell through to the legacy interrupt path instead of the
// pause-and-judge floor (2026-08-21 assessment, outstanding item #3).
func TestSessionFullDuplexAECArmsNativeFloor(t *testing.T) {
	t.Setenv("KOE_VPIO_BARGE_IN", "1")

	on := NewSession(SessionConfig{
		BurstID:       "burst-floor-on",
		Audio:         &fakeExternalAudio{},
		FullDuplexAEC: true,
	})
	if !on.handler.nativeFloorEnabled() {
		t.Fatal("FullDuplexAEC: true must arm the native floor gate")
	}

	off := NewSession(SessionConfig{
		BurstID: "burst-floor-off",
		Audio:   &fakeExternalAudio{},
	})
	if off.handler.nativeFloorEnabled() {
		t.Fatal("floor gate must stay off for a half-duplex host")
	}
}

// Usage reporting must cross the façade: the brain fires reportUsage on every
// response.done (realtime.go), but the relay closure lived only in cmd/koe.go
// (macOS main), so a Session host never received a report and iOS realtime
// turns were never billed (2026-08-24 assessment, outstanding item #4). The
// façade hands the host the brain's verbatim report body; Cloud requires
// model and response_id, and model only ever comes from webrtc.go's Connect
// on macOS — the façade must carry it for hosts with no Connect.
func TestSessionReportsUsageToHost(t *testing.T) {
	reports := make(chan string, 1)
	s := NewSession(SessionConfig{
		BurstID: "burst-usage",
		Audio:   &fakeExternalAudio{},
		Model:   "gpt-realtime-test",
		OnUsage: func(usageJSON string) { reports <- usageJSON },
	})

	s.HandleEvent([]byte(`{"type":"response.done","response":{"id":"resp-u1","status":"completed","usage":{"total_tokens":42,"input_tokens":30,"output_tokens":12,"input_token_details":{"audio_tokens":25,"text_tokens":5}}}}`))

	var got string
	select {
	case got = <-reports:
	case <-time.After(2 * time.Second):
		t.Fatal("response.done with usage never reached the host's OnUsage")
	}

	var report struct {
		Provider   string          `json:"provider"`
		Model      string          `json:"model"`
		ResponseID string          `json:"response_id"`
		Usage      json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(got), &report); err != nil {
		t.Fatalf("usage report is not valid JSON: %v; body=%s", err, got)
	}
	if report.Model != "gpt-realtime-test" {
		t.Fatalf("report.model = %q, want the config's model (Cloud 400s an empty model); body=%s", report.Model, got)
	}
	if report.ResponseID != "resp-u1" {
		t.Fatalf("report.response_id = %q, want %q; body=%s", report.ResponseID, "resp-u1", got)
	}
	// The usage object must be the provider's verbatim shape — the host and
	// daemon both forward it unparsed, so Cloud sees one shape from every
	// platform (including fields this build does not know about).
	var usage map[string]any
	if err := json.Unmarshal(report.Usage, &usage); err != nil {
		t.Fatalf("report.usage is not an object: %v; body=%s", err, got)
	}
	if usage["total_tokens"] != float64(42) {
		t.Fatalf("usage.total_tokens = %v, want 42 (verbatim passthrough); body=%s", usage["total_tokens"], got)
	}
	if _, ok := usage["input_token_details"]; !ok {
		t.Fatalf("usage.input_token_details missing — nested detail fields must survive passthrough; body=%s", got)
	}
}

// A response.done without usage (an early or failed turn) must not produce a
// report: Cloud requires response_id as the quota idempotency key and rejects
// an empty report, so the brain's own skip guard is the contract here.
func TestSessionSkipsUsageReportWithoutUsage(t *testing.T) {
	reports := make(chan string, 1)
	s := NewSession(SessionConfig{
		BurstID: "burst-usage-empty",
		Audio:   &fakeExternalAudio{},
		Model:   "gpt-realtime-test",
		OnUsage: func(usageJSON string) { reports <- usageJSON },
	})

	s.HandleEvent([]byte(`{"type":"response.done","response":{"id":"resp-u2","status":"completed"}}`))

	select {
	case got := <-reports:
		t.Fatalf("usage-less response.done must not be reported; got %s", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// waitForSentPayload polls the send log until one client event contains every
// marker, or fails the test. Tool outputs are submitted asynchronously.
func waitForSentPayload(t *testing.T, snapshot func() []string, markers ...string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, payload := range snapshot() {
			ok := true
			for _, m := range markers {
				if !strings.Contains(payload, m) {
					ok = false
					break
				}
			}
			if ok {
				return payload
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no sent payload contained %q; sent: %v", markers, snapshot())
	return ""
}

// A host that cannot perform a UI action must be able to say so: the error
// comes back to the model as a failed tool result with the host's own reason,
// not the generic "not wired" of a missing hook — that is the whole honest-
// degradation contract the iOS host relies on.
func TestSessionControlAppHostErrorReachesModel(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	snapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sent...)
	}
	s := NewSession(SessionConfig{
		BurstID: "burst-control-err",
		Audio:   &fakeExternalAudio{},
		Send: func(payloadJSON string) error {
			mu.Lock()
			sent = append(sent, payloadJSON)
			mu.Unlock()
			return nil
		},
		ControlApp: func(action string) error {
			if action == "open_settings" {
				return fmt.Errorf("on iPhone the user opens settings by hand in the app")
			}
			return nil
		},
	})

	s.HandleEvent([]byte(`{"type":"input_audio_buffer.committed"}`))
	s.HandleEvent([]byte(`{"type":"response.created","response":{"id":"ctl-response"}}`))
	s.HandleEvent([]byte(`{"type":"response.function_call_arguments.done","response_id":"ctl-response","name":"control_app","call_id":"ctl-1","arguments":"{\"action\":\"open_settings\"}"}`))

	waitForSentPayload(t, snapshot, "function_call_output", "failed", "opens settings by hand")
}

// The same seam must keep reporting success for actions the host does honor.
func TestSessionControlAppHostSuccess(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	snapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sent...)
	}
	s := NewSession(SessionConfig{
		BurstID: "burst-control-ok",
		Audio:   &fakeExternalAudio{},
		Send: func(payloadJSON string) error {
			mu.Lock()
			sent = append(sent, payloadJSON)
			mu.Unlock()
			return nil
		},
		ControlApp: func(string) error { return nil },
	})

	s.HandleEvent([]byte(`{"type":"input_audio_buffer.committed"}`))
	s.HandleEvent([]byte(`{"type":"response.created","response":{"id":"ctl-response-2"}}`))
	s.HandleEvent([]byte(`{"type":"response.function_call_arguments.done","response_id":"ctl-response-2","name":"control_app","call_id":"ctl-2","arguments":"{\"action\":\"show\"}"}`))

	waitForSentPayload(t, snapshot, "function_call_output", "ok")
}
