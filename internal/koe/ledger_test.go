//go:build darwin && !ios && cgo

package koe

import (
	"sync"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/executionprofile"
)

func TestBeginTaskLaneSelection(t *testing.T) {
	state := NewCallState("burst-1", "")
	first := state.BeginTask("check the weather", "")
	if first.ID != "t01" || first.ThreadID != "burst-1" || first.State != TaskRunning || first.Revision != 1 {
		t.Fatalf("first task defaults wrong: %+v", first)
	}
	second := state.BeginTask("sort my email", "")
	if second.ThreadID != "burst-1.t02" {
		t.Fatalf("concurrent same-agent task must fork a sub-lane: %+v", second)
	}
	otherAgent := state.BeginTask("draft the report", "writer")
	if otherAgent.ThreadID != "burst-1" {
		t.Fatalf("different agent owns a separate main route: %+v", otherAgent)
	}
	state.LandResult(first.ID, SayResult{Status: "ok", Reply: "sunny"})
	sequential := state.BeginTask("book a table", "")
	if sequential.ThreadID != "burst-1" {
		t.Fatalf("completed main lane must be reused: %+v", sequential)
	}
}

func TestBeginFollowUpRevision(t *testing.T) {
	state := NewCallState("burst-2", "")
	first := state.BeginTask("compare weather", "")
	if _, ok := state.BeginFollowUp("t99", "add Shanghai"); ok {
		t.Fatal("unknown task id must not resolve")
	}
	followUp, ok := state.BeginFollowUp(first.ID, "add Shanghai")
	if !ok || followUp.Revision != 2 || followUp.Label != "add Shanghai" {
		t.Fatalf("follow-up bookkeeping wrong: ok=%v task=%+v", ok, followUp)
	}
	state.LandResult(first.ID, SayResult{Status: "ok", Reply: "done"})
	reopened, ok := state.BeginFollowUp(first.ID, "now add Osaka")
	if !ok || reopened.State != TaskRunning || reopened.Revision != 3 {
		t.Fatalf("completed task must reopen on follow-up: ok=%v task=%+v", ok, reopened)
	}
}

func TestLandResultUpdatesTaskLifecycle(t *testing.T) {
	state := NewCallState("burst-3", "")
	first := state.BeginTask("task a", "")
	state.LandResult(first.ID, SayResult{Status: "injected"})
	if got, _ := state.TaskByID(first.ID); got.State != TaskRunning {
		t.Fatalf("injected landing mutated state: %+v", got)
	}
	state.LandResult(first.ID, SayResult{Status: "ok", Reply: "sunny", Deliverables: []Deliverable{{ID: "d1", Filename: "weather.html"}}})
	if got, _ := state.TaskByID(first.ID); got.State != TaskCompleted || got.Reply != "sunny" || got.DeliveredRevision != 1 || len(got.Deliverables) != 1 {
		t.Fatalf("completed landing wrong: %+v", got)
	}
	if _, ok := state.BeginFollowUp(first.ID, "correct it"); !ok {
		t.Fatal("follow-up did not reopen task")
	}
	landed, supersedes := state.LandResult(first.ID, SayResult{Status: "ok", Reply: "corrected"})
	if !supersedes || landed.Revision != 2 || landed.Reply != "corrected" {
		t.Fatalf("delivered correction metadata wrong: landed=%+v supersedes=%t", landed, supersedes)
	}
	failed := state.BeginTask("task b", "")
	state.LandResult(failed.ID, SayResult{Status: "failed", FailReason: "boom"})
	if got, _ := state.TaskByID(failed.ID); got.State != TaskFailed || got.FailReason != "boom" {
		t.Fatalf("failed landing wrong: %+v", got)
	}
	cancelled := state.BeginTask("task c", "")
	state.MarkCancelled(cancelled.ID)
	if got, _ := state.TaskByID(cancelled.ID); got.State != TaskCancelled {
		t.Fatalf("cancelled landing wrong: %+v", got)
	}
}

func TestVoiceTaskExecutionRunsAreImmutableAndFullIsSticky(t *testing.T) {
	state := NewCallState("burst-runs", "")
	task := state.BeginTaskWithMode("bounded lookup", "", executionprofile.ModeFast)
	first := task.CurrentExecutionRun()
	if first.Profile.RequestedMode != executionprofile.ModeFast || first.Profile.EffectiveMode != "" {
		t.Fatalf("provisional fast run = %+v", first)
	}
	resolved := first
	resolved.Profile = executionprofile.Profile{
		RequestedMode:       executionprofile.ModeFast,
		EffectiveMode:       executionprofile.ModeFast,
		SchemaVersion:       executionprofile.FastSchemaVersion,
		ProfileName:         executionprofile.FastProfileName,
		ProfileVersion:      executionprofile.FastProfileVersion,
		ProfileID:           "kfp1_voice-test",
		Provider:            "openai",
		Model:               "gpt-5.6-luna",
		APISurface:          "openai_responses",
		ToolContract:        executionprofile.FastToolContract,
		ReasoningEffort:     "medium",
		ServiceTier:         "fast",
		ParallelToolCalls:   true,
		ResponseCachePolicy: executionprofile.ResponseCacheOff,
		ResolutionReason:    "cloud_profile_resolved",
	}
	if !state.RecordExecutionRun(task.ID, resolved) || !state.RecordExecutionRun(task.ID, resolved) {
		t.Fatal("identical execution resolution must be idempotent")
	}
	conflict := resolved
	conflict.Profile.ProfileID = "kfp1_conflict"
	if state.RecordExecutionRun(task.ID, conflict) {
		t.Fatal("same run accepted conflicting immutable profile")
	}

	upgraded, ok := state.BeginFollowUpWithMode(task.ID, "do full review", executionprofile.ModeFull)
	if !ok {
		t.Fatal("fast->full follow-up did not resolve")
	}
	child := upgraded.CurrentExecutionRun()
	if child.RunID == first.RunID || child.ParentRunID != first.RunID || child.Profile.EffectiveMode != executionprofile.ModeFull {
		t.Fatalf("fast->full child = %+v, parent=%+v", child, first)
	}

	// A fast request on a lineage that is already full does not downgrade it.
	same, ok := state.BeginFollowUpWithMode(task.ID, "quick correction", executionprofile.ModeFast)
	if !ok || same.CurrentExecutionMode() != executionprofile.ModeFull ||
		same.CurrentExecutionRun().RunID != child.RunID {
		t.Fatalf("full->fast mutated lineage: %+v", same)
	}
	state.LandResult(task.ID, SayResult{Status: "ok", Reply: "done"})
	reopened, ok := state.BeginFollowUpWithMode(task.ID, "later fast correction", executionprofile.ModeFast)
	if !ok {
		t.Fatal("completed full lineage did not reopen")
	}
	later := reopened.CurrentExecutionRun()
	if later.RunID == child.RunID || later.ParentRunID != child.RunID ||
		later.Profile.RequestedMode != executionprofile.ModeFast ||
		later.Profile.EffectiveMode != executionprofile.ModeFull ||
		later.Profile.ResolutionReason != "lineage_full_preserved" {
		t.Fatalf("completed full->fast generation = %+v", later)
	}
}

func TestStaleParentExecutionResultCannotOverwriteChild(t *testing.T) {
	state := NewCallState("burst-1", "")
	task := state.BeginTaskWithMode("quick pass", "", executionprofile.ModeFast)
	parentRunID := task.CurrentExecutionRun().RunID
	child, ok := state.BeginFollowUpWithMode(task.ID, "do the full pass", executionprofile.ModeFull)
	if !ok {
		t.Fatal("BeginFollowUpWithMode failed")
	}
	childRunID := child.CurrentExecutionRun().RunID
	if childRunID == parentRunID {
		t.Fatal("fast to full did not mint a child generation")
	}

	stale, _, current := state.LandResultForRun(task.ID, parentRunID, SayResult{Status: "ok", Reply: "quick result"})
	if current || stale.State != TaskRunning || stale.Reply != "" ||
		stale.CurrentExecutionRun().RunID != childRunID {
		t.Fatalf("stale parent result mutated child task: current=%t task=%+v", current, stale)
	}
	landed, _, current := state.LandResultForRun(task.ID, childRunID, SayResult{Status: "ok", Reply: "full result"})
	if !current || landed.State != TaskCompleted || landed.Reply != "full result" {
		t.Fatalf("child result did not land: current=%t task=%+v", current, landed)
	}
}

func TestCallStateReturnsDetachedVoiceTaskSnapshots(t *testing.T) {
	state := NewCallState("burst-snapshots", "")
	task := state.BeginTaskWithMode("bounded lookup", "", executionprofile.ModeFast)
	task.Label = "caller mutation"
	task.ExecutionRuns[0].Profile.RequestedMode = executionprofile.ModeFull
	task.ExecutionRuns[0].Evidence.ToolOutcomes = append(
		task.ExecutionRuns[0].Evidence.ToolOutcomes,
		executionprofile.ToolOutcomeEvidence{ToolCallID: "caller-owned"},
	)

	stored, ok := state.TaskByID(task.ID)
	if !ok {
		t.Fatal("task missing from ledger")
	}
	if stored.Label != "bounded lookup" ||
		stored.ExecutionRuns[0].Profile.RequestedMode != executionprofile.ModeFast ||
		len(stored.ExecutionRuns[0].Evidence.ToolOutcomes) != 0 {
		t.Fatalf("caller mutated ledger-owned task through returned pointer: %+v", stored)
	}

	followUp, ok := state.BeginFollowUpWithMode(task.ID, "refine it", executionprofile.ModeFast)
	if !ok {
		t.Fatal("follow-up missing from ledger")
	}
	followUp.Label = "follow-up caller mutation"
	stored, _ = state.TaskByID(task.ID)
	if stored.Label != "refine it" {
		t.Fatalf("caller mutated ledger-owned follow-up: %+v", stored)
	}
}

func TestRecordExecutionRunAcceptsOnlyValidatedDirectChild(t *testing.T) {
	state := NewCallState("burst-child", "")
	task := state.BeginTaskWithMode("bounded lookup", "", executionprofile.ModeFast)
	parent := task.CurrentExecutionRun()
	parent.Profile = fastProfileForVoiceLedgerTest()
	if !state.RecordExecutionRun(task.ID, parent) {
		t.Fatal("failed to resolve parent run")
	}

	child := executionprofile.Run{
		LogicalTaskID: parent.LogicalTaskID,
		RunID:         parent.RunID + ".daemon-child",
		ParentRunID:   parent.RunID,
		Profile:       fastProfileForVoiceLedgerTest(),
	}
	landingRunID, accepted := acceptDaemonExecutionRun(state, task.ID, parent.RunID, &child)
	if !accepted || landingRunID != child.RunID {
		t.Fatalf("validated direct child = (%q, %t), want (%q, true)", landingRunID, accepted, child.RunID)
	}
	landed, _, current := state.LandResultForRun(
		task.ID,
		landingRunID,
		SayResult{Status: "ok", Reply: "child result"},
	)
	if !current || landed.Reply != "child result" ||
		landed.CurrentExecutionRun().RunID != child.RunID {
		t.Fatalf("daemon child result did not land on child identity: current=%t task=%+v", current, landed)
	}

	state = NewCallState("burst-invalid-child", "")
	task = state.BeginTaskWithMode("bounded lookup", "", executionprofile.ModeFast)
	parent = task.CurrentExecutionRun()
	parent.Profile = fastProfileForVoiceLedgerTest()
	if !state.RecordExecutionRun(task.ID, parent) {
		t.Fatal("failed to resolve invalid-child parent run")
	}
	invalid := child
	invalid.LogicalTaskID = "burst-other:t01"
	if landingRunID, accepted := acceptDaemonExecutionRun(
		state,
		task.ID,
		parent.RunID,
		&invalid,
	); accepted || landingRunID != "" {
		t.Fatalf("invalid daemon child accepted: run_id=%q accepted=%t", landingRunID, accepted)
	}
	stored, _ := state.TaskByID(task.ID)
	if len(stored.ExecutionRuns) != 1 || stored.CurrentExecutionRun().RunID != parent.RunID {
		t.Fatalf("invalid daemon child mutated ledger: %+v", stored.ExecutionRuns)
	}
}

func TestReturnedVoiceTaskSnapshotDoesNotRaceExecutionResolution(t *testing.T) {
	state := NewCallState("burst-race", "")
	task := state.BeginTaskWithMode("bounded lookup", "", executionprofile.ModeFast)
	resolved := task.CurrentExecutionRun()
	resolved.Profile = fastProfileForVoiceLedgerTest()

	start := make(chan struct{})
	failed := make(chan struct{}, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 1000; i++ {
			_ = task.CurrentExecutionRun().Profile.EffectiveMode
			_ = task.Label
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 1000; i++ {
			if !state.RecordExecutionRun(task.ID, resolved) {
				select {
				case failed <- struct{}{}:
				default:
				}
				return
			}
		}
	}()
	close(start)
	wg.Wait()
	select {
	case <-failed:
		t.Fatal("identical execution resolution was not idempotent")
	default:
	}
}

func fastProfileForVoiceLedgerTest() executionprofile.Profile {
	return executionprofile.Profile{
		RequestedMode:       executionprofile.ModeFast,
		EffectiveMode:       executionprofile.ModeFast,
		SchemaVersion:       executionprofile.FastSchemaVersion,
		ProfileName:         executionprofile.FastProfileName,
		ProfileVersion:      executionprofile.FastProfileVersion,
		ProfileID:           "kfp1_voice-ledger-test",
		Provider:            "openai",
		Model:               "gpt-5.6-luna",
		APISurface:          "openai_responses",
		ToolContract:        executionprofile.FastToolContract,
		ReasoningEffort:     "medium",
		ServiceTier:         "fast",
		ParallelToolCalls:   true,
		ResponseCachePolicy: executionprofile.ResponseCacheOff,
		ResolutionReason:    "cloud_profile_resolved",
	}
}
