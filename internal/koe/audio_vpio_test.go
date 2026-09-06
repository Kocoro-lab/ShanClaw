//go:build darwin && !ios && cgo

package koe

import (
	"math"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestStartVPIOHardwareCapturesAndPlays(t *testing.T) {
	if os.Getenv("KOE_VPIO_TEST") != "1" {
		t.Skip("set KOE_VPIO_TEST=1 to exercise the macOS VPIO hardware backend")
	}
	a, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	if err := a.StartVPIO(); err != nil {
		t.Fatalf("StartVPIO: %v", err)
	}
	defer a.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 160; i++ {
			frame := make([]int16, audioFrameSize)
			for j := range frame {
				s := math.Sin(2 * math.Pi * 440 * float64(i*audioFrameSize+j) / audioSampleRate)
				frame[j] = int16(s * 800)
			}
			a.Play(frame)
			time.Sleep(audioFrameMs * time.Millisecond)
		}
	}()

	cmd := exec.Command("say", "kocoro v p i o capture test")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start say: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	deadline := time.After(8 * time.Second)
	gotFrames := 0
	maxLevel := 0.0
	for gotFrames < 8 || maxLevel < 0.001 {
		select {
		case <-deadline:
			stats := a.vpioDebugStats()
			t.Fatalf("VPIO did not capture audible input: frames=%d max_level=%.5f stats=%+v", gotFrames, maxLevel, stats)
		case frame := <-a.Frames():
			gotFrames++
			if level := rmsLevel(frame); level > maxLevel {
				maxLevel = level
			}
		}
	}
	<-done
	stats := a.vpioDebugStats()
	if stats.InputCallbacks == 0 || stats.InputFrames == 0 {
		t.Fatalf("VPIO input callback did not run: %+v", stats)
	}
	if stats.OutputCallbacks == 0 || stats.OutputFrames == 0 {
		t.Fatalf("VPIO output callback did not run: %+v", stats)
	}
	t.Logf("VPIO hardware stats: %+v", stats)
}

func TestVPIOHardwareSendsOnlySilenceWhileSpeaking(t *testing.T) {
	if os.Getenv("KOE_VPIO_TEST") != "1" {
		t.Skip("set KOE_VPIO_TEST=1 to exercise the macOS VPIO hardware backend")
	}
	a, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	if err := a.StartVPIO(); err != nil {
		t.Fatalf("StartVPIO: %v", err)
	}
	defer a.Stop()

	drainCapturedFrames(a, 300*time.Millisecond)
	a.SetSpeaking(true)
	drainCapturedFrames(a, 120*time.Millisecond)
	before := a.vpioDebugStats()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			frame := make([]int16, audioFrameSize)
			for j := range frame {
				s := math.Sin(2 * math.Pi * 660 * float64(i*audioFrameSize+j) / audioSampleRate)
				frame[j] = int16(s * 4000)
			}
			a.Play(frame)
			time.Sleep(audioFrameMs * time.Millisecond)
		}
	}()

	deadline := time.After(4 * time.Second)
	keepaliveFrames := 0
	maxForwardedLevel := 0.0
	for {
		select {
		case frame := <-a.Frames():
			keepaliveFrames++
			if level := rmsLevel(frame); level > maxForwardedLevel {
				maxForwardedLevel = level
			}
		case <-done:
			after := a.vpioDebugStats()
			if after.GateDropped <= before.GateDropped {
				t.Fatalf("speaking gate did not drop any VPIO capture frames: before=%+v after=%+v", before, after)
			}
			if after.ForwardedFrames != before.ForwardedFrames {
				t.Fatalf("VPIO forwarded capture while speaking: before=%+v after=%+v", before, after)
			}
			if keepaliveFrames == 0 {
				t.Fatalf("speaking gate should send silent keepalive frames to preserve capture timing: before=%+v after=%+v", before, after)
			}
			if maxForwardedLevel > 0.0001 {
				t.Fatalf("speaking gate leaked audible capture, max forwarded RMS=%.5f frames=%d before=%+v after=%+v",
					maxForwardedLevel, keepaliveFrames, before, after)
			}
			if after.PlayUnderruns != before.PlayUnderruns {
				t.Fatalf("VPIO playback underrun while testing speaking gate: before=%+v after=%+v", before, after)
			}
			t.Logf("VPIO speaking-gate stats: keepalive=%d max_rms=%.5f before=%+v after=%+v",
				keepaliveFrames, maxForwardedLevel, before, after)
			return
		case <-deadline:
			t.Fatalf("timed out waiting for speaking-gate playback; stats=%+v", a.vpioDebugStats())
		}
	}
}

func TestVPIOHardwareBurstPlaybackDoesNotOverwriteRing(t *testing.T) {
	if os.Getenv("KOE_VPIO_TEST") != "1" {
		t.Skip("set KOE_VPIO_TEST=1 to exercise the macOS VPIO hardware backend")
	}
	a, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	if err := a.StartVPIO(); err != nil {
		t.Fatalf("StartVPIO: %v", err)
	}
	defer a.Stop()
	a.SetSpeaking(true)

	before := a.vpioDebugStats()
	for i := 0; i < 140; i++ {
		frame := make([]int16, audioFrameSize)
		for j := range frame {
			s := math.Sin(2 * math.Pi * 440 * float64(i*audioFrameSize+j) / audioSampleRate)
			frame[j] = int16(s * 2500)
		}
		a.Play(frame)
	}
	time.Sleep(700 * time.Millisecond)
	after := a.vpioDebugStats()
	if after.PlayOverwrites != before.PlayOverwrites {
		t.Fatalf("VPIO playback ring overwrote queued audio under burst input: before=%+v after=%+v", before, after)
	}
	if after.PlayBuffered > vpioPlaybackHighWaterSamples+audioFrameSize {
		t.Fatalf("VPIO playback buffered too much audio, latency risk: %+v", after)
	}
	t.Logf("VPIO burst playback stats: before=%+v after=%+v", before, after)
}

// TestStopVPIOSkipsTeardownWhenOwnershipTransferred pins S1: after a newer
// StartVPIO on a different instance takes ownership of the process-global VPIO
// unit/rings, a late stopVPIO from the old instance must NOT run the C teardown
// (that would dispose the new session's live unit/rings → silence / UAF).
// claimVPIOTeardown is the pure-Go ownership gate stopVPIO uses; exercise it
// directly so no audio hardware is needed. Holds vpioLifecycleMu (the invariant)
// and restores the global on exit.
func TestStopVPIOSkipsTeardownWhenOwnershipTransferred(t *testing.T) {
	vpioLifecycleMu.Lock()
	prev := vpioOwner
	defer func() {
		vpioOwner = prev
		vpioLifecycleMu.Unlock()
	}()

	old := &AudioIO{}
	fresh := &AudioIO{}
	vpioOwner = fresh // a newer StartVPIO already claimed ownership

	if old.claimVPIOTeardown() {
		t.Fatal("stale stopVPIO from a non-owner must skip C teardown")
	}
	if vpioOwner != fresh {
		t.Fatal("non-owner teardown must leave the current owner intact")
	}

	if !fresh.claimVPIOTeardown() {
		t.Fatal("the current owner must run C teardown")
	}
	if vpioOwner != nil {
		t.Fatal("owner teardown must clear vpioOwner")
	}
}

func drainCapturedFrames(a *AudioIO, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-a.Frames():
		case <-timer.C:
			return
		}
	}
}
