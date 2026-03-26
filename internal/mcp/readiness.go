package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	markerVersion  = 1
	markerFileName = "playwright-readiness.json"
)

// PlaywrightMarker is the local machine readiness state for Playwright MCP.
type PlaywrightMarker struct {
	Version      int       `json:"version"`
	CommandSig   string    `json:"command_signature"`
	TokenPresent bool      `json:"token_present"`
	LastVerified time.Time `json:"last_verified_at"`
}

// CommandSignature returns a deterministic sha256[:16] hash of command|arg1|arg2.
func CommandSignature(command string, args []string) string {
	h := sha256.New()
	h.Write([]byte(command))
	for _, a := range args {
		h.Write([]byte("|"))
		h.Write([]byte(a))
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func markerPath(localDir string) string {
	return filepath.Join(localDir, markerFileName)
}

// ReadPlaywrightMarker reads the marker file. Returns nil if missing, corrupt, or wrong version.
func ReadPlaywrightMarker(localDir string) (*PlaywrightMarker, error) {
	data, err := os.ReadFile(markerPath(localDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m PlaywrightMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, nil // treat corrupt as missing
	}
	if m.Version != markerVersion {
		return nil, nil // incompatible version
	}
	return &m, nil
}

// WritePlaywrightMarker writes the marker file, creating the local dir with 0700 if needed.
func WritePlaywrightMarker(localDir, commandSig string, tokenPresent bool) error {
	if err := os.MkdirAll(localDir, 0700); err != nil {
		return err
	}
	m := PlaywrightMarker{
		Version:      markerVersion,
		CommandSig:   commandSig,
		TokenPresent: tokenPresent,
		LastVerified: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(markerPath(localDir), data, 0600)
}

// ValidatePlaywrightMarker checks if a valid marker exists with matching command signature.
func ValidatePlaywrightMarker(localDir, expectedSig string) bool {
	m, err := ReadPlaywrightMarker(localDir)
	if err != nil || m == nil {
		return false
	}
	return m.CommandSig == expectedSig
}

// ValidatePlaywrightMarkerFull checks marker exists, signature matches, and token state matches.
func ValidatePlaywrightMarkerFull(localDir, expectedSig string, tokenPresent bool) bool {
	m, err := ReadPlaywrightMarker(localDir)
	if err != nil || m == nil {
		return false
	}
	return m.CommandSig == expectedSig && m.TokenPresent == tokenPresent
}

// ClearPlaywrightMarker removes the marker file. Returns error for auditability.
func ClearPlaywrightMarker(localDir string) error {
	err := os.Remove(markerPath(localDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
