//go:build !darwin

package daemon

import "context"

func probePermissions(_ context.Context) permissionStatus {
	return permissionStatus{ScreenRecording: "unsupported", Accessibility: "unsupported", Automation: "unsupported"}
}

func requestPermission(_ context.Context, _ string) permissionResult {
	return permissionResult{Status: "unsupported", Message: "permissions probing is only available on macOS"}
}
