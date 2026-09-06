//go:build darwin && !ios && cgo

package koe

import "testing"

func TestUserMicOffGatesCapture(t *testing.T) {
	a, err := NewAudioIO()
	if err != nil {
		t.Fatal(err)
	}
	if a.captureSuppressed() {
		t.Fatal("capture suppressed at rest")
	}
	a.SetUserMicOff(true)
	if !a.captureSuppressed() {
		t.Fatal("user mic off must suppress capture")
	}
	if a.dropCapture() {
		t.Fatal("user mic off must NOT leak into the speaking gate")
	}
	a.SetUserMicOff(false)
	a.SetSpeaking(true)
	if !a.captureSuppressed() {
		t.Fatal("speaking gate must still suppress capture")
	}
}

func TestUserMicOffOutranksVPIOBargeIn(t *testing.T) {
	t.Setenv("KOE_VPIO_BARGE_IN", "1")
	a, err := NewAudioIO()
	if err != nil {
		t.Fatal(err)
	}
	a.SetUserMicOff(true)
	// Loud sustained frames: barge-in would normally start forwarding after
	// the KOE_VPIO_BARGE_MS window. User mic off must win every single frame.
	for i := 0; i < 100; i++ {
		if a.shouldForwardVPIOCapture(0.5) {
			t.Fatal("KOE_VPIO_BARGE_IN must never bypass user mic off")
		}
	}
}

func TestUserMicOffZeroesInputLevel(t *testing.T) {
	a, err := NewAudioIO()
	if err != nil {
		t.Fatal(err)
	}
	a.setInputLevel(0.4)
	if a.InputLevel() == 0 {
		t.Fatal("input level should read back at rest")
	}
	a.SetUserMicOff(true)
	if a.InputLevel() != 0 {
		t.Fatal("mic off must zero the reported input level (UI trust: the sprite must not react to room speech)")
	}
}
