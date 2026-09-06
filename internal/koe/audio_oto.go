//go:build darwin && !ios && cgo

package koe

import (
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
)

// audio_oto.go — the PRODUCTION playback backend. malgo/miniaudio's low-level
// CoreAudio AudioUnit callback corrupts samples into static on this hardware
// (proven by ear: `shan koe --tone-direct`/`--tone-native` are noise while afplay
// and an oto tone of the SAME PCM are clean). oto drives macOS AudioToolbox (the
// high-level path, via purego — no cgo) and plays cleanly. We keep malgo for the
// mic (capture-only) and swap ONLY playback.
//
// Push→pull bridge: WebRTC OnTrack pushes decoded frames via AudioIO.Play →
// a.playBuf; oto pulls via otoSource.Read. The reader never returns EOF (that
// would end the stream) — it fills silence on underrun so the live call survives
// gaps in the network feed.

// oto allows exactly one Context per process, so build it once and reuse it across
// Start/Stop cycles (future per-call mic lifecycle re-opens the device, not the
// context).
var (
	otoOnce    sync.Once
	otoContext *oto.Context
	otoInitErr error
)

// otoBufferSize is the oto device buffer. WORKLOAD: a live voice reply drained by
// a steady consumer while the Docker VM eats ~half a core, so oto's reader
// goroutine can be scheduled late. SYMPTOM when too small: clean-but-choppy 滴滴滴
// (consumer-side underrun). 120 ms rides over scheduling gaps at a latency the
// voice path already exceeds. OVERRIDE: tune by ear; pair with otoPrerollFrames.
const otoBufferSize = 120 * time.Millisecond

func ensureOtoContext() (*oto.Context, error) {
	otoOnce.Do(func() {
		ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
			SampleRate:   audioSampleRate,
			ChannelCount: audioChannels, // mono — the Opus/WebRTC reply is 1 ch
			Format:       oto.FormatSignedInt16LE,
			BufferSize:   otoBufferSize,
		})
		if err != nil {
			otoInitErr = err
			return
		}
		<-ready
		otoContext = ctx
	})
	return otoContext, otoInitErr
}

// otoPrerollFrames is the PRODUCER-side jitter cushion. WORKLOAD: OpenAI streams
// the reply over WebRTC in network-paced bursts; oto drains at a steady clock.
// SYMPTOM when absent: the first frames drain before the next burst lands →
// underrun gaps at turn start. 4 frames ≈ 80 ms, the low end of voice jitter
// buffers. This is SEPARATE from oto's BufferSize: BufferSize only delays the
// silence we hand oto, it cannot manufacture the missing producer audio.
const otoPrerollFrames = 4

// otoSource bridges the push (Play→playBuf) and pull (oto Read) models. Read runs
// on oto's single playback goroutine, so its state needs no locking.
type otoSource struct {
	a        *AudioIO
	primed   bool   // pre-roll gate: hold silence until playBuf accumulates ≥ preroll
	leftover []byte // tail of a frame the previous Read could not fully place in p
}

func (s *otoSource) Read(p []byte) (int, error) {
	if s.a.PlaybackPaused() {
		s.a.setOutputLevel(0)
		zeroBytes(p)
		return len(p), nil
	}
	i := 0
	// 1. Place any tail left from the previous Read first (a frame straddling the
	//    p boundary), so no decoded audio is dropped.
	if len(s.leftover) > 0 {
		n := copy(p, s.leftover)
		s.leftover = s.leftover[n:]
		i += n
	}
	// 2. Pre-roll: until primed, emit silence until enough frames have queued.
	if !s.primed {
		if len(s.a.playBuf) < otoPrerollFrames {
			s.a.setOutputLevel(0) // D3w: silent while pre-rolling
			zeroBytes(p[i:])
			return len(p), nil
		}
		s.primed = true
	}
	// 3. Fill the remainder from playBuf; on underrun, silence-fill and re-arm so
	//    playback never resumes starved.
	for i < len(p) {
		select {
		case pcm := <-s.a.playBuf:
			s.a.setOutputLevel(rmsLevel(pcm)) // D3w: reactive speaking amplitude
			b := s16Bytes(pcm)
			n := copy(p[i:], b)
			i += n
			if n < len(b) {
				s.leftover = b[n:] // b is freshly allocated per frame — safe to retain
			}
		default:
			s.primed = false
			s.a.setOutputLevel(0) // D3w: underran → silent
			zeroBytes(p[i:])
			return len(p), nil
		}
	}
	return len(p), nil
}

// s16Bytes converts a PCM frame to a fresh little-endian S16 byte slice.
func s16Bytes(pcm []int16) []byte {
	b := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		b[2*i] = byte(s)
		b[2*i+1] = byte(s >> 8)
	}
	return b
}
