package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/session"
)

func TestRenderStartupHeader_FirstFrame(t *testing.T) {
	result := renderStartupHeader(0, 60, "dev", "small", "https://api.test.com", "/tmp", nil, 0)
	if !strings.Contains(result, "Shannon CLI") {
		t.Error("first frame should contain title in top border")
	}
	if !strings.Contains(result, "▀") && !strings.Contains(result, "█") {
		t.Error("first frame should show pixel frog logo")
	}
}

func TestRenderStartupHeader_FinalFrame(t *testing.T) {
	sessions := []session.SessionSummary{
		{ID: "abc", Title: "test session", CreatedAt: time.Now().Add(-2 * time.Hour), MsgCount: 5},
	}
	result := renderStartupHeader(headerTotalFrames-1, 80, "v0.1.0", "large", "https://api.test.com", "/home/user/project", sessions, 0)

	if !strings.Contains(result, "Shannon CLI") {
		t.Error("final frame should contain title")
	}
	if !strings.Contains(result, "▀") && !strings.Contains(result, "█") {
		t.Error("final frame should contain pixel frog logo")
	}
	if !strings.Contains(result, "Tips") {
		t.Error("final frame should contain Tips section")
	}
	if !strings.Contains(result, "Recent activity") {
		t.Error("final frame should contain Recent activity section")
	}
	if !strings.Contains(result, "2h ago") {
		t.Error("final frame should show relative time for recent session")
	}
}

func TestRenderStartupHeader_NarrowTerminal(t *testing.T) {
	result := renderStartupHeader(headerTotalFrames-1, 40, "dev", "small", "https://api.test.com", "/tmp", nil, 0)
	if result == "" {
		t.Error("should render something even on narrow terminal")
	}
}

func TestRenderStartupHeader_WideTerminal(t *testing.T) {
	result := renderStartupHeader(headerTotalFrames-1, 200, "dev", "small", "https://api.test.com", "/tmp", nil, 0)
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		visible := stripAnsi(line)
		runeLen := len([]rune(visible))
		if runeLen > 102 {
			t.Errorf("line too wide (%d runes): %s", runeLen, visible)
		}
	}
}

func TestRenderStartupHeader_LogoAlwaysVisible(t *testing.T) {
	result := renderStartupHeader(0, 80, "dev", "small", "https://api.test.com", "/tmp", nil, 0)
	if !strings.Contains(result, "▀") && !strings.Contains(result, "█") {
		t.Error("frame 0 should show pixel frog logo")
	}
}

func TestRenderStartupHeader_EyeBlinkAnimation(t *testing.T) {
	// Frames 0-2 show eyes open, frames 3-5 show eyes closed.
	eyesOpen := renderStartupHeader(0, 80, "dev", "small", "https://api.test.com", "/tmp", nil, 0)
	eyesClosed := renderStartupHeader(3, 80, "dev", "small", "https://api.test.com", "/tmp", nil, 0)
	if eyesOpen == eyesClosed {
		t.Error("animation frames from different poses should differ")
	}
}

func TestTimeAgo(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5 min ago"},
		{1 * time.Minute, "1 min ago"},
		{2 * time.Hour, "2h ago"},
		{1 * time.Hour, "1h ago"},
		{25 * time.Hour, "yesterday"},
		{72 * time.Hour, "3 days ago"},
	}
	for _, tt := range tests {
		got := timeAgo(time.Now().Add(-tt.d))
		if got != tt.want {
			t.Errorf("timeAgo(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestStripAnsi(t *testing.T) {
	styled := "\033[38;5;196mhello\033[0m"
	if got := stripAnsi(styled); got != "hello" {
		t.Errorf("stripAnsi() = %q, want %q", got, "hello")
	}
}

func TestTruncateStr(t *testing.T) {
	if got := truncateStr("hello world", 8); got != "hello..." {
		t.Errorf("truncateStr() = %q, want %q", got, "hello...")
	}
	if got := truncateStr("short", 10); got != "short" {
		t.Errorf("truncateStr() = %q, want %q", got, "short")
	}
}
