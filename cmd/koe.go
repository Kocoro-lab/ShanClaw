//go:build darwin && cgo

package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Kocoro-lab/ShanClaw/internal/config"
	"github.com/Kocoro-lab/ShanClaw/internal/koe"
)

// koeConfig holds the resolved settings for one `shan koe` voice session.
type koeConfig struct {
	openAIKey       string // DEV-KEY: replaced by the deferred daemon mint relay (→ Plan D Cloud mint)
	daemonURL       string
	agent           string
	provider        koe.RealtimeProvider
	model           string
	voice           string
	qwenModel       string
	qwenVoice       string
	language        string
	controlPort     string // Desktop↔Koe control server port (Kocoro Desktop passes it); empty = no control channel
	controlToken    string // Desktop-owned Bearer token from KOE_CONTROL_TOKEN; never passed via argv
	aec             string // echo control: "" / "gate" = oto half-duplex fallback, "vpio" = Apple VoiceProcessingIO full-duplex AEC
	audioProcessing string // auto | mac_voice | clean_device; controls whether VPIO applies or bypasses Apple's voice processing
	micDevice       string // --mic-device: CoreAudio input device UID (empty = system default; vpio only)
	speakerDevice   string // --speaker-device: CoreAudio output device UID (empty = system default; vpio only)
	bargeIn         bool   // --barge-in: allow interruption while Kocoro speaks (vpio backend only)
	bargeInSet      bool   // whether --barge-in was explicit; false must override an inherited debug env
	// Debug harness (workstream A): headless file-backed audio so a run needs no
	// mic/ears. All empty/zero = normal mic+speaker device.
	sayText     string // --say: synthesize this text (macOS say) as the mic input
	audioIn     string // --audio-in: WAV file to feed as the mic input
	audioOut    string // --audio-out: capture the reply audio to this WAV
	audioPeriod int    // --audio-period: renderInto pull size in samples (480 reproduces the framing bug; 0=960)
	once        bool   // --once: exit shortly after the reply finishes
	timeoutSec  int    // --timeout: hard exit after N seconds (0 = none)
}

func defaultKoeConfig() koeConfig {
	return koeConfig{
		daemonURL:       "http://127.0.0.1:7533", // must match the daemon's listen addr; Desktop (Plan E) passes the real one
		provider:        koe.ProviderAuto,
		model:           koe.DefaultOpenAIRealtimeModel,
		qwenModel:       koe.DefaultQwenRealtimeModel,
		audioProcessing: audioProcessingAuto,
	}
}

// resolveDevKey picks the dev OpenAI key for the direct-mint path. An explicit
// --openai-key flag always wins. The OPENAI_API_KEY env fallback applies ONLY in
// standalone mode (no --control-port): Kocoro Desktop inherits the login-shell
// env, so on a machine with a personal key exported, the fallback would silently
// bypass the daemon mint production path — billing the personal key while the
// usage relay still debits Cloud quota.
func resolveDevKey(flagKey, envKey, controlPort string) string {
	if flagKey != "" {
		return flagKey
	}
	if controlPort == "" {
		return envKey
	}
	return ""
}

// shouldRelayRealtimeUsage keeps usage from a personal provider credential out
// of Cloud while still metering a Cloud-backed fallback session. The provider
// is stamped by the Realtime event handler, so the decision follows the actual
// connected route rather than the requested auto mode.
func shouldRelayRealtimeUsage(openAIKey string, usage json.RawMessage) bool {
	if strings.TrimSpace(openAIKey) == "" {
		return true
	}
	var envelope struct {
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(usage, &envelope); err != nil {
		// A malformed report must not accidentally debit the Cloud account for a
		// direct-key session. The normal Cloud-backed path still sends it so the
		// server can reject and diagnose the malformed payload.
		return false
	}
	return strings.EqualFold(strings.TrimSpace(envelope.Provider), string(koe.ProviderQwen))
}

func reportRealtimeUsageRelayFailure(opts koe.ConnectOptions, err error) {
	if err == nil {
		return
	}
	wrapped := fmt.Errorf("realtime usage relay admission failed: %w", err)
	log.Printf("koe: %v", wrapped)
	if opts.OnClosed != nil {
		opts.OnClosed(wrapped)
		return
	}
	if opts.OnEndCall != nil {
		opts.OnEndCall()
	}
}

// applyBargeInEnv enables the VPIO raw-audio floor loop. Playback pauses locally;
// Realtime then chooses resume_playback or accept_turn without ASR admission.
// KOE_NATIVE_FLOOR=0 plus KOE_INTERRUPT_RESPONSE=1 remains the rollback to the old
// irreversible server-cancel experiment.
func applyBargeInEnv(bargeIn, explicit bool) {
	if !bargeIn && !explicit {
		return
	}
	if !bargeIn {
		os.Setenv("KOE_VPIO_BARGE_IN", "0")
		os.Setenv("KOE_NATIVE_FLOOR", "0")
		os.Setenv("KOE_INTERRUPT_RESPONSE", "0")
		return
	}
	os.Setenv("KOE_VPIO_BARGE_IN", "1")
	os.Setenv("KOE_NATIVE_FLOOR", "1")
	os.Setenv("KOE_INTERRUPT_RESPONSE", "0")
	log.Printf("koe[barge]: --barge-in on — KOE_VPIO_BARGE_IN=%s KOE_NATIVE_FLOOR=%s KOE_INTERRUPT_RESPONSE=%s",
		os.Getenv("KOE_VPIO_BARGE_IN"), os.Getenv("KOE_NATIVE_FLOOR"), os.Getenv("KOE_INTERRUPT_RESPONSE"))
}

// bargeInBackendWarning returns a non-empty warning when barge-in is enabled on a
// backend that cannot honor it. Barge-in lives entirely on the VPIO capture path
// (shouldForwardVPIOCapture) and the fullDuplexAEC-gated native floor; the
// gate/oto fallback never reads either, so --barge-in there is a silent no-op.
func bargeInBackendWarning(bargeIn bool, aec string) string {
	if bargeIn && aec != "vpio" {
		return "barge-in has no effect on the current audio backend — it needs the VPIO backend (--aec vpio); the mic stays half-duplex while Kocoro speaks"
	}
	return ""
}

const (
	audioProcessingAuto        = "auto"
	audioProcessingMacVoice    = "mac_voice"
	audioProcessingCleanDevice = "clean_device"
)

type selfProcessedAudioDeviceRule struct {
	reason         string
	micMarkers     []string
	speakerMarkers []string
}

func selfProcessedHardwareDeviceRule(reason string, markers ...string) selfProcessedAudioDeviceRule {
	return selfProcessedAudioDeviceRule{reason: reason, micMarkers: markers, speakerMarkers: markers}
}

var selfProcessedAudioDeviceRules = []selfProcessedAudioDeviceRule{
	selfProcessedHardwareDeviceRule("reachy mini audio", "reachy mini audio", "xvf3800", "pollen robotics"),
	selfProcessedHardwareDeviceRule("anker powerconf", "powerconf", "ankerwork powerconf", "anker powerconf"),
	selfProcessedHardwareDeviceRule("jabra speak", "jabra speak"),
	selfProcessedHardwareDeviceRule("poly sync", "poly sync"),
	selfProcessedHardwareDeviceRule("yealink cp", "yealink cp"),
	selfProcessedHardwareDeviceRule("logitech rally", "logitech rally", "rally bar"),
	selfProcessedHardwareDeviceRule("logitech meetup", "logitech meetup", "meetup 2"),
	selfProcessedHardwareDeviceRule("logitech group", "logitech group"),
	selfProcessedHardwareDeviceRule("shure stem", "shure stem", "stem table", "stem wall", "stem ceiling", "stem hub"),
	selfProcessedHardwareDeviceRule("epos expand", "epos expand", "sennheiser sp 30", "sennheiser sp30"),
	selfProcessedHardwareDeviceRule("yamaha yvc", "yamaha yvc", "yvc 200", "yvc 330", "yvc 1000"),
	selfProcessedHardwareDeviceRule("konftel", "konftel"),
	{reason: "krisp", micMarkers: []string{"krisp microphone", "krisp"}, speakerMarkers: []string{"krisp speaker", "krisp"}},
}

type audioProcessingDecision struct {
	Requested string
	Resolved  string
	Bypass    bool
	Reason    string
}

func normalizeAudioProcessingMode(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		mode = audioProcessingAuto
	}
	switch mode {
	case audioProcessingAuto, audioProcessingMacVoice, audioProcessingCleanDevice:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid --audio-processing %q (want auto, mac_voice, or clean_device)", raw)
	}
}

func resolveAudioProcessingMode(raw, micDevice, speakerDevice string) (audioProcessingDecision, error) {
	mode, err := normalizeAudioProcessingMode(raw)
	if err != nil {
		return audioProcessingDecision{}, err
	}
	switch mode {
	case audioProcessingMacVoice:
		return audioProcessingDecision{Requested: mode, Resolved: mode, Bypass: false, Reason: "explicit_mac_voice"}, nil
	case audioProcessingCleanDevice:
		return audioProcessingDecision{Requested: mode, Resolved: mode, Bypass: true, Reason: "explicit_clean_device"}, nil
	}
	if marker := selfProcessedAudioDeviceMarker(micDevice, speakerDevice); marker != "" {
		return audioProcessingDecision{Requested: mode, Resolved: audioProcessingCleanDevice, Bypass: true, Reason: "known_self_processed_device:" + marker}, nil
	}
	return audioProcessingDecision{Requested: mode, Resolved: audioProcessingMacVoice, Bypass: false, Reason: "default_mac_voice"}, nil
}

func selfProcessedAudioDeviceMarker(micDevice, speakerDevice string) string {
	mic := normalizeAudioDeviceName(micDevice)
	speaker := normalizeAudioDeviceName(speakerDevice)
	for _, rule := range selfProcessedAudioDeviceRules {
		if !audioDeviceNameContainsAny(mic, rule.micMarkers) {
			continue
		}
		if len(rule.speakerMarkers) > 0 && !audioDeviceNameContainsAny(speaker, rule.speakerMarkers) {
			continue
		}
		return rule.reason
	}
	return ""
}

func audioDeviceNameContainsAny(device string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(device, normalizeAudioDeviceName(marker)) {
			return true
		}
	}
	return false
}

func normalizeAudioDeviceName(device string) string {
	device = strings.ToLower(device)
	var b strings.Builder
	lastSpace := true
	for _, r := range device {
		isWord := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isWord {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func applyAudioProcessing(audio *koe.AudioIO, cfg koeConfig, fullDuplexAEC bool) (audioProcessingDecision, error) {
	decision, err := resolveAudioProcessingMode(cfg.audioProcessing, cfg.micDevice, cfg.speakerDevice)
	if err != nil {
		return audioProcessingDecision{}, err
	}
	if !fullDuplexAEC {
		if cfg.audioProcessing != "" && cfg.audioProcessing != audioProcessingAuto {
			log.Printf("koe[audio]: audio_processing=%s ignored because aec=%q does not use VPIO", decision.Requested, cfg.aec)
		}
		return decision, nil
	}
	audio.SetVPIOVoiceProcessingBypassed(decision.Bypass)
	log.Printf("koe[audio]: audio_processing=%s resolved=%s bypass_voice_processing=%t reason=%s",
		decision.Requested, decision.Resolved, decision.Bypass, decision.Reason)
	return decision, nil
}

var koeCmd = &cobra.Command{
	Use:   "koe",
	Short: "Voice front-brain: a realtime voice agent that delegates to the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := defaultKoeConfig()
		if v, _ := cmd.Flags().GetString("openai-key"); v != "" {
			cfg.openAIKey = v
		}
		if v, _ := cmd.Flags().GetString("daemon-url"); v != "" {
			cfg.daemonURL = v
		}
		cfg.agent, _ = cmd.Flags().GetString("agent")
		providerRaw, _ := cmd.Flags().GetString("provider")
		provider, err := koe.ParseRealtimeProvider(providerRaw)
		if err != nil {
			return err
		}
		cfg.provider = provider
		if v, _ := cmd.Flags().GetString("model"); v != "" {
			cfg.model = v
		}
		cfg.voice, _ = cmd.Flags().GetString("voice")
		if v, _ := cmd.Flags().GetString("qwen-model"); v != "" {
			cfg.qwenModel = v
		}
		cfg.qwenVoice, _ = cmd.Flags().GetString("qwen-voice")
		cfg.language, _ = cmd.Flags().GetString("language")
		cfg.controlPort, _ = cmd.Flags().GetString("control-port")
		cfg.controlToken = os.Getenv("KOE_CONTROL_TOKEN")
		cfg.openAIKey = resolveDevKey(cfg.openAIKey, os.Getenv("OPENAI_API_KEY"), cfg.controlPort)
		if v, _ := cmd.Flags().GetString("aec"); v != "" {
			cfg.aec = v
		} else {
			cfg.aec = os.Getenv("KOE_AEC")
		}
		if v, _ := cmd.Flags().GetString("audio-processing"); v != "" {
			cfg.audioProcessing = v
		} else if v := os.Getenv("KOE_AUDIO_PROCESSING"); v != "" {
			cfg.audioProcessing = v
		}
		cfg.micDevice, _ = cmd.Flags().GetString("mic-device")
		cfg.speakerDevice, _ = cmd.Flags().GetString("speaker-device")
		cfg.bargeIn, _ = cmd.Flags().GetBool("barge-in")
		cfg.bargeInSet = cmd.Flags().Changed("barge-in")
		cfg.sayText, _ = cmd.Flags().GetString("say")
		cfg.audioIn, _ = cmd.Flags().GetString("audio-in")
		cfg.audioOut, _ = cmd.Flags().GetString("audio-out")
		cfg.audioPeriod, _ = cmd.Flags().GetInt("audio-period")
		cfg.once, _ = cmd.Flags().GetBool("once")
		cfg.timeoutSec, _ = cmd.Flags().GetInt("timeout")

		// No key check: with no --openai-key/OPENAI_API_KEY, runKoeCall mints via
		// the daemon (production path — Koe holds no credential). A dev key, if set,
		// takes the direct mint path instead.
		if cfg.aec != "" && cfg.aec != "gate" && cfg.aec != "vpio" {
			return fmt.Errorf("invalid --aec %q (want gate or vpio)", cfg.aec)
		}
		if err := koe.ValidateRealtimeModel(koe.ProviderOpenAI, cfg.model); err != nil {
			return err
		}
		if err := koe.ValidateRealtimeModel(koe.ProviderQwen, cfg.qwenModel); err != nil {
			return err
		}
		// Voices degrade instead of hard-failing: a legacy config value would
		// otherwise brick voice at startup, and the engine substitutes its
		// default for an empty voice. New writes are rejected at PATCH /config.
		if !config.IsValidKoeRealtimeVoice("openai", cfg.voice) {
			log.Printf("koe[provider]: --voice %q is not an advertised OpenAI Realtime voice; using the default", cfg.voice)
			cfg.voice = ""
		}
		if !config.IsValidKoeRealtimeVoice("qwen", cfg.qwenVoice) {
			log.Printf("koe[provider]: --qwen-voice %q is not an advertised Qwen Realtime voice; using the default", cfg.qwenVoice)
			cfg.qwenVoice = ""
		}
		mode, err := normalizeAudioProcessingMode(cfg.audioProcessing)
		if err != nil {
			return err
		}
		cfg.audioProcessing = mode
		return runKoeCall(cmd.Context(), cfg)
	},
}

func init() {
	koeCmd.Flags().String("openai-key", "", "OpenAI API key (dev; C-minimal only)")
	koeCmd.Flags().String("daemon-url", "", "daemon base URL (default http://127.0.0.1:7533)")
	koeCmd.Flags().String("agent", "", "bound back-brain agent slug (empty = daemon default)")
	koeCmd.Flags().String("provider", "auto", "realtime provider: auto (OpenAI then Qwen) | openai | qwen")
	koeCmd.Flags().String("model", "", "realtime model (default gpt-realtime-2.1)")
	koeCmd.Flags().String("voice", "", "realtime output voice (marin/cedar/shimmer/…; empty = marin)")
	koeCmd.Flags().String("qwen-model", "", "Qwen fallback model (default qwen3.5-omni-flash-realtime)")
	koeCmd.Flags().String("qwen-voice", "", "Qwen fallback voice (empty = Tina)")
	koeCmd.Flags().String("language", "", "conversation language hint")
	koeCmd.Flags().String("control-port", "", "Desktop↔Koe control server port (Kocoro Desktop passes it)")
	koeCmd.Flags().String("aec", "", "echo control: gate (default, oto half-duplex) | vpio (Apple VoiceProcessingIO full-duplex AEC)")
	koeCmd.Flags().String("audio-processing", "", "voice processing: auto (default) | mac_voice | clean_device")
	koeCmd.Flags().String("mic-device", "", "CoreAudio input device UID (empty = system default; vpio backend only)")
	koeCmd.Flags().String("speaker-device", "", "CoreAudio output device UID (empty = system default; vpio backend only)")
	koeCmd.Flags().Bool("barge-in", false, "allow native-S2S interruption while Kocoro speaks (reversible pause; vpio backend only)")
	koeCmd.Flags().String("say", "", "debug: synthesize this text as the mic input (macOS say) — headless file mode")
	koeCmd.Flags().String("audio-in", "", "debug: WAV file to feed as the mic input — headless file mode")
	koeCmd.Flags().String("audio-out", "", "debug: capture the reply audio to this WAV")
	koeCmd.Flags().Int("audio-period", 0, "debug: renderInto pull size in samples (480 reproduces the framing bug; 0=960)")
	koeCmd.Flags().Bool("once", false, "debug: exit shortly after the reply finishes")
	koeCmd.Flags().Int("timeout", 0, "debug: hard exit after N seconds (0=none)")
	rootCmd.AddCommand(koeCmd)
}

// The spoken persona lives in internal/koe, shared with the iOS host so both
// compose the same personality and tool discipline. koePersona is the
// desktop-variant base that buildKoePersona and the content-discipline tests
// anchor on; language pinning goes through koePersonaForLanguage below.
var koePersona = koe.SpokenPersona("", koe.HostDesktop)

// koeAgentListLine renders the specialist agents Koe can hand a task to (names
// only, no capability text) so the Realtime model can answer "which agents do I
// have?". Empty when there are no agents.
func koeAgentListLine(agents []koe.AgentSummary) string {
	if len(agents) == 0 {
		return ""
	}
	labels := make([]string, 0, len(agents))
	for _, a := range agents {
		label := a.Slug
		if a.DisplayName != "" && !strings.EqualFold(a.DisplayName, a.Slug) {
			label = a.DisplayName + " (" + a.Slug + ")"
		}
		labels = append(labels, label)
	}
	return "Specialist agents you can hand a task to (say the name to switch; otherwise the current one handles it): " +
		strings.Join(labels, ", ") + ". If the user asks which agents exist, tell them these."
}

// onceGrace is how long after the reply finishes (→ "listening") --once waits
// before exiting, so an async do_task result still lands in the debug harness.
const onceGrace = 15 * time.Second

const (
	audioStartTimeout         = 5 * time.Second
	audioStartTimeoutExitCode = 124
	warmSessionTTL            = 45 * time.Minute
)

// OpenAI/Gateway realtime client secrets carry an expires_at; Koe currently only
// consumes the value, so keep the local warm cache below the typical 10-minute
// server lifetime and retry with a fresh mint if a cached secret is rejected.
const warmMintTTL = 8 * time.Minute

func koeOpenAICircuitCooldown() time.Duration {
	const fallback = 5 * time.Minute
	raw := strings.TrimSpace(os.Getenv("KOE_OPENAI_FAILURE_COOLDOWN_MS"))
	if raw == "" {
		return fallback
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

type warmMint struct {
	mint              func(context.Context) (string, error)
	mintWithPrincipal func(context.Context) (string, string, error)
	ttl               time.Duration

	mu             sync.Mutex
	value          string
	valuePrincipal string
	mintedAt       time.Time
	inFlight       bool
}

func newWarmMint(ctx context.Context, mint func(context.Context) (string, error), ttl time.Duration) *warmMint {
	return newWarmMintWithPrincipal(ctx, mint, nil, ttl)
}

func newWarmMintWithPrincipal(
	ctx context.Context,
	mint func(context.Context) (string, error),
	mintWithPrincipal func(context.Context) (string, string, error),
	ttl time.Duration,
) *warmMint {
	w := &warmMint{mint: mint, mintWithPrincipal: mintWithPrincipal, ttl: ttl}
	w.prefetch(ctx)
	return w
}

func (w *warmMint) take(ctx context.Context) (string, bool, string, error) {
	now := time.Now()
	w.mu.Lock()
	if w.value != "" && now.Sub(w.mintedAt) < w.ttl {
		v, p := w.value, w.valuePrincipal
		w.value, w.valuePrincipal = "", ""
		w.mintedAt = time.Time{}
		w.mu.Unlock()
		w.prefetch(context.Background())
		return v, true, p, nil
	}
	w.mu.Unlock()
	var v, p string
	var err error
	if w.mintWithPrincipal != nil {
		v, p, err = w.mintWithPrincipal(ctx)
	} else {
		v, err = w.mint(ctx)
	}
	return v, false, p, err
}

func (w *warmMint) prefetch(ctx context.Context) {
	w.mu.Lock()
	if w.inFlight || (w.value != "" && time.Since(w.mintedAt) < w.ttl) {
		w.mu.Unlock()
		return
	}
	w.inFlight = true
	w.mu.Unlock()

	go func() {
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		var v, p string
		var err error
		if w.mintWithPrincipal != nil {
			v, p, err = w.mintWithPrincipal(pctx)
		} else {
			v, err = w.mint(pctx)
		}

		w.mu.Lock()
		defer w.mu.Unlock()
		w.inFlight = false
		if err != nil || v == "" {
			if err != nil {
				log.Printf("koe[timing]: warm mint failed: %v", err)
			}
			return
		}
		w.value = v
		w.valuePrincipal = p
		w.mintedAt = time.Now()
		log.Printf("koe[timing]: warm mint ready")
	}()
}

type realtimeConnector struct {
	mode                  koe.RealtimeProvider
	openAIModel           string
	openAIVoice           string
	qwenModel             string
	qwenVoice             string
	mint                  func(context.Context) (string, error)
	mintWithPrincipal     func(context.Context) (string, string, error)
	warm                  *warmMint
	exchange              func(context.Context, string) (string, error)
	exchangeWithPrincipal func(context.Context, string) (string, string, error)
	relayUsage            func(string, json.RawMessage) error
	circuit               *koe.OpenAICircuit
	// qwenHealth stops Auto mode trading a working OpenAI for a fallback the
	// backend has said it cannot serve. See koe.FallbackHealth.
	qwenHealth *koe.FallbackHealth
}

// noteQwenOutcome feeds the fallback-availability cache from a Qwen attempt.
func (c *realtimeConnector) noteQwenOutcome(err error) {
	if err == nil {
		c.qwenHealth.RecordAvailable()
		return
	}
	if koe.IsProviderNotConfigured(err) {
		c.qwenHealth.RecordUnavailable()
		log.Printf("koe[provider]: Qwen is not configured on this backend — Auto will stop treating it as a fallback")
	}
}

// openAICircuitWorthOpening reports whether recording an OpenAI failure can
// actually help. It cannot when the only fallback is known unusable: opening
// the circuit would pin every later call onto a provider that will refuse, so
// the honest move is to keep trying OpenAI and report its real error.
func (c *realtimeConnector) openAICircuitWorthOpening() bool {
	return !c.qwenHealth.Unavailable()
}

// autoShouldSkipOpenAI reports whether an Auto call should bypass OpenAI on
// this attempt and go straight to the fallback.
//
// An open circuit is NOT sufficient on its own, because the two caches are
// learned in the wrong order. The circuit opens on ONE eligible OpenAI failure
// and stays open for its cooldown (5 min); the fallback's own health is only
// learned from the fallback attempt that failure triggers. So the ordinary
// sequence is: OpenAI blips -> circuit opens (openAICircuitWorthOpening was
// true, nothing was known about Qwen yet) -> Qwen answers 503
// provider_not_configured -> qwenHealth caches it unavailable for its TTL.
// Consuming the circuit without re-reading that cache pinned every later call
// in the cooldown onto a provider guaranteed to refuse, never retried a
// possibly-recovered OpenAI, and reported Qwen's error to the user — hiding
// what actually failed. That is the same failure mode
// openAICircuitWorthOpening() prevents, entered from the other side.
func (c *realtimeConnector) autoShouldSkipOpenAI() bool {
	return c.mode == koe.ProviderAuto && c.circuit.Skip() && !c.qwenHealth.Unavailable()
}

func (c *realtimeConnector) connect(ctx context.Context, audio *koe.AudioIO, persona string, state *koe.CallState, disp *koe.Dispatcher, opts koe.ConnectOptions) (*koe.RealtimeConn, koe.RealtimeProvider, error) {
	connectQwen := func() (*koe.RealtimeConn, error) {
		qwenOpts := opts
		qwenOpts.Model = c.qwenModel
		qwenOpts.Voice = c.qwenVoice
		var principalMu sync.RWMutex
		qwenPrincipal := ""
		qwenExchange := func(sctx context.Context, offer string) (string, error) {
			var answer, principal string
			var err error
			if c.exchangeWithPrincipal != nil {
				answer, principal, err = c.exchangeWithPrincipal(sctx, offer)
			} else {
				answer, err = c.exchange(sctx, offer)
			}
			if err == nil {
				principalMu.Lock()
				qwenPrincipal = strings.TrimSpace(principal)
				principalMu.Unlock()
			}
			return answer, err
		}
		if c.relayUsage != nil {
			var usageFailureOnce sync.Once
			reportUsageFailure := func(err error) {
				usageFailureOnce.Do(func() {
					go reportRealtimeUsageRelayFailure(opts, err)
				})
			}
			original := qwenOpts.OnUsage
			qwenOpts.OnUsage = func(raw json.RawMessage) {
				principalMu.RLock()
				principal := qwenPrincipal
				principalMu.RUnlock()
				if err := c.relayUsage(principal, raw); err != nil {
					reportUsageFailure(err)
				}
				if original != nil {
					original(raw)
				}
			}
			return koe.ConnectQwen(ctx, audio, qwenExchange, persona, state, disp, qwenOpts)
		}
		return koe.ConnectQwen(ctx, audio, c.exchange, persona, state, disp, qwenOpts)
	}
	if c.mode == koe.ProviderQwen {
		conn, err := connectQwen()
		c.noteQwenOutcome(err)
		return conn, koe.ProviderQwen, err
	}
	if c.autoShouldSkipOpenAI() {
		log.Printf("koe[provider]: auto skipped OpenAI during availability cooldown; connecting Qwen")
		conn, err := connectQwen()
		c.noteQwenOutcome(err)
		return conn, koe.ProviderQwen, err
	}

	openAIOpts := opts
	openAIOpts.Model = c.openAIModel
	openAIOpts.Voice = c.openAIVoice
	// Bound the synchronous mint like the pre-provider warm path did (15s). The
	// session ctx is long-lived; without this the only bound is the daemon
	// client's 30s HTTP timeout, doubling worst-case time-to-fallback.
	mctx, mcancel := context.WithTimeout(ctx, 15*time.Second)
	secret, cached, principal, err := c.takeOpenAISecret(mctx)
	mcancel()
	if err != nil {
		err = koe.OpenAIMintError(err)
		if c.mode == koe.ProviderAuto && koe.AutoFallbackEligible(err) && c.openAICircuitWorthOpening() {
			c.circuit.RecordFailure(err)
			log.Printf("koe[provider]: OpenAI mint unavailable; falling back to Qwen: %v", err)
			conn, qerr := connectQwen()
			c.noteQwenOutcome(qerr)
			return conn, koe.ProviderQwen, qerr
		}
		return nil, koe.ProviderOpenAI, err
	}
	var reportUsageFailure func(error)
	if c.relayUsage != nil {
		var usageFailureOnce sync.Once
		reportUsageFailure = func(err error) {
			usageFailureOnce.Do(func() {
				go reportRealtimeUsageRelayFailure(opts, err)
			})
		}
		original := openAIOpts.OnUsage
		openAIOpts.OnUsage = func(raw json.RawMessage) {
			if err := c.relayUsage(principal, raw); err != nil {
				reportUsageFailure(err)
			}
			if original != nil {
				original(raw)
			}
		}
	}
	conn, err := koe.Connect(ctx, audio, secret, persona, state, disp, openAIOpts)
	if err != nil && cached && (c.mode == koe.ProviderOpenAI || !koe.AutoFallbackEligible(err)) {
		log.Printf("koe[provider]: cached OpenAI secret rejected; retrying once with a fresh mint: %v", err)
		fctx, fcancel := context.WithTimeout(ctx, 15*time.Second)
		fresh, freshPrincipal, mintErr := c.mintOpenAISecret(fctx)
		fcancel()
		if mintErr != nil {
			err = koe.OpenAIMintError(mintErr)
		} else {
			if c.relayUsage != nil {
				original := opts.OnUsage
				openAIOpts.OnUsage = func(raw json.RawMessage) {
					if err := c.relayUsage(freshPrincipal, raw); err != nil {
						if reportUsageFailure != nil {
							reportUsageFailure(err)
						}
					}
					if original != nil {
						original(raw)
					}
				}
			}
			conn, err = koe.Connect(ctx, audio, fresh, persona, state, disp, openAIOpts)
		}
	}
	if err == nil {
		c.circuit.RecordSuccess()
		return conn, koe.ProviderOpenAI, nil
	}
	if c.mode != koe.ProviderAuto || !koe.AutoFallbackEligible(err) || !c.openAICircuitWorthOpening() {
		return nil, koe.ProviderOpenAI, err
	}
	c.circuit.RecordFailure(err)
	log.Printf("koe[provider]: OpenAI connection unavailable before media; falling back to Qwen: %v", err)
	conn, qerr := connectQwen()
	c.noteQwenOutcome(qerr)
	return conn, koe.ProviderQwen, qerr
}

func (c *realtimeConnector) takeOpenAISecret(ctx context.Context) (string, bool, string, error) {
	if c.warm != nil {
		return c.warm.take(ctx)
	}
	secret, principal, err := c.mintOpenAISecret(ctx)
	return secret, false, principal, err
}

func (c *realtimeConnector) mintOpenAISecret(ctx context.Context) (string, string, error) {
	if c.mintWithPrincipal != nil {
		return c.mintWithPrincipal(ctx)
	}
	secret, err := c.mint(ctx)
	return secret, "", err
}

func newBurstID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "burst-" + hex.EncodeToString(b[:])
}

func koeAudioStartTimeout() time.Duration {
	raw := os.Getenv("KOE_AUDIO_START_TIMEOUT_MS")
	if raw == "" {
		return audioStartTimeout
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return audioStartTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

func koeWarmSessionTTL() time.Duration {
	raw := os.Getenv("KOE_WARM_SESSION_TTL_MS")
	if raw == "" {
		return warmSessionTTL
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return warmSessionTTL
	}
	return time.Duration(ms) * time.Millisecond
}

func armAudioStartWatchdog(label string, timeout time.Duration) func() {
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			log.Printf("koe: %s audio start timed out after %s; exiting for supervisor restart", label, timeout)
			os.Exit(audioStartTimeoutExitCode)
		}
	}()
	return func() { close(done) }
}

func onceVoiceStateHandler(cancel func(), grace time.Duration) func(string) {
	var graceMu sync.Mutex
	var graceTimer *time.Timer
	sawAssistantOutput := false
	return func(s string) {
		graceMu.Lock()
		defer graceMu.Unlock()
		if graceTimer != nil {
			graceTimer.Stop()
			graceTimer = nil
		}
		switch s {
		case "speaking", "thinking":
			sawAssistantOutput = true
		case "listening":
			if sawAssistantOutput {
				graceTimer = time.AfterFunc(grace, cancel)
			}
		}
	}
}

func desktopParentDone(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	parentPID := os.Getppid()
	if parentPID <= 1 {
		close(done)
		return done
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ppid := os.Getppid(); ppid != parentPID || ppid <= 1 {
					log.Printf("koe: desktop parent exited; shutting down parent_pid=%d current_ppid=%d", parentPID, ppid)
					close(done)
					return
				}
			}
		}
	}()
	return done
}

func koePersonaForLanguage(lang string) string {
	return koe.SpokenPersona(lang, koe.HostDesktop)
}

// buildKoePersona assembles the full spoken persona: the base persona + the daemon's
// small-tier-distilled user context (who the user is / how to address them —
// best-effort, bounded to 3s) + the agent-registry list (so the Realtime model can
// answer "which agents do I have?"; names only, matching stays the resolver's job) +
// the pinned reply language. The standalone path calls this synchronously; the
// desktop path calls it AFTER its control listener binds and hot-swaps the result in.
func buildKoePersona(ctx context.Context, client *koe.DaemonClient, cfg koeConfig, agents []koe.AgentSummary) string {
	basePersona := koePersonaForLanguage(cfg.language)
	persona := basePersona
	pctx, pcancel := context.WithTimeout(ctx, 3*time.Second)
	if extra, perr := client.FetchPersona(pctx); perr == nil && extra != "" {
		persona = basePersona + " " + extra
	}
	pcancel()
	if list := koeAgentListLine(agents); list != "" {
		persona += "\n\n" + list
	}
	persona = koe.AppendTaskLedgerPersona(persona)
	return persona
}

// baseKoePersona is what a desktop warm session uses before the async registry +
// persona fetch lands (or if a /call/start races that window): the base persona +
// the pinned language, without the daemon-distilled user context or agent list yet.
func baseKoePersona(cfg koeConfig) string {
	persona := koePersonaForLanguage(cfg.language)
	persona = koe.AppendTaskLedgerPersona(persona)
	return persona
}

const activeReconnectContextBoundary = "CONNECTION RECOVERY: This Realtime session does not contain the earlier voice conversation. Do not pretend to remember wording from before the reconnect. If the user's next request depends on that missing wording, briefly ask the user to restate it instead of guessing. Newly injected task-result data is authoritative and must still be delivered normally."

func desktopSessionPersona(persona, reason string) string {
	if reason != "active_session_reconnect" {
		return persona
	}
	return persona + "\n\n" + activeReconnectContextBoundary
}

func runKoeCall(ctx context.Context, cfg koeConfig) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// --barge-in selects native floor control before any audio/session code reads
	// the env gates (covers both Desktop and standalone branches below).
	applyBargeInEnv(cfg.bargeIn, cfg.bargeInSet)
	if w := bargeInBackendWarning(cfg.bargeIn, cfg.aec); w != "" {
		log.Printf("koe[barge]: WARNING — %s", w)
	}

	// Plan B wiring: link to the daemon back-brain.
	client := koe.NewDaemonClient(cfg.daemonURL)
	// The robot facade or reverse proxy validates this optional bearer. Plain HTTP
	// is limited to the trusted private LAN used by Wireless carriers.
	client.SetToken(os.Getenv("KOE_DAEMON_TOKEN"))

	// G3: hand each turn's token usage to the daemon without blocking the
	// Realtime event callback. The relay durably spools accepted reports before
	// fixed workers drain them; Cloud delivery happens in the daemon worker.
	usageRelay := newRealtimeUsageRelay(client, cfg.openAIKey)
	defer usageRelay.Close()
	onUsage := usageRelay.Enqueue
	// mintEK mints a fresh ephemeral secret (ephemeral keys are short-lived, so this
	// runs per call): a dev key (--openai-key/OPENAI_API_KEY) takes the direct mint,
	// else the via-daemon relay (Koe holds no long-lived credential).
	mintEK := func(mctx context.Context) (string, error) {
		if strings.TrimSpace(cfg.openAIKey) != "" {
			log.Printf("koe: using dev OpenAI key (direct mint, bypassing daemon relay)")
			return koe.MintEphemeral(mctx, cfg.openAIKey, cfg.model)
		}
		return client.MintViaDaemon(mctx, cfg.model)
	}
	var mintWithPrincipal func(context.Context) (string, string, error)
	if strings.TrimSpace(cfg.openAIKey) == "" {
		mintWithPrincipal = func(mctx context.Context) (string, string, error) {
			return client.MintViaDaemonWithPrincipal(mctx, cfg.model)
		}
	}
	connector := &realtimeConnector{
		mode:              cfg.provider,
		openAIModel:       cfg.model,
		openAIVoice:       cfg.voice,
		qwenModel:         cfg.qwenModel,
		qwenVoice:         cfg.qwenVoice,
		mint:              mintEK,
		mintWithPrincipal: mintWithPrincipal,
		relayUsage:        onUsage,
		exchange: func(sctx context.Context, offer string) (string, error) {
			return client.ExchangeSDPViaDaemon(sctx, string(koe.ProviderQwen), cfg.qwenModel, offer)
		},
		exchangeWithPrincipal: func(sctx context.Context, offer string) (string, string, error) {
			return client.ExchangeSDPViaDaemonWithPrincipal(sctx, string(koe.ProviderQwen), cfg.qwenModel, offer)
		},
		circuit:    koe.NewOpenAICircuit(koeOpenAICircuitCooldown()),
		qwenHealth: koe.NewFallbackHealth(0),
	}

	// ── Kocoro Desktop (control-port) mode: warm session + call-scoped audio ──
	// Koe pre-warms the next OpenAI session while idle, but it opens the local audio
	// device only for an active Desktop call. The agent-registry + persona fetches
	// are done INSIDE runDesktopCall, AFTER its control listener binds, so Desktop can
	// reach koe during a slow-daemon window (those fetches used to run here first,
	// blocking for up to ~33s while koe's control port stayed unbound).
	if cfg.controlPort != "" {
		return runDesktopCall(ctx, cfg, client, connector, onUsage)
	}

	// ── Standalone / headless mode: always-on (CLI + E2E + --say/--audio-in) ──
	// No control port to keep reachable, so fetch the registry + persona synchronously.
	agents, err := client.ListAgents(ctx)
	if err != nil {
		log.Printf("koe: list agents failed (continuing with empty registry): %v", err)
	}
	resolver := koe.NewAgentResolver(agents, koe.NoopSemanticMatcher{})
	persona := buildKoePersona(ctx, client, cfg, agents)
	state := koe.NewCallState(newBurstID(), cfg.agent)
	disp := koe.NewDispatcher(client, resolver, state, nil)
	resultMailbox := koe.NewResultMailbox()
	resultMailbox.BeginBurst(state.BurstID())
	audio, err := koe.NewAudioIO()
	if err != nil {
		return fmt.Errorf("audio init: %v", err)
	}
	audio.SetPreferredDevices(cfg.micDevice, cfg.speakerDevice)
	startAudio := audio.Start
	fullDuplexAEC := cfg.aec == "vpio"
	// Headless debug mode (workstream A): --say/--audio-in replace the mic+speaker
	// with a file backend (feed a WAV, capture the reply to --audio-out) so the
	// whole path runs without a mic or ears.
	fileMode := cfg.sayText != "" || cfg.audioIn != ""
	if fileMode {
		inPCM, lerr := koe.LoadInputPCM(cfg.sayText, cfg.audioIn)
		if lerr != nil {
			return fmt.Errorf("audio input: %v", lerr)
		}
		log.Printf("koe[debug]: file mode — %d input samples (%.2fs) out=%q period=%d",
			len(inPCM), float64(len(inPCM))/48000, cfg.audioOut, cfg.audioPeriod)
		startAudio = func() error { return audio.StartFile(inPCM, cfg.audioOut, cfg.audioPeriod) }
		fullDuplexAEC = false
	}
	if _, err := applyAudioProcessing(audio, cfg, fullDuplexAEC); err != nil {
		return err
	}
	if !fileMode && fullDuplexAEC {
		startAudio = audio.StartVPIO
	}
	disarmAudioWatchdog := func() {}
	if fullDuplexAEC && !fileMode {
		disarmAudioWatchdog = armAudioStartWatchdog("standalone vpio", koeAudioStartTimeout())
	}
	if err := startAudio(); err != nil {
		disarmAudioWatchdog()
		return fmt.Errorf("audio start: %v", err)
	}
	disarmAudioWatchdog()
	defer audio.Stop()

	// Debug harness: --once exits a short grace after the reply finishes (→
	// "listening"), pausing the timer while thinking/speaking so do_task latency
	// doesn't trip it; --timeout is a hard fallback.
	var onVoiceState func(string)
	if cfg.once {
		onVoiceState = onceVoiceStateHandler(cancel, onceGrace)
	}
	if cfg.timeoutSec > 0 {
		time.AfterFunc(time.Duration(cfg.timeoutSec)*time.Second, cancel)
	}

	var dismissOnce sync.Once
	conn, provider, err := connector.connect(ctx, audio, persona, state, disp, koe.ConnectOptions{
		OnVoiceState:  onVoiceState,
		Model:         cfg.model,
		Voice:         cfg.voice,
		OnUsage:       nil,
		ResultMailbox: resultMailbox,
		// Standalone/CLI dismiss (end_call tool or a dismiss phrase) = play the goodbye
		// cue, then exit the process (there is no warm-session teardown to return to).
		// sync.Once makes it idempotent: the tool and the deterministic phrase can both
		// fire for one utterance, and only the first should earcon + exit (Desktop's
		// endCall gets this from its callActive guard).
		OnEndCall: func() {
			dismissOnce.Do(func() {
				if koe.DismissEarconEnabled() {
					audio.PlayDismissEarcon()
				}
				cancel()
			})
		},
		Language:      cfg.language,
		FullDuplexAEC: fullDuplexAEC,
	})
	if err != nil {
		return fmt.Errorf("connect: %v", err)
	}
	log.Printf("koe[provider]: connected provider=%s model=%s", provider, map[koe.RealtimeProvider]string{koe.ProviderOpenAI: cfg.model, koe.ProviderQwen: cfg.qwenModel}[provider])
	defer conn.Close()

	if !fileMode {
		fmt.Println("Kocoro is listening. Speak; Ctrl-C to end.")
	}
	<-ctx.Done()
	if fileMode {
		m := audio.CapturedMetrics()
		log.Printf("koe[debug]: captured %d samples (%.2fs) rms=%.4f peak=%.4f disc=%.4f silence=%.2f clip=%.4f",
			m.Samples, float64(m.Samples)/48000, m.RMS, m.Peak, m.DiscontinuityRatio, m.SilenceRatio, m.ClippingRatio)
	}
	return nil
}

func closeDesktopSessionState(mailbox *koe.ResultMailbox, state *koe.CallState, retireCall bool) *koe.CallState {
	if state == nil || !retireCall {
		return state
	}
	mailbox.RetireBurst(state.BurstID())
	return nil
}

// runDesktopCall is the resident control-port loop. Desktop keeps a warm Realtime
// session ready while idle, but the audio device is call-scoped: /call/start opens
// the selected backend, /call/end closes the used session and audio, then warms
// the next session without touching the mic.
func runDesktopCall(ctx context.Context, cfg koeConfig, client *koe.DaemonClient,
	connector *realtimeConnector, onUsage func(string, json.RawMessage) error) error {

	fullDuplexAEC := cfg.aec == "vpio"
	// One mailbox spans every warm Realtime session in this resident process. A
	// do_task can outlive the session that dispatched it; its spoken result must not.
	resultMailbox := koe.NewResultMailbox()

	// The agent registry + persona are fetched AFTER the control listener binds (see
	// below), so they start as an empty registry + base persona and are hot-swapped
	// in once the fetch lands. Each warm session reads the CURRENT holder value at
	// creation time (newSessionState / connectWith run per session), so a swap applies
	// to the next warm session; the atomics let the fetch goroutine publish without
	// taking sessMu (which a /call/start also needs).
	var resolverHolder atomic.Pointer[koe.AgentResolver]
	resolverHolder.Store(koe.NewAgentResolver(nil, koe.NoopSemanticMatcher{}))
	base := baseKoePersona(cfg)
	var personaHolder atomic.Pointer[string]
	personaHolder.Store(&base)

	var ctrl *koe.ControlServer
	// endCall is forward-declared so the connect closure can pass it as ConnectOptions.
	// OnEndCall (the end_call voice tool hook) — it is assigned further down, next to
	// startCall/interruptCall, and read only at call time.
	var endCall func()
	var callContext koe.StartCallRequest
	newSessionState := func(existing *koe.CallState) (*koe.CallState, *koe.Dispatcher) {
		state := existing
		if state == nil {
			state = koe.NewCallState(newBurstID(), cfg.agent)
			resultMailbox.BeginBurst(state.BurstID())
		}
		state.SetCallContext(callContext)
		disp := koe.NewDispatcher(client, resolverHolder.Load(), state, func(_ context.Context, action string) error {
			if ctrl == nil {
				return nil
			}
			ctrl.EmitControlApp(action)
			return nil
		})
		return state, disp
	}
	if cfg.provider != koe.ProviderQwen {
		connector.warm = newWarmMintWithPrincipal(ctx, connector.mint, connector.mintWithPrincipal, warmMintTTL)
	}

	// The RealtimeConn is warmed while idle, then consumed by one foreground call.
	// callActive gates mic frames inside pumpSendTrack; inactive sessions drain and
	// discard local capture, so OpenAI never hears the room before the double-tap.
	var sessMu sync.Mutex
	var curConn *koe.RealtimeConn
	var curState *koe.CallState
	var curAudio *koe.AudioIO
	var curAudioStarted bool
	// Snapshot pointers for the control server's voice_state stamping. The
	// providers run inside ControlServer.broadcast — sometimes under sessMu
	// (emitReadyLocked) — so they must NOT take sessMu themselves. CallState /
	// AudioIO have their own locks, safe to touch here.
	var snapState atomic.Pointer[koe.CallState]
	var snapAudio atomic.Pointer[koe.AudioIO]
	var sessionCancel context.CancelFunc
	var sessionSeq uint64
	var warming bool
	var sessionReady bool
	var callActive bool
	var callStarted time.Time
	var readyEmitted bool
	idleSessionTTL := koeWarmSessionTTL()

	stopSessionResources := func(conn *koe.RealtimeConn, cancel context.CancelFunc, audio *koe.AudioIO) {
		if cancel != nil {
			cancel()
		}
		if conn != nil {
			conn.Close()
		}
		if audio != nil {
			audio.Stop()
		}
	}
	startSessionAudioLocked := func(reason string) error {
		if curAudioStarted {
			return nil
		}
		if curAudio == nil {
			return fmt.Errorf("audio session unavailable")
		}
		curAudio.PrepareForCall()
		startAudio := curAudio.Start
		if fullDuplexAEC {
			startAudio = curAudio.StartVPIO
		}
		started := time.Now()
		disarmAudioWatchdog := armAudioStartWatchdog("desktop call audio", koeAudioStartTimeout())
		if err := startAudio(); err != nil {
			disarmAudioWatchdog()
			return err
		}
		curAudioStarted = true
		curAudio.SetPlaybackEnabled(false)
		disarmAudioWatchdog()
		log.Printf("koe[timing]: desktop call audio ready in %dms aec=%s reason=%s", time.Since(started).Milliseconds(), cfg.aec, reason)
		return nil
	}
	emitReadyLocked := func() {
		if !callActive || !sessionReady || !curAudioStarted || readyEmitted {
			return
		}
		readyEmitted = true
		if !callStarted.IsZero() {
			log.Printf("koe[timing]: call ready in %dms warm_session=true", time.Since(callStarted).Milliseconds())
		}
		ctrl.EmitCallState("on_call")
		ctrl.EmitVoiceState("listening")
		// Sound the "ready" earcon once per call (async — the speaking gate mutes
		// the mic for its duration, so it can't self-trigger the server VAD; see
		// koe.PlayReadyEarcon). readyEmitted above already guarantees single-fire.
		if curAudio != nil && koe.ReadyEarconEnabled() {
			audio := curAudio
			go audio.PlayReadyEarcon()
		}
	}
	closeSessionLocked := func(retireCall bool) (*koe.RealtimeConn, context.CancelFunc, *koe.AudioIO) {
		sessionSeq++
		conn, cancel := curConn, sessionCancel
		audio := curAudio
		curState = closeDesktopSessionState(resultMailbox, curState, retireCall)
		curConn, curAudio, sessionCancel = nil, nil, nil
		if retireCall {
			snapState.Store(nil)
			callContext = koe.StartCallRequest{}
			callStarted = time.Time{}
		}
		snapAudio.Store(nil)
		curAudioStarted = false
		warming = false
		sessionReady = false
		readyEmitted = false
		return conn, cancel, audio
	}
	var ensureWarmSessionLocked func(string)
	var scheduleWarmRotationLocked func(uint64, string)
	// Idle pre-warm retry state. All three are guarded by sessMu.
	var scheduleWarmRetryLocked func(reason, failureCode string)
	var warmRetry koe.WarmRetryPolicy
	// warmRetryLane retires superseded retry timers so exactly one is ever live;
	// its generation counter is guarded by sessMu (see koe.WarmRetryLane).
	var warmRetryLane koe.WarmRetryLane
	var handleSessionClosed func(uint64, error)
	// scheduleWarmRetryLocked arms the ONE pending idle-prewarm retry.
	//
	// Two things it fixes, both measured 2026-08-28 against a backend that had
	// been failing for three days (29,897 connect failures, ~8.8 MB of log/day):
	//
	//  1. SINGLE LANE. Every failure used to spawn its own 5s timer goroutine and
	//     they accumulated; koe.WarmRetryLane owns that fix and carries the
	//     measurements.
	//
	//  2. POLICY, not a constant. WarmRetryPolicy decides the delay (backoff +
	//     jitter) and whether to retry at all — a shared-prerequisite failure
	//     pauses idle pre-warming instead of asking again every 5 seconds.
	//
	// Must be called with sessMu held: it mutates the lane's generation counter.
	scheduleWarmRetryLocked = func(reason, failureCode string) {
		delay, retry := warmRetry.OnFailure(failureCode)
		if !retry {
			_, why := warmRetry.Paused()
			log.Printf("koe[warm]: idle pre-warm paused after %s (%s) — pressing the call button still connects",
				reason, why)
			warmRetryLane.Retire() // nothing is armed while paused
			return
		}
		delay = koe.JitterWarmRetryDelay(delay)
		if warmRetry.Streak() > 1 {
			log.Printf("koe[warm]: retry #%d in %s (%s)", warmRetry.Streak(), delay.Round(time.Millisecond), reason)
		}
		warmRetryLane.Arm(ctx, delay, func(gen uint64) {
			sessMu.Lock()
			defer sessMu.Unlock()
			// Superseded by a newer arm (or by a user-initiated call, which
			// retires the lane and attempts immediately).
			if !warmRetryLane.IsCurrent(gen) {
				return
			}
			if curConn == nil && !warming {
				ensureWarmSessionLocked(reason)
			}
		})
	}
	scheduleWarmRotationLocked = func(seq uint64, reason string) {
		if idleSessionTTL <= 0 {
			return
		}
		go func() {
			select {
			case <-time.After(idleSessionTTL):
			case <-ctx.Done():
				return
			}
			sessMu.Lock()
			if seq != sessionSeq || callActive || curConn == nil {
				sessMu.Unlock()
				return
			}
			log.Printf("koe[timing]: refreshing idle warm session after %s reason=%s", idleSessionTTL, reason)
			conn, cancel, audio := closeSessionLocked(true)
			ensureWarmSessionLocked("ttl_refresh")
			sessMu.Unlock()
			stopSessionResources(conn, cancel, audio)
		}()
	}
	handleSessionClosed = func(seq uint64, err error) {
		sessMu.Lock()
		if seq != sessionSeq {
			sessMu.Unlock()
			return
		}
		log.Printf("koe: warm session closed: %v", err)
		if !callActive {
			conn, cancel, audio := closeSessionLocked(true)
			ensureWarmSessionLocked("session_closed")
			sessMu.Unlock()
			stopSessionResources(conn, cancel, audio)
			return
		}

		// A provider session can expire while the logical Desktop call is still
		// active. Preserve the CallState and ResultMailbox burst so queued task results
		// survive, but give the replacement session an explicit context boundary: the
		// provider conversation itself cannot be transferred across WebRTC sessions.
		ctrl.EmitCallState("connecting")
		callStarted = time.Now()
		conn, cancel, audio := closeSessionLocked(false)
		sessMu.Unlock()
		stopSessionResources(conn, cancel, audio)

		sessMu.Lock()
		if !callActive {
			sessMu.Unlock()
			return
		}
		ensureWarmSessionLocked("active_session_reconnect")
		if err := startSessionAudioLocked("active_session_reconnect"); err != nil {
			log.Printf("koe: audio restart failed: %v", err)
			callActive = false
			ctrl.EmitVoiceState("idle")
			ctrl.EmitCallFailed(koe.CallFailureAudio)
			conn, cancel, audio := closeSessionLocked(true)
			ensureWarmSessionLocked("audio_restart_retry")
			sessMu.Unlock()
			stopSessionResources(conn, cancel, audio)
			return
		}
		emitReadyLocked()
		resultMailbox.Wake()
		sessMu.Unlock()
	}
	// A failed bringup used to reach Desktop as a bare `ended` — the same event
	// a normal hang-up sends — with the cause going only to this log. The user
	// saw the call screen open and shut instantly and could not find out why;
	// two days of "realtime usage principal unavailable" read as "it hangs up
	// the second it connects". The reason now rides the event.
	failActiveCallLocked := func(msg string, err error, reason string) {
		log.Printf("koe: %s: %v", msg, err)
		if reason == "" {
			reason = koe.ClassifyCallFailure(err)
		}
		if callActive {
			callActive = false
			callStarted = time.Time{}
			readyEmitted = false
			ctrl.EmitVoiceState("idle")
			ctrl.EmitCallFailed(reason)
		}
	}
	ensureWarmSessionLocked = func(reason string) {
		if ctx.Err() != nil {
			return
		}
		if curConn != nil || warming {
			return
		}
		audio, aerr := koe.NewAudioIO()
		if aerr != nil {
			failActiveCallLocked("audio init failed", aerr, koe.CallFailureAudio)
			scheduleWarmRetryLocked("audio_init_retry", koe.CallFailureAudio)
			return
		}
		audio.SetPreferredDevices(cfg.micDevice, cfg.speakerDevice)
		if _, err := applyAudioProcessing(audio, cfg, fullDuplexAEC); err != nil {
			failActiveCallLocked("audio processing config failed", err, koe.CallFailureAudio)
			scheduleWarmRetryLocked("audio_processing_retry", koe.CallFailureAudio)
			return
		}
		audio.SetPlaybackEnabled(false)
		started := time.Now()
		warming = true
		sessionReady = false
		readyEmitted = false
		sessionSeq++
		seq := sessionSeq
		var existingState *koe.CallState
		if callActive {
			existingState = curState
		}
		state, disp := newSessionState(existingState)
		curState = state
		curAudio = audio
		snapState.Store(state)
		snapAudio.Store(audio)
		curAudioStarted = false
		sessionCtx, cancel := context.WithCancel(ctx)
		sessionCancel = cancel
		log.Printf("koe[timing]: warming realtime session reason=%s burst=%s", reason, state.BurstID())

		go func() {
			isActive := func() bool {
				sessMu.Lock()
				defer sessMu.Unlock()
				return seq == sessionSeq && callActive
			}
			onVoiceState := func(s string) {
				if isActive() {
					ctrl.EmitVoiceState(s)
				}
			}
			onVoiceLevel := func(s string, level float64) {
				if isActive() {
					ctrl.EmitVoiceLevel(s, level)
				}
			}
			onCallState := func(s string) {
				switch s {
				case "connecting":
					return
				case "on_call":
					sessMu.Lock()
					if seq == sessionSeq {
						sessionReady = true
						// The prerequisite that was missing is evidently back:
						// clear the backoff streak and any idle-prewarm pause.
						warmRetry.OnSuccess()
						log.Printf("koe[timing]: warm session ready in %dms reason=%s", time.Since(started).Milliseconds(), reason)
						emitReadyLocked()
						scheduleWarmRotationLocked(seq, reason)
					}
					sessMu.Unlock()
				default:
					if isActive() {
						ctrl.EmitCallState(s)
					}
				}
			}
			callActiveFn := func() bool {
				sessMu.Lock()
				defer sessMu.Unlock()
				return seq == sessionSeq && callActive
			}

			connectOpts := koe.ConnectOptions{
				OnVoiceState:  onVoiceState,
				OnCallState:   onCallState,
				OnVoiceLevel:  onVoiceLevel,
				CallActive:    callActiveFn,
				ResultMailbox: resultMailbox,
				OnUsage:       nil,
				OnEndCall: func() {
					if endCall != nil {
						endCall()
					}
				}, // end_call voice tool → hang up + goodbye earcon; endCall is forward-declared (assigned below)
				Language:      cfg.language,
				FullDuplexAEC: fullDuplexAEC,
				OnClosed:      func(err error) { handleSessionClosed(seq, err) },
			}
			persona := desktopSessionPersona(*personaHolder.Load(), reason)
			conn, provider, cerr := connector.connect(sessionCtx, audio, persona, state, disp, connectOpts)
			if cerr == nil {
				log.Printf("koe[provider]: warm session connected provider=%s in %dms reason=%s", provider, time.Since(started).Milliseconds(), reason)
			}
			sessMu.Lock()
			if seq != sessionSeq {
				sessMu.Unlock()
				if conn != nil {
					conn.Close()
				}
				cancel()
				return
			}
			if cerr != nil {
				failureCode := koe.ClassifyCallFailure(cerr)
				failActiveCallLocked("connect failed", cerr, failureCode)
				conn, scancel, audio := closeSessionLocked(true)
				scheduleWarmRetryLocked("connect_retry", failureCode)
				sessMu.Unlock()
				stopSessionResources(conn, scancel, audio)
				return
			}
			curConn = conn
			warming = false
			emitReadyLocked()
			sessMu.Unlock()
		}()
	}

	startCall := func(req koe.StartCallRequest) {
		sessMu.Lock()
		if callActive {
			sessMu.Unlock()
			return
		}
		callContext = req
		if curState != nil {
			// Warm sessions are created before Desktop knows which app/window was
			// foregrounded at the actual wake gesture. Patch the per-call context
			// into the live state before any do_task can be prepared.
			curState.SetCallContext(req)
		}
		callStarted = time.Now()
		readyEmitted = false
		ctrl.EmitCallState("connecting")
		// The escape hatch that makes pausing safe: a user-initiated call always
		// attempts, whatever the idle policy decided, and retires the pending
		// idle lane so this attempt is immediate rather than inheriting a 60s
		// backoff. The pause itself is cleared only by an actual success, so a
		// still-broken prerequisite re-pauses instead of restarting the storm.
		warmRetry.UserRequestedCall()
		warmRetryLane.Retire()
		if curConn == nil && !warming {
			ensureWarmSessionLocked("call_start")
		}
		if err := startSessionAudioLocked("call_start"); err != nil {
			log.Printf("koe: audio start failed: %v", err)
			ctrl.EmitVoiceState("idle")
			ctrl.EmitCallFailed(koe.CallFailureAudio)
			conn, cancel, audio := closeSessionLocked(true)
			ensureWarmSessionLocked("audio_start_retry")
			sessMu.Unlock()
			stopSessionResources(conn, cancel, audio)
			return
		}
		callActive = true
		resultMailbox.Wake()
		if curConn != nil && sessionReady {
			emitReadyLocked()
			sessMu.Unlock()
			return
		}
		ensureWarmSessionLocked("call_start")
		sessMu.Unlock()
	}

	endCall = func() {
		sessMu.Lock()
		if !callActive {
			sessMu.Unlock()
			return
		}
		callActive = false
		conn, cancel, audio := closeSessionLocked(true)
		ctrl.EmitVoiceState("idle")
		ctrl.EmitCallState("ended")
		ensureWarmSessionLocked("post_call")
		sessMu.Unlock()
		// Stop/clear the old Realtime output before the goodbye cue. The audio device
		// stays open just long enough to play the cue, then is torn down below.
		if conn != nil {
			conn.InterruptOutput()
		}
		if cancel != nil {
			cancel()
		}
		if conn != nil {
			conn.Close()
		}
		if audio != nil && koe.DismissEarconEnabled() {
			audio.PlayDismissEarcon()
		}
		if audio != nil {
			audio.Stop()
		}
	}

	interruptCall := func() {
		sessMu.Lock()
		if !callActive || curConn == nil {
			sessMu.Unlock()
			return
		}
		conn := curConn
		sessMu.Unlock()
		conn.InterruptOutput()
	}

	ctrl = koe.NewControlServer(startCall, endCall, interruptCall)
	ctrl.SetToken(cfg.controlToken)
	ctrl.SetSnapshotProviders(
		func() bool { s := snapState.Load(); return s != nil && s.InFlight() != "" },
		func() bool { a := snapAudio.Load(); return a != nil && a.UserMicOff() },
	)
	ctrl.SetMicHandler(func(off bool) error {
		sessMu.Lock()
		audio := curAudio
		state := curState
		active := callActive
		sessMu.Unlock()
		if !active || audio == nil {
			return koe.ErrNoActiveCall
		}
		// Mute works in ANY active call (the Desktop trigger gesture mutes
		// instead of hanging up). A mute taken OUTSIDE a task window is
		// sticky: maybeRestoreUserMic's task-drain auto-restore must not
		// lift it — only the user does. Task-window mutes keep the original
		// koe-mic-off auto-restore. Any user restore clears the latch.
		if off {
			audio.SetUserMicSticky(state == nil || state.InFlight() == "")
		} else {
			audio.SetUserMicSticky(false)
		}
		audio.SetUserMicOff(off)
		ctrl.ReemitVoiceState()
		return nil
	})
	addr := "127.0.0.1:" + cfg.controlPort
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("control listen %s: %w", addr, err)
	}
	var controlClosing atomic.Bool
	go func() {
		// A dead control channel makes this process unreachable to Desktop, whose
		// respawn logic sees a live PID and never restarts it — so exit and let
		// Desktop's terminationHandler respawn koe cleanly on a fresh port.
		if err := http.Serve(ln, ctrl.Handler()); err != nil && !controlClosing.Load() {
			log.Fatalf("koe: control server on %s exited: %v", addr, err)
		}
	}()

	// Silent-input watchdog: in clamshell mode the OS default input stays the
	// built-in mic, which is covered and delivers pure silence — VPIO starts fine
	// and forwards ~0-RMS frames forever, so the call looks live but the model's
	// VAD never fires and Kocoro never responds. Sample the live input level and,
	// if it stays sub-floor while capture is expected, tell Desktop so it can warn
	// the user ("Kocoro can't hear you — check your microphone"). We do NOT restart
	// or rebind: in the reported case there is no other input device to switch to,
	// and a restart re-opens the same dead default (crash loop).
	go func() {
		floor := koe.MicSilenceFloor()
		window := koe.MicSilenceWindow()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		var watch koe.MicSilenceState
		var lastAudio *koe.AudioIO
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				audio := snapAudio.Load()
				if audio != lastAudio { // new call (or call ended) → fresh watch
					lastAudio = audio
					watch.Reset()
				}
				capturing := audio != nil && audio.VPIOActive() && audio.CaptureExpected()
				level := 0.0
				if audio != nil {
					level = audio.InputLevel()
				}
				switch watch.Observe(now, capturing, level, floor, window) {
				case koe.MicSilenceSilent:
					log.Printf("koe[mic]: no input above %.4f RMS for %s while capturing — mic likely unavailable (clamshell/covered/dead)", floor, window)
					ctrl.EmitMicStatus("silent")
				case koe.MicSilenceRecovered:
					log.Printf("koe[mic]: input recovered")
					ctrl.EmitMicStatus("ok")
				}
			}
		}
	}()

	// Fetch the agent registry + persona ONLY AFTER the listener above is bound, so
	// Desktop can already reach koe (409 no_active_call on /call/mic, or /call/start
	// kicking a warm session) during a slow-daemon window — ListAgents alone can block
	// up to 30s. Failure semantics are unchanged: a ListAgents error → empty registry,
	// FetchPersona → base persona. The values are published to the holders the warm
	// sessions read; a /call/start that races this window uses the base persona /
	// empty registry (graceful degradation), and the swapped-in full values apply from
	// the next warm session onward.
	agents, err := client.ListAgents(ctx)
	if err != nil {
		log.Printf("koe: list agents failed (continuing with empty registry): %v", err)
	}
	resolverHolder.Store(koe.NewAgentResolver(agents, koe.NoopSemanticMatcher{}))
	full := buildKoePersona(ctx, client, cfg, agents)
	personaHolder.Store(&full)

	sessMu.Lock()
	ensureWarmSessionLocked("startup")
	sessMu.Unlock()

	select {
	case <-ctx.Done():
	case <-desktopParentDone(ctx):
	}
	controlClosing.Store(true)
	_ = ln.Close()
	sessMu.Lock()
	conn, cancel, audio := closeSessionLocked(true)
	sessMu.Unlock()
	stopSessionResources(conn, cancel, audio)
	return nil
}
