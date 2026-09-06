package koe

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// Sentinel rejections for POST /call/mic — the body of the 409 response, so
// Desktop can distinguish "no call" from "task already drained" races.
var (
	ErrNoActiveCall  = errors.New("no_active_call")
	ErrNoTaskPending = errors.New("no_task_pending")
)

// controlEvent is one Koe→Desktop SSE payload, discriminated by Type. Wire shapes
// are pinned in Plan E (the Desktop client) — keep them byte-identical:
//
//	{"type":"voice_state","state":"idle"|"listening"|"thinking"|"speaking"[,"level":0..1][,"task_pending":true][,"mic":"off"]}
//	{"type":"control_app","action":"show"|"hide"|"new_conversation"|"open_settings"}
//	{"type":"call_state","state":"connecting"|"on_call"|"ended"}
//
// level (D3w) is an additive, omitempty field carrying the reactive RMS amplitude
// while listening (input) / speaking (output) so the Desktop Island sprite tracks
// the real signal instead of a canned animation; absent on transition events and
// for thinking/idle. task_pending and mic are additive, omit-when-default snapshot
// fields stamped on every voice_state (koe-mic-off): absent task_pending means
// false (no do_task in flight), absent mic means "on". The koe↔Desktop control
// channel is localhost-only and may be protected by a Desktop-generated Bearer
// token; these fields remain additive-only.
type controlEvent struct {
	Type        string  `json:"type"`
	State       string  `json:"state,omitempty"`        // voice_state / call_state
	Action      string  `json:"action,omitempty"`       // control_app
	Level       float64 `json:"level,omitempty"`        // voice_state reactive RMS amplitude (0..1)
	TaskPending bool    `json:"task_pending,omitempty"` // voice_state: a do_task is in flight (koe-mic-off)
	Mic         string  `json:"mic,omitempty"`          // voice_state: "off" while user mic-off; absent = on
	Reason      string  `json:"reason,omitempty"`       // call_state "ended": why it failed; absent = a normal hang-up
}

// StartCallRequest is the optional Desktop→Koe context payload for POST
// /call/start. Older Desktop builds send only {"command":"start_call"}; that
// still decodes with zero values.
type StartCallRequest struct {
	CWD            string          `json:"cwd,omitempty"`
	ForegroundHint *ForegroundHint `json:"foreground_hint,omitempty"`
}

// ControlServer is the Koe-side HTTP+SSE control channel for Kocoro Desktop: it
// accepts POST /call/start|end and broadcasts GET /events (voice_state,
// control_app, call_state). This is the SERVER half of the Desktop↔Koe contract
// (Desktop is the client, same shape as Desktop→daemon). It satisfies Plan B's
// ControlAppFunc seam: when the model calls control_app, the dispatcher's hook
// calls EmitControlApp and Desktop performs the actual window action.
type ControlServer struct {
	mu          sync.Mutex
	subscribers map[chan controlEvent]struct{}
	onStart     func(StartCallRequest) // Desktop pressed talk: start a call
	onEnd       func()                 // Desktop ended: tear the call down
	onInterrupt func()                 // Desktop explicitly interrupted playback
	onMic       func(off bool) error   // POST /call/mic (nil until SetMicHandler)
	taskPending func() bool            // nil-safe snapshot providers, stamped on every voice_state
	micOff      func() bool
	token       string       // optional Bearer token for Desktop-owned requests
	lastVoice   atomic.Value // string: last voice_state, replayed by ReemitVoiceState
}

// NewControlServer wires the Desktop-driven start/end callbacks (either may be nil).
func NewControlServer(onStart func(StartCallRequest), onEnd func(), onInterrupt func()) *ControlServer {
	return &ControlServer{
		subscribers: make(map[chan controlEvent]struct{}),
		onStart:     onStart,
		onEnd:       onEnd,
		onInterrupt: onInterrupt,
	}
}

// SetMicHandler wires POST /call/mic. Called once at startup, before Handler()
// serves — no locking needed.
func (s *ControlServer) SetMicHandler(h func(off bool) error) { s.onMic = h }

// SetToken enables Bearer-token auth for every control request. Empty preserves
// older Desktop builds and test harnesses.
func (s *ControlServer) SetToken(token string) { s.token = token }

// SetSnapshotProviders wires the task_pending / mic snapshot stamped on every
// voice_state event. Providers must be sessMu-free (EmitVoiceState is called
// with cmd/koe.go's session mutex held) — cmd/koe.go passes atomic-pointer
// reads over CallState/AudioIO, whose own locks are safe here.
func (s *ControlServer) SetSnapshotProviders(taskPending, micOff func() bool) {
	s.taskPending = taskPending
	s.micOff = micOff
}

func (s *ControlServer) stampVoice(ev controlEvent) controlEvent {
	if s.taskPending != nil && s.taskPending() {
		ev.TaskPending = true
	}
	if s.micOff != nil && s.micOff() {
		ev.Mic = "off"
	}
	return ev
}

// ReemitVoiceState replays the last voice state with a fresh task/mic snapshot —
// the /call/mic flow calls it so Desktop sees the flip immediately instead of
// waiting for the next natural voice_state transition.
func (s *ControlServer) ReemitVoiceState() {
	if v, ok := s.lastVoice.Load().(string); ok && v != "" {
		s.broadcast(s.stampVoice(controlEvent{Type: "voice_state", State: v}))
	}
}

// Handler returns the localhost mux for `shan koe --control-port N`.
func (s *ControlServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /call/start", func(w http.ResponseWriter, r *http.Request) {
		var req StartCallRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "invalid start payload", http.StatusBadRequest)
			return
		}
		if s.onStart != nil {
			go s.onStart(req)
		}
		writeControlOK(w)
	})
	mux.HandleFunc("POST /call/end", func(w http.ResponseWriter, r *http.Request) {
		if s.onEnd != nil {
			s.onEnd()
		}
		writeControlOK(w)
	})
	mux.HandleFunc("POST /call/interrupt", func(w http.ResponseWriter, r *http.Request) {
		if s.onInterrupt != nil {
			s.onInterrupt()
		}
		writeControlOK(w)
	})
	mux.HandleFunc("POST /call/mic", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Mic string `json:"mic"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || (req.Mic != "on" && req.Mic != "off") {
			http.Error(w, `{"error":"invalid_mic"}`, http.StatusBadRequest)
			return
		}
		if s.onMic == nil {
			writeControlOK(w)
			return
		}
		if err := s.onMic(req.Mic == "off"); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"` + err.Error() + `"}`))
			return
		}
		writeControlOK(w)
	})
	mux.HandleFunc("GET /events", s.handleEvents)
	if s.token == "" {
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorizedControlRequest(r, s.token) {
			writeControlUnauthorized(w)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func authorizedControlRequest(r *http.Request, token string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func writeControlUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}

func writeControlOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *ControlServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch := make(chan controlEvent, 16)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subscribers, ch)
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			b, _ := json.Marshal(ev)
			_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
			flusher.Flush()
		}
	}
}

func (s *ControlServer) broadcast(ev controlEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subscribers {
		select {
		case ch <- ev:
		default: // drop on a wedged subscriber rather than block the call loop
		}
	}
}

// EmitVoiceState pushes the ambient voice state to Desktop (drives the Island
// sprite), stamped with the task_pending/mic snapshot (koe-mic-off).
func (s *ControlServer) EmitVoiceState(state string) {
	s.lastVoice.Store(state)
	s.broadcast(s.stampVoice(controlEvent{Type: "voice_state", State: state}))
}

// EmitVoiceLevel pushes a voice_state with the reactive RMS amplitude (D3w): the
// level pump calls this at animation cadence while listening/speaking so the sprite
// tracks the real signal. Same event type as EmitVoiceState — just with level set,
// stamped like EmitVoiceState.
func (s *ControlServer) EmitVoiceLevel(state string, level float64) {
	s.lastVoice.Store(state)
	s.broadcast(s.stampVoice(controlEvent{Type: "voice_state", State: state, Level: level}))
}

// EmitControlApp asks Desktop to perform a window action (the control_app tool).
func (s *ControlServer) EmitControlApp(action string) {
	s.broadcast(controlEvent{Type: "control_app", Action: action})
}

// EmitCallState reports the call lifecycle to Desktop.
func (s *ControlServer) EmitCallState(state string) {
	s.broadcast(controlEvent{Type: "call_state", State: state})
}

// EmitCallFailed reports a call that never came up, or died on its own, as
// `ended` PLUS a reason code (see ClassifyCallFailure).
//
// The state is hard-coded rather than a parameter. Desktop's decoder drops a
// call_state event whose `state` it does not recognize, so any other value
// would make an older Desktop ignore the event entirely and leave the call UI
// open forever — strictly worse than the silence this fixes. A parameter every
// caller filled with "ended" was an invitation to do exactly the thing this
// comment warns about. `reason` is an additive field on the same event instead:
// older builds ignore it and keep today's behaviour, newer builds can finally
// tell the user what happened. Same discipline as voice_state's task_pending/mic.
//
// An empty reason falls back to a plain `ended`, so a normal hang-up can never
// be dressed up as a failure.
func (s *ControlServer) EmitCallFailed(reason string) {
	if reason == "" {
		s.EmitCallState("ended")
		return
	}
	s.broadcast(controlEvent{Type: "call_state", State: "ended", Reason: reason})
}

// EmitMicStatus reports microphone health to Desktop. "silent" = the bound input
// produced no signal for the watchdog window while capture was expected (e.g. a
// clamshell built-in mic with the lid shut); "ok" = input recovered. Additive
// event — older Desktop builds skip the unknown type, so it is safe to emit
// unconditionally.
func (s *ControlServer) EmitMicStatus(status string) {
	s.broadcast(controlEvent{Type: "mic_status", State: status})
}
