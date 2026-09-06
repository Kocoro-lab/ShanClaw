package koe

// Shared audio-level primitives. Deliberately untagged: realtime.go (built for
// iOS via gomobile) consumes them for the Qwen remote-audio tail, while the
// darwin-only playback paths (audio.go, noise_gate.go, webrtc.go) use the same
// definitions. Keeping them in a darwin-tagged file breaks the koebind build.

import "math"

// rmsLevel returns the RMS amplitude of a PCM frame normalized to 0..1.
func rmsLevel(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}
	var sumSq float64
	for _, s := range pcm {
		v := float64(s)
		sumSq += v * v
	}
	return math.Sqrt(sumSq/float64(len(pcm))) / 32768.0
}

// playbackIdleLevelEps separates "reply audio audibly playing" from silence /
// warm-session comfort noise for PlaybackIdle. WORKLOAD: TTS speech RMS runs
// well above 0.01; decoded silent RTP and drained pipelines sit near 0. SYMPTOM
// if too high: the speaking watchdog releases (and cuts) mid-speech; if too low:
// residual noise keeps the watchdog waiting until its hard cap. OVERRIDE: none —
// revisit alongside the playback paths' level reporting.
const playbackIdleLevelEps = 0.005
