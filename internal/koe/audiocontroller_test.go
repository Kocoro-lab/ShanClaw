//go:build darwin && !ios && cgo

package koe

import "testing"

// A typed-nil *AudioIO must be stored as a nil AudioController. eventHandler
// guards eleven audio call sites with `h.audio != nil`; before AudioController
// existed those guards saw a nil *AudioIO pointer and correctly skipped. An
// interface holding a nil pointer is NOT == nil, so without normalization every
// guard would flip to true and the first audio call would panic.
//
// This is reachable from real code, not hypothetical: a test in this package
// builds its handler from `audio, _ := NewAudioIO()`, discarding the error.
func TestTypedNilAudioIsStoredAsNilController(t *testing.T) {
	var typedNil *AudioIO

	h := newEventHandler(nil, NewCallState("burst-typed-nil", ""), typedNil, func(any) error { return nil })

	if h.audio != nil {
		t.Fatalf("typed-nil *AudioIO must normalize to a nil AudioController, got %#v", h.audio)
	}
}

// The guarded paths must survive a typed-nil audio without panicking — the
// normalization is only useful if the guards actually protect the call sites.
func TestGuardedAudioPathsToleratePreNormalizedTypedNil(t *testing.T) {
	var typedNil *AudioIO

	h := newEventHandler(nil, NewCallState("burst-typed-nil-paths", ""), typedNil, func(any) error { return nil })

	// Each of these reads or writes audio state behind an `h.audio != nil` guard.
	if h.isSpeakingOrResponding() {
		t.Fatal("isSpeakingOrResponding must be false with no audio attached")
	}
	h.maybeRestoreUserMic()
}

// A real *AudioIO must pass through unchanged — normalization must not also
// discard live implementations.
func TestLiveAudioIOIsNotNormalizedAway(t *testing.T) {
	audio, err := NewAudioIO()
	if err != nil {
		t.Skipf("NewAudioIO unavailable in this environment: %v", err)
	}

	h := newEventHandler(nil, NewCallState("burst-live-audio", ""), audio, func(any) error { return nil })

	if h.audio == nil {
		t.Fatal("a live *AudioIO must be retained as the AudioController")
	}
}
