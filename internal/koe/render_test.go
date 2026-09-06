//go:build darwin && !ios && cgo

package koe

import (
	"math"
	"path/filepath"
	"testing"
)

// rampFrame returns a 960-sample frame of distinct, increasing values so dropped
// samples are detectable by position.
func rampFrame(base int16) []int16 {
	f := make([]int16, audioFrameSize)
	for i := range f {
		f[i] = base + int16(i)
	}
	return f
}

// allZero reports whether every byte is zero (silence).
func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// TestRenderIntoPreRollHoldsThenDrains: the jitter buffer (C3 — the static fix)
// holds playback (silence) until prerollFrames have buffered, then drains FIFO.
// This absorbs the bursty WebRTC feed so the strict 48k hardware clock never
// underruns mid-reply — the garble the user heard.
func TestRenderIntoPreRollHoldsThenDrains(t *testing.T) {
	a, _ := NewAudioIO()
	out := make([]byte, audioFrameSize*2)
	// Below the cushion: held silent even though frames are queued.
	for i := range prerollFrames - 1 {
		a.playBuf <- rampFrame(int16(i * 100))
	}
	a.renderInto(out)
	if !allZero(out) {
		t.Fatal("renderInto must hold (silence) until the pre-roll cushion fills")
	}
	// Reaching the cushion primes it; it then drains the OLDEST frame first.
	a.playBuf <- rampFrame(int16((prerollFrames - 1) * 100))
	a.renderInto(out)
	got, want := bytesToS16(out), rampFrame(0)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("primed drain sample %d: got %d want %d", i, got[i], want[i])
		}
	}
}

// TestRenderIntoReprimesOnUnderrun: once the buffer drains empty it re-arms
// (silence) rather than glitching, and does not resume until the cushion refills.
func TestRenderIntoReprimesOnUnderrun(t *testing.T) {
	a, _ := NewAudioIO()
	out := make([]byte, audioFrameSize*2)
	for range prerollFrames {
		a.playBuf <- rampFrame(0)
	}
	for range prerollFrames { // drain the whole cushion
		a.renderInto(out)
	}
	a.renderInto(out) // now empty → underrun → silence + re-arm
	if !allZero(out) {
		t.Fatal("underrun must yield silence")
	}
	a.playBuf <- rampFrame(0) // one frame is below the cushion → still held
	a.renderInto(out)
	if !allZero(out) {
		t.Fatal("must re-prime (refill the cushion), not resume on a single frame")
	}
}

// TestRenderIntoUnderrunSilence: an empty playBuf zero-fills (no stale/garbage).
func TestRenderIntoUnderrunSilence(t *testing.T) {
	a, _ := NewAudioIO()
	out := make([]byte, audioFrameSize*2)
	for i := range out {
		out[i] = 0xFF // pre-dirty so a no-op would be visible
	}
	a.renderInto(out)
	for i, b := range out {
		if b != 0 {
			t.Fatalf("underrun must zero-fill, byte %d = %d", i, b)
		}
	}
}

// TestWavRoundTrip: write→read is identity — the --audio-out sink and its readback.
func TestWavRoundTrip(t *testing.T) {
	pcm := make([]int16, 4800) // 100 ms
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(i)/audioSampleRate))
	}
	p := filepath.Join(t.TempDir(), "rt.wav")
	if err := writeWavS16(p, pcm); err != nil {
		t.Fatalf("writeWavS16: %v", err)
	}
	got, err := readWavS16(p)
	if err != nil {
		t.Fatalf("readWavS16: %v", err)
	}
	if len(got) != len(pcm) {
		t.Fatalf("roundtrip len %d, want %d", len(got), len(pcm))
	}
	for i := range pcm {
		if got[i] != pcm[i] {
			t.Fatalf("roundtrip sample %d: got %d want %d", i, got[i], pcm[i])
		}
	}
}

// TestWavMetrics: the numeric verdict behaves — silence reads as silent, an
// alternating full-scale signal reads as maximally discontinuous (the static
// signature), and a clean tone reads as continuous.
func TestWavMetrics(t *testing.T) {
	sil := make([]int16, audioFrameSize*5)
	if m := wavMetrics(sil); m.RMS != 0 || m.SilenceRatio != 1 {
		t.Errorf("silence: rms=%.4f silenceRatio=%.4f, want 0 and 1", m.RMS, m.SilenceRatio)
	}

	saw := make([]int16, 1000) // ±full-scale every sample = maximal discontinuity
	for i := range saw {
		if i%2 == 0 {
			saw[i] = 30000
		} else {
			saw[i] = -30000
		}
	}
	if m := wavMetrics(saw); m.DiscontinuityRatio < 0.9 {
		t.Errorf("alternating signal disc=%.4f, want ~1", m.DiscontinuityRatio)
	}

	tone := make([]int16, 4800)
	for i := range tone {
		tone[i] = int16(8000 * math.Sin(2*math.Pi*220*float64(i)/audioSampleRate))
	}
	if m := wavMetrics(tone); m.DiscontinuityRatio > 0.01 {
		t.Errorf("clean tone disc=%.4f, want ~0", m.DiscontinuityRatio)
	}
}
