//go:build darwin && !ios && cgo

package koe

import (
	"sync"
	"time"
)

// audio_file.go — the file/debug audio backend (workstream A). It replaces the
// malgo device for headless runs: instead of a mic it streams a WAV (or
// `say`-synthesized speech) into a.frames at the 20 ms cadence; instead of a
// speaker it drains a.playBuf through the SHARED renderInto — so the real
// 480-vs-960 render path is exercised, not a copy — into a capture buffer that is
// flushed to a WAV on Stop. Selected by `shan koe --audio-in` / `--say`. Mirrors
// the file-backend lifecycle in audio.go's Stop().
type fileBackend struct {
	outPath     string
	pullSamples int           // samples per renderInto pull (audioFrameSize/2 reproduces the bug)
	done        chan struct{} // closed by stopFile to stop both goroutines
	flushed     chan struct{} // closed by the capture goroutine once outPath is written
	mu          sync.Mutex
	captured    []int16
}

// StartFile drives the file backend: stream inPCM into the mic channel and capture
// the playback into outWAV. pullSamples is the renderInto buffer size in samples
// (audioFrameSize for faithful capture; audioFrameSize/2 to reproduce the framing
// bug). outWAV may be "" to skip the capture sink (input-only runs).
func (a *AudioIO) StartFile(inPCM []int16, outWAV string, pullSamples int) error {
	if pullSamples <= 0 {
		pullSamples = audioFrameSize
	}
	fb := &fileBackend{
		outPath:     outWAV,
		pullSamples: pullSamples,
		done:        make(chan struct{}),
		flushed:     make(chan struct{}),
	}
	a.file = fb

	// Mic: stream inPCM into a.frames (the shared feed; trailing silence lets
	// server-VAD mark end-of-turn).
	go a.feedFrames(inPCM, fb.done)

	// Speaker: pull from playBuf via the SHARED renderInto at the playback cadence,
	// capturing each rendered buffer. An empty playBuf at a tick renders as silence
	// — faithfully reproducing the real device's underrun behaviour.
	go func() {
		period := time.Duration(pullSamples) * time.Second / time.Duration(audioSampleRate)
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		buf := make([]byte, pullSamples*2)
		for {
			select {
			case <-fb.done:
				if fb.outPath != "" {
					fb.mu.Lock()
					_ = writeWavS16(fb.outPath, fb.captured)
					fb.mu.Unlock()
				}
				close(fb.flushed)
				return
			case <-ticker.C:
				a.renderInto(buf)
				fb.mu.Lock()
				fb.captured = append(fb.captured, bytesToS16(buf)...)
				fb.mu.Unlock()
			}
		}
	}()
	return nil
}

// stopFile stops the file backend and waits for the capture to flush to disk.
func (a *AudioIO) stopFile() {
	if a.file == nil {
		return
	}
	close(a.file.done)
	<-a.file.flushed
}

// CapturedMetrics returns the metrics of the audio captured so far — the numeric
// "clean vs static" verdict for a headless run (workstream A4).
func (a *AudioIO) CapturedMetrics() WavMetrics {
	if a.file == nil {
		return WavMetrics{}
	}
	a.file.mu.Lock()
	defer a.file.mu.Unlock()
	return wavMetrics(a.file.captured)
}

// feedFrames streams inPCM into a.frames at the 20 ms cadence, then continuous
// silence until the backend stops. Real mic devices never stop producing frames;
// the file backend must mirror that so semantic/server VAD can endpoint turns.
func (a *AudioIO) feedFrames(inPCM []int16, done <-chan struct{}) {
	// Wait until the send pump is draining (OpenAI session configured) before
	// streaming this one-shot utterance, so it isn't fed into the 64-frame buffer
	// before the pump is ready to drain it — that overflow dropped/bursted the
	// synthesized speech and caused the silent/truncated --say runs. Cancellable.
	select {
	case <-a.sendReady:
	case <-done:
		return
	}
	ticker := time.NewTicker(audioFrameMs * time.Millisecond)
	defer ticker.Stop()
	emit := func(frame []int16) bool {
		select {
		case <-done:
			return false
		case <-ticker.C:
		}
		select {
		case a.frames <- frame:
		default: // drop if the send path is behind (matches the device callback)
		}
		return true
	}
	for off := 0; off+audioFrameSize <= len(inPCM); off += audioFrameSize {
		if !emit(append([]int16(nil), inPCM[off:off+audioFrameSize]...)) {
			return
		}
	}
	silence := make([]int16, audioFrameSize)
	for {
		if !emit(silence) {
			return
		}
	}
}
