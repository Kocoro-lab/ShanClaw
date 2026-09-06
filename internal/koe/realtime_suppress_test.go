//go:build darwin && !ios && cgo

package koe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestShouldVoiceDoTaskResult pins the stale-result suppression decision: the
// function_call_output is always submitted; this gate only decides whether the
// result is spoken. The single signal is "did the user speak conversationally since
// the most recent do_task" — a follow-up advances the last-do_task marker (so it is
// NOT counted as moving on), a correction / topic change does not.
func TestShouldVoiceDoTaskResult(t *testing.T) {
	ok := SayResult{Status: "ok", Say: "Done — reminder set."}
	failed := SayResult{Status: "failed", Say: "Sorry, that failed."}
	injected := SayResult{Status: "injected"}
	emptySay := SayResult{Status: "ok", Say: ""}
	cancelled := SayResult{Status: "cancelled"} // MapDoTaskOutcome leaves Say empty

	cases := []struct {
		name        string
		r           SayResult
		userMovedOn bool // committed a conversational turn since the last do_task
		want        bool
	}{
		{"normal completed, user waited", ok, false, true},
		{"user moved on to conversation → suppress", ok, true, false},
		{"failed result still voices when user waited", failed, false, true},
		{"failed result suppressed after user moved on", failed, true, false},
		{"injected never voices", injected, false, false},
		{"injected never voices even after moving on", injected, true, false},
		{"empty say never voices", emptySay, false, false},
		{"cancelled (empty say) never voices", cancelled, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldVoiceDoTaskResult(c.r, c.userMovedOn); got != c.want {
				t.Errorf("shouldVoiceDoTaskResult(%+v, userSpokeSinceLastDoTask=%v) = %v, want %v",
					c.r, c.userMovedOn, got, c.want)
			}
		})
	}
}

// TestDoTaskVoicingGateWiring drives the REAL handleFunctionCall/do_task goroutine
// against a mock daemon whose /message result is released on demand, so a mid-task
// user turn can be interleaved deterministically. It asserts the wiring — counter
// capture at dispatch, comparison at land-time — actually suppresses the voicing
// (the response.create), while the function_call_output is still submitted. No live
// API: the gate is production code exercised end-to-end.
func TestDoTaskVoicingGateWiring(t *testing.T) {
	// This test pins the rollback path's historical stale-result gate. The default
	// delivery path now defers stale results in ResultMailbox instead of dropping them.
	t.Setenv("KOE_RESULT_DELIVERY", "0")
	t.Setenv("KOE_TOOL_CONTINUATION", "0")
	run := func(t *testing.T, midTaskUserTurn bool) (fnOutputs, voiceRequested int) {
		t.Helper()
		release := make(chan struct{})
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/message" {
				<-release // hold the result until the test decides the mid-task timing
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"reply": "Done, reminder set.", "spoken_summary": "Done, reminder set."})
		}))
		defer mock.Close()

		audio, err := NewAudioIO()
		if err != nil {
			t.Fatalf("NewAudioIO: %v", err)
		}
		state := NewCallState("burst-gate", "")
		disp := NewDispatcher(NewDaemonClient(mock.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)

		var mu sync.Mutex
		fnOut := 0
		sendFn := func(v any) error {
			b, _ := json.Marshal(v)
			var m struct {
				Type string `json:"type"`
				Item struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			_ = json.Unmarshal(b, &m)
			if m.Type == "conversation.item.create" && m.Item.Type == "function_call_output" {
				mu.Lock()
				fnOut++
				mu.Unlock()
			}
			return nil
		}
		h := newEventHandler(disp, state, audio, sendFn)
		ctx := context.Background()

		// do_task fires (arguments arrive string-encoded, like the live API).
		fc, _ := json.Marshal(map[string]any{
			"type": "response.function_call_arguments.done", "name": "do_task",
			"call_id": "call_gate", "arguments": `{"task":"remind me to call mom at six"}`})
		h.handleEvent(ctx, fc)

		if midTaskUserTurn {
			// The user takes the floor mid-task (e.g. "you got that wrong") → the server
			// commits their turn. This is the stale-making signal.
			committed, _ := json.Marshal(map[string]any{"type": "input_audio_buffer.committed"})
			h.handleEvent(ctx, committed)
		}
		close(release) // result lands now

		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := fnOut
			mu.Unlock()
			if n > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond) // settle: voice decision runs right after the output
		mu.Lock()
		fnOutputs = fnOut
		mu.Unlock()
		return fnOutputs, len(h.respReq) // respReq holds the queued voicing response.create
	}

	t.Run("user waited → result voiced", func(t *testing.T) {
		out, voice := run(t, false)
		if out == 0 {
			t.Fatalf("function_call_output was never submitted")
		}
		if voice != 1 {
			t.Errorf("expected the result to be voiced (respReq=1), got respReq=%d", voice)
		}
	})
	t.Run("user took the floor mid-task → result NOT voiced", func(t *testing.T) {
		out, voice := run(t, true)
		if out == 0 {
			t.Fatalf("function_call_output must still be submitted even when suppressed")
		}
		if voice != 0 {
			t.Errorf("expected the stale result to be suppressed (respReq=0), got respReq=%d", voice)
		}
	})
}

// TestDoTaskVoicingGateFollowUpThenMoveOn reproduces the live 2026-07-07 miss: a
// long primary do_task ("news") runs; a follow-up ("email") is injected and refines
// it; then the user moves on to plain conversation ("explain quantum computing")
// while the primary is STILL running; the primary's combined result lands 60s later.
// It must be SUPPRESSED — the user moved on AFTER the last do_task. The earlier
// per-dispatch "followUp" flag latched true and wrongly voiced it; the last-do_task
// commit marker fixes it. The no-move-on control still voices the combined result.
func TestDoTaskVoicingGateFollowUpThenMoveOn(t *testing.T) {
	// Keep the old gate covered behind its rollback flag; durable delivery has its
	// own cross-session and user-floor tests in result_mailbox_test.go.
	t.Setenv("KOE_RESULT_DELIVERY", "0")
	t.Setenv("KOE_TOOL_CONTINUATION", "0")
	run := func(t *testing.T, moveOn bool) (fnOutputs, voiceRequested int) {
		t.Helper()
		release := make(chan struct{})
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/message" {
				body, _ := io.ReadAll(r.Body)
				var req struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(body, &req)
				if strings.Contains(req.Text, "email") { // the follow-up → absorbed by the primary run
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "injected", "route": "r"})
					return
				}
				<-release // the primary ("news") holds until the test says the result lands
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"reply": "News plus your email overview.", "spoken_summary": "Here's your email overview."})
		}))
		defer mock.Close()

		audio, err := NewAudioIO()
		if err != nil {
			t.Fatalf("NewAudioIO: %v", err)
		}
		state := NewCallState("burst-followup", "")
		disp := NewDispatcher(NewDaemonClient(mock.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)

		var mu sync.Mutex
		fnOut := 0
		sendFn := func(v any) error {
			b, _ := json.Marshal(v)
			var m struct {
				Type string `json:"type"`
				Item struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			_ = json.Unmarshal(b, &m)
			if m.Type == "conversation.item.create" && m.Item.Type == "function_call_output" {
				mu.Lock()
				fnOut++
				mu.Unlock()
			}
			return nil
		}
		h := newEventHandler(disp, state, audio, sendFn)
		ctx := context.Background()
		commit := func() {
			c, _ := json.Marshal(map[string]any{"type": "input_audio_buffer.committed"})
			h.handleEvent(ctx, c)
		}
		doTask := func(id, task string) {
			fc, _ := json.Marshal(map[string]any{
				"type": "response.function_call_arguments.done", "name": "do_task",
				"call_id": id, "arguments": `{"task":"` + task + `"}`})
			h.handleEvent(ctx, fc)
		}

		commit()                         // news turn committed
		doTask("call_news", "the news")  // primary → blocks on release
		commit()                         // email turn committed
		doTask("call_email", "my email") // follow-up → injected, advances lastDoTaskCommitSeq
		if moveOn {
			commit() // user moves on to plain conversation ("explain quantum computing") — no do_task
		}
		close(release) // the 60s-late primary result lands now

		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := fnOut
			mu.Unlock()
			if n >= 2 { // both the injected follow-up and the primary submitted their outputs
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		fnOutputs = fnOut
		mu.Unlock()
		return fnOutputs, len(h.respReq)
	}

	t.Run("moved on to conversation after the follow-up → stale result suppressed", func(t *testing.T) {
		out, voice := run(t, true)
		if out < 2 {
			t.Fatalf("both function_call_outputs must be submitted, got %d", out)
		}
		if voice != 0 {
			t.Errorf("expected suppression after the user moved on (respReq=0), got respReq=%d", voice)
		}
	})
	t.Run("follow-up with no move-on → combined result still voiced", func(t *testing.T) {
		out, voice := run(t, false)
		if out < 2 {
			t.Fatalf("both function_call_outputs must be submitted, got %d", out)
		}
		if voice != 1 {
			t.Errorf("expected the combined result to voice (respReq=1), got respReq=%d", voice)
		}
	})
}
