package koe

import "reflect"

// AudioController is the seam between the front brain and whatever owns the
// microphone and speaker. It is deliberately the EXACT set of methods
// realtime.go calls — every one of them is boolean playback/capture state, and
// no audio sample ever crosses this boundary. That is what lets the brain run on
// a platform whose audio stack has nothing to do with CoreAudio.
//
// On macOS the implementation is *AudioIO (audio.go, malgo + opus + VPIO); it
// satisfies this interface implicitly, so nothing about the macOS audio layer
// changes. Deliberately NOT build-tagged: this file must compile everywhere the
// brain does.
//
// dropCapture stays unexported to keep it off the package's public API.
// Implementations outside package koe go through ExternalAudio below.
type AudioController interface {
	SetSpeaking(s bool)
	SetPlaybackEnabled(s bool)
	SetPlaybackPaused(paused bool)
	SetPlaybackTailProtected(protected bool)
	dropCapture() bool
	UserMicOff() bool
	SetUserMicOff(off bool)
	UserMicSticky() bool
	PlaybackIdle() bool
}

// ExternalAudio is AudioController for implementations OUTSIDE package koe —
// the iOS audio layer, which is Swift behind a gomobile binding. It is method
// for method identical except that dropCapture is exported as DropCapture,
// because Go only lets a package implement its own unexported methods.
//
// Exporting dropCapture on AudioController itself would have been the shorter
// route, but dropCapture has ~45 call sites in this package's tests; renaming
// them is exactly the kind of mass test edit that hides a real behaviour change.
// This adapter costs one small type and leaves every existing line untouched.
type ExternalAudio interface {
	SetSpeaking(s bool)
	SetPlaybackEnabled(s bool)
	SetPlaybackPaused(paused bool)
	DropCapture() bool
	UserMicOff() bool
	SetUserMicOff(off bool)
	UserMicSticky() bool
	PlaybackIdle() bool
}

// NewExternalAudioController adapts an out-of-package audio implementation so
// the front brain can drive it. Returns nil for a nil (or typed-nil) input, so
// the caller inherits the same "no audio attached" semantics as passing nil.
func NewExternalAudioController(e ExternalAudio) AudioController {
	if e == nil {
		return nil
	}
	if v := reflect.ValueOf(e); v.Kind() == reflect.Ptr && v.IsNil() {
		return nil
	}
	return externalAudioController{e}
}

type externalAudioController struct{ e ExternalAudio }

func (a externalAudioController) SetSpeaking(s bool)        { a.e.SetSpeaking(s) }
func (a externalAudioController) SetPlaybackEnabled(s bool) { a.e.SetPlaybackEnabled(s) }
func (a externalAudioController) SetPlaybackPaused(p bool)  { a.e.SetPlaybackPaused(p) }

// SetPlaybackTailProtected is deliberately a no-op for external hosts. The tail
// window it protects is a gate inside AudioIO's OWN capture pipeline
// (shouldForwardVPIOCapture) — external hosts run their capture gates outside
// this package (Swift VoiceGateState on iOS), where the flag has nothing to
// act on. It only ever goes true on the Qwen provider path, which no external
// host routes today; if one ever does, this gate must be ported into the host's
// capture layer rather than wired through here.
func (a externalAudioController) SetPlaybackTailProtected(bool) {}
func (a externalAudioController) dropCapture() bool             { return a.e.DropCapture() }
func (a externalAudioController) UserMicOff() bool              { return a.e.UserMicOff() }
func (a externalAudioController) SetUserMicOff(off bool)        { a.e.SetUserMicOff(off) }
func (a externalAudioController) UserMicSticky() bool           { return a.e.UserMicSticky() }
func (a externalAudioController) PlaybackIdle() bool            { return a.e.PlaybackIdle() }

// normalizeAudioController converts a typed-nil implementation to a nil
// interface.
//
// This is load-bearing, not defensive noise. eventHandler guards its audio
// calls with `h.audio != nil` in eleven places, and those guards were written
// against a *AudioIO field, where a nil pointer compares equal to nil. An
// interface holding a (*AudioIO)(nil) does NOT compare equal to nil, so every
// one of those guards would flip to true and then panic on first use.
//
// The hazard is already reachable: at least one test constructs its handler with
// `audio, _ := NewAudioIO()`, discarding the error. Today that yields a nil
// pointer and the guards correctly skip; without this normalization it would
// yield a live-looking interface over a nil pointer.
//
// Called once per handler construction, never on the event path.
func normalizeAudioController(audio AudioController) AudioController {
	if audio == nil {
		return nil
	}
	if v := reflect.ValueOf(audio); v.Kind() == reflect.Ptr && v.IsNil() {
		return nil
	}
	return audio
}
