//go:build darwin && cgo && koe_diagnostic && !ios

package koe

// Live reproduction of the production bug: after the user corrects a MISHEARD
// request mid-flight, the (now-stale) do_task result still barges in and gets
// spoken. This test measures whether the RESULT-VOICING STRATEGY changes that
// outcome, against live GA Realtime — WITHOUT changing any production code, by
// replaying both strategies at the wire level:
//
//   - "forced": inject the result's function_call_output, then response.create
//     WITH exactSpeechInstructions — i.e. exactly what realtime.go's
//     requestResponseForSpeech does today (forces a verbatim read).
//   - "plain":  inject the same function_call_output, then a BARE response.create
//     (the fast-tool style: sendRaw → requestResponse) — lets the model decide,
//     with the full conversation (request, ack, correction, correction-reply,
//     result) in context.
//
// Per trial: feed the request utterance → the model calls do_task → feed a spoken
// correction → wait for the model to reply to the correction → THEN inject the
// stale result with the variant's response.create → observe whether the model
// speaks the stale result content.
//
// This is a diagnostic comparison, not a release test: model behavior is
// tallied for human inspection and does not produce a pass/fail quality gate.
// The dedicated koe_diagnostic build tag keeps it out of default and release
// test discovery. It mints via the running daemon (KOE_DAEMON_URL, default
// http://127.0.0.1:7533) so no raw OPENAI_API_KEY is needed. macOS-only (say).
//
//	KOE_CORRECTION_SUPPRESSION_DIAGNOSTIC=1 PKG_CONFIG_PATH=/opt/homebrew/lib/pkgconfig \
//	  go test -tags=koe_diagnostic ./internal/koe/ -run '^TestKoeCorrectionSuppressionDiagnostic$' -v -count=1 -timeout=900s

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// correctionSpoken mirrors the real "你弄错了" moment: a clear negation of the
// just-issued request, without explicitly naming "cancel the reminder" (that would
// lead the model). e2eSpoken ("Add a reminder to call mom at six.") is the request.
const correctionSpoken = "No, you got that wrong. That is not what I asked for."

// staleResultText is the do_task result the daemon would return for the misheard
// request — plausible, affirmative, and now unwanted. The keyword scan below flags
// it if the model reads it aloud after the correction.
const staleResultText = "Done — I set a reminder to call mom at six."

// correctionSuppressClause is the "guided" variant's added persona instruction —
// the strongest reasonable prompt-only attempt to make the model drop a result the
// user corrected mid-flight. Tests whether a PROMPT change alone is enough.
const correctionSuppressClause = "IMPORTANT: If the user corrected you, said you misheard, or took back a request while a task was still running, do NOT read that task's result aloud when it comes back — the user no longer wants it. Just briefly acknowledge, and only speak a result the user still wants."

func TestKoeCorrectionSuppressionDiagnostic(t *testing.T) {
	if os.Getenv("KOE_CORRECTION_SUPPRESSION_DIAGNOSTIC") != "1" {
		t.Skip("correction-suppression diagnostic: set KOE_CORRECTION_SUPPRESSION_DIAGNOSTIC=1 (mints via the running daemon)")
	}
	trials := koeEnvInt("KOE_TRIALS", 3)
	variants := []string{"forced", "plain", "guided"}
	if v := os.Getenv("KOE_RESULT_MODE"); v != "" {
		variants = []string{v}
	}
	type tally struct{ spoke, suppressed, inconclusive int }
	results := map[string]*tally{}
	for _, variant := range variants {
		tl := &tally{}
		results[variant] = tl
		for i := range trials {
			outcome, transcripts := runCorrectionTrial(t, variant)
			switch outcome {
			case "spoke":
				tl.spoke++
			case "suppressed":
				tl.suppressed++
			default:
				tl.inconclusive++
			}
			t.Logf("=== variant=%s trial=%d → %s | post-inject spoke=%q", variant, i, outcome, transcripts)
		}
	}
	t.Log("========== SUMMARY (read the per-trial transcripts above; keyword tally is a first pass) ==========")
	for _, variant := range variants {
		r := results[variant]
		t.Logf("variant=%-6s trials=%d  spoke-stale-result=%d  suppressed=%d  inconclusive=%d",
			variant, trials, r.spoke, r.suppressed, r.inconclusive)
	}
}

// runCorrectionTrial runs one live trial and returns ("spoke"|"suppressed"|
// "inconclusive", post-inject transcripts). It never t.Fatal on model BEHAVIOR
// (that is the measurement); it only fails on harness/transport errors.
func runCorrectionTrial(t *testing.T, variant string) (string, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()

	reqPCM := synthSpokenWAV(t, e2eSpoken)

	daemonBase := os.Getenv("KOE_DAEMON_URL")
	if daemonBase == "" {
		daemonBase = "http://127.0.0.1:7533"
	}
	ek, err := NewDaemonClient(daemonBase).MintViaDaemon(ctx, e2eModelName())
	if err != nil {
		t.Fatalf("mint via daemon: %v", err)
	}

	audio, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	rc, err := newPeerConnection(audio)
	if err != nil {
		t.Fatalf("newPeerConnection: %v", err)
	}
	defer rc.Close()
	send := func(v any) { b, _ := json.Marshal(v); _ = rc.dc.SendText(string(b)) }

	dbg := os.Getenv("KOE_CORR_DEBUG") == "1"
	var (
		mu               sync.Mutex
		callID           string
		correctionSent   bool // the user correction turn has been injected (R2 requested)
		resultInjected   bool // the stale do_task result has been injected (R3 requested)
		resultInjectedAt time.Time
		postInject       []string
		spokeStale       bool
		evCounts         = map[string]int{}
	)
	connected := make(chan struct{})
	configured := make(chan struct{})
	var connOnce, cfgOnce sync.Once

	// injectResult sends the stale do_task result as the function_call_output for the
	// original call_id, then a response.create in the chosen variant's style.
	injectResult := func() {
		resultInjected = true
		resultInjectedAt = time.Now()
		out, _ := json.Marshal(map[string]any{
			"spoken_summary": staleResultText, "say": staleResultText, "status": "ok",
		})
		send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
			"type": "function_call_output", "call_id": callID, "output": string(out)}})
		if variant == "forced" {
			send(map[string]any{"type": "response.create", "response": map[string]any{
				"instructions": exactSpeechInstructions(staleResultText)}})
		} else {
			send(map[string]any{"type": "response.create"}) // fast-tool style: let the model decide
		}
	}

	rc.pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			connOnce.Do(func() { close(connected) })
		}
	})
	persona := e2ePersona
	if variant == "guided" {
		persona = e2ePersona + " " + correctionSuppressClause
	}
	rc.dc.OnOpen(func() { send(sessionConfig(persona, "marin", false)) })
	rc.dc.OnMessage(func(m webrtc.DataChannelMessage) {
		var ev struct {
			Type       string `json:"type"`
			CallID     string `json:"call_id"`
			Transcript string `json:"transcript"`
		}
		_ = json.Unmarshal(m.Data, &ev)
		mu.Lock()
		defer mu.Unlock()
		evCounts[ev.Type]++
		switch ev.Type {
		case "session.updated":
			cfgOnce.Do(func() { close(configured) })
		case "response.function_call_arguments.done":
			if callID == "" {
				callID = ev.CallID
				if dbg {
					t.Logf("[%s] do_task fired (call_id=%s)", variant, ev.CallID)
				}
			}
		case "response.done":
			if dbg {
				t.Logf("[%s] response.done (callID=%q correctionSent=%v injected=%v)",
					variant, callID, correctionSent, resultInjected)
			}
			switch {
			case callID != "" && !correctionSent:
				// R1 (ack + do_task) finished. Inject the user's spoken correction as a
				// conversation turn — the model sees an identical "user says you got it
				// wrong" turn as ASR would produce, without the audio-VAD timing that
				// makes a second live utterance truncate R1 mid-turn. Then response.create
				// so the model actually REPLIES to the correction (R2), reproducing the
				// user's "koe 和我正常对话" step.
				correctionSent = true
				send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
					"type": "message", "role": "user",
					"content": []map[string]any{{"type": "input_text", "text": correctionSpoken}}}})
				send(map[string]any{"type": "response.create"})
				if dbg {
					t.Logf("[%s] injected correction turn → awaiting reply", variant)
				}
			case correctionSent && !resultInjected:
				// R2 (the reply to the correction) finished → NOW the stale do_task result
				// lands. Voice it the way the chosen variant would.
				injectResult()
				if dbg {
					t.Logf("[%s] injected stale result (%s)", variant, variant)
				}
			}
		case "response.output_audio_transcript.done":
			// Only scan what the model says AFTER we inject the stale result.
			if resultInjected && ev.Transcript != "" {
				postInject = append(postInject, ev.Transcript)
				low := strings.ToLower(ev.Transcript)
				// Affirmative "I did it" signature — distinguishes reading the result
				// from a suppression ack / clarifying question. "call mom" or a
				// set/added-reminder phrasing means it voiced the stale result.
				if strings.Contains(low, "call mom") ||
					(strings.Contains(low, "reminder") && (strings.Contains(low, "set") || strings.Contains(low, "added") || strings.Contains(low, "done"))) {
					spokeStale = true
				}
			}
		}
	})

	if err := rc.dialOpenAI(ctx, ek); err != nil {
		t.Fatalf("dialOpenAI: %v", err)
	}
	go rc.pumpSendTrack(ctx)
	go func() {
		select {
		case <-connected:
		case <-ctx.Done():
			return
		}
		select {
		case <-configured:
		case <-ctx.Done():
			return
		}
		feedWAV(ctx, audio, reqPCM) // utterance 1: the spoken request → triggers do_task.
		// The correction + result are injected from OnMessage as the responses land.
	}()

	report := func(outcome string) (string, []string) {
		mu.Lock()
		defer mu.Unlock()
		if dbg {
			t.Logf("[%s] outcome=%s events=%v", variant, outcome, evCounts)
		}
		return outcome, append([]string(nil), postInject...)
	}

	const injectGrace = 13 * time.Second // time for R3 (the model's post-inject turn) to complete
	deadline := time.After(95 * time.Second)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return report("inconclusive")
		case <-deadline:
			mu.Lock()
			inj, spoke := resultInjected, spokeStale
			mu.Unlock()
			switch {
			case !inj:
				return report("inconclusive")
			case spoke:
				return report("spoke")
			default:
				return report("suppressed")
			}
		case <-tick.C:
			mu.Lock()
			inj, spoke, injAt := resultInjected, spokeStale, resultInjectedAt
			mu.Unlock()
			if spoke {
				return report("spoke")
			}
			if inj && time.Since(injAt) > injectGrace {
				return report("suppressed") // injected, waited a full turn, model did not read it
			}
		}
	}
}
