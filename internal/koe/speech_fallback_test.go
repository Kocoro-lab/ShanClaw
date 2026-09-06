//go:build darwin && !ios && cgo

package koe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestFallbackLang covers the S7 language resolution: a pinned koe language wins,
// else the utterance decides (Han → zh, else the en default). The first row is the
// reported bug — an English utterance with no pin must resolve to English.
func TestFallbackLang(t *testing.T) {
	cases := []struct {
		name      string
		pinned    string
		utterance string
		want      string
	}{
		{"english utterance, no pin (reported bug)", "", "Add a reminder to call mom", "en"},
		{"chinese utterance, no pin", "", "帮我加个提醒", "zh"},
		{"en pin overrides han utterance", "en", "帮我加个提醒", "en"},
		{"zh pin overrides latin utterance", "zh", "Add a reminder", "zh"},
		{"empty utterance, no pin → default", "", "", "en"},
		{"ja pin falls through, latin utterance", "ja", "reminder tomorrow", "en"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fallbackLang(tc.pinned, tc.utterance); got != tc.want {
				t.Errorf("fallbackLang(%q, %q) = %q, want %q", tc.pinned, tc.utterance, got, tc.want)
			}
		})
	}
}

// TestFallbackSayPerLanguage: every fallback id resolves to a non-empty, correctly
// scripted line in each language — en lines carry no Han, zh lines do.
func TestFallbackSayPerLanguage(t *testing.T) {
	keys := []string{"transport_failed", "busy", "misheard", "clarify_which", "clarify_unknown"}
	for _, key := range keys {
		en := fallbackSay("en", key)
		zh := fallbackSay("zh", key)
		if en == "" || zh == "" {
			t.Fatalf("key %q missing a language line (en=%q zh=%q)", key, en, zh)
		}
		if containsHan(en) {
			t.Errorf("en line for %q contains Han script: %q", key, en)
		}
		if !containsHan(zh) {
			t.Errorf("zh line for %q has no Han script: %q", key, zh)
		}
	}
	// An unknown language degrades to the en default, never an empty line.
	if got := fallbackSay("fr", "busy"); got != fallbackSpeech["en"]["busy"] {
		t.Errorf("unknown language should degrade to en default, got %q", got)
	}
}

// TestJoinHumanAndClarifyWhich checks the language-aware candidate rendering; zh
// concatenates without a separating space (preserving the original contract).
func TestJoinHumanAndClarifyWhich(t *testing.T) {
	if got := joinHuman("en", []string{"finance", "research"}); got != "finance or research" {
		t.Errorf("joinHuman en = %q", got)
	}
	if got := joinHuman("zh", []string{"finance", "research"}); got != "finance 还是 research" {
		t.Errorf("joinHuman zh = %q", got)
	}
	if got := clarifyWhich("en", []string{"finance", "research"}); got != "Which agent do you mean? finance or research" {
		t.Errorf("clarifyWhich en = %q", got)
	}
	if got := clarifyWhich("zh", []string{"finance"}); got != "你是指哪个 agent？finance" {
		t.Errorf("clarifyWhich zh = %q", got)
	}
}

// TestMapDoTaskOutcomeFallbackLanguage: the mechanical transport-failure and busy
// lines follow the passed language.
func TestMapDoTaskOutcomeFallbackLanguage(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		transport := MapDoTaskOutcome(DoTaskOutcome{}, fmt.Errorf("boom"), lang)
		if transport.Say != fallbackSpeech[lang]["transport_failed"] {
			t.Errorf("[%s] transport failure say = %q", lang, transport.Say)
		}
		busy := MapDoTaskOutcome(DoTaskOutcome{Kind: OutcomeRejected, Reason: "queue_full"}, nil, lang)
		if busy.Say != fallbackSpeech[lang]["busy"] {
			t.Errorf("[%s] busy say = %q", lang, busy.Say)
		}
		if (lang == "en") == containsHan(transport.Say) {
			t.Errorf("[%s] transport line script mismatch: %q", lang, transport.Say)
		}
	}
}

// TestPrepareDoTaskClarifyLanguage: the unknown-agent clarify follows the passed
// language.
func TestPrepareDoTaskClarifyLanguage(t *testing.T) {
	d := NewDispatcher(nil, NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), NewCallState("b", "default"), nil)
	for _, lang := range []string{"en", "zh"} {
		_, _, clarify, err := d.PrepareDoTask([]byte(`{"task":"ask nonexistent zzz to check x","agent":"nonexistent zzz"}`), lang, false)
		if err != nil || clarify == nil {
			t.Fatalf("[%s] expected clarify, err=%v clarify=%v", lang, err, clarify)
		}
		if clarify.Say != fallbackSpeech[lang]["clarify_unknown"] {
			t.Errorf("[%s] clarify say = %q", lang, clarify.Say)
		}
	}
}

// TestHandleFunctionCallRejectionSpeaksUtteranceLanguage is the end-to-end proof of
// the S7 wiring: with no pinned language (h.language == ""), a rejected delegation's
// spoken fallback follows the language of the user's own utterance — English for an
// English task, Chinese for a Chinese one. A pinned language overrides it.
func TestHandleFunctionCallRejectionSpeaksUtteranceLanguage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "rejected", "reason": "queue_full"})
	}))
	defer srv.Close()

	run := func(pinned, taskJSON string) *captureSender {
		state := NewCallState("burst-lang", "")
		disp := NewDispatcher(NewDaemonClient(srv.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)
		cap := &captureSender{}
		h := newEventHandler(disp, state, nil, cap.send)
		h.language = pinned
		ctx, cancel := context.WithCancel(context.Background())
		go h.runResponseSender(ctx)
		h.handleFunctionCall(ctx, "c1", "do_task", []byte(taskJSON))
		// Ledger mode emits the immediate running ack first, then the failed task
		// update carrying the localized fallback.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if cap.countType("conversation.item.create") >= 2 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		return cap
	}

	en := run("", `{"task":"remind me to call mom"}`)
	if !en.sentContains(fallbackSpeech["en"]["busy"]) {
		t.Errorf("English utterance should speak the English busy fallback; sent=%v", en.types())
	}
	if en.sentContains(fallbackSpeech["zh"]["busy"]) {
		t.Error("English utterance must not speak the Chinese fallback (the S7 bug)")
	}

	zh := run("", `{"task":"帮我提醒一下打电话"}`)
	if !zh.sentContains(fallbackSpeech["zh"]["busy"]) {
		t.Errorf("Chinese utterance should speak the Chinese busy fallback; sent=%v", zh.types())
	}

	pinned := run("en", `{"task":"帮我提醒一下打电话"}`)
	if !pinned.sentContains(fallbackSpeech["en"]["busy"]) {
		t.Errorf("a pinned en language must override the Han utterance; sent=%v", pinned.types())
	}
}
