package koe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The desktop persona must be byte-identical to what cmd/koe.go composed before
// the persona moved into this package. The goldens were dumped from the legacy
// appendTaskLedgerPersona(koePersonaForLanguage(lang)) with the ledger enabled.
func TestSpokenPersonaDesktopMatchesLegacyGolden(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	for name, lang := range map[string]string{
		"default": "",
		"en":      "en",
		"ja":      "ja",
		"zh":      "zh",
	} {
		golden, err := os.ReadFile(filepath.Join("testdata", "persona_desktop_"+name+".golden"))
		if err != nil {
			t.Fatalf("golden %s: %v", name, err)
		}
		got := AppendTaskLedgerPersona(SpokenPersona(lang, HostDesktop))
		if got != string(golden) {
			t.Errorf("desktop persona (%q) drifted from legacy composition", lang)
			reportFirstDiff(t, string(golden), got)
		}
	}
}

func reportFirstDiff(t *testing.T, want, got string) {
	t.Helper()
	limit := min(len(want), len(got))
	for i := 0; i < limit; i++ {
		if want[i] != got[i] {
			lo := max(0, i-60)
			t.Logf("first divergence at byte %d\nwant …%q\ngot  …%q", i, want[lo:min(len(want), i+60)], got[lo:min(len(got), i+60)])
			return
		}
	}
	t.Logf("one output is a prefix of the other: want %d bytes, got %d bytes", len(want), len(got))
}

// The mobile persona swaps exactly the host-specific guidance: how the call is
// hosted, what control_app can do, and how the user gets back after end_call.
// Everything else — tone, tool discipline, stop/cancel vocabulary — is shared.
func TestSpokenPersonaMobileVariance(t *testing.T) {
	mobile := SpokenPersona("", HostMobile)
	desktop := SpokenPersona("", HostDesktop)

	// The Option key does not exist on an iPhone; telling the user to double-tap
	// it after end_call is the exact bug the host variance exists to fix.
	if strings.Contains(mobile, "Option key") {
		t.Error("mobile persona still tells the user to press the Option key")
	}
	if !strings.Contains(desktop, "Option key") {
		t.Error("desktop persona lost its Option-key return hint")
	}
	if !strings.Contains(mobile, "tapping the call button") {
		t.Error("mobile persona missing its own return-path hint after end_call")
	}

	// Identity line: the mobile host is the iPhone app, not Kocoro Desktop.
	if strings.Contains(mobile, "speaking by voice through Kocoro Desktop") {
		t.Error("mobile persona still claims the call is hosted by Kocoro Desktop")
	}
	if !strings.Contains(mobile, "iPhone") {
		t.Error("mobile persona never mentions the actual host device")
	}

	// control_app on iPhone is wired but degraded (show only); the persona must
	// not promise view switching the host will refuse.
	if strings.Contains(mobile, "control_app only opens, hides, or switches app views") {
		t.Error("mobile persona still advertises desktop-only control_app actions")
	}
	if !strings.Contains(mobile, "control_app") {
		t.Error("mobile persona should still explain control_app's limits")
	}

	// Shared discipline must survive on both hosts.
	for _, section := range []string{
		"# Personality and Tone",
		"# When to Speak",
		"# Task Handoff",
		"# Results",
		"# Stop, Cancel, and End Call",
		"stop_speaking",
		VoiceIdentityInstructions,
	} {
		if !strings.Contains(mobile, section) {
			t.Errorf("mobile persona lost shared section %q", section)
		}
		if !strings.Contains(desktop, section) {
			t.Errorf("desktop persona lost shared section %q", section)
		}
	}
}

func TestSpokenPersonaLanguagePinning(t *testing.T) {
	for _, host := range []Host{HostDesktop, HostMobile} {
		if got := SpokenPersona("zh", host); !strings.Contains(got, "Always reply in Chinese") {
			t.Errorf("host %v: zh persona not pinned to Chinese", host)
		}
		if got := SpokenPersona("ja", host); !strings.Contains(got, "Always reply in Japanese") {
			t.Errorf("host %v: ja persona not pinned to Japanese", host)
		}
		if got := SpokenPersona("en", host); !strings.Contains(got, "Always reply in English") {
			t.Errorf("host %v: en persona not pinned to English", host)
		}
		// Unknown or empty language keeps the mirror-the-user default.
		for _, lang := range []string{"", "fr"} {
			if got := SpokenPersona(lang, host); !strings.Contains(got, "Reply in the language of the user's current utterance") {
				t.Errorf("host %v lang %q: default language section missing", host, lang)
			}
		}
	}
}

func TestAppendTaskLedgerPersonaGate(t *testing.T) {
	base := SpokenPersona("", HostMobile)

	t.Setenv("KOE_TASK_LEDGER", "1")
	if got := AppendTaskLedgerPersona(base); !strings.Contains(got, "# Concurrent Tasks") {
		t.Error("ledger enabled: concurrent-tasks section missing")
	}

	t.Setenv("KOE_TASK_LEDGER", "0")
	if got := AppendTaskLedgerPersona(base); got != base {
		t.Error("ledger disabled: persona must be unchanged")
	}
}
