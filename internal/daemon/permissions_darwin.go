package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// probePermissions checks current TCC permission status via ax_server.
func probePermissions(ctx context.Context) permissionStatus {
	path, err := axServerPath()
	if err != nil {
		return permissionStatus{
			ScreenRecording: "unknown",
			Accessibility:   "unknown",
			Automation:      "unknown",
		}
	}

	cmd := exec.CommandContext(ctx, path, "--check-permissions")
	out, err := cmd.Output()
	if err != nil {
		return permissionStatus{
			ScreenRecording: "unknown",
			Accessibility:   "unknown",
			Automation:      "unknown",
		}
	}

	var status map[string]string
	if err := json.Unmarshal(out, &status); err != nil {
		return permissionStatus{
			ScreenRecording: "unknown",
			Accessibility:   "unknown",
			Automation:      "unknown",
		}
	}

	return permissionStatus{
		ScreenRecording: statusOrUnknown(status, "screen_recording"),
		Accessibility:   statusOrUnknown(status, "accessibility"),
		Automation:      statusOrUnknown(status, "automation"),
	}
}

// requestPermission triggers macOS permission dialogs via ax_server.
func requestPermission(ctx context.Context, permission string) permissionResult {
	switch permission {
	case "screen_recording", "accessibility", "automation":
		// valid
	default:
		return permissionResult{Permission: permission, Status: "unknown", Message: "unsupported permission"}
	}

	path, err := axServerPath()
	if err != nil {
		return permissionResult{
			Permission: permission,
			Status:     "unknown",
			Message:    fmt.Sprintf("ax_server not found: %v", err),
		}
	}

	cmd := exec.CommandContext(ctx, path, "--request-permission", permission)
	out, err := cmd.Output()
	if err != nil {
		return permissionResult{
			Permission: permission,
			Status:     "unknown",
			Message:    fmt.Sprintf("ax_server request failed: %v", err),
		}
	}

	var result map[string]string
	if err := json.Unmarshal(out, &result); err != nil {
		return permissionResult{
			Permission: permission,
			Status:     "unknown",
			Message:    "failed to parse ax_server response",
		}
	}

	return permissionResult{
		Permission: result["permission"],
		Status:     result["status"],
		Message:    result["message"],
	}
}

func statusOrUnknown(m map[string]string, key string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return "unknown"
}

// axServerPath finds the ax_server binary.
// Mirrors the lookup in internal/tools/axclient.go.
func axServerPath() (string, error) {
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		// Same directory as shan binary
		p := filepath.Join(dir, "ax_server")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		// npm: bin/ax_server
		p = filepath.Join(dir, "bin", "ax_server")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// Development: relative to working directory
	p := filepath.Join("internal", "tools", "axserver", ".build", "debug", "ax_server")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("ax_server binary not found")
}
