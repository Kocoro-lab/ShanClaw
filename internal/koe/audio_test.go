//go:build darwin && !ios && cgo

package koe

import (
	"math"
	"testing"
)

func TestOpusRoundTrip(t *testing.T) {
	a, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	// A 20 ms 48 kHz mono frame of a low-amplitude tone.
	frame := make([]int16, 960)
	for i := range frame {
		frame[i] = int16(1000 * ((i % 48) - 24))
	}
	enc, err := a.EncodeFrame(frame)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	if len(enc) == 0 {
		t.Fatal("encoded frame is empty")
	}
	dec, err := a.DecodeFrame(enc)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if len(dec) != 960 {
		t.Errorf("decoded %d samples, want 960", len(dec))
	}
}

// TestRenderIntoReportsOutputLevelAndIdle pins the drain signal the speaking
// watchdog relies on: while frames play the output level is non-zero (not idle);
// on underrun the level returns to zero and PlaybackIdle reports true. Without
// the level honestly zeroing, the watchdog either cuts long replies mid-word
// (the 2026-07-02 "Koe interrupts itself" report) or never releases the mic.
func TestRenderIntoReportsOutputLevelAndIdle(t *testing.T) {
	a, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	a.SetPlaybackEnabled(true)
	if !a.PlaybackIdle() {
		t.Fatal("fresh AudioIO must report playback idle")
	}

	loud := make([]int16, audioFrameSize)
	for i := range loud {
		loud[i] = 8000
	}
	for i := 0; i < prerollFrames; i++ {
		a.Play(append([]int16(nil), loud...))
	}
	out := make([]byte, audioFrameSize*2)
	a.renderInto(out)
	if a.PlaybackIdle() {
		t.Fatal("playing a loud frame must not report idle")
	}

	for i := 0; i < prerollFrames+1; i++ { // drain the queue, then one underrun render
		a.renderInto(out)
	}
	if !a.PlaybackIdle() {
		t.Fatalf("underrun must zero the output level and report idle, level=%f", a.OutputLevel())
	}
}

func TestPlaybackQueueReportsDroppedFrames(t *testing.T) {
	a, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	frame := make([]int16, audioFrameSize)
	for i := 0; i < playbackBufferFrames+1; i++ {
		a.Play(frame)
	}
	if got := a.playbackQueueDrops.Load(); got != 1 {
		t.Fatalf("playback queue drops=%d, want 1", got)
	}
	if got := a.playbackQueueMax.Load(); got != playbackBufferFrames {
		t.Fatalf("playback queue max=%d, want %d", got, playbackBufferFrames)
	}
}

// TestResolveCaptureFrameKeepaliveSilence pins the RTP-continuity contract: when
// the speak-gate suppresses capture, the pipeline must forward a SILENT frame by
// default instead of halting the send track. Halting glues the pre/post-speech RTP
// timelines together; the drift accumulated over a few assistant turns is the
// prime suspect for the 2026-07-02 mid-call server-VAD deafness.
func TestResolveCaptureFrameKeepaliveSilence(t *testing.T) {
	a := &AudioIO{}
	frame := []int16{1, 2, 3}

	if got := a.resolveCaptureFrame(frame, true); len(got) != 3 || &got[0] != &frame[0] {
		t.Fatal("forwarded capture must pass the original frame through unchanged")
	}

	got := a.resolveCaptureFrame(frame, false)
	if len(got) != audioFrameSize {
		t.Fatalf("suppressed capture must forward a full silent keepalive frame, got len %d", len(got))
	}
	for i, v := range got {
		if v != 0 {
			t.Fatalf("keepalive frame must be silent, got %d at index %d", v, i)
		}
	}

	t.Setenv("KOE_CAPTURE_KEEPALIVE", "0")
	if got := a.resolveCaptureFrame(frame, false); got != nil {
		t.Fatal("KOE_CAPTURE_KEEPALIVE=0 must restore the legacy drop behaviour")
	}
}

func TestHalfDuplexGateDropsWhileSpeaking(t *testing.T) {
	a, _ := NewAudioIO()
	a.SetSpeaking(true)
	if !a.dropCapture() {
		t.Error("while speaking, capture must be dropped")
	}
	a.SetSpeaking(false)
	if a.dropCapture() {
		t.Error("when not speaking, capture must flow")
	}
}

func TestAudioInputBufferCoversColdStartWindow(t *testing.T) {
	a, _ := NewAudioIO()
	frame := make([]int16, audioFrameSize)
	for i := 0; i < inputBufferFrames; i++ {
		select {
		case a.frames <- frame:
		default:
			t.Fatalf("input buffer accepted %d frame(s), want %d", i, inputBufferFrames)
		}
	}
	select {
	case a.frames <- frame:
		t.Fatalf("input buffer accepted more than %d frame(s)", inputBufferFrames)
	default:
	}
}

func TestVPIOVoiceProcessingBypassSetting(t *testing.T) {
	a, _ := NewAudioIO()
	if a.VPIOVoiceProcessingBypassed() {
		t.Fatal("VPIO voice processing bypass should default off")
	}
	a.SetVPIOVoiceProcessingBypassed(true)
	if !a.VPIOVoiceProcessingBypassed() {
		t.Fatal("VPIO voice processing bypass should be settable")
	}
	a.SetVPIOVoiceProcessingBypassed(false)
	if a.VPIOVoiceProcessingBypassed() {
		t.Fatal("VPIO voice processing bypass should clear")
	}
}

func TestPlaybackGateDropsFramesUntilEnabled(t *testing.T) {
	a, _ := NewAudioIO()
	frame := make([]int16, audioFrameSize)

	a.Play(frame)
	if got := len(a.playBuf); got != 1 {
		t.Fatalf("playback should default enabled, playBuf len=%d", got)
	}

	a.SetPlaybackEnabled(false)
	if got := len(a.playBuf); got != 0 {
		t.Fatalf("disabling playback should drain playBuf, len=%d", got)
	}
	a.Play(frame)
	if got := len(a.playBuf); got != 0 {
		t.Fatalf("disabled playback should drop inbound audio, len=%d", got)
	}

	a.SetPlaybackEnabled(true)
	a.Play(frame)
	if got := len(a.playBuf); got != 1 {
		t.Fatalf("enabled playback should accept inbound audio, len=%d", got)
	}
}

func TestPlaybackPauseRetainsFramesForResume(t *testing.T) {
	a, _ := NewAudioIO()
	a.SetPlaybackEnabled(true)
	frame := make([]int16, audioFrameSize)
	for i := range frame {
		frame[i] = 8000
	}
	for i := 0; i < prerollFrames; i++ {
		a.Play(append([]int16(nil), frame...))
	}

	a.SetPlaybackPaused(true)
	out := make([]byte, audioFrameSize*2)
	a.renderInto(out)
	if got := len(a.playBuf); got != prerollFrames {
		t.Fatalf("paused playback consumed queued frames: got=%d want=%d", got, prerollFrames)
	}
	for i, sample := range out {
		if sample != 0 {
			t.Fatalf("paused playback emitted non-silence byte %d at %d", sample, i)
		}
	}

	a.SetPlaybackPaused(false)
	a.renderInto(out)
	if got := len(a.playBuf); got != prerollFrames-1 {
		t.Fatalf("resumed playback did not continue queue: got=%d want=%d", got, prerollFrames-1)
	}
	if a.PlaybackIdle() {
		t.Fatal("resumed non-silent frame must be audible")
	}
}

func TestSubUint64ClampsCounterReset(t *testing.T) {
	if got := subUint64(10, 3); got != 7 {
		t.Fatalf("subUint64(10, 3) = %d, want 7", got)
	}
	if got := subUint64(3, 10); got != 0 {
		t.Fatalf("subUint64(3, 10) = %d, want 0", got)
	}
}

func TestVPIODebugStatsTracksMaxOutputLevel(t *testing.T) {
	a, _ := NewAudioIO()
	a.vpioActive.Store(true)
	a.resetVPIOCallStats()

	a.setOutputLevel(0.12)
	a.setOutputLevel(0.08)
	a.setOutputLevel(0.34)

	stats := a.vpioDebugStatsSinceBase()
	if stats.MaxOutputLevel != 0.34 {
		t.Fatalf("MaxOutputLevel = %.2f, want 0.34", stats.MaxOutputLevel)
	}
}

func TestVPIOGateSuppressesWhileSpeakingByDefault(t *testing.T) {
	a, _ := NewAudioIO()
	a.SetSpeaking(true)

	for i := 0; i < 50; i++ {
		if a.shouldForwardVPIOCapture(1.0) {
			t.Fatalf("speaking capture frame %d should not pass without explicit barge-in opt-in", i)
		}
	}
	if got := a.vpioGateDropped.Load(); got == 0 {
		t.Fatal("speaking gate should count dropped VPIO frames")
	}

	a.SetSpeaking(false)
	if !a.shouldForwardVPIOCapture(0) {
		t.Fatal("capture should flow normally when Koe is not speaking")
	}
}

func TestVPIOBargeInForwardsContinuouslyWhileSpeaking(t *testing.T) {
	// Approach A: with barge-in enabled the mic stays live during playback and the
	// server VAD decides — the client no longer energy-gates. Every captured frame
	// forwards immediately regardless of level, so real speech (which dips between
	// syllables) is never dropped before it reaches the server.
	t.Setenv("KOE_VPIO_BARGE_IN", "1")
	a, _ := NewAudioIO()
	a.SetSpeaking(true)

	for i, level := range []float64{0.5, 0.001, 0.2, 0.0, 0.05} {
		if !a.shouldForwardVPIOCapture(level) {
			t.Fatalf("frame %d (level=%.3f) should forward continuously while barge-in is on", i, level)
		}
	}
	if got := a.vpioBargePassed.Load(); got == 0 {
		t.Fatal("barge-in should count frames forwarded while speaking")
	}

	a.SetSpeaking(false)
	if !a.shouldForwardVPIOCapture(0) {
		t.Fatal("capture should flow normally when Koe is not speaking")
	}
}

func TestVPIOQwenBargeInFollowsUserSetting(t *testing.T) {
	t.Setenv("KOE_VPIO_BARGE_IN", "1")
	a, _ := NewAudioIO()
	a.SetRealtimeProvider(ProviderQwen)
	a.SetSpeaking(true)

	if !a.shouldForwardVPIOCapture(0.5) {
		t.Fatal("Qwen playback capture should flow when barge-in is enabled")
	}
	t.Setenv("KOE_VPIO_BARGE_IN", "0")
	if a.shouldForwardVPIOCapture(0.5) {
		t.Fatal("Qwen playback capture must stay gated when barge-in is disabled")
	}
}

func TestVPIOBargeInNeverOverridesUserMicOff(t *testing.T) {
	// User mic-off outranks barge-in: never forward while the user asked for silence.
	t.Setenv("KOE_VPIO_BARGE_IN", "1")
	a, _ := NewAudioIO()
	a.SetSpeaking(true)
	a.userMicOff.Store(true)

	if a.shouldForwardVPIOCapture(0.9) {
		t.Fatal("mic-off must suppress capture even with barge-in enabled")
	}
}

func TestMicNoiseGateDropsQuietFrames(t *testing.T) {
	g := newMicNoiseGate()
	quiet := make([]int16, audioFrameSize)
	for i := range quiet {
		quiet[i] = 20
	}

	out := g.process(quiet)
	if len(out) != 1 || !allZeroSamples(out[0]) {
		t.Fatal("quiet background should be replaced with digital silence for server VAD")
	}
	if got := g.stats.MutedFrames; got != 1 {
		t.Fatalf("muted frames = %d, want 1", got)
	}
}

func TestMicNoiseGateRejectsShortNoiseBurstByDefault(t *testing.T) {
	g := newMicNoiseGate()
	burst := make([]int16, audioFrameSize)
	for i := range burst {
		burst[i] = 1800
	}

	required := requiredMicGateHotEvidenceFrames(msToAudioFrames(defaultMicGateStartMS))
	for i := 0; i < required-1; i++ {
		if out := g.process(burst); len(out) != 1 || !allZeroSamples(out[0]) {
			t.Fatalf("short noise burst frame %d should produce only digital silence before sustained gate start", i)
		}
	}
	if got := g.stats.SpeechStarts; got != 0 {
		t.Fatalf("short noise burst opened the gate %d time(s)", got)
	}
	if out := g.process(burst); len(out) != required || !sameSamples(out[0], burst) {
		t.Fatalf("sustained speech should pass with pre-roll once the default start window is satisfied, got %d frame(s)", len(out))
	}
}

func TestDefaultMicNoiseGateRejectsPostAECQuietSpeechLevel(t *testing.T) {
	g := newMicNoiseGate()
	quietSpeech := make([]int16, audioFrameSize)
	for i := range quietSpeech {
		quietSpeech[i] = 140
	}

	for i := 0; i < requiredMicGateHotEvidenceFrames(msToAudioFrames(defaultMicGateStartMS))+2; i++ {
		if out := g.process(quietSpeech); len(out) != 1 || !allZeroSamples(out[0]) {
			t.Fatalf("default gate should keep post-AEC low-level frame %d muted", i)
		}
	}
	if got := g.stats.SpeechStarts; got != 0 {
		t.Fatalf("default gate opened on post-AEC quiet speech level %d time(s)", got)
	}
}

func TestVPIOMicNoiseGateOpensOnPostAECQuietSpeechLevel(t *testing.T) {
	g := newVPIOMicNoiseGate()
	quietSpeech := make([]int16, audioFrameSize)
	for i := range quietSpeech {
		quietSpeech[i] = 140
	}

	required := requiredMicGateHotEvidenceFrames(msToAudioFrames(defaultMicGateStartMS))
	for i := 0; i < required-1; i++ {
		if out := g.process(quietSpeech); len(out) != 1 || !allZeroSamples(out[0]) {
			t.Fatalf("VPIO gate should wait for sustained speech before opening, frame %d", i)
		}
	}
	if got := g.stats.SpeechStarts; got != 0 {
		t.Fatalf("VPIO gate opened early %d time(s)", got)
	}
	if out := g.process(quietSpeech); len(out) != required || !sameSamples(out[0], quietSpeech) {
		t.Fatalf("VPIO gate should open on sustained post-AEC quiet speech, got %d frame(s)", len(out))
	}
}

func TestVPIOMicNoiseGateRaisesStartThresholdDuringAssistantPlayback(t *testing.T) {
	g := newVPIOMicNoiseGate()
	residualEcho := make([]int16, audioFrameSize)
	for i := range residualEcho {
		residualEcho[i] = 200
	}

	required := requiredMicGateHotEvidenceFrames(msToAudioFrames(defaultMicGateStartMS))
	for i := 0; i < required+2; i++ {
		if out := g.processWithStartThreshold(residualEcho, defaultMicGateThreshold); len(out) != 1 || !allZeroSamples(out[0]) {
			t.Fatalf("assistant playback residual frame %d should stay muted", i)
		}
	}
	if got := g.stats.SpeechStarts; got != 0 {
		t.Fatalf("assistant playback residual opened the gate %d time(s)", got)
	}

	userSpeech := make([]int16, audioFrameSize)
	for i := range userSpeech {
		userSpeech[i] = 1200
	}
	for i := 0; i < required-1; i++ {
		if out := g.processWithStartThreshold(userSpeech, defaultMicGateThreshold); len(out) != 1 || !allZeroSamples(out[0]) {
			t.Fatalf("talk-over frame %d should wait for sustained start evidence", i)
		}
	}
	if out := g.processWithStartThreshold(userSpeech, defaultMicGateThreshold); len(out) != required || !sameSamples(out[0], userSpeech) {
		t.Fatalf("sustained talk-over should open with pre-roll, got %d frame(s)", len(out))
	}
}

func TestMicNoiseGateStartThresholdDoesNotCutOffOpenTalkOver(t *testing.T) {
	t.Setenv("KOE_MIC_GATE_START_MS", "20")
	g := newVPIOMicNoiseGate()
	userSpeech := make([]int16, audioFrameSize)
	for i := range userSpeech {
		userSpeech[i] = 1200
	}
	if out := g.processWithStartThreshold(userSpeech, defaultMicGateThreshold); len(out) != 1 || !sameSamples(out[0], userSpeech) {
		t.Fatal("talk-over should open the gate")
	}

	quietContinuation := make([]int16, audioFrameSize)
	for i := range quietContinuation {
		quietContinuation[i] = 140
	}
	if out := g.processWithStartThreshold(quietContinuation, defaultMicGateThreshold); len(out) != 1 || !sameSamples(out[0], quietContinuation) {
		t.Fatal("higher start threshold must not cut off an already-open talk-over segment")
	}
}

func TestVPIOBargeStartGateWaitsForAudibleAssistantOutput(t *testing.T) {
	g := newVPIOBargeStartGate()
	if got := g.threshold(false, 0); got != 0 {
		t.Fatalf("idle threshold = %.4f, want 0", got)
	}
	if got := g.threshold(true, 0); !math.IsInf(got, 1) {
		t.Fatalf("pre-roll threshold = %.4f, want +Inf", got)
	}
	if got := g.threshold(true, 0.10); math.Abs(got-0.03) > 0.0001 {
		t.Fatalf("audible playback threshold = %.4f, want 0.0300", got)
	}
}

func TestVPIOBargeStartGateTracksOutputEnvelopeAndResetsBetweenResponses(t *testing.T) {
	g := newVPIOBargeStartGate()
	if got := g.threshold(true, 0.30); math.Abs(got-defaultVPIOBargeStartCeiling) > 0.0001 {
		t.Fatalf("loud playback threshold = %.4f, want capped %.4f", got, defaultVPIOBargeStartCeiling)
	}
	if got := g.threshold(true, 0); got <= defaultVPIOBargeStartThreshold {
		t.Fatalf("one silent playback frame discarded the output envelope: %.4f", got)
	}
	if got := g.threshold(false, 0); got != 0 {
		t.Fatalf("idle threshold = %.4f, want 0", got)
	}
	if got := g.threshold(true, 0); !math.IsInf(got, 1) {
		t.Fatalf("new response inherited the prior output envelope: %.4f", got)
	}
}

func TestVPIOMicNoiseGateHonorsGlobalThresholdOverride(t *testing.T) {
	t.Setenv("KOE_MIC_GATE_THRESHOLD", "0.010")
	g := newVPIOMicNoiseGate()
	quietSpeech := make([]int16, audioFrameSize)
	for i := range quietSpeech {
		quietSpeech[i] = 140
	}

	for i := 0; i < requiredMicGateHotEvidenceFrames(msToAudioFrames(defaultMicGateStartMS))+2; i++ {
		if out := g.process(quietSpeech); len(out) != 1 || !allZeroSamples(out[0]) {
			t.Fatalf("global override should keep post-AEC low-level frame %d muted", i)
		}
	}
	if got := g.stats.SpeechStarts; got != 0 {
		t.Fatalf("global override was ignored; gate opened %d time(s)", got)
	}
}

func TestMicNoiseGateTracksHotStreakForDiagnostics(t *testing.T) {
	t.Setenv("KOE_MIC_GATE_START_MS", "500")
	g := newMicNoiseGate()
	burst := make([]int16, audioFrameSize)
	for i := range burst {
		burst[i] = 1800
	}

	for i := 0; i < msToAudioFrames(100)-1; i++ {
		_ = g.process(burst)
	}
	if got, want := g.stats.HotFramesMax, msToAudioFrames(100)-1; got != want {
		t.Fatalf("HotFramesMax = %d, want %d", got, want)
	}
}

func TestMicNoiseGateOpensOnBrokenHumanSpeechCadence(t *testing.T) {
	g := newMicNoiseGate()
	speech := make([]int16, audioFrameSize)
	for i := range speech {
		speech[i] = 1800
	}
	quiet := make([]int16, audioFrameSize)

	for cycle := 0; cycle < 4 && g.stats.SpeechStarts == 0; cycle++ {
		for i := 0; i < 3 && g.stats.SpeechStarts == 0; i++ {
			_ = g.process(speech)
		}
		if g.stats.SpeechStarts == 0 {
			_ = g.process(quiet)
		}
	}
	if got := g.stats.SpeechStarts; got != 1 {
		t.Fatalf("broken but repeated speech evidence opened %d time(s), want 1; hot max=%d score max=%d",
			got, g.stats.HotFramesMax, g.stats.StartScoreMax)
	}
	if got := g.stats.HotFramesMax; got > 3 {
		t.Fatalf("test cadence unexpectedly had %d consecutive hot frames", got)
	}
}

func TestMicNoiseGateRejectsSparseEchoBurstsByDefault(t *testing.T) {
	g := newMicNoiseGate()
	echo := make([]int16, audioFrameSize)
	for i := range echo {
		echo[i] = 600
	}
	quiet := make([]int16, audioFrameSize)

	for cycle := 0; cycle < 10; cycle++ {
		for i := 0; i < 2; i++ {
			if out := g.process(echo); len(out) != 1 || !allZeroSamples(out[0]) {
				t.Fatalf("sparse echo burst cycle %d frame %d should stay muted", cycle, i)
			}
		}
		for i := 0; i < 6; i++ {
			if out := g.process(quiet); len(out) != 1 || !allZeroSamples(out[0]) {
				t.Fatalf("quiet gap cycle %d frame %d should stay muted", cycle, i)
			}
		}
	}
	if got := g.stats.SpeechStarts; got != 0 {
		t.Fatalf("sparse echo bursts opened the gate %d time(s)", got)
	}
	if got, want := g.stats.StartScoreMax, 4; got != want {
		t.Fatalf("StartScoreMax = %d, want %d", got, want)
	}
}

func TestMicNoiseGateDoesNotLearnSpeechAsNoiseFloor(t *testing.T) {
	g := newMicNoiseGate()
	softStart := make([]int16, audioFrameSize)
	for i := range softStart {
		softStart[i] = 150
	}
	speech := make([]int16, audioFrameSize)
	for i := range speech {
		speech[i] = 6000
	}

	for i := 0; i < 8; i++ {
		_ = g.process(softStart)
	}
	required := requiredMicGateHotEvidenceFrames(msToAudioFrames(defaultMicGateStartMS))
	for i := 0; i < required-1; i++ {
		if out := g.process(speech); len(out) != 1 || !allZeroSamples(out[0]) {
			t.Fatalf("speech frame %d should produce only digital silence before sustained start window", i)
		}
	}
	if out := g.process(speech); len(out) == 0 {
		t.Fatal("sustained speech after soft lead-in should open the gate")
	}
	if g.noiseFloor >= defaultMicGateThreshold {
		t.Fatalf("speech-like frames should not raise noise floor above base threshold: %.4f", g.noiseFloor)
	}
}

func TestMicNoiseGateRequiresSustainedSpeechAndHangover(t *testing.T) {
	t.Setenv("KOE_MIC_GATE_START_MS", "100")
	t.Setenv("KOE_MIC_GATE_HANGOVER_MS", "60")
	t.Setenv("KOE_MIC_GATE_ENDPOINT_MS", "60")
	g := newMicNoiseGate()
	loud := make([]int16, audioFrameSize)
	for i := range loud {
		loud[i] = 2000
	}
	quiet := make([]int16, audioFrameSize)

	for i := 0; i < 2; i++ {
		if out := g.process(loud); len(out) != 1 || !allZeroSamples(out[0]) {
			t.Fatalf("speech frame %d should produce only digital silence before sustained start", i)
		}
	}
	if out := g.process(loud); len(out) != 3 || !sameSamples(out[0], loud) || !sameSamples(out[2], loud) {
		t.Fatalf("sustained speech should open the mic gate with pre-roll, got %d frame(s)", len(out))
	}
	if out := g.process(quiet); !sameFrame(onlyFrame(out), quiet) {
		t.Fatal("hangover should preserve the first quiet tail frame")
	}
	if out := g.process(quiet); !sameFrame(onlyFrame(out), quiet) {
		t.Fatal("hangover should preserve the second quiet tail frame")
	}
	if out := g.process(quiet); len(out) != 1 || sameFrame(onlyFrame(out), quiet) {
		t.Fatal("gate should send endpoint silence after hangover expires")
	}
	for i := 0; i < msToAudioFrames(60)-1; i++ {
		if out := g.process(quiet); len(out) != 1 || sameFrame(onlyFrame(out), quiet) {
			t.Fatalf("endpoint silence frame %d missing", i)
		}
	}
	if out := g.process(quiet); len(out) != 1 || !allZeroSamples(out[0]) {
		t.Fatal("gate should keep server-VAD silence flowing after endpoint silence")
	}
	if got := g.stats.SpeechStarts; got != 1 {
		t.Fatalf("speech starts = %d, want 1", got)
	}
}

func TestMicNoiseGateResetStateDropsPendingPreroll(t *testing.T) {
	t.Setenv("KOE_MIC_GATE_START_MS", "100")
	g := newMicNoiseGate()
	loud := make([]int16, audioFrameSize)
	for i := range loud {
		loud[i] = 2000
	}

	for i := 0; i < 2; i++ {
		if out := g.process(loud); len(out) != 1 || !allZeroSamples(out[0]) {
			t.Fatalf("speech frame %d should produce only digital silence before sustained start", i)
		}
	}
	g.resetState()
	if out := g.process(loud); len(out) != 1 || !allZeroSamples(out[0]) {
		t.Fatal("reset gate should require a fresh sustained start")
	}
	if len(g.pending) != 1 {
		t.Fatalf("pending after reset + one hot frame = %d, want 1", len(g.pending))
	}
}

func requiredMicGateHotEvidenceFrames(startFrames int) int {
	return (startFrames + micGateHotEvidenceWeight - 1) / micGateHotEvidenceWeight
}

func onlyFrame(frames [][]int16) []int16 {
	if len(frames) != 1 {
		return nil
	}
	return frames[0]
}

func sameFrame(a, b []int16) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	return &a[0] == &b[0]
}

func sameSamples(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func allZeroSamples(a []int16) bool {
	if len(a) == 0 {
		return false
	}
	for _, v := range a {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestSetPreferredDevices(t *testing.T) {
	a, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	a.SetPreferredDevices("mic-uid", "spk-uid")
	if a.preferredMicUID != "mic-uid" || a.preferredSpeakerUID != "spk-uid" {
		t.Fatalf("preferred devices not stored: %q %q", a.preferredMicUID, a.preferredSpeakerUID)
	}
}
