//go:build darwin && !ios && cgo

package koe

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestEndCallToolTriggersHangupWithoutOutput pins the end_call wiring: the tool
// invokes onEndCall (the Desktop hang-up + goodbye earcon) and sends NO
// function_call_output — the teardown is the response, and a spoken reply is
// exactly what dismiss must avoid.
func TestEndCallToolTriggersHangupWithoutOutput(t *testing.T) {
	audio, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	state := NewCallState("burst-end", "")
	disp := NewDispatcher(NewDaemonClient(""), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	cap := &captureSender{}
	h := newEventHandler(disp, state, audio, cap.send)
	called := make(chan struct{}, 1)
	h.onEndCall = func() { called <- struct{}{} }
	h.handleEvent(context.Background(), []byte(`{"type":"input_audio_buffer.committed"}`))
	h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"end-response"}}`))

	ev, _ := json.Marshal(map[string]any{
		"type":        "response.function_call_arguments.done",
		"response_id": "end-response", "name": "end_call", "call_id": "c1", "arguments": "{}",
	})
	h.handleEvent(context.Background(), ev)

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("end_call did not invoke onEndCall")
	}
	if n := cap.countType("conversation.item.create"); n != 0 {
		t.Errorf("end_call must not send a function_call_output, got %d conversation.item.create", n)
	}
	if n := cap.countType("response.create"); n != 0 {
		t.Errorf("end_call must not request a spoken response, got %d response.create", n)
	}
}

func TestEndCallWaitsForFinalUsageBeforeTeardown(t *testing.T) {
	t.Setenv("KOE_TOOL_CONTINUATION", "0")
	t.Setenv("KOE_REALTIME_USAGE_CLOSE_GRACE_MS", "500")
	h := newEventHandler(nil, NewCallState("burst-end-usage", ""), nil, (&captureSender{}).send)
	usageSeen := make(chan struct{}, 1)
	ended := make(chan struct{}, 1)
	h.onUsage = func(json.RawMessage) { usageSeen <- struct{}{} }
	h.onEndCall = func() { ended <- struct{}{} }

	h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"end-usage-response"}}`))
	h.handleEvent(context.Background(), []byte(`{"type":"response.function_call_arguments.done","response_id":"end-usage-response","call_id":"end-usage-call","name":"end_call","arguments":"{}"}`))
	select {
	case <-ended:
		t.Fatal("end_call tore down before final response.done")
	case <-time.After(50 * time.Millisecond):
	}

	h.handleEvent(context.Background(), []byte(`{"type":"response.done","response":{"id":"end-usage-response","status":"completed","usage":{"input_tokens":3,"output_tokens":2}}}`))
	select {
	case <-usageSeen:
	case <-time.After(time.Second):
		t.Fatal("final response.done did not reach usage relay")
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("end_call did not tear down after final usage admission")
	}
}

func TestEndCallUsageWaitIsBounded(t *testing.T) {
	t.Setenv("KOE_TOOL_CONTINUATION", "0")
	t.Setenv("KOE_REALTIME_USAGE_CLOSE_GRACE_MS", "25")
	h := newEventHandler(nil, NewCallState("burst-end-usage-timeout", ""), nil, (&captureSender{}).send)
	h.onUsage = func(json.RawMessage) {}
	ended := make(chan struct{}, 1)
	h.onEndCall = func() { ended <- struct{}{} }

	h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"end-timeout-response"}}`))
	started := time.Now()
	h.handleEvent(context.Background(), []byte(`{"type":"response.function_call_arguments.done","response_id":"end-timeout-response","call_id":"end-timeout-call","name":"end_call","arguments":"{}"}`))
	select {
	case <-ended:
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("bounded end_call wait took %s", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("end_call wait did not time out")
	}
}

func TestRealtimeConnCloseWaitsForInFlightUsageCallback(t *testing.T) {
	t.Setenv("KOE_REALTIME_USAGE_CLOSE_GRACE_MS", "500")
	h := newEventHandler(nil, NewCallState("burst-close-usage", ""), nil, (&captureSender{}).send)
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackFinished := make(chan struct{})
	h.onUsage = func(json.RawMessage) {
		close(callbackStarted)
		<-releaseCallback
		close(callbackFinished)
	}
	h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"close-usage-response"}}`))
	responseDone := make(chan struct{})
	go func() {
		h.handleEvent(context.Background(), []byte(`{"type":"response.done","response":{"id":"close-usage-response","status":"completed","usage":{"input_tokens":3,"output_tokens":2}}}`))
		close(responseDone)
	}()
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("usage callback did not start")
	}
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		t.Fatal("response.done handler remained blocked on usage callback")
	}
	if h.respBusy.Load() {
		t.Fatal("response.done callback test did not reach the respBusy=false race window")
	}
	cancelled := make(chan struct{})
	closed := make(chan struct{})
	rc := &RealtimeConn{
		waitForUsage: h.waitForActiveUsage,
		cancel:       func() { close(cancelled) },
	}
	go func() {
		rc.Close()
		close(closed)
	}()
	select {
	case <-cancelled:
		t.Fatal("WebRTC close cancelled while usage callback was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCallback)
	select {
	case <-callbackFinished:
	case <-time.After(time.Second):
		t.Fatal("usage callback did not finish")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("WebRTC close did not finish after usage callback")
	}
}

func TestRealtimeConnCloseAfterInterruptWaitsForLateUsage(t *testing.T) {
	t.Setenv("KOE_REALTIME_USAGE_CLOSE_GRACE_MS", "500")
	cap := &captureSender{}
	h := newEventHandler(nil, NewCallState("burst-close-late-usage", ""), nil, cap.send)
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	h.onUsage = func(json.RawMessage) {
		close(callbackStarted)
		<-releaseCallback
	}
	h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"close-late-response"}}`))
	h.interruptOutput()
	if got := cap.countType("response.cancel"); got != 1 {
		t.Fatalf("interrupt response.cancel count=%d, want 1", got)
	}
	if h.respBusy.Load() || h.activeResponseID() != "" {
		t.Fatal("interrupt did not clear the active response state")
	}
	if waiters := h.pendingTerminalUsageWaiters(); len(waiters) != 1 {
		t.Fatalf("pending terminal usage waiters=%d, want 1 after interrupt", len(waiters))
	}
	cancelled := make(chan struct{})
	closed := make(chan struct{})
	rc := &RealtimeConn{
		waitForUsage: h.waitForActiveUsage,
		cancel:       func() { close(cancelled) },
	}
	go func() {
		rc.Close()
		close(closed)
	}()
	responseDone := make(chan struct{})
	go func() {
		h.handleEvent(context.Background(), []byte(`{"type":"response.done","response":{"id":"close-late-response","status":"cancelled","usage":{"input_tokens":3,"output_tokens":2}}}`))
		close(responseDone)
	}()
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("late response.done usage callback did not start")
	}
	select {
	case <-cancelled:
		t.Fatal("Close cancelled transport before late usage admission finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCallback)
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		t.Fatal("late response.done handler did not finish")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after late response.done")
	}
}

func TestQwenLateResponseIDFromToolKeepsCloseWaitingForUsage(t *testing.T) {
	t.Setenv("KOE_TOOL_CONTINUATION", "1")
	t.Setenv("KOE_REALTIME_USAGE_CLOSE_GRACE_MS", "500")
	cap := &captureSender{}
	h := newEventHandler(nil, NewCallState("burst-qwen-late-id-close", ""), nil, cap.send)
	h.provider = string(ProviderQwen)
	h.toolLoop.setLazyBind(true)
	h.toolLoop.noteUserCommit(1)
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackFinished := make(chan struct{})
	h.onUsage = func(json.RawMessage) {
		close(callbackStarted)
		<-releaseCallback
		close(callbackFinished)
	}

	// Qwen can omit response.created.response.id, then identify the same
	// response on its tool event. The tool event must establish the terminal
	// usage waiter before stop_speaking clears the active response locally.
	h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{}}`))
	h.handleEvent(context.Background(), []byte(`{"type":"response.function_call_arguments.done","response_id":"qwen-late-response","call_id":"late-stop","name":"stop_speaking","arguments":"{}"}`))
	if waiters := h.pendingTerminalUsageWaiters(); len(waiters) != 1 {
		t.Fatalf("pending terminal usage waiters=%d, want 1 after late-ID tool event", len(waiters))
	}

	cancelled := make(chan struct{})
	closed := make(chan struct{})
	rc := &RealtimeConn{
		waitForUsage: h.waitForActiveUsage,
		cancel:       func() { close(cancelled) },
	}
	go func() {
		rc.Close()
		close(closed)
	}()
	responseDone := make(chan struct{})
	go func() {
		h.handleEvent(context.Background(), []byte(`{"type":"response.done","response":{"id":"qwen-late-response","status":"cancelled","usage":{"input_tokens":3,"output_tokens":2}}}`))
		close(responseDone)
	}()
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("late-ID response.done usage callback did not start")
	}
	select {
	case <-cancelled:
		t.Fatal("Close cancelled transport before late-ID usage admission finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCallback)
	select {
	case <-callbackFinished:
	case <-time.After(time.Second):
		t.Fatal("late-ID usage callback did not finish")
	}
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		t.Fatal("late-ID response.done handler did not finish")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after late-ID response.done")
	}
}

func TestEndCallToolClearsActiveOutputBeforeHangup(t *testing.T) {
	audio, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	state := NewCallState("burst-end-active", "")
	disp := NewDispatcher(NewDaemonClient(""), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	cap := &captureSender{}
	h := newEventHandler(disp, state, audio, cap.send)
	h.handleEvent(context.Background(), []byte(`{"type":"input_audio_buffer.committed"}`))
	h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"end-active-response"}}`))
	h.outputBufferActive.Store(true)
	called := make(chan struct{}, 1)
	h.onEndCall = func() { called <- struct{}{} }

	ev, _ := json.Marshal(map[string]any{
		"type":        "response.function_call_arguments.done",
		"response_id": "end-active-response", "name": "end_call", "call_id": "c1", "arguments": "{}",
	})
	h.handleEvent(context.Background(), ev)

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("end_call did not invoke onEndCall")
	}
	for _, want := range []string{"input_audio_buffer.clear", "response.cancel", "output_audio_buffer.clear"} {
		if n := cap.countType(want); n != 1 {
			t.Errorf("end_call active-output cleanup sent %d %s messages, want 1", n, want)
		}
	}
}

// TestDismissTranscriptHangsUp pins the deterministic backstop: a whole-utterance
// dismiss phrase in the input transcription hangs up (onEndCall) even when the model
// never calls the end_call tool — the reliable path for the fixed vocabulary. A
// non-dismiss transcript must NOT hang up.
func TestDismissTranscriptHangsUp(t *testing.T) {
	t.Setenv("KOE_ASR_DISMISS_BACKSTOP", "1")
	newH := func() (*eventHandler, chan struct{}) {
		audio, err := NewAudioIO()
		if err != nil {
			t.Fatalf("NewAudioIO: %v", err)
		}
		state := NewCallState("burst-dismiss", "")
		disp := NewDispatcher(NewDaemonClient(""), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
		h := newEventHandler(disp, state, audio, (&captureSender{}).send)
		hung := make(chan struct{}, 1)
		h.onEndCall = func() { hung <- struct{}{} }
		return h, hung
	}
	feed := func(h *eventHandler, transcript string) {
		raw, _ := json.Marshal(map[string]any{
			"type":       "conversation.item.input_audio_transcription.completed",
			"transcript": transcript,
		})
		h.handleEvent(context.Background(), raw)
	}

	t.Run("stop-speaking phrase stays on call", func(t *testing.T) {
		h, hung := newH()
		feed(h, "闭嘴。")
		select {
		case <-hung:
			t.Fatal("stop-speaking transcript hung up the call")
		case <-time.After(100 * time.Millisecond):
		}
	})
	t.Run("terminal dismiss phrase hangs up", func(t *testing.T) {
		h, hung := newH()
		feed(h, "退出吧。")
		select {
		case <-hung:
		case <-time.After(2 * time.Second):
			t.Fatal("terminal dismiss transcript did not hang up")
		}
	})
	t.Run("non-dismiss transcript stays on the call", func(t *testing.T) {
		h, hung := newH()
		feed(h, "解释一下量子纠缠")
		select {
		case <-hung:
			t.Fatal("a normal request must not hang up")
		case <-time.After(300 * time.Millisecond):
		}
	})
	t.Run("ambiguous stop while task running is left to the model", func(t *testing.T) {
		h, hung := newH()
		h.state.SetInFlight("running task")
		feed(h, "停止")
		select {
		case <-hung:
			t.Fatal("ambiguous stop during a task must not deterministic-hangup")
		case <-time.After(300 * time.Millisecond):
		}
	})
	t.Run("stop speaking still stays on call while task running", func(t *testing.T) {
		h, hung := newH()
		h.state.SetInFlight("running task")
		feed(h, "闭嘴")
		select {
		case <-hung:
			t.Fatal("stop speaking during a task hung up the call")
		case <-time.After(100 * time.Millisecond):
		}
	})
}

func TestTranscriptIsEvidenceOnlyByDefault(t *testing.T) {
	t.Setenv("KOE_ASR_DISMISS_BACKSTOP", "0")
	h := newEventHandler(nil, NewCallState("burst-dismiss", ""), nil, func(any) error { return nil })
	hung := make(chan struct{}, 1)
	h.onEndCall = func() { hung <- struct{}{} }
	h.handleInputTranscript("闭嘴")
	select {
	case <-hung:
		t.Fatal("default ASR evidence path must not own call control")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestQwenDismissTranscriptUsesBackstopByDefault(t *testing.T) {
	t.Setenv("KOE_ASR_DISMISS_BACKSTOP", "")
	h := newEventHandler(nil, NewCallState("burst-qwen-dismiss", ""), nil, func(any) error { return nil })
	h.provider = string(ProviderQwen)
	ended := make(chan struct{}, 1)
	h.onEndCall = func() { ended <- struct{}{} }

	h.handleInputTranscript("退出吧。")

	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("Qwen dismiss transcript did not use the deterministic backstop by default")
	}
}

func TestQwenDismissAcknowledgementDoesNotEndAfterFailedTranscript(t *testing.T) {
	h := newEventHandler(nil, NewCallState("burst-qwen-dismiss-ack", ""), nil, func(any) error { return nil })
	h.provider = string(ProviderQwen)
	ended := make(chan struct{}, 1)
	h.onEndCall = func() { ended <- struct{}{} }

	h.handleEvent(context.Background(), []byte(`{"type":"input_audio_buffer.speech_started"}`))
	h.handleEvent(context.Background(), []byte(`{"type":"conversation.item.input_audio_transcription.failed"}`))
	h.handleEvent(context.Background(), []byte(`{"type":"response.audio_transcript.done","transcript":"好的，再见。"}`))

	select {
	case <-ended:
		t.Fatal("assistant goodbye translation ended the call without user-side dismissal evidence")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestFailedTranscriptNormalReplyDoesNotEndCall(t *testing.T) {
	h := newEventHandler(nil, NewCallState("burst-qwen-normal-ack", ""), nil, func(any) error { return nil })
	h.provider = string(ProviderQwen)
	ended := make(chan struct{}, 1)
	h.onEndCall = func() { ended <- struct{}{} }

	h.handleEvent(context.Background(), []byte(`{"type":"input_audio_buffer.speech_started"}`))
	h.handleEvent(context.Background(), []byte(`{"type":"conversation.item.input_audio_transcription.failed"}`))
	h.handleEvent(context.Background(), []byte(`{"type":"response.audio_transcript.done","transcript":"好的，我来帮你。"}`))

	select {
	case <-ended:
		t.Fatal("normal acknowledgement after failed input transcription ended the call")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestEndCallToolNilHookIsSafe: the standalone/CLI path leaves onEndCall nil, so a
// stray end_call must be an inert no-op, never a panic.
func TestEndCallToolNilHookIsSafe(t *testing.T) {
	audio, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	state := NewCallState("burst-end2", "")
	disp := NewDispatcher(NewDaemonClient(""), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	h := newEventHandler(disp, state, audio, (&captureSender{}).send)
	h.handleEvent(context.Background(), []byte(`{"type":"input_audio_buffer.committed"}`))
	h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"end-nil-response"}}`))
	// onEndCall stays nil.
	ev, _ := json.Marshal(map[string]any{
		"type":        "response.function_call_arguments.done",
		"response_id": "end-nil-response", "name": "end_call", "call_id": "c1", "arguments": "{}",
	})
	h.handleEvent(context.Background(), ev) // must not panic
}

func TestEndCallIsTerminalAndIdempotentInsideRealtimeHandler(t *testing.T) {
	h := newEventHandler(nil, NewCallState("burst-end-terminal", ""), nil, func(any) error { return nil })
	ended := make(chan struct{}, 2)
	h.onEndCall = func() { ended <- struct{}{} }

	h.handleFunctionCall(context.Background(), "end-1", "end_call", nil)
	h.handleFunctionCall(context.Background(), "end-2", "end_call", nil)
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("first end_call did not invoke onEndCall")
	}
	select {
	case <-ended:
		t.Fatal("duplicate end_call invoked onEndCall twice")
	case <-time.After(50 * time.Millisecond):
	}

	h.queueAcceptedNativeTurn(1)
	h.requestResponse()
	if got := len(h.loopRespReq) + len(h.respReq); got != 0 {
		t.Fatalf("terminal handler accepted %d new response requests", got)
	}
}

func TestEndCallDropsLateToolsWhenContinuationIsDisabled(t *testing.T) {
	t.Setenv("KOE_TOOL_CONTINUATION", "0")
	state := NewCallState("burst-end-late-tool", "")
	disp := NewDispatcher(NewDaemonClient(""), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	cap := &captureSender{}
	h := newEventHandler(disp, state, nil, cap.send)
	ended := make(chan struct{}, 1)
	h.onEndCall = func() { ended <- struct{}{} }

	h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"end-response"}}`))
	h.handleEvent(context.Background(), []byte(`{"type":"response.function_call_arguments.done","response_id":"end-response","call_id":"end-1","name":"end_call","arguments":"{}"}`))
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("end_call did not invoke onEndCall")
	}

	h.handleEvent(context.Background(), []byte(`{"type":"response.function_call_arguments.done","response_id":"end-response","call_id":"late-status","name":"get_status","arguments":"{}"}`))
	if got := cap.countType("conversation.item.create"); got != 0 {
		t.Fatalf("terminal handler executed a late tool and sent %d function outputs", got)
	}
}

func TestDismissTranscriptAndEndCallShareOneTerminal(t *testing.T) {
	t.Setenv("KOE_ASR_DISMISS_BACKSTOP", "1")
	h := newEventHandler(nil, NewCallState("burst-end-shared-terminal", ""), nil, func(any) error { return nil })
	ended := make(chan struct{}, 2)
	h.onEndCall = func() { ended <- struct{}{} }

	h.handleInputTranscript("退出吧")
	h.handleFunctionCall(context.Background(), "end-after-transcript", "end_call", nil)
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("dismiss transcript did not invoke onEndCall")
	}
	select {
	case <-ended:
		t.Fatal("dismiss transcript and end_call invoked onEndCall twice")
	case <-time.After(50 * time.Millisecond):
	}
	if !h.ending.Load() {
		t.Fatal("dismiss transcript did not enter the shared terminal state")
	}
}

func TestEndCallCancelsLateResponseCreatedWithoutReopeningPlayback(t *testing.T) {
	cap := &captureSender{}
	h := newEventHandler(nil, NewCallState("burst-end-late-response", ""), nil, cap.send)
	h.onEndCall = func() {}
	if !h.requestEndCall("end-before-created") {
		t.Fatal("end_call was not accepted")
	}

	h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"late-response"}}`))

	if got := cap.countType("response.cancel"); got != 1 {
		t.Fatalf("late response.created cancellation count=%d, want 1", got)
	}
	if h.respBusy.Load() {
		t.Fatal("late response.created reopened the response slot after end_call")
	}
	if got := h.activeResponseID(); got != "" {
		t.Fatalf("late response.created became active after end_call: %q", got)
	}
	if got := h.voiceState(); got != "idle" {
		t.Fatalf("voice state after late response.created=%q, want idle", got)
	}
}

func TestEndCallStopsResponseCreateAlreadyWaitingForIdle(t *testing.T) {
	cap := &captureSender{}
	h := newEventHandler(nil, NewCallState("burst-end-waiting-response", ""), nil, cap.send)
	h.respBusy.Store(true)
	h.onEndCall = func() {}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		h.sendQueuedResponse(ctx, responseCreateRequest{})
		close(done)
	}()

	time.Sleep(40 * time.Millisecond)
	if !h.requestEndCall("end-waiting-response") {
		t.Fatal("end_call was not accepted")
	}
	time.Sleep(80 * time.Millisecond)
	if got := cap.countType("response.create"); got != 0 {
		t.Fatalf("terminal handler sent %d response.create frames after ending", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("response sender did not stop after end_call")
	}
}

func TestEndCallLeavesVoiceStateIdle(t *testing.T) {
	h := newEventHandler(nil, NewCallState("burst-end-idle", ""), nil, func(any) error { return nil })
	h.respBusy.Store(true)
	h.asyncTaskPending.Store(true)
	var states []string
	h.onVoiceState = func(state string) { states = append(states, state) }
	h.onEndCall = func() {}

	if !h.requestEndCall("end-idle") {
		t.Fatal("end_call was not accepted")
	}
	if len(states) == 0 {
		t.Fatal("end_call emitted no terminal voice state")
	}
	for _, state := range states {
		if state != "idle" {
			t.Fatalf("end_call emitted non-terminal voice state %q; states=%v", state, states)
		}
	}
	if got := h.voiceState(); got != "idle" {
		t.Fatalf("voice state after end_call=%q, want idle", got)
	}
}

func TestEndCallStopsResultVoiceGapWait(t *testing.T) {
	h := newEventHandler(nil, NewCallState("burst-end-result-wait", ""), nil, func(any) error { return nil })
	h.respBusy.Store(true)
	h.onEndCall = func() {}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waited := make(chan bool, 1)
	go func() { waited <- h.waitResultVoiceGap(ctx) }()

	time.Sleep(40 * time.Millisecond)
	if !h.requestEndCall("end-result-wait") {
		t.Fatal("end_call was not accepted")
	}
	select {
	case accepted := <-waited:
		if accepted {
			t.Fatal("result voice gap became admissible after end_call")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("result voice gap wait did not stop after end_call")
	}
}

func TestStopSpeakingEndsOnlyTheCurrentTurnWithoutContinuation(t *testing.T) {
	cap := &captureSender{}
	h := newEventHandler(nil, NewCallState("burst-stop-speaking", ""), nil, cap.send)
	ended := make(chan struct{}, 1)
	h.onEndCall = func() { ended <- struct{}{} }
	h.handleEvent(context.Background(), []byte(`{"type":"input_audio_buffer.committed"}`))
	h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"stop-response"}}`))
	h.handleEvent(context.Background(), []byte(`{"type":"response.function_call_arguments.done","response_id":"stop-response","call_id":"stop-1","name":"stop_speaking","arguments":"{}"}`))
	h.handleEvent(context.Background(), []byte(`{"type":"response.done","response":{"id":"stop-response","status":"cancelled"}}`))

	select {
	case <-ended:
		t.Fatal("stop_speaking ended the voice call")
	case <-time.After(50 * time.Millisecond):
	}
	if got := cap.countType("response.cancel"); got != 1 {
		t.Fatalf("stop_speaking response.cancel count=%d, want 1", got)
	}
	if got := len(h.loopRespReq) + len(h.respReq); got != 0 {
		t.Fatalf("stop_speaking queued %d acknowledgement/continuation responses", got)
	}
	if h.ending.Load() {
		t.Fatal("stop_speaking entered the call terminal state")
	}
}
