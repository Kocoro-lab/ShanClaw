//go:build darwin && !ios && cgo

package koe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/executionprofile"
)

func TestToolDefsShape(t *testing.T) {
	defs := ToolDefs()
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
		if d.Type != "function" {
			t.Errorf("tool %q type = %q, want function", d.Name, d.Type)
		}
	}
	for _, want := range []string{"do_task", "cancel", "get_status", "control_app", "switch_agent", "stop_speaking", "end_call"} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}
	if len(defs) != 7 {
		t.Errorf("got %d tools, want exactly 7", len(defs))
	}
}

// TestEndCallDescriptionSignalsDismissIntent keeps end_call scoped to a real
// conversation-ending dismiss (not a topic change or a single-task cancel), tells
// the model to judge from audio (the input transcription garbles short phrases),
// and to stay silent. It also pins the re-activation contract (double-tap Option).
func TestEndCallDescriptionSignalsDismissIntent(t *testing.T) {
	var desc string
	for _, d := range ToolDefs() {
		if d.Name == "end_call" {
			desc = d.Description
		}
	}
	if desc == "" {
		t.Fatal("end_call tool missing")
	}
	for _, want := range []string{"退出吧", "goodbye", "double-tap the Option", "Say NOTHING", "cancel", "stop_speaking", "unsure"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("end_call description missing %q", want)
		}
	}
}

func TestVoiceControlToolsSeparateStopSpeakingFromEndingTheCall(t *testing.T) {
	var stopDesc, endDesc string
	for _, d := range ToolDefs() {
		switch d.Name {
		case "stop_speaking":
			stopDesc = d.Description
		case "end_call":
			endDesc = d.Description
		}
	}
	for _, want := range []string{"停一下", "stop talking", "keep the voice call active", "not end_call"} {
		if !strings.Contains(stopDesc, want) {
			t.Fatalf("stop_speaking description missing %q: %s", want, stopDesc)
		}
	}
	for _, want := range []string{"退出", "结束通话", "End the entire voice conversation"} {
		if !strings.Contains(endDesc, want) {
			t.Fatalf("end_call description missing %q: %s", want, endDesc)
		}
	}
}

// TestCancelDescriptionRequiresExplicitStopRequest: live 2026-07-02 13:57 the
// S2S model heard 5.8s of background noise mid-task and called cancel, killing a
// 53s task. The description must set an explicit-request bar for cancelling.
func TestCancelDescriptionRequiresExplicitStopRequest(t *testing.T) {
	var desc string
	for _, d := range ToolDefs() {
		if d.Name == "cancel" {
			desc = d.Description
		}
	}
	for _, want := range []string{"clearly and explicitly", "not addressed to you"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("cancel description missing %q", want)
		}
	}
}

// TestDoTaskDescriptionMatchesPersonaContract keeps the tool description in sync
// with the persona: varied content-free acknowledgement (no single mandated
// phrase) and the information-source split (conversation-internal one-step
// replies may be answered directly; the outside world goes through do_task).
func TestDoTaskDescriptionMatchesPersonaContract(t *testing.T) {
	var desc string
	for _, d := range ToolDefs() {
		if d.Name == "do_task" {
			desc = d.Description
		}
	}
	for _, want := range []string{"vary", "one obvious step", "never quiz the user", "full final user-facing reply", "stable public knowledge", "nature of the information", "active reply language"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("do_task description missing %q", want)
		}
	}
	if strings.Contains(desc, `Chinese utterance -> "我来处理"`) {
		t.Fatal("do_task description must not mandate a single fixed acknowledgement phrase anymore")
	}
	if strings.Contains(desc, "language of the utterance") {
		t.Fatal("do_task description must defer to the session's active reply language")
	}
}

func TestDoTaskDescriptionSeparatesCurrentHandoffFromLaterTurns(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	var desc string
	for _, d := range ToolDefs() {
		if d.Name == "do_task" {
			desc = d.Description
		}
	}
	desc = strings.ToLower(desc)
	for _, want := range []string{
		"after the do_task call, emit no more audio in this response",
		"later user turns may continue normally while the task is running",
		"never narrate the delivery mechanics",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("do_task handoff contract missing %q", want)
		}
	}
}

func TestDoTaskContractGatesParallelAndMakesEachCallDisjoint(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	var def ToolDef
	for _, candidate := range ToolDefs() {
		if candidate.Name == "do_task" {
			def = candidate
			break
		}
	}
	combined := def.Description + " " + string(def.Parameters)
	for _, want := range []string{
		"Default to exactly one do_task call.",
		"only when the user explicitly asks",
		"each call must contain exactly one disjoint work unit",
		"Never send the full compound request in one call while also sending any of its parts",
		"Exactly one task scope for this call",
		"exclude work assigned to other calls",
	} {
		if !strings.Contains(combined, want) {
			t.Errorf("do_task contract missing %q", want)
		}
	}
	if strings.Contains(combined, "use one complete compound task or disjoint concrete tasks") {
		t.Fatal("do_task description still offers the ambiguous compound-plus-split choice")
	}
}

func TestBurstRouteKey(t *testing.T) {
	// MUST equal the keys Plan A Task 3 pins daemon-side.
	if got := burstRouteKey("finance", "burst-123"); got != "agent:finance:koe:burst-123" {
		t.Errorf("burstRouteKey(finance,burst-123) = %q, want agent:finance:koe:burst-123", got)
	}
	if got := burstRouteKey("", "burst-123"); got != "default:koe:burst-123" {
		t.Errorf("burstRouteKey(\"\",burst-123) = %q, want default:koe:burst-123", got)
	}
}

func TestPrepareDoTaskUsesBoundAgent(t *testing.T) {
	state := NewCallState("burst-1", "finance")
	d := NewDispatcher(nil, NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	req, _, clarify, err := d.PrepareDoTask([]byte(`{"task":"check NVDA"}`), "zh", false)
	if err != nil || clarify != nil {
		t.Fatalf("PrepareDoTask err=%v clarify=%v", err, clarify)
	}
	if req.Agent != "finance" || req.ThreadID != "burst-1" || req.Text != "check NVDA" {
		t.Errorf("req = %+v", req)
	}
}

func TestPrepareDoTaskCarriesCallContext(t *testing.T) {
	state := NewCallState("burst-1", "finance")
	state.SetCallContext(StartCallRequest{
		CWD: "/Users/hu/project",
		ForegroundHint: &ForegroundHint{
			PID:      123,
			AppName:  "Mail",
			BundleID: "com.apple.mail",
		},
	})
	d := NewDispatcher(nil, NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	req, _, clarify, err := d.PrepareDoTask([]byte(`{"task":"summarize this window"}`), "zh", false)
	if err != nil || clarify != nil {
		t.Fatalf("PrepareDoTask err=%v clarify=%v", err, clarify)
	}
	if req.CWD != "/Users/hu/project" || req.ForegroundHint == nil {
		t.Fatalf("request did not carry call context: %+v", req)
	}
	if req.ForegroundHint.PID != 123 || req.ForegroundHint.AppName != "Mail" || req.ForegroundHint.BundleID != "com.apple.mail" {
		t.Fatalf("foreground hint = %+v", req.ForegroundHint)
	}
}

func TestPrepareDoTaskClarifyOnUnknownAgent(t *testing.T) {
	state := NewCallState("burst-1", "default")
	d := NewDispatcher(nil, NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	_, _, clarify, err := d.PrepareDoTask([]byte(`{"task":"ask nonexistent zzz to check x","agent":"nonexistent zzz"}`), "zh", false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if clarify == nil || clarify.Status != "clarify" {
		t.Errorf("expected clarify SayResult, got %+v", clarify)
	}
}

func TestMapDoTaskOutcomePreservesCompleteReplyAndDeliverables(t *testing.T) {
	long := strings.Repeat("详情内容", 300)
	deliverable := Deliverable{ID: "d1", Filename: "report.html", Title: "AI report", MIME: "text/html", ByteSize: 4096}
	r := MapDoTaskOutcome(DoTaskOutcome{
		Kind: OutcomeCompleted, Reply: long, SpokenSummary: "查完了。", SessionID: "s1",
		Deliverables: []Deliverable{deliverable},
	}, nil, "zh")
	if r.Reply != long {
		t.Fatalf("complete reply was truncated: got %d runes, want %d", len([]rune(r.Reply)), len([]rune(long)))
	}
	if r.SpokenSummary != "" || r.Say != "" {
		t.Fatalf("successful result must not pin model-authored speech: %+v", r)
	}
	if r.LegacySpeech != "查完了。" || r.SessionID != "s1" {
		t.Fatalf("compatibility/session metadata missing: %+v", r)
	}
	if len(r.Deliverables) != 1 || r.Deliverables[0] != deliverable {
		t.Fatalf("deliverables not preserved: %+v", r.Deliverables)
	}
}

// TestMapDoTaskOutcomeCancelledStaysSilent: a user-cancelled run's reply is the
// tail of whatever was streaming when the run died — live 2026-07-02 13:57 Koe
// read the killed run's progress line "正在将报告要点写入桌面 Markdown 文件。"
// right after announcing the cancel. A cancelled outcome must carry status only:
// the model already acknowledged the stop in its own words when the cancel tool
// returned, so there is nothing to voice (and no canned phrase either).
func TestMapDoTaskOutcomeCancelledStaysSilent(t *testing.T) {
	r := MapDoTaskOutcome(DoTaskOutcome{
		Kind:        OutcomeCompleted,
		Reply:       "正在将报告要点写入桌面 Markdown 文件。",
		FailureCode: "user_cancelled",
	}, nil, "zh")
	if r.Status != "cancelled" {
		t.Fatalf("cancelled run status = %q, want cancelled", r.Status)
	}
	if r.Say != "" || r.SpokenSummary != "" || r.Reply != "" {
		t.Fatalf("cancelled run must carry no result speech, got %+v", r)
	}
}

// TestMapDoTaskOutcomePartialDoesNotVoiceProgress: a partial run (soft timeout /
// max-iter / idle stop, NOT user_cancelled) returns only the tail of whatever was
// streaming — often a tool preamble / progress line ("正在整理成结构化报告。").
// Voicing that (or seeding it as a recap digest) reads a progress narration aloud
// as if it were the finished result. A partial outcome must speak a safe status
// line instead and claim no completion. Repro 2026-07-10: a timed-out MARL research
// delegation had its progress line "已收集足够信息，现在整理成结构化报告。" projected
// into spoken_summary.
func TestMapDoTaskOutcomePartialDoesNotVoiceProgress(t *testing.T) {
	progress := "已收集足够信息，现在整理成结构化报告。"
	// Every partial finish reason (idle/deadline timeout, iteration limit, or an
	// unlabelled soft stop) leaves Reply/SpokenSummary as an untrustworthy progress
	// tail. None of them may be voiced or seeded as a digest.
	for _, failure := range []string{"deadline_exceeded", "iteration_limit", ""} {
		r := MapDoTaskOutcome(DoTaskOutcome{
			Kind:          OutcomeCompleted,
			Reply:         progress,
			SpokenSummary: progress, // mechanical fallback over the partial lastText
			Partial:       true,
			FailureCode:   failure,
		}, nil, "zh")
		if r.Status != "failed" {
			t.Fatalf("partial(%q) status = %q, want failed", failure, r.Status)
		}
		if strings.Contains(r.Say, progress) || strings.Contains(r.SpokenSummary, progress) {
			t.Fatalf("partial(%q) must NOT voice the progress line, got say=%q spoken=%q", failure, r.Say, r.SpokenSummary)
		}
		if r.Reply != "" {
			t.Fatalf("partial(%q) must not seed a final reply, got %q", failure, r.Reply)
		}
		if want := fallbackSay("zh", "incomplete"); r.Say == "" || r.Say != want {
			t.Fatalf("partial(%q) say = %q, want the safe canned line %q", failure, r.Say, want)
		}
	}
}

// TestMapDoTaskOutcomeCancelBeatsPartial: a user-cancelled run may ALSO be flagged
// partial (the cancel force-stops mid-stream). The cancel guard runs first, so it
// must win — status "cancelled" with no speech, never the partial "incomplete"
// line, so Koe stays silent (the model already acknowledged the stop).
func TestMapDoTaskOutcomeCancelBeatsPartial(t *testing.T) {
	r := MapDoTaskOutcome(DoTaskOutcome{
		Kind:        OutcomeCompleted,
		Reply:       "正在整理…",
		Partial:     true,
		FailureCode: "user_cancelled",
	}, nil, "zh")
	if r.Status != "cancelled" {
		t.Fatalf("cancelled+partial status = %q, want cancelled", r.Status)
	}
	if r.Say != "" || r.SpokenSummary != "" || r.Reply != "" {
		t.Fatalf("cancelled+partial must stay silent, got %+v", r)
	}
}

func TestMapDoTaskOutcome(t *testing.T) {
	if got := MapDoTaskOutcome(DoTaskOutcome{Kind: OutcomeCompleted, Reply: "long done", SpokenSummary: "done"}, nil, "zh"); got.Status != "ok" || got.Reply != "long done" || got.LegacySpeech != "done" || got.Say != "" {
		t.Errorf("completed: %+v", got)
	}
	if got := MapDoTaskOutcome(DoTaskOutcome{Kind: OutcomeCompleted, Reply: "done"}, nil, "zh"); got.Reply != "done" || got.LegacySpeech != "done" {
		t.Errorf("completed without spoken summary: %+v", got)
	}
	// injected MUST carry an empty say so the front brain doesn't double-speak.
	if got := MapDoTaskOutcome(DoTaskOutcome{Kind: OutcomeInjected}, nil, "zh"); got.Status != "injected" || got.Say != "" {
		t.Errorf("injected → %+v", got)
	}
	if got := MapDoTaskOutcome(DoTaskOutcome{Kind: OutcomeRejected, Reason: "cwd_conflict"}, nil, "zh"); got.Status != "failed" {
		t.Errorf("rejected → %+v", got)
	}
	if got := MapDoTaskOutcome(DoTaskOutcome{}, fmt.Errorf("boom"), "zh"); got.Status != "failed" {
		t.Errorf("transport error → %+v", got)
	}
}

func TestDispatchRejectsDoTask(t *testing.T) {
	d := NewDispatcher(nil, NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), NewCallState("b", "default"), nil)
	if _, err := d.Dispatch(context.Background(), "do_task", []byte(`{"task":"x"}`)); err == nil {
		t.Error("Dispatch(do_task) must error — do_task is async (PrepareDoTask + goroutine), not a fast tool")
	}
}

func TestDispatchCancelUsesBurstKey(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		if got["route_key"] != "agent:finance:koe:burst-1" {
			t.Errorf("cancel route_key = %v, want agent:finance:koe:burst-1", got["route_key"])
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	state := NewCallState("burst-1", "finance")
	d := NewDispatcher(NewDaemonClient(srv.URL), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	if _, err := d.Dispatch(context.Background(), "cancel", []byte(`{"reason":"user_cancel"}`)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

func TestDispatchCancelUsesInFlightPerCallAgentRoute(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		if got["route_key"] != "agent:finance:koe:burst-1" {
			t.Errorf("cancel route_key = %v, want agent:finance:koe:burst-1", got["route_key"])
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	state := NewCallState("burst-1", "")
	state.SetInFlightForAgent("check NVDA", "finance")
	d := NewDispatcher(NewDaemonClient(srv.URL), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	if _, err := d.Dispatch(context.Background(), "cancel", []byte(`{"reason":"user_cancel"}`)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got := state.InFlight(); got != "" {
		t.Fatalf("cancel should clear all in-flight state, got %q", got)
	}
}

func TestDispatchCancelCancelsAllInFlightRoutes(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "0")
	var routes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		routes = append(routes, got["route_key"].(string))
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	state := NewCallState("burst-1", "")
	state.SetInFlightForAgent("check NVDA", "finance")
	state.SetInFlightForAgent("review contract", "legal")
	d := NewDispatcher(NewDaemonClient(srv.URL), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	if _, err := d.Dispatch(context.Background(), "cancel", []byte(`{"reason":"user_cancel"}`)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	want := []string{"agent:finance:koe:burst-1", "agent:legal:koe:burst-1"}
	if strings.Join(routes, ",") != strings.Join(want, ",") {
		t.Fatalf("cancel routes = %v, want %v", routes, want)
	}
}

func TestDispatchSwitchAgentRebinds(t *testing.T) {
	state := NewCallState("burst-1", "default")
	d := NewDispatcher(nil, NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	if _, err := d.Dispatch(context.Background(), "switch_agent", []byte(`{"agent":"finance"}`)); err != nil {
		t.Fatalf("switch_agent: %v", err)
	}
	if got := state.BoundAgent(); got != "finance" {
		t.Errorf("bound agent = %q, want finance", got)
	}
}

func TestDispatchControlAppStub(t *testing.T) {
	var seen string
	state := NewCallState("burst-1", "default")
	d := NewDispatcher(nil, NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state,
		func(ctx context.Context, action string) error { seen = action; return nil })
	if _, err := d.Dispatch(context.Background(), "control_app", []byte(`{"action":"open_settings"}`)); err != nil {
		t.Fatalf("control_app: %v", err)
	}
	if seen != "open_settings" {
		t.Errorf("control_app action = %q, want open_settings", seen)
	}
}

// TestDoTaskDescriptionUsesOneSelfFraming pins the schema-level framing: the tool
// description the model reads when deciding whether to call do_task must NOT
// contradict the one-self persona ("back-brain" / "delegate to" frame it as
// someone else, which let the model improvise must-delegate intents like math),
// and must give a concrete reason to delegate.
func TestDoTaskDescriptionUsesOneSelfFraming(t *testing.T) {
	var doTask, controlApp, switchAgent string
	for _, d := range ToolDefs() {
		switch d.Name {
		case "do_task":
			doTask = strings.ToLower(d.Description)
		case "control_app":
			controlApp = strings.ToLower(d.Description)
		case "switch_agent":
			switchAgent = strings.ToLower(d.Description)
		}
	}
	if doTask == "" {
		t.Fatal("do_task tool not found")
	}
	for _, banned := range []string{"back-brain", "back brain", "delegate to", "kocoro already", "kocoro's full"} {
		if strings.Contains(doTask, banned) {
			t.Errorf("do_task description must not contain %q (contradicts one-self persona)", banned)
		}
	}
	if !strings.Contains(doTask, strings.ToLower(VoiceIdentityInstructions)) {
		t.Error("do_task description must include the shared single-Kocoro identity contract")
	}
	// "own hands" (not the removed "own tools" lecture sentence): first-person
	// framing survives in the description head after the one-self trim (2026-07-02).
	for _, want := range []string{"calculate precisely", "never answer", "own hands", "long or multi-part", "content/results to show in kocoro desktop"} {
		if !strings.Contains(doTask, want) {
			t.Errorf("do_task description missing %q", want)
		}
	}
	for _, want := range []string{"never use this to display", "use do_task for content/results"} {
		if !strings.Contains(controlApp, want) {
			t.Errorf("control_app description missing %q", want)
		}
	}
	if strings.Contains(switchAgent, "back-brain") {
		t.Error("switch_agent description must not contain 'back-brain'")
	}
}

func TestToolDefsLedgerSchema(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	on := ToolDefs()
	t.Setenv("KOE_TASK_LEDGER", "0")
	off := ToolDefs()
	find := func(defs []ToolDef, name string) ToolDef {
		for _, def := range defs {
			if def.Name == name {
				return def
			}
		}
		t.Fatalf("tool %q missing", name)
		return ToolDef{}
	}
	if !strings.Contains(string(find(on, "do_task").Parameters), `"relationship"`) ||
		!strings.Contains(string(find(on, "cancel").Parameters), `"task_id"`) ||
		!strings.Contains(string(find(on, "cancel").Parameters), `"all_running"`) {
		t.Fatal("ledger tool schemas must expose relationship, task identity, and all_running cancel")
	}
	if strings.Contains(string(find(off, "do_task").Parameters), `"relationship"`) ||
		strings.Contains(string(find(off, "cancel").Parameters), `"task_id"`) ||
		strings.Contains(string(find(off, "cancel").Parameters), `"all_running"`) {
		t.Fatal("ledger rollback must restore legacy schemas")
	}
	for name, defs := range map[string][]ToolDef{"ledger": on, "legacy": off} {
		doTask := find(defs, "do_task")
		params := string(doTask.Parameters)
		for _, want := range []string{
			`"additionalproperties":false`,
			"kocoro is the assistant identity",
		} {
			if !strings.Contains(strings.ToLower(params), want) {
				t.Fatalf("%s do_task schema missing %q: %s", name, want, params)
			}
		}
		// The voice model must not be asked to route: execution mode comes from
		// koe.fast_effort and is admitted daemon-side, not selected here.
		for _, unwanted := range []string{
			"execution_mode",
			"full_reason",
			"production_incident_or_recovery",
			"broad_cross_system_change",
			"classify only the current task",
		} {
			if strings.Contains(strings.ToLower(params), unwanted) {
				t.Fatalf("%s do_task schema still exposes routing field %q: %s", name, unwanted, params)
			}
		}
		var schema struct {
			Properties           map[string]json.RawMessage `json:"properties"`
			Required             []string                   `json:"required"`
			AdditionalProperties *bool                      `json:"additionalProperties"`
		}
		if err := json.Unmarshal(doTask.Parameters, &schema); err != nil {
			t.Fatalf("%s do_task schema: %v", name, err)
		}
		required := make(map[string]bool, len(schema.Required))
		for _, field := range schema.Required {
			required[field] = true
		}
		for field := range schema.Properties {
			if !required[field] {
				t.Errorf("%s closed do_task field %q is not required", name, field)
			}
		}
		if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
			t.Errorf("%s closed do_task must reject additional properties", name)
		}
		if strings.Contains(strings.ToLower(params), "missing or unknown values are treated as full") {
			t.Fatalf("%s do_task schema contains contradictory unknown-value fallback: %s", name, params)
		}
	}
}

func TestPrepareDoTaskTreatsKocoroAsDefaultAgent(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	dispatcher := NewDispatcher(
		NewDaemonClient(""),
		NewAgentResolver(nil, NoopSemanticMatcher{}),
		NewCallState("burst-kocoro-default", ""),
		nil,
	)

	req, task, clarify, err := dispatcher.PrepareDoTask(
		[]byte(`{"task":"calculate 17 times 19","agent":"Kocoro","relationship":"new","task_id":null}`),
		"en",
		false,
	)
	if err != nil || clarify != nil || task == nil {
		t.Fatalf("PrepareDoTask: req=%+v task=%+v clarify=%+v err=%v", req, task, clarify, err)
	}
	if req.Agent != "" {
		t.Fatalf("PrepareDoTask agent=%q, want implicit default", req.Agent)
	}
}

// The voice model no longer selects an execution mode, so every do_task
// requests Fast and the run lineage is what this pins. Full is reached only by
// the daemon downgrading a resolved run (koe.fast_effort off, or a Cloud
// profile-resolution failure), which comes back through RecordExecutionRun.
func TestPrepareDoTaskPinsFastAndChainsLineage(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	state := NewCallState("burst-mode", "")
	dispatcher := NewDispatcher(NewDaemonClient(""), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)

	fastReq, task, clarify, err := dispatcher.PrepareDoTask(
		[]byte(`{"task":"check one stable value","relationship":"new"}`), "en", false)
	if err != nil || clarify != nil || task == nil {
		t.Fatalf("prepare: req=%+v task=%+v clarify=%+v err=%v", fastReq, task, clarify, err)
	}
	if fastReq.ExecutionMode != executionprofile.ModeFast ||
		fastReq.RequestedExecutionMode == nil || *fastReq.RequestedExecutionMode != "fast" ||
		fastReq.FullReason != executionprofile.FullReasonNone ||
		fastReq.LogicalTaskID == "" || fastReq.ExecutionRunID == "" || fastReq.ParentRunID != "" {
		t.Fatalf("first request = %+v, want a parentless Fast run with lineage ids", fastReq)
	}

	// A follow-up while the task is still running stays on the same run: with the
	// selector gone, no request can escalate the mode mid-flight.
	sameRun, followed, clarify, err := dispatcher.PrepareDoTask(
		[]byte(`{"task":"also mention the timezone","relationship":"follow_up","task_id":"`+task.ID+`"}`), "en", false)
	if err != nil || clarify != nil || followed == nil {
		t.Fatalf("prepare in-flight follow-up: req=%+v task=%+v clarify=%+v err=%v", sameRun, followed, clarify, err)
	}
	if sameRun.ExecutionRunID != fastReq.ExecutionRunID || sameRun.ParentRunID != "" {
		t.Fatalf("in-flight follow-up = %+v, want the parent run reused", sameRun)
	}

	// A follow-up after the task landed opens a child run under the same logical task.
	state.LandResult(task.ID, SayResult{Status: "ok", Reply: "done"})
	childReq, _, clarify, err := dispatcher.PrepareDoTask(
		[]byte(`{"task":"refine that answer","relationship":"follow_up","task_id":"`+task.ID+`"}`), "en", false)
	if err != nil || clarify != nil {
		t.Fatalf("prepare follow-up: req=%+v clarify=%+v err=%v", childReq, clarify, err)
	}
	if childReq.ParentRunID != fastReq.ExecutionRunID ||
		childReq.ExecutionRunID == fastReq.ExecutionRunID ||
		childReq.LogicalTaskID != fastReq.LogicalTaskID {
		t.Fatalf("follow-up lineage = %+v, parent=%+v", childReq, fastReq)
	}
}

// A follow-up on a task the daemon already downgraded to Full must inherit
// Full rather than silently restarting the work in Fast.
func TestPrepareDoTaskInheritsDaemonDowngradedFull(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	state := NewCallState("burst-inherit", "")
	dispatcher := NewDispatcher(NewDaemonClient(""), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)

	first, task, clarify, err := dispatcher.PrepareDoTask(
		[]byte(`{"task":"summarize the sensitive review","relationship":"new"}`), "en", false)
	if err != nil || clarify != nil || task == nil {
		t.Fatalf("prepare: req=%+v task=%+v clarify=%+v err=%v", first, task, clarify, err)
	}

	// Exactly what the daemon does when koe.fast_effort is off: a Fast request
	// resolves to a Full profile, and the resolved run is written back.
	resolved := task.CurrentExecutionRun()
	resolved.Profile = executionprofile.Resolve(executionprofile.ResolutionInput{
		RequestedMode: executionprofile.ModeFast,
		FastEnabled:   false,
	})
	if resolved.Profile.EffectiveMode != executionprofile.ModeFull {
		t.Fatalf("fast_effort=off must resolve Full, got %+v", resolved.Profile)
	}
	if !state.RecordExecutionRun(task.ID, resolved) {
		t.Fatal("daemon-resolved run was rejected by the ledger")
	}
	state.LandResult(task.ID, SayResult{Status: "ok", Reply: "done"})

	next, _, clarify, err := dispatcher.PrepareDoTask(
		[]byte(`{"task":"make one quick correction","relationship":"follow_up","task_id":"`+task.ID+`"}`), "en", false)
	if err != nil || clarify != nil {
		t.Fatalf("prepare follow-up: req=%+v clarify=%+v err=%v", next, clarify, err)
	}
	if next.ExecutionMode != executionprofile.ModeFast || next.InheritedMode != executionprofile.ModeFull {
		t.Fatalf("follow-up = %+v, want a Fast request inheriting Full", next)
	}
}

func TestCancelAllRunningCancelsEveryTaskInOneCall(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	var routes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		routes = append(routes, got["route_key"].(string))
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	state := NewCallState("burst-all", "")
	first := state.BeginTask("check Tokyo weather", "finance")
	second := state.BeginTask("review contract", "legal")
	d := NewDispatcher(NewDaemonClient(srv.URL), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)

	out, err := d.Dispatch(context.Background(), "cancel", []byte(`{"all_running":true}`))
	if err != nil {
		t.Fatalf("cancel all_running: %v", err)
	}
	var decoded struct {
		Status    string   `json:"status"`
		Cancelled []string `json:"cancelled"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "ok" || len(decoded.Cancelled) != 2 {
		t.Fatalf("output = %s, want ok with both task ids", out)
	}
	if len(routes) != 2 {
		t.Fatalf("daemon cancel routes = %v, want one per running task", routes)
	}
	for _, id := range []string{first.ID, second.ID} {
		if task, ok := state.TaskByID(id); !ok || task.State != TaskCancelled {
			t.Fatalf("task %s state = %+v, want cancelled", id, task)
		}
	}
}

func TestCancelAllRunningReportsPartialFailure(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		if strings.Contains(got["route_key"].(string), "legal") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	state := NewCallState("burst-part", "")
	okTask := state.BeginTask("check Tokyo weather", "finance")
	failTask := state.BeginTask("review contract", "legal")
	d := NewDispatcher(NewDaemonClient(srv.URL), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)

	out, err := d.Dispatch(context.Background(), "cancel", []byte(`{"all_running":true}`))
	if err != nil {
		t.Fatalf("cancel all_running: %v", err)
	}
	var decoded struct {
		Status    string              `json:"status"`
		Cancelled []string            `json:"cancelled"`
		Failed    []map[string]string `json:"failed"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "partial" || len(decoded.Cancelled) != 1 || len(decoded.Failed) != 1 ||
		decoded.Cancelled[0] != okTask.ID || decoded.Failed[0]["task_id"] != failTask.ID {
		t.Fatalf("output = %s, want partial with %s cancelled and %s failed", out, okTask.ID, failTask.ID)
	}
	if task, _ := state.TaskByID(okTask.ID); task.State != TaskCancelled {
		t.Fatalf("succeeded task state = %v, want cancelled", task.State)
	}
	if task, _ := state.TaskByID(failTask.ID); task.State != TaskRunning {
		t.Fatalf("failed task state = %v, must stay running for a retry", task.State)
	}
}

func TestCancelAllRunningWithNoTasksFallsBackToBlindCancel(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		if got["route_key"] != "agent:finance:koe:burst-idle" {
			t.Errorf("blind cancel route_key = %v, want agent:finance:koe:burst-idle", got["route_key"])
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	state := NewCallState("burst-idle", "finance")
	d := NewDispatcher(NewDaemonClient(srv.URL), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	out, err := d.Dispatch(context.Background(), "cancel", []byte(`{"all_running":true}`))
	if err != nil || !strings.Contains(string(out), `"idle"`) {
		t.Fatalf("no-task all_running = %s err=%v, want idle blind-cancel", out, err)
	}
}

func TestPrepareDoTaskRelationship(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	newDispatcher := func() (*Dispatcher, *CallState) {
		state := NewCallState("burst-p", "")
		return NewDispatcher(NewDaemonClient(""), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil), state
	}

	t.Run("parallel independent calls use separate lanes", func(t *testing.T) {
		dispatcher, _ := newDispatcher()
		first, firstTask, clarify, err := dispatcher.PrepareDoTask([]byte(`{"task":"check Tokyo weather","relationship":"new"}`), "en", false)
		if err != nil || clarify != nil || firstTask == nil || first.ThreadID != "burst-p" {
			t.Fatalf("first task: req=%+v task=%+v clarify=%+v err=%v", first, firstTask, clarify, err)
		}
		second, secondTask, clarify, err := dispatcher.PrepareDoTask([]byte(`{"task":"check Osaka weather","relationship":"new"}`), "en", false)
		if err != nil || clarify != nil || secondTask == nil || second.ThreadID != "burst-p.t02" {
			t.Fatalf("second task: req=%+v task=%+v clarify=%+v err=%v", second, secondTask, clarify, err)
		}
	})

	t.Run("second same-response omitted relationship still forks", func(t *testing.T) {
		dispatcher, _ := newDispatcher()
		_, first, _, _ := dispatcher.PrepareDoTask([]byte(`{"task":"check Tokyo weather"}`), "en", false)
		req, second, clarify, err := dispatcher.PrepareDoTask([]byte(`{"task":"check Osaka news"}`), "en", true)
		if err != nil || clarify != nil || first == nil || second == nil || first.ID == second.ID || req.ThreadID != "burst-p.t02" {
			t.Fatalf("same-response split failed: first=%+v second=%+v req=%+v clarify=%+v err=%v", first, second, req, clarify, err)
		}
	})

	t.Run("follow-up targets task identity", func(t *testing.T) {
		dispatcher, state := newDispatcher()
		state.BeginTask("check Tokyo weather", "")
		target := state.BeginTask("sort unread email", "")
		req, task, clarify, err := dispatcher.PrepareDoTask([]byte(`{"task":"only include urgent messages","relationship":"follow_up","task_id":"`+target.ID+`"}`), "en", false)
		if err != nil || clarify != nil || task == nil || task.ID != target.ID || req.ThreadID != target.ThreadID {
			t.Fatalf("targeted follow-up drifted: req=%+v task=%+v clarify=%+v err=%v", req, task, clarify, err)
		}
	})

	t.Run("ambiguous follow-up clarifies", func(t *testing.T) {
		dispatcher, state := newDispatcher()
		state.BeginTask("check Tokyo weather", "")
		state.BeginTask("sort unread email", "")
		_, task, clarify, err := dispatcher.PrepareDoTask([]byte(`{"task":"change that","relationship":"follow_up","task_id":"t99"}`), "en", false)
		if err != nil || task != nil || clarify == nil || clarify.Status != "clarify" {
			t.Fatalf("want task clarification: task=%+v clarify=%+v err=%v", task, clarify, err)
		}
	})
}

func TestDispatchLedgerStatusAndTargetedCancel(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	cancelled := make(chan string, 2)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req CancelRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		cancelled <- req.RouteKey
		w.WriteHeader(http.StatusOK)
	}))
	defer mock.Close()

	state := NewCallState("burst-c", "")
	dispatcher := NewDispatcher(NewDaemonClient(mock.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)
	first := state.BeginTask("check Tokyo weather", "")
	second := state.BeginTask("sort unread email", "")

	status, err := dispatcher.Dispatch(context.Background(), "get_status", []byte(`{}`))
	if err != nil || !strings.Contains(string(status), `"task_id":"t01"`) || !strings.Contains(string(status), `"task_id":"t02"`) {
		t.Fatalf("ledger status missing tasks: %s err=%v", status, err)
	}
	ambiguous, _ := dispatcher.Dispatch(context.Background(), "cancel", []byte(`{}`))
	if !strings.Contains(string(ambiguous), `"clarify"`) {
		t.Fatalf("ambiguous cancel must not kill all tasks: %s", ambiguous)
	}
	select {
	case route := <-cancelled:
		t.Fatalf("ambiguous cancel unexpectedly hit %q", route)
	default:
	}

	out, _ := dispatcher.Dispatch(context.Background(), "cancel", []byte(`{"task_id":"`+second.ID+`"}`))
	if !strings.Contains(string(out), `"status":"ok"`) {
		t.Fatalf("targeted cancel failed: %s", out)
	}
	select {
	case route := <-cancelled:
		if route != routeKeyFor("", second.ThreadID) {
			t.Fatalf("cancel route=%q, want %q", route, routeKeyFor("", second.ThreadID))
		}
	case <-time.After(time.Second):
		t.Fatal("targeted cancel did not reach daemon")
	}
	if got, _ := state.TaskByID(second.ID); got.State != TaskCancelled {
		t.Fatalf("cancelled task state=%s", got.State)
	}
	if got, _ := state.TaskByID(first.ID); got.State != TaskRunning {
		t.Fatalf("unrelated task state=%s, want running", got.State)
	}
}

func TestPerCallAgentOverrideTrustsNativeExplicitAgent(t *testing.T) {
	newDispatcher := func() *Dispatcher {
		state := NewCallState("burst-agent", "")
		return NewDispatcher(NewDaemonClient(""), NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{}), state, nil)
	}

	// Realtime may extract the explicitly spoken agent into its own field while
	// normalizing the task text. The default must not require duplicate wording.
	t.Setenv("KOE_AGENT_OVERRIDE_GUARD", "")
	dispatcher := newDispatcher()
	req, _, clarify, err := dispatcher.PrepareDoTask([]byte(`{"task":"check Tokyo weather","agent":"finance"}`), "en", false)
	if err != nil || clarify != nil || req.Agent != "finance" {
		t.Fatalf("native explicit override was not honored: req=%+v clarify=%+v err=%v", req, clarify, err)
	}

	// The old literal-containment policy remains available as a field rollback.
	t.Setenv("KOE_AGENT_OVERRIDE_GUARD", "1")
	dispatcher = newDispatcher()
	req, _, clarify, err = dispatcher.PrepareDoTask([]byte(`{"task":"check Tokyo weather","agent":"finance"}`), "en", false)
	if err != nil || clarify != nil || req.Agent != "" {
		t.Fatalf("rollback guard did not reject non-contained agent: req=%+v clarify=%+v err=%v", req, clarify, err)
	}

	dispatcher = newDispatcher()
	req, _, clarify, err = dispatcher.PrepareDoTask([]byte(`{"task":"ask finance to check NVDA","agent":"finance"}`), "en", false)
	if err != nil || clarify != nil || req.Agent != "finance" {
		t.Fatalf("rollback guard rejected contained agent: req=%+v clarify=%+v err=%v", req, clarify, err)
	}
}
