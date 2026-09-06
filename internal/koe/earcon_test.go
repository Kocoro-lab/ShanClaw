//go:build darwin && !ios && cgo

package koe

import (
	"path/filepath"
	"testing"
	"time"
)

// TestReadyEarconFramesDecode checks the embedded asset decodes into whole,
// non-silent audioFrameSize frames — the format Play() requires.
func TestReadyEarconFramesDecode(t *testing.T) {
	frames := readyEarconFrames()
	if len(frames) == 0 {
		t.Fatal("no frames decoded from embedded assets/ready.pcm")
	}
	var peak int16
	for i, f := range frames {
		if len(f) != audioFrameSize {
			t.Fatalf("frame %d has %d samples, want %d", i, len(f), audioFrameSize)
		}
		for _, s := range f {
			if s > peak {
				peak = s
			}
		}
	}
	if peak == 0 {
		t.Fatal("embedded earcon decoded to silence")
	}
	t.Logf("decoded %d frames (%dms), peak=%d", len(frames), len(frames)*audioFrameMs, peak)
}

// TestDismissEarconFramesDecode checks the embedded goodbye cue decodes into whole,
// non-silent audioFrameSize frames.
func TestDismissEarconFramesDecode(t *testing.T) {
	frames := decodeEarconFrames(dismissEarconPCM)
	if len(frames) == 0 {
		t.Fatal("no frames decoded from embedded assets/dismiss.pcm")
	}
	var peak int16
	for i, f := range frames {
		if len(f) != audioFrameSize {
			t.Fatalf("frame %d has %d samples, want %d", i, len(f), audioFrameSize)
		}
		for _, s := range f {
			if s > peak {
				peak = s
			}
		}
	}
	if peak == 0 {
		t.Fatal("embedded dismiss earcon decoded to silence")
	}
	t.Logf("decoded %d frames (%dms), peak=%d", len(frames), len(frames)*audioFrameMs, peak)
}

// TestDismissEarconEnabledEnv pins the kill-switch: default on, KOE_DISMISS_EARCON toggles.
func TestDismissEarconEnabledEnv(t *testing.T) {
	t.Setenv("KOE_DISMISS_EARCON", "")
	if !DismissEarconEnabled() {
		t.Error("default should be enabled")
	}
	t.Setenv("KOE_DISMISS_EARCON", "0")
	if DismissEarconEnabled() {
		t.Error("KOE_DISMISS_EARCON=0 should disable")
	}
}

// TestReadyEarconEnabledEnv pins the kill-switch semantics: default on, and the
// KOE_READY_EARCON env var toggles it.
func TestReadyEarconEnabledEnv(t *testing.T) {
	t.Setenv("KOE_READY_EARCON", "")
	if !ReadyEarconEnabled() {
		t.Error("default should be enabled")
	}
	t.Setenv("KOE_READY_EARCON", "0")
	if ReadyEarconEnabled() {
		t.Error("KOE_READY_EARCON=0 should disable")
	}
	t.Setenv("KOE_READY_EARCON", "on")
	if !ReadyEarconEnabled() {
		t.Error("KOE_READY_EARCON=on should enable")
	}
}

// TestPlayReadyEarconMutesMicAndPlays is the self-trigger-safety gate: while the
// earcon plays, capture MUST be suppressed (the mic can't hear the cue), the cue
// MUST be audible on the playback path, and suppression MUST release afterward.
// It drives the headless file backend (shared renderInto), no OpenAI/device.
//
// PlayReadyEarcon now holds the gate via the drain-aware PlaybackIdle poll (not a
// fixed sleep), so the release keys on the file backend's real 20ms-ticked output
// level draining, then the default one-frame idle hold. The ~540ms cue keeps the
// 300ms mid-check inside it.
func TestPlayReadyEarconMutesMicAndPlays(t *testing.T) {
	t.Setenv("KOE_VPIO_BARGE_IN", "1")
	a, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	// Drain the mic channel so the file backend's feed goroutine never blocks.
	go func() {
		for range a.Frames() {
		}
	}()

	out := filepath.Join(t.TempDir(), "earcon.wav")
	if err := a.StartFile(nil, out, audioFrameSize); err != nil {
		t.Fatalf("StartFile: %v", err)
	}

	if a.captureSuppressed() {
		t.Fatal("capture must not be suppressed before the earcon")
	}

	done := make(chan struct{})
	go func() {
		a.PlayReadyEarcon()
		close(done)
	}()

	// Mid-playback: the speaking gate must be suppressing capture. The cue runs
	// ~540ms, so 300ms lands squarely inside it (before the drain poll releases).
	time.Sleep(300 * time.Millisecond)
	if !a.captureSuppressed() {
		t.Error("capture must be suppressed WHILE the earcon plays (self-trigger guard)")
	}
	if a.shouldForwardVPIOCapture(0.2) {
		t.Error("VPIO barge-in must not bypass the known earcon capture hold")
	}

	<-done // PlayReadyEarcon returns once the drain poll sees the cue silent for the hold
	if a.captureSuppressed() {
		t.Error("capture suppression must be released after the earcon drains")
	}
	if !a.shouldForwardVPIOCapture(0.2) {
		t.Error("capture must resume after the known earcon drains")
	}

	m := a.CapturedMetrics() // read before Stop tears the backend down
	a.Stop()
	if m.RMS == 0 {
		t.Error("earcon playback captured as silence — the cue did not play")
	}
	t.Logf("earcon playback rms=%.4f samples=%d", m.RMS, m.Samples)
}

// TestPlayReadyEarconExtendsGateWhilePlayoutAudible pins S10: the gate release is
// drain-aware, not a fixed clock. With no backend consuming playBuf we drive the
// output level directly to simulate a tail that outlasts the nominal floor — the
// case the old fixed time.Sleep(nominal) cut off. The gate MUST stay up while the
// level reads audible past the floor and release only once it drains.
func TestPlayReadyEarconExtendsGateWhilePlayoutAudible(t *testing.T) {
	t.Setenv("KOE_EARCON_IDLE_HOLD_MS", "20")
	a, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	a.setOutputLevel(0.4) // simulate reply/cue audio still audibly playing

	done := make(chan struct{})
	go func() {
		a.PlayReadyEarcon()
		close(done)
	}()

	// Well past the nominal floor (27+8+2 frames * 20ms = 740ms): the level is still
	// audible, so the gate must stay up. The old fixed time.Sleep(nominal) would have
	// already returned here and released the mic mid-playout.
	time.Sleep(900 * time.Millisecond)
	if !a.captureSuppressed() {
		t.Fatal("gate released while output was still audibly playing past the nominal floor")
	}
	select {
	case <-done:
		t.Fatal("PlayReadyEarcon returned before the audible tail drained")
	default:
	}

	a.setOutputLevel(0) // tail drained
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gate did not release after the tail drained")
	}
	if a.captureSuppressed() {
		t.Fatal("gate must release once the tail drains")
	}
}
