//go:build darwin && !ios && cgo

package koe

import (
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStartFileCapturesPlayback drives the file backend with no input (just the
// trailing-silence feed) while frames are Play()'d, and asserts the capture sink
// writes a non-silent WAV — i.e. playback flowing through the shared renderInto is
// captured to disk. This is the headless --audio-out path minus OpenAI.
func TestStartFileCapturesPlayback(t *testing.T) {
	a, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	// Drain the mic channel so the feed goroutine never blocks (stands in for
	// pumpSendTrack, which isn't running in this unit test).
	go func() {
		for range a.Frames() {
		}
	}()

	out := filepath.Join(t.TempDir(), "cap.wav")
	if err := a.StartFile(nil, out, audioFrameSize); err != nil {
		t.Fatalf("StartFile: %v", err)
	}

	// Queue tone frames as if decoded from an inbound RTP reply — at least
	// prerollFrames so the playback jitter buffer primes and actually drains.
	for range prerollFrames + 4 {
		tone := make([]int16, audioFrameSize)
		for i := range tone {
			tone[i] = int16(8000 * math.Sin(2*math.Pi*220*float64(i)/audioSampleRate))
		}
		a.Play(tone)
	}
	time.Sleep(200 * time.Millisecond) // let the 20ms capture ticker drain playBuf

	m := a.CapturedMetrics() // read before Stop tears the backend down
	a.Stop()

	got, err := readWavS16(out)
	if err != nil {
		t.Fatalf("readWavS16: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("capture WAV is empty — playback was not captured")
	}
	if m.RMS == 0 {
		t.Errorf("captured audio has zero RMS (silence) — the tone was not captured")
	}
	t.Logf("captured %d samples, rms=%.4f disc=%.4f", m.Samples, m.RMS, m.DiscontinuityRatio)
}

func TestFeedFramesContinuesSilenceUntilStopped(t *testing.T) {
	a, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	a.markSendReady()
	done := make(chan struct{})
	defer close(done)
	go a.feedFrames(nil, done)

	deadline := time.After(300 * time.Millisecond)
	got := 0
	for got < 5 {
		select {
		case frame := <-a.Frames():
			got++
			if rmsLevel(frame) != 0 {
				t.Fatal("trailing file-backend frames must be silence")
			}
		case <-deadline:
			t.Fatalf("feedFrames stopped early after %d silence frames", got)
		}
	}
}

func TestSynthSpeechChineseProducesNonTrivialAudio(t *testing.T) {
	if out, err := exec.Command("say", "-v", "?").Output(); err != nil || !strings.Contains(string(out), "Tingting") {
		t.Skip("Tingting system voice not installed; skipping Chinese synthesis check")
	}
	t.Setenv("KOE_SAY_VOICE", "")
	pcm, err := synthSpeech("请查询东京今天的天气，并告诉我结果。")
	if err != nil {
		t.Fatalf("synthSpeech: %v", err)
	}
	if len(pcm) < audioSampleRate {
		t.Fatalf("Chinese synthesis produced only %.3fs of audio", float64(len(pcm))/audioSampleRate)
	}
	if got := wavMetrics(pcm).RMS; got < 0.005 {
		t.Fatalf("Chinese synthesis is effectively silent: rms=%.4f", got)
	}
}
