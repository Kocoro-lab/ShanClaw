package daemon

type permissionStatus struct {
	ScreenRecording string `json:"screen_recording"` // "granted", "denied", "unknown", "unsupported"
	Accessibility   string `json:"accessibility"`     // "granted", "denied", "unknown", "unsupported"
	Automation      string `json:"automation"`        // "granted", "denied", "unknown", "unsupported"
}

type permissionResult struct {
	Permission string `json:"permission"`
	Status     string `json:"status"` // "granted", "denied", "prompted", "unsupported"
	Message    string `json:"message,omitempty"`
}
