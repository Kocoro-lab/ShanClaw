package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInstructions_BasicHierarchy(t *testing.T) {
	shannonDir := t.TempDir()
	projectDir := t.TempDir()

	// Create global instructions
	os.WriteFile(filepath.Join(shannonDir, "instructions.md"), []byte("global instructions"), 0644)

	// Create global rules
	os.MkdirAll(filepath.Join(shannonDir, "rules"), 0755)
	os.WriteFile(filepath.Join(shannonDir, "rules", "alpha.md"), []byte("rule alpha"), 0644)
	os.WriteFile(filepath.Join(shannonDir, "rules", "beta.md"), []byte("rule beta"), 0644)

	// Create project instructions
	os.WriteFile(filepath.Join(projectDir, "instructions.md"), []byte("project instructions"), 0644)

	// Create project rules
	os.MkdirAll(filepath.Join(projectDir, "rules"), 0755)
	os.WriteFile(filepath.Join(projectDir, "rules", "gamma.md"), []byte("rule gamma"), 0644)

	// Create project local
	os.WriteFile(filepath.Join(projectDir, "instructions.local.md"), []byte("local overrides"), 0644)

	result, err := LoadInstructions(shannonDir, projectDir, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all files appear in order
	globalIdx := strings.Index(result, "global instructions")
	alphaIdx := strings.Index(result, "rule alpha")
	betaIdx := strings.Index(result, "rule beta")
	projectIdx := strings.Index(result, "project instructions")
	gammaIdx := strings.Index(result, "rule gamma")
	localIdx := strings.Index(result, "local overrides")

	if globalIdx == -1 || alphaIdx == -1 || betaIdx == -1 ||
		projectIdx == -1 || gammaIdx == -1 || localIdx == -1 {
		t.Fatalf("expected all content present, got:\n%s", result)
	}

	if !(globalIdx < alphaIdx && alphaIdx < betaIdx &&
		betaIdx < projectIdx && projectIdx < gammaIdx && gammaIdx < localIdx) {
		t.Errorf("content not in expected priority order")
	}
}

func TestLoadInstructions_SourceComments(t *testing.T) {
	shannonDir := t.TempDir()
	os.WriteFile(filepath.Join(shannonDir, "instructions.md"), []byte("hello"), 0644)

	result, err := LoadInstructions(shannonDir, "", 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "<!-- from: " + filepath.Join(shannonDir, "instructions.md") + " -->"
	if !strings.Contains(result, expected) {
		t.Errorf("expected source comment %q, got:\n%s", expected, result)
	}
}

func TestLoadInstructions_MissingFiles(t *testing.T) {
	shannonDir := t.TempDir()
	projectDir := t.TempDir()

	// No files created — all should be missing
	result, err := LoadInstructions(shannonDir, projectDir, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result for missing files, got: %q", result)
	}
}

func TestLoadInstructions_EmptyDirs(t *testing.T) {
	result, err := LoadInstructions("", "", 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result, got: %q", result)
	}
}

func TestLoadInstructions_Deduplication(t *testing.T) {
	shannonDir := t.TempDir()
	projectDir := t.TempDir()

	// Same line in both global and project — project (higher priority) should keep it
	os.WriteFile(filepath.Join(shannonDir, "instructions.md"), []byte("shared line\nglobal only"), 0644)
	os.WriteFile(filepath.Join(projectDir, "instructions.md"), []byte("shared line\nproject only"), 0644)

	result, err := LoadInstructions(shannonDir, projectDir, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "shared line" should appear exactly once
	count := strings.Count(result, "shared line")
	if count != 1 {
		t.Errorf("expected 'shared line' exactly once, found %d times in:\n%s", count, result)
	}

	// The kept occurrence should be in the project section
	projectComment := "<!-- from: " + filepath.Join(projectDir, "instructions.md") + " -->"
	projectSection := result[strings.Index(result, projectComment):]
	if !strings.Contains(projectSection, "shared line") {
		t.Errorf("expected 'shared line' in project section, not found")
	}

	if !strings.Contains(result, "global only") {
		t.Errorf("expected 'global only' to be present")
	}
	if !strings.Contains(result, "project only") {
		t.Errorf("expected 'project only' to be present")
	}
}

func TestLoadInstructions_Truncation(t *testing.T) {
	shannonDir := t.TempDir()

	// Create content that exceeds budget
	bigContent := strings.Repeat("x", 5000)
	os.WriteFile(filepath.Join(shannonDir, "instructions.md"), []byte(bigContent), 0644)

	// Budget of 500 tokens = 2000 chars
	result, err := LoadInstructions(shannonDir, "", 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasSuffix(result, "\n[Instructions truncated — reduce content in lower-priority files]") {
		t.Errorf("expected truncation message, got suffix: %q", result[len(result)-80:])
	}
}

func TestLoadInstructions_NonMDFilesIgnored(t *testing.T) {
	shannonDir := t.TempDir()
	os.MkdirAll(filepath.Join(shannonDir, "rules"), 0755)

	os.WriteFile(filepath.Join(shannonDir, "rules", "valid.md"), []byte("valid rule"), 0644)
	os.WriteFile(filepath.Join(shannonDir, "rules", "ignored.txt"), []byte("should be ignored"), 0644)
	os.WriteFile(filepath.Join(shannonDir, "rules", "ignored.yaml"), []byte("also ignored"), 0644)

	result, err := LoadInstructions(shannonDir, "", 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "valid rule") {
		t.Errorf("expected 'valid rule' to be present")
	}
	if strings.Contains(result, "should be ignored") {
		t.Errorf("expected .txt file to be ignored")
	}
	if strings.Contains(result, "also ignored") {
		t.Errorf("expected .yaml file to be ignored")
	}
}

func TestLoadInstructions_InvalidUTF8(t *testing.T) {
	shannonDir := t.TempDir()

	// Write invalid UTF-8
	os.WriteFile(filepath.Join(shannonDir, "instructions.md"), []byte{0xff, 0xfe, 0xfd}, 0644)

	result, err := LoadInstructions(shannonDir, "", 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Invalid UTF-8 file should be skipped
	if result != "" {
		t.Errorf("expected empty result for invalid UTF-8, got: %q", result)
	}
}

func TestLoadInstructions_RulesSortedAlphabetically(t *testing.T) {
	shannonDir := t.TempDir()
	os.MkdirAll(filepath.Join(shannonDir, "rules"), 0755)

	os.WriteFile(filepath.Join(shannonDir, "rules", "charlie.md"), []byte("charlie"), 0644)
	os.WriteFile(filepath.Join(shannonDir, "rules", "alice.md"), []byte("alice"), 0644)
	os.WriteFile(filepath.Join(shannonDir, "rules", "bob.md"), []byte("bob"), 0644)

	result, err := LoadInstructions(shannonDir, "", 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	aliceIdx := strings.Index(result, "alice")
	bobIdx := strings.Index(result, "bob")
	charlieIdx := strings.Index(result, "charlie")

	if !(aliceIdx < bobIdx && bobIdx < charlieIdx) {
		t.Errorf("expected alphabetical order (alice < bob < charlie), got indices: %d, %d, %d",
			aliceIdx, bobIdx, charlieIdx)
	}
}

func TestLoadMemory_Exists(t *testing.T) {
	shannonDir := t.TempDir()
	os.MkdirAll(filepath.Join(shannonDir, "memory"), 0755)

	lines := make([]string, 300)
	for i := range lines {
		lines[i] = "line " + string(rune('A'+i%26))
	}
	content := strings.Join(lines, "\n")
	os.WriteFile(filepath.Join(shannonDir, "memory", "MEMORY.md"), []byte(content), 0644)

	result, err := LoadMemory(shannonDir, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resultLines := strings.Split(result, "\n")
	if len(resultLines) != 200 {
		t.Errorf("expected 200 lines, got %d", len(resultLines))
	}
}

func TestLoadMemory_Missing(t *testing.T) {
	shannonDir := t.TempDir()

	result, err := LoadMemory(shannonDir, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result for missing file, got: %q", result)
	}
}

func TestLoadMemory_EmptyDir(t *testing.T) {
	result, err := LoadMemory("", 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result, got: %q", result)
	}
}

func TestLoadMemory_ShortFile(t *testing.T) {
	shannonDir := t.TempDir()
	os.MkdirAll(filepath.Join(shannonDir, "memory"), 0755)
	os.WriteFile(filepath.Join(shannonDir, "memory", "MEMORY.md"), []byte("short\ncontent"), 0644)

	result, err := LoadMemory(shannonDir, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "short\ncontent" {
		t.Errorf("expected full content, got: %q", result)
	}
}

func TestLoadCustomCommands_Basic(t *testing.T) {
	shannonDir := t.TempDir()
	projectDir := t.TempDir()

	os.MkdirAll(filepath.Join(shannonDir, "commands"), 0755)
	os.MkdirAll(filepath.Join(projectDir, "commands"), 0755)

	os.WriteFile(filepath.Join(shannonDir, "commands", "deploy.md"), []byte("global deploy"), 0644)
	os.WriteFile(filepath.Join(shannonDir, "commands", "test.md"), []byte("global test"), 0644)
	os.WriteFile(filepath.Join(projectDir, "commands", "deploy.md"), []byte("project deploy"), 0644)

	commands, err := LoadCustomCommands(shannonDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if commands["deploy"] != "project deploy" {
		t.Errorf("expected project deploy to override global, got: %q", commands["deploy"])
	}
	if commands["test"] != "global test" {
		t.Errorf("expected global test command, got: %q", commands["test"])
	}
}

func TestLoadCustomCommands_BuiltinSkipped(t *testing.T) {
	shannonDir := t.TempDir()
	os.MkdirAll(filepath.Join(shannonDir, "commands"), 0755)

	os.WriteFile(filepath.Join(shannonDir, "commands", "help.md"), []byte("custom help"), 0644)
	os.WriteFile(filepath.Join(shannonDir, "commands", "quit.md"), []byte("custom quit"), 0644)
	os.WriteFile(filepath.Join(shannonDir, "commands", "deploy.md"), []byte("deploy cmd"), 0644)

	commands, err := LoadCustomCommands(shannonDir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, exists := commands["help"]; exists {
		t.Errorf("expected 'help' to be skipped as builtin")
	}
	if _, exists := commands["quit"]; exists {
		t.Errorf("expected 'quit' to be skipped as builtin")
	}
	if commands["deploy"] != "deploy cmd" {
		t.Errorf("expected 'deploy' command, got: %q", commands["deploy"])
	}
}

func TestLoadCustomCommands_Truncation(t *testing.T) {
	shannonDir := t.TempDir()
	os.MkdirAll(filepath.Join(shannonDir, "commands"), 0755)

	bigContent := strings.Repeat("x", 10000)
	os.WriteFile(filepath.Join(shannonDir, "commands", "big.md"), []byte(bigContent), 0644)

	commands, err := LoadCustomCommands(shannonDir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(commands["big"]) != maxCommandFileChars {
		t.Errorf("expected truncated to %d chars, got %d", maxCommandFileChars, len(commands["big"]))
	}
}

func TestLoadCustomCommands_EmptyDirs(t *testing.T) {
	commands, err := LoadCustomCommands("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commands) != 0 {
		t.Errorf("expected empty map, got %d entries", len(commands))
	}
}

func TestLoadCustomCommands_MissingDirs(t *testing.T) {
	shannonDir := t.TempDir()
	// Don't create the commands directory
	commands, err := LoadCustomCommands(shannonDir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commands) != 0 {
		t.Errorf("expected empty map, got %d entries", len(commands))
	}
}

func TestLoadInstructions_DeduplicationPreservesEmptyLines(t *testing.T) {
	shannonDir := t.TempDir()
	projectDir := t.TempDir()

	os.WriteFile(filepath.Join(shannonDir, "instructions.md"), []byte("line1\n\nline2"), 0644)
	os.WriteFile(filepath.Join(projectDir, "instructions.md"), []byte("line3\n\nline4"), 0644)

	result, err := LoadInstructions(shannonDir, projectDir, 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Empty lines should be preserved in both
	if !strings.Contains(result, "line1") || !strings.Contains(result, "line2") ||
		!strings.Contains(result, "line3") || !strings.Contains(result, "line4") {
		t.Errorf("expected all content lines present, got:\n%s", result)
	}
}

func TestLoadMemoryFrom_ExpandsDetailFiles(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(
		"# Memory\n- basic fact\n- [2026-03-12] See [detail.md](detail.md) for more\n- another fact\n",
	), 0644)
	os.WriteFile(filepath.Join(dir, "detail.md"), []byte(
		"# Detail\n- expanded fact 1\n- expanded fact 2\n",
	), 0644)

	result, err := LoadMemoryFrom(dir, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "expanded fact 1") {
		t.Error("should inline detail file content")
	}
	if !strings.Contains(result, "expanded fact 2") {
		t.Error("should inline all lines from detail file")
	}
	if !strings.Contains(result, "another fact") {
		t.Error("should keep lines after the expanded link")
	}
	// The pointer line itself should be replaced by the detail content
	if strings.Contains(result, "See [detail.md]") {
		t.Error("pointer line should be replaced by expanded content")
	}
}

func TestLoadMemoryFrom_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()

	// These should NOT be expanded
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(
		"- [link](../etc/passwd.md)\n- [link](sub/dir.md)\n- [link](back\\.md)\n",
	), 0644)

	result, err := LoadMemoryFrom(dir, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All lines should remain as-is (not expanded)
	if !strings.Contains(result, "../etc/passwd.md") {
		t.Error("should keep traversal link as-is")
	}
	if !strings.Contains(result, "sub/dir.md") {
		t.Error("should keep subdir link as-is")
	}
}

func TestLoadMemoryFrom_MissingDetailFile(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(
		"- [link](nonexistent.md)\n- kept line\n",
	), 0644)

	result, err := LoadMemoryFrom(dir, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Missing file: keep the pointer line as-is
	if !strings.Contains(result, "nonexistent.md") {
		t.Error("should keep pointer to missing file as-is")
	}
	if !strings.Contains(result, "kept line") {
		t.Error("should keep other lines")
	}
}

func TestLoadMemoryFrom_SkipsSelfReference(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(
		"- [self](MEMORY.md)\n",
	), 0644)

	result, err := LoadMemoryFrom(dir, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "MEMORY.md") {
		t.Error("should keep self-reference as-is without infinite loop")
	}
}

func TestLoadMemoryFrom_RespectsMaxLines(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(
		"- line 1\n- [link](big.md)\n- line after\n",
	), 0644)
	// Detail file has 10 lines
	var bigLines []string
	for i := 0; i < 10; i++ {
		bigLines = append(bigLines, "- big line")
	}
	os.WriteFile(filepath.Join(dir, "big.md"), []byte(strings.Join(bigLines, "\n")), 0644)

	// maxLines=5: should get line 1 + first 4 lines from big.md, then stop
	result, err := LoadMemoryFrom(dir, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lines := strings.Split(result, "\n")
	if len(lines) > 5 {
		t.Errorf("should respect maxLines, got %d lines", len(lines))
	}
	if strings.Contains(result, "line after") {
		t.Error("should not include lines past maxLines limit")
	}
}
