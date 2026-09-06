//go:build darwin && !ios && cgo

package koe

import (
	_ "embed"
	"encoding/binary"
	"log"
	"time"
)

// ready.pcm is the "ready" earcon played once when a Desktop call reaches the
// listening state — a short, soft brand cue (the "aurora" design) so the user
// hears that Kocoro is ready without a spoken line. Format: 48kHz mono 16-bit
// little-endian PCM, frame-aligned to audioFrameSize (960-sample / 20ms) frames,
// i.e. exactly the format Play() accepts.
//
//go:embed assets/ready.pcm
var readyEarconPCM []byte

// dismiss.pcm is the "goodbye" earcon played once when a call ends (Esc / menu Stop
// / the end_call voice tool) — a short soft descending cue signalling the call is
// over and Kocoro is going dormant (re-activate with a double-tap Option). Same
// format as ready.pcm: 48kHz mono 16-bit LE PCM, frame-aligned to audioFrameSize.
//
//go:embed assets/dismiss.pcm
var dismissEarconPCM []byte

const (
	// earconDrainIdleHoldMS is how long the earcon output level must read silent —
	// PAST the nominal-playout floor — before PlayReadyEarcon treats the cue as
	// drained and releases the speaking gate. WORKLOAD: a ~540ms brand cue on the
	// Desktop call-open path. SYMPTOM if too low: the gate releases while a
	// buffering-delayed tail is still audible on the speaker, re-opening the
	// self-trigger window; if too high: the first word spoken immediately after
	// the cue is clipped. VPIO already cancels the known speaker output, so one
	// additional audio-frame window is enough after the measured output drains.
	// OVERRIDE: KOE_EARCON_IDLE_HOLD_MS.
	earconDrainIdleHoldMS = 20
	// earconDrainMaxExtraMS is the slack added to the cue's nominal playout length to
	// form the hard cap on how long PlayReadyEarcon holds the speaking gate waiting
	// for drain. WORKLOAD: the same ~540ms cue under real device buffering. SYMPTOM if
	// it binds: a wedged/absent output-level reading would otherwise pin the mic shut
	// past the cue; the cap guarantees the mic reopens. OVERRIDE: KOE_EARCON_MAX_EXTRA_MS.
	earconDrainMaxExtraMS = 1000
)

// ReadyEarconEnabled reports whether the ready earcon should play. Default on;
// KOE_READY_EARCON=0 disables it for users who find the cue intrusive. WORKLOAD:
// Desktop voice calls. SYMPTOM if it binds: a user wants silence on connect.
// OVERRIDE: this env var (there is no config surface — it's a personal cue).
func ReadyEarconEnabled() bool { return koeEnvBool("KOE_READY_EARCON", true) }

// DismissEarconEnabled reports whether the goodbye earcon should play on call end.
// Default on; KOE_DISMISS_EARCON=0 disables it for users who want a silent hangup.
func DismissEarconEnabled() bool { return koeEnvBool("KOE_DISMISS_EARCON", true) }

// decodeEarconFrames decodes embedded PCM into audioFrameSize int16 frames. Each
// frame is a freshly allocated slice because Play() takes ownership of the slice it
// is handed without copying.
func decodeEarconFrames(pcm []byte) [][]int16 {
	n := len(pcm) / 2
	if n < audioFrameSize {
		return nil
	}
	frames := make([][]int16, 0, n/audioFrameSize)
	for off := 0; off+audioFrameSize <= n; off += audioFrameSize {
		frame := make([]int16, audioFrameSize)
		for i := range audioFrameSize {
			frame[i] = int16(binary.LittleEndian.Uint16(pcm[(off+i)*2:]))
		}
		frames = append(frames, frame)
	}
	return frames
}

// readyEarconFrames decodes the embedded ready cue (kept for callers/tests).
func readyEarconFrames() [][]int16 { return decodeEarconFrames(readyEarconPCM) }

// PlayReadyEarcon plays the ready cue; PlayDismissEarcon plays the goodbye cue.
func (a *AudioIO) PlayReadyEarcon()   { a.playEarcon("ready", readyEarconFrames()) }
func (a *AudioIO) PlayDismissEarcon() { a.playEarcon("dismiss", decodeEarconFrames(dismissEarconPCM)) }

// playEarcon plays a decoded earcon through the playback path with the mic muted for
// the cue's full duration, then restores the prior gate state. It blocks until
// playback drains, so call it in a goroutine.
//
// Self-trigger safety: the dedicated knownOutputCaptureHold is not bypassed by
// VPIO barge-in, unlike the ordinary speaking gate. This keeps the cue and its AEC
// tail out of server VAD without making assistant speech uninterruptible. At a call
// boundary no reply is in flight (PrepareForCall zeroed the gates), so nothing is
// clobbered; the captured prior state is restored on return regardless.
func (a *AudioIO) playEarcon(name string, frames [][]int16) {
	if len(frames) == 0 {
		return
	}
	prevSpeaking := a.speaking.Load()
	prevPlayback := a.playback.Load()
	prevCaptureHold := a.knownOutputCaptureHold.Load()
	started := time.Now()

	a.knownOutputCaptureHold.Store(true)
	a.SetPlaybackEnabled(true)
	a.SetSpeaking(true)

	// Queue every frame up front. renderInto only starts draining once
	// prerollFrames have accumulated, so drip-feeding one frame per tick would
	// never cross the pre-roll threshold and nothing would play. playBuf (cap 256)
	// holds the whole cue without overflow.
	for _, f := range frames {
		a.Play(f)
	}

	// Hold the speaking gate until playback has actually DRAINED, not on a fixed
	// clock: releasing exactly at the computed wall time cut the tail (or left it
	// audible on the speaker → self-trigger window) whenever device buffering pushed
	// the real playout past the estimate. Two parts:
	//   (1) a floor at the nominal playout length — the cue's frames physically need
	//       that long to render at one 20ms device tick each, and the "aurora" cue has
	//       soft internal passages whose RMS dips below playbackIdleLevelEps, so a
	//       pure level poll would false-release mid-cue; never release before the floor.
	//   (2) past the floor, the drain-aware PlaybackIdle poll (mirrors realtime.go's
	//       releaseSpeakingAfterOutputBufferWait): release once the output level has
	//       read silent for the idle hold — extending past nominal if buffering delayed
	//       the tail — with a nominal+slack hard cap so a wedged/absent level reading
	//       can never pin the mic shut.
	nominal := time.Duration((len(frames)+prerollFrames+2)*audioFrameMs) * time.Millisecond
	hold := time.Duration(koeEnvInt("KOE_EARCON_IDLE_HOLD_MS", earconDrainIdleHoldMS)) * time.Millisecond
	floor := started.Add(nominal)
	deadline := floor.Add(time.Duration(koeEnvInt("KOE_EARCON_MAX_EXTRA_MS", earconDrainMaxExtraMS)) * time.Millisecond)
	ticker := time.NewTicker(playbackIdlePollInterval)
	defer ticker.Stop()
	var idleSince time.Time
	for {
		<-ticker.C
		now := time.Now()
		if now.Before(floor) {
			continue // still within the cue's nominal playout — keep the gate up
		}
		if a.PlaybackIdle() {
			if idleSince.IsZero() {
				idleSince = now
			}
			if now.Sub(idleSince) >= hold {
				break // drained past the floor — safe to release
			}
		} else {
			idleSince = time.Time{} // tail ran past nominal (buffering) — keep waiting
		}
		if now.After(deadline) {
			break // hard cap: never leave the mic muted on a wedged level reading
		}
	}

	a.SetSpeaking(prevSpeaking)
	a.SetPlaybackEnabled(prevPlayback)
	a.knownOutputCaptureHold.Store(prevCaptureHold)
	log.Printf("koe[earcon]: %s earcon played frames=%d dur=%dms", name, len(frames), time.Since(started).Milliseconds())
}
