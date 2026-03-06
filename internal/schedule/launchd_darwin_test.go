//go:build darwin

package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratePlist(t *testing.T) {
	plist := GeneratePlist("abc123", "ops-bot", "0 9 * * *", "check prod", "/usr/local/bin/shan")
	if !strings.Contains(plist, "com.shannon.schedule.abc123") {
		t.Error("missing label")
	}
	if !strings.Contains(plist, "--agent") {
		t.Error("missing --agent flag")
	}
	if !strings.Contains(plist, "ops-bot") {
		t.Error("missing agent name")
	}
	if !strings.Contains(plist, "<key>Hour</key>") {
		t.Error("missing Hour key")
	}
	if !strings.Contains(plist, "<integer>9</integer>") {
		t.Error("missing hour value 9")
	}
}

func TestGeneratePlistNoAgent(t *testing.T) {
	plist := GeneratePlist("abc123", "", "*/5 * * * *", "check", "/usr/local/bin/shan")
	if strings.Contains(plist, "--agent") {
		t.Error("should not contain --agent when agent is empty")
	}
}

func TestGeneratePlistEscapesXML(t *testing.T) {
	plist := GeneratePlist("abc", "", "0 9 * * *", "check <prod> & \"staging\"", "/usr/local/bin/shan")
	if strings.Contains(plist, "<prod>") {
		t.Error("unescaped < in prompt")
	}
	if !strings.Contains(plist, "&lt;prod&gt;") {
		t.Error("missing escaped < in prompt")
	}
}

func TestCronRangeExpansion(t *testing.T) {
	// "0 9 * * 1-5" = minute 0, hour 9, weekdays Mon-Fri
	// Should produce 5 CalendarInterval dicts (one per weekday)
	plist := GeneratePlist("r1", "", "0 9 * * 1-5", "check", "/usr/local/bin/shan")
	if !strings.Contains(plist, "<array>") {
		t.Error("expected array of CalendarInterval dicts for range expression")
	}
	// Should have entries for weekdays 1 through 5
	for _, day := range []string{"1", "2", "3", "4", "5"} {
		if !strings.Contains(plist, "<integer>"+day+"</integer>") {
			t.Errorf("missing weekday %s in range expansion", day)
		}
	}
	// All should have Hour=9 and Minute=0
	if strings.Count(plist, "<key>Hour</key>") != 5 {
		t.Errorf("expected 5 Hour entries, got %d", strings.Count(plist, "<key>Hour</key>"))
	}
}

func TestCronListExpansion(t *testing.T) {
	// "0 9 * * 1,3,5" = Mon, Wed, Fri at 9:00
	plist := GeneratePlist("l1", "", "0 9 * * 1,3,5", "check", "/usr/local/bin/shan")
	if !strings.Contains(plist, "<array>") {
		t.Error("expected array of CalendarInterval dicts for list expression")
	}
	if strings.Count(plist, "<key>Weekday</key>") != 3 {
		t.Errorf("expected 3 Weekday entries, got %d", strings.Count(plist, "<key>Weekday</key>"))
	}
}

func TestCronHourRange(t *testing.T) {
	// "0 9-11 * * *" = 9:00, 10:00, 11:00 daily
	plist := GeneratePlist("h1", "", "0 9-11 * * *", "check", "/usr/local/bin/shan")
	if !strings.Contains(plist, "<array>") {
		t.Error("expected array for hour range")
	}
	if strings.Count(plist, "<key>Hour</key>") != 3 {
		t.Errorf("expected 3 Hour entries, got %d", strings.Count(plist, "<key>Hour</key>"))
	}
}

func TestCronStepUsesInterval(t *testing.T) {
	// "*/5 * * * *" = every 5 minutes, should use StartInterval
	plist := GeneratePlist("s1", "", "*/5 * * * *", "check", "/usr/local/bin/shan")
	if !strings.Contains(plist, "StartInterval") {
		t.Error("expected StartInterval for step expression")
	}
	if !strings.Contains(plist, "<integer>300</integer>") {
		t.Error("expected 300 seconds (5 * 60) for */5")
	}
}

func TestCronHourStepUsesInterval(t *testing.T) {
	// "0 */2 * * *" = every 2 hours
	plist := GeneratePlist("s2", "", "0 */2 * * *", "check", "/usr/local/bin/shan")
	if !strings.Contains(plist, "StartInterval") {
		t.Error("expected StartInterval for hour step")
	}
	if !strings.Contains(plist, "<integer>7200</integer>") {
		t.Error("expected 7200 seconds (2 * 3600) for */2 hours")
	}
}

func TestPlistPath(t *testing.T) {
	p := PlistPath("abc123")
	if !strings.Contains(p, "com.shannon.schedule.abc123.plist") {
		t.Errorf("unexpected path: %s", p)
	}
	if !strings.Contains(p, "LaunchAgents") {
		t.Errorf("expected LaunchAgents dir: %s", p)
	}
}

func TestWriteAndRemovePlist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.plist")
	content := "<plist>test</plist>"
	if err := WritePlist(path, content); err != nil {
		t.Fatalf("WritePlist: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != content {
		t.Errorf("content mismatch")
	}
	if err := RemovePlist(path); err != nil {
		t.Fatalf("RemovePlist: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("plist file still exists")
	}
}
