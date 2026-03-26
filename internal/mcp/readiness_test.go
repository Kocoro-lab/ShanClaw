package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadMarker_Missing(t *testing.T) {
	dir := t.TempDir()
	m, err := ReadPlaywrightMarker(filepath.Join(dir, "local"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Fatal("expected nil marker for missing file")
	}
}

func TestWriteAndReadMarker(t *testing.T) {
	dir := t.TempDir()
	localDir := filepath.Join(dir, "local")

	sig := CommandSignature("playwright-mcp", []string{"--browser", "chrome"})
	err := WritePlaywrightMarker(localDir, sig, true)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	m, err := ReadPlaywrightMarker(localDir)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil marker")
	}
	if m.CommandSig != sig {
		t.Errorf("sig mismatch: got %q, want %q", m.CommandSig, sig)
	}
	if !m.TokenPresent {
		t.Error("expected token_present=true")
	}
}

func TestValidateMarker_SignatureMismatch(t *testing.T) {
	dir := t.TempDir()
	localDir := filepath.Join(dir, "local")

	sig := CommandSignature("playwright-mcp", []string{"--browser", "chrome"})
	WritePlaywrightMarker(localDir, sig, false)

	newSig := CommandSignature("playwright-mcp", []string{"--browser", "firefox"})
	if ValidatePlaywrightMarker(localDir, newSig) {
		t.Error("expected invalid for mismatched signature")
	}
}

func TestClearMarker(t *testing.T) {
	dir := t.TempDir()
	localDir := filepath.Join(dir, "local")

	sig := CommandSignature("playwright-mcp", []string{"--browser", "chrome"})
	WritePlaywrightMarker(localDir, sig, false)

	err := ClearPlaywrightMarker(localDir)
	if err != nil {
		t.Fatalf("clear should return nil error on success: %v", err)
	}

	m, _ := ReadPlaywrightMarker(localDir)
	if m != nil {
		t.Error("expected nil after clear")
	}
}

func TestClearMarker_AlreadyMissing(t *testing.T) {
	dir := t.TempDir()
	localDir := filepath.Join(dir, "local")

	err := ClearPlaywrightMarker(localDir)
	if err != nil {
		t.Fatalf("clear of missing marker should return nil error: %v", err)
	}
}

func TestCommandSignature_Deterministic(t *testing.T) {
	a := CommandSignature("cmd", []string{"a", "b"})
	b := CommandSignature("cmd", []string{"a", "b"})
	if a != b {
		t.Error("expected deterministic signature")
	}
	c := CommandSignature("cmd", []string{"a", "c"})
	if a == c {
		t.Error("expected different signature for different args")
	}
}

func TestCommandSignature_Length(t *testing.T) {
	sig := CommandSignature("playwright-mcp", []string{"--browser", "chrome"})
	if len(sig) != 16 {
		t.Errorf("expected signature length 16, got %d", len(sig))
	}
}

func TestReadMarker_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	localDir := filepath.Join(dir, "local")
	os.MkdirAll(localDir, 0700)

	os.WriteFile(filepath.Join(localDir, markerFileName), []byte("not json{{{"), 0600)

	m, err := ReadPlaywrightMarker(localDir)
	if err != nil {
		t.Fatalf("corrupt JSON should not return error: %v", err)
	}
	if m != nil {
		t.Fatal("corrupt JSON should be treated as missing (nil)")
	}
}

func TestReadMarker_WrongVersion(t *testing.T) {
	dir := t.TempDir()
	localDir := filepath.Join(dir, "local")
	os.MkdirAll(localDir, 0700)

	data := []byte(`{"version": 99, "command_signature": "abc", "token_present": true, "last_verified_at": "2026-01-01T00:00:00Z"}`)
	os.WriteFile(filepath.Join(localDir, markerFileName), data, 0600)

	m, err := ReadPlaywrightMarker(localDir)
	if err != nil {
		t.Fatalf("wrong version should not return error: %v", err)
	}
	if m != nil {
		t.Fatal("wrong version should be treated as missing (nil)")
	}
}

func TestValidateMarkerFull_TokenDrift(t *testing.T) {
	dir := t.TempDir()
	localDir := filepath.Join(dir, "local")

	sig := CommandSignature("playwright-mcp", []string{"--browser", "chrome"})
	WritePlaywrightMarker(localDir, sig, true)

	// Signature matches but token state drifted: marker says true, we expect false.
	if ValidatePlaywrightMarkerFull(localDir, sig, false) {
		t.Error("expected invalid when token_present drifts (marker=true, expected=false)")
	}

	// Correct token state should pass.
	if !ValidatePlaywrightMarkerFull(localDir, sig, true) {
		t.Error("expected valid when both signature and token match")
	}
}

func TestValidateMarkerFull_SignatureMismatch(t *testing.T) {
	dir := t.TempDir()
	localDir := filepath.Join(dir, "local")

	sig := CommandSignature("playwright-mcp", []string{"--browser", "chrome"})
	WritePlaywrightMarker(localDir, sig, true)

	wrongSig := CommandSignature("playwright-mcp", []string{"--browser", "firefox"})
	if ValidatePlaywrightMarkerFull(localDir, wrongSig, true) {
		t.Error("expected invalid for mismatched signature even when token matches")
	}
}

func TestValidateMarkerFull_Missing(t *testing.T) {
	dir := t.TempDir()
	localDir := filepath.Join(dir, "local")

	if ValidatePlaywrightMarkerFull(localDir, "anything", true) {
		t.Error("expected invalid for missing marker")
	}
}
