//go:build darwin && !ios && cgo

package koe

import (
	"log"
	"math"
	"os"
)

const (
	// WORKLOAD: far-field laptop mic, quiet/long user utterances. A higher fixed
	// floor or short hangover made the local gate stop RTP before server_vad could
	// endpoint, so the user had to say a second phrase. OVERRIDE: KOE_MIC_GATE_*.
	defaultMicGateThreshold        = 0.010
	defaultVPIOMicGateThreshold    = 0.0038
	defaultVPIOBargeStartThreshold = defaultMicGateThreshold
	defaultVPIOBargeOutputRatio    = 0.30
	defaultVPIOBargeStartCeiling   = 0.045
	defaultVPIOBargeOutputDecay    = 0.92
	defaultMicGateNoiseMultiplier  = 2.0
	defaultMicGateStartMS          = 160
	defaultMicGateHangoverMS       = 2000
	micGateHotEvidenceWeight       = 2
	micGateNoiseAlpha              = 0.04
)

type micGateStats struct {
	PassedFrames  uint64
	MutedFrames   uint64
	SpeechStarts  uint64
	MaxLevel      float64
	NoiseFloor    float64
	Threshold     float64
	HotFramesMax  int
	StartScoreMax int
	StartFrames   int
}

type micNoiseGate struct {
	enabled bool

	threshold       float64
	noiseMultiplier float64
	startFrames     int
	hangoverFrames  int

	noiseFloor float64
	maxLevel   float64
	hotFrames  int
	startScore int
	hangover   int
	open       bool
	pending    [][]int16

	zero []int16

	stats micGateStats
}

type vpioBargeStartGate struct {
	baseThreshold  float64
	outputRatio    float64
	ceiling        float64
	outputDecay    float64
	outputEnvelope float64
	audible        bool
}

func newVPIOBargeStartGate() *vpioBargeStartGate {
	baseThreshold := koeEnvFloat("KOE_VPIO_BARGE_START_THRESHOLD", defaultVPIOBargeStartThreshold)
	ceiling := koeEnvFloat("KOE_VPIO_BARGE_START_CEILING", defaultVPIOBargeStartCeiling)
	if ceiling < baseThreshold {
		ceiling = baseThreshold
	}
	return &vpioBargeStartGate{
		baseThreshold: baseThreshold,
		outputRatio:   math.Max(0, koeEnvFloat("KOE_VPIO_BARGE_OUTPUT_RATIO", defaultVPIOBargeOutputRatio)),
		ceiling:       ceiling,
		outputDecay:   defaultVPIOBargeOutputDecay,
	}
}

func (g *vpioBargeStartGate) threshold(assistantSpeaking bool, outputLevel float64) float64 {
	if !assistantSpeaking {
		g.outputEnvelope = 0
		g.audible = false
		return 0
	}
	g.outputEnvelope *= g.outputDecay
	if outputLevel > g.outputEnvelope {
		g.outputEnvelope = outputLevel
	}
	if outputLevel >= playbackIdleLevelEps {
		g.audible = true
	}
	if !g.audible {
		return math.Inf(1)
	}
	return math.Min(g.ceiling, math.Max(g.baseThreshold, g.outputEnvelope*g.outputRatio))
}

func newMicNoiseGate() *micNoiseGate {
	return &micNoiseGate{
		enabled:         !koeEnvBool("KOE_MIC_GATE_OFF", false),
		threshold:       koeEnvFloat("KOE_MIC_GATE_THRESHOLD", defaultMicGateThreshold),
		noiseMultiplier: koeEnvFloat("KOE_MIC_GATE_NOISE_MULTIPLIER", defaultMicGateNoiseMultiplier),
		startFrames:     msToAudioFrames(koeEnvInt("KOE_MIC_GATE_START_MS", defaultMicGateStartMS)),
		hangoverFrames:  msToAudioFrames(koeEnvInt("KOE_MIC_GATE_HANGOVER_MS", defaultMicGateHangoverMS)),
		zero:            make([]int16, audioFrameSize),
	}
}

func newVPIOMicNoiseGate() *micNoiseGate {
	g := newMicNoiseGate()
	if raw := os.Getenv("KOE_VPIO_MIC_GATE_THRESHOLD"); raw != "" {
		g.threshold = koeEnvFloat("KOE_VPIO_MIC_GATE_THRESHOLD", defaultVPIOMicGateThreshold)
		return g
	}
	if os.Getenv("KOE_MIC_GATE_THRESHOLD") == "" {
		g.threshold = defaultVPIOMicGateThreshold
	}
	return g
}

func msToAudioFrames(ms int) int {
	frames := (ms + audioFrameMs - 1) / audioFrameMs
	if frames < 1 {
		return 1
	}
	return frames
}

func (g *micNoiseGate) process(frame []int16) [][]int16 {
	return g.processWithStartThreshold(frame, 0)
}

func (g *micNoiseGate) processWithStartThreshold(frame []int16, startThreshold float64) [][]int16 {
	if !g.enabled {
		g.stats.PassedFrames++
		return [][]int16{frame}
	}
	level := rmsLevel(frame)
	if level > g.maxLevel {
		g.maxLevel = level
	}
	threshold := math.Max(g.threshold, g.noiseFloor*g.noiseMultiplier)
	if !g.open {
		threshold = math.Max(threshold, startThreshold)
	}
	hot := level >= threshold

	if g.open {
		if hot {
			g.hangover = g.hangoverFrames
		} else {
			g.updateNoiseFloorIfAmbient(level)
			g.hangover--
			if g.hangover <= 0 {
				g.open = false
				g.hotFrames = 0
			}
		}
		if g.open {
			g.stats.PassedFrames++
			return [][]int16{frame}
		}
	}

	// Real speech often has low-energy consonant gaps; score evidence lets those
	// gaps decay gradually instead of resetting the start window to zero.
	if hot {
		g.hotFrames++
		g.startScore += micGateHotEvidenceWeight
		if g.startScore > g.startFrames {
			g.startScore = g.startFrames
		}
		if g.hotFrames > g.stats.HotFramesMax {
			g.stats.HotFramesMax = g.hotFrames
		}
	} else {
		g.hotFrames = 0
		if g.startScore > 0 {
			g.startScore--
		}
		if g.startScore == 0 {
			g.pending = g.pending[:0]
		}
		g.updateNoiseFloorIfAmbient(level)
	}
	if g.startScore > g.stats.StartScoreMax {
		g.stats.StartScoreMax = g.startScore
	}
	if g.startScore > 0 {
		g.pending = append(g.pending, append([]int16(nil), frame...))
		if len(g.pending) > g.startFrames {
			g.pending = g.pending[len(g.pending)-g.startFrames:]
		}
	}
	if g.startScore >= g.startFrames {
		g.open = true
		g.hangover = g.hangoverFrames
		g.stats.SpeechStarts++
		out := append([][]int16(nil), g.pending...)
		g.pending = g.pending[:0]
		g.stats.PassedFrames += uint64(len(out))
		return out
	}

	g.stats.MutedFrames++
	return [][]int16{g.zero}
}

func (g *micNoiseGate) resetState() {
	g.hotFrames = 0
	g.startScore = 0
	g.hangover = 0
	g.open = false
	g.pending = g.pending[:0]
}

func (g *micNoiseGate) updateNoiseFloorIfAmbient(level float64) {
	if level >= g.threshold {
		return
	}
	g.updateNoiseFloor(level)
}

func (g *micNoiseGate) updateNoiseFloor(level float64) {
	if level <= 0 {
		return
	}
	if g.noiseFloor == 0 {
		g.noiseFloor = level
		return
	}
	g.noiseFloor = (1-micGateNoiseAlpha)*g.noiseFloor + micGateNoiseAlpha*level
}

func (g *micNoiseGate) logStats() {
	if os.Getenv("KOE_AUDIO_LOG") != "1" && os.Getenv("KOE_EVENT_LOG") != "1" {
		return
	}
	g.stats.MaxLevel = g.maxLevel
	g.stats.NoiseFloor = g.noiseFloor
	g.stats.Threshold = g.threshold
	g.stats.StartFrames = g.startFrames
	log.Printf("koe[audio]: mic gate stats: %+v", g.stats)
}
