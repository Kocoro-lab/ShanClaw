//go:build darwin && !ios && cgo

package koe

import (
	"context"
	"strings"
	"testing"
)

// TestDispatchGetStatusDuringInFlight locks get_status to the async window: while
// the deferred-ack goroutine runs (SetInFlight set, ClearInFlight not yet), a
// "is it done?" must report running + the task; after ClearInFlight (cancel or
// completion) it reports idle. C-minimal wired this but never exercised it
// mid-flight (sync do_task had no window).
func TestDispatchGetStatusDuringInFlight(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "0")
	state := NewCallState("burst-x", "")
	disp := NewDispatcher(NewDaemonClient(""), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)

	state.SetInFlight("remind me") // the goroutine sets this before DoTask
	out, err := disp.Dispatch(context.Background(), "get_status", []byte(`{}`))
	if err != nil {
		t.Fatalf("get_status: %v", err)
	}
	if !strings.Contains(string(out), "running") || !strings.Contains(string(out), "remind me") {
		t.Errorf("get_status should report the in-flight task as running; got %s", out)
	}

	state.ClearInFlight() // the goroutine clears this after DoTask returns
	out, _ = disp.Dispatch(context.Background(), "get_status", []byte(`{}`))
	if !strings.Contains(string(out), "idle") {
		t.Errorf("get_status should be idle after ClearInFlight; got %s", out)
	}
}

// TestCallStateConcurrentInFlight covers the follow-up case: a 2nd do_task
// ("change it to 6pm") spawns a goroutine while the 1st still runs. The in-flight
// state must survive until the LAST one clears, not the first — otherwise
// get_status would report idle while a delegation is still running.
func TestCallStateConcurrentInFlight(t *testing.T) {
	s := NewCallState("burst-x", "")
	s.SetInFlight("add a reminder")   // do_task #1 goroutine
	s.SetInFlight("change it to 6pm") // do_task #2 (follow-up) goroutine

	s.ClearInFlight() // #2 returns fast (injected into #1's running turn)
	if s.InFlight() == "" {
		t.Error("in-flight cleared while a concurrent do_task is still running")
	}

	s.ClearInFlight() // #1 returns (final result)
	if s.InFlight() != "" {
		t.Errorf("in-flight should be idle after the last do_task; got %q", s.InFlight())
	}
}
