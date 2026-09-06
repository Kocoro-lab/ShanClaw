//go:build darwin && !ios && cgo

package koe

// wav.go — WAV read/write, audio metrics, and speech synthesis for the headless
// debug harness (workstream A). These live in prod (non-test) code because the
// `shan koe` debug flags (--audio-in / --audio-out / --say) use them, and prod
// code cannot import a _test.go file. readWavS16 moved here from e2e_test.go.

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
)

// readWavS16 reads a PCM16 WAV, locating the data chunk (header not assumed 44 bytes).
func readWavS16(path string) ([]int16, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errNotWave
	}
	for i := 12; i+8 <= len(b); {
		id := string(b[i : i+4])
		sz := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		if id == "data" {
			start, end := i+8, i+8+sz
			if end > len(b) {
				end = len(b)
			}
			d := b[start:end]
			out := make([]int16, len(d)/2)
			for j := range out {
				out[j] = int16(binary.LittleEndian.Uint16(d[2*j:]))
			}
			return out, nil
		}
		i += 8 + sz + (sz & 1)
	}
	return nil, errNoDataChunk
}

// writeWavS16 writes 48 kHz mono S16 PCM as a canonical-header WAV — the
// --audio-out sink, so a captured reply can be inspected, measured, or listened to.
func writeWavS16(path string, pcm []int16) error {
	const bits = 16
	dataLen := len(pcm) * 2
	byteRate := audioSampleRate * audioChannels * bits / 8
	blockAlign := audioChannels * bits / 8

	buf := make([]byte, 0, 44+dataLen)
	put4 := func(s string) { buf = append(buf, s...) }
	put32 := func(v uint32) { buf = binary.LittleEndian.AppendUint32(buf, v) }
	put16 := func(v uint16) { buf = binary.LittleEndian.AppendUint16(buf, v) }

	put4("RIFF")
	put32(uint32(36 + dataLen))
	put4("WAVE")
	put4("fmt ")
	put32(16) // PCM fmt chunk size
	put16(1)  // audio format = PCM
	put16(uint16(audioChannels))
	put32(uint32(audioSampleRate))
	put32(uint32(byteRate))
	put16(uint16(blockAlign))
	put16(bits)
	put4("data")
	put32(uint32(dataLen))
	for _, s := range pcm {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(s))
	}
	return os.WriteFile(path, buf, 0o644)
}

// WavMetrics is a numeric verdict on captured audio so the harness can judge
// "clean vs static" without a human ear (workstream A4, Q2a). The discriminating
// field for the 480-vs-960 render bug is DiscontinuityRatio: dropped half-frames
// splice non-adjacent samples, spiking adjacent-sample deltas that a smooth voice
// signal never produces.
type WavMetrics struct {
	Samples            int
	RMS                float64 // 0..1, normalized to int16 full-scale
	Peak               float64 // 0..1
	ClippingRatio      float64 // fraction at/near full-scale
	SilenceRatio       float64 // fraction of 20 ms frames below ~ -50 dBFS
	DCOffset           float64 // mean / full-scale (a stuck-bias symptom)
	DiscontinuityRatio float64 // fraction of adjacent steps > 0.5 full-scale
}

func wavMetrics(pcm []int16) WavMetrics {
	m := WavMetrics{Samples: len(pcm)}
	if len(pcm) == 0 {
		return m
	}
	const fs = 32768.0
	var sumSq, sum float64
	var clipped, bigStep int
	prev := 0.0
	for i, s := range pcm {
		v := float64(s)
		sumSq += v * v
		sum += v
		if v >= 32000 || v <= -32000 {
			clipped++
		}
		if a := math.Abs(v); a/fs > m.Peak {
			m.Peak = a / fs
		}
		if i > 0 && math.Abs(v-prev)/fs > 0.5 {
			bigStep++
		}
		prev = v
	}
	m.RMS = math.Sqrt(sumSq/float64(len(pcm))) / fs
	m.DCOffset = (sum / float64(len(pcm))) / fs
	m.ClippingRatio = float64(clipped) / float64(len(pcm))
	if len(pcm) > 1 {
		m.DiscontinuityRatio = float64(bigStep) / float64(len(pcm)-1)
	}
	frames, silent := 0, 0
	for off := 0; off+audioFrameSize <= len(pcm); off += audioFrameSize {
		frames++
		var fsq float64
		for _, s := range pcm[off : off+audioFrameSize] {
			fsq += float64(s) * float64(s)
		}
		if math.Sqrt(fsq/float64(audioFrameSize))/fs < 0.00316 { // ~ -50 dBFS
			silent++
		}
	}
	if frames > 0 {
		m.SilenceRatio = float64(silent) / float64(frames)
	}
	return m
}

// synthSpeech renders text to 48 kHz mono S16 PCM via macOS `say` + `afconvert`
// (quiet, -o file — no audible playback). Powers `shan koe --say` and the e2e WAV
// synthesis. macOS-only (the whole koe package is already CoreAudio/cgo).
func synthSpeech(text string) ([]int16, error) {
	dir, err := os.MkdirTemp("", "koe-say-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	aiff := filepath.Join(dir, "s.aiff")
	wav := filepath.Join(dir, "s.wav")
	// say's default voice on some Macs is a Siri/Premium voice that renders to a
	// file (`-o`) as near-empty audio. A classic voice must also match the script:
	// Samantha renders Han text as roughly 12 ms of silence, so Chinese E2E input
	// otherwise never reaches Realtime. Keep an explicit override for other locales.
	voice := os.Getenv("KOE_SAY_VOICE")
	if voice == "" {
		voice = "Samantha"
		if containsHan(text) {
			voice = "Tingting"
		}
	}
	if out, err := exec.Command("say", "-v", voice, text, "-o", aiff).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("say: %v: %s", err, out)
	}
	// 48 kHz, mono, little-endian signed 16-bit — exactly the Opus/WebRTC path format.
	if out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@48000", "-c", "1", aiff, wav).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("afconvert: %v: %s", err, out)
	}
	return readWavS16(wav)
}

// LoadInputPCM resolves the debug audio input for `shan koe`: sayText via macOS
// say, else audioInPath via readWavS16. Exactly one should be set; both is an
// error, neither returns (nil, nil). Exported so package cmd can build file mode.
func LoadInputPCM(sayText, audioInPath string) ([]int16, error) {
	switch {
	case sayText != "" && audioInPath != "":
		return nil, fmt.Errorf("set only one of --say / --audio-in")
	case sayText != "":
		return synthSpeech(sayText)
	case audioInPath != "":
		return readWavS16(audioInPath)
	default:
		return nil, nil
	}
}

var (
	errNotWave     = &wavErr{"not a RIFF/WAVE file"}
	errNoDataChunk = &wavErr{"no data chunk"}
)

type wavErr struct{ s string }

func (e *wavErr) Error() string { return e.s }
