package heartbeat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsHeartbeatOK(t *testing.T) {
	tests := []struct {
		reply string
		want  bool
	}{
		{"HEARTBEAT_OK", true},
		{"heartbeat_ok", true},
		{"  HEARTBEAT_OK  ", true},
		{"\nHEARTBEAT_OK\n", true},
		{"HEARTBEAT_OK and some extra text", false},
		{"Everything looks fine", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.reply, func(t *testing.T) {
			if got := IsHeartbeatOK(tt.reply); got != tt.want {
				t.Errorf("IsHeartbeatOK(%q) = %v, want %v", tt.reply, got, tt.want)
			}
		})
	}
}

func TestFormatPrompt(t *testing.T) {
	checklist := "- Check disk\n- Check memory"
	got := FormatPrompt(checklist)
	if got == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(got, "HEARTBEAT_OK") {
		t.Error("prompt should mention HEARTBEAT_OK")
	}
	if !strings.Contains(got, checklist) {
		t.Error("prompt should contain checklist")
	}
}

func TestReadChecklist(t *testing.T) {
	dir := t.TempDir()

	// Missing file — should return empty.
	content, err := ReadChecklist(filepath.Join(dir, "HEARTBEAT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		t.Errorf("expected empty for missing file, got %q", content)
	}

	// Empty file — should return empty.
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("   \n\n  "), 0644)
	content, err = ReadChecklist(filepath.Join(dir, "HEARTBEAT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		t.Errorf("expected empty for whitespace-only file, got %q", content)
	}

	// Valid file.
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("- Check disk\n- Check memory"), 0644)
	content, err = ReadChecklist(filepath.Join(dir, "HEARTBEAT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if content != "- Check disk\n- Check memory" {
		t.Errorf("unexpected content: %q", content)
	}
}

func TestReadChecklist_MaxSize(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 5000)
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte(big), 0644)

	content, err := ReadChecklist(filepath.Join(dir, "HEARTBEAT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(content) > maxChecklistChars+100 {
		t.Errorf("expected truncated content, got %d chars", len(content))
	}
}
