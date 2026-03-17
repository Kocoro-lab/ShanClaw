package context

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

const (
	// maxMemoryLines is the maximum number of lines in MEMORY.md.
	// Exceeding this triggers overflow to a detail file.
	maxMemoryLines = 150

	// consolidateThreshold is the minimum number of auto-*.md files before GC runs.
	consolidateThreshold = 12
	// consolidateCooldown is the minimum time between consolidation runs.
	consolidateCooldown = 7 * 24 * time.Hour

	consolidatePrompt = `You are consolidating an AI agent's auto-persisted memory entries.

These entries were extracted automatically from past conversations. Many are duplicated or superseded by newer entries.

Merge them into a single clean list:
- Deduplicate facts that say the same thing
- When entries conflict, keep the most recent version
- Drop ephemeral or stale information no longer useful
- Keep: decisions, preferences, patterns, gotchas, references, contacts, people

Output a markdown bulleted list, one fact per bullet (max ~100 chars each).
Target: under 100 lines total.
If nothing worth keeping survives, return exactly "NONE".`

	persistPrompt = `You are extracting durable knowledge from a conversation before context is compacted.

Review the conversation and identify facts worth remembering in FUTURE conversations. Focus on:
- Decisions made (technical, design, business, or personal preferences)
- User corrections or preferences about how they want to work
- Important facts about projects, people, systems, or environments
- Patterns, gotchas, or insights discovered
- Configuration, setup, or process details that were hard to find
- Contacts, resources, or reference information mentioned

Do NOT include:
- Current task progress or status (captured separately)
- Verbatim code, file contents, or command output
- Ephemeral information only relevant to this conversation
- Things already present in the existing memory shown below

Format rules:
- Return a markdown bulleted list, one fact per bullet
- Each bullet should be a SHORT one-line summary (max ~100 chars)
- If a fact needs more detail, note "(detail)" at the end — it will be expanded separately
- If nothing new is worth persisting, return exactly "NONE"`
)

// PersistLearnings extracts durable knowledge from a conversation and appends
// it to MEMORY.md before context compaction discards the messages.
// memoryDir is the directory containing MEMORY.md (e.g. ~/.shannon/memory/ or
// ~/.shannon/agents/<name>/).
// Returns nil if nothing worth persisting, or if memoryDir is empty.
func PersistLearnings(ctx context.Context, c Completer, messages []client.Message, memoryDir string) error {
	if memoryDir == "" {
		return nil
	}

	// Read existing memory to include in prompt (avoids duplicate extraction)
	memoryPath := filepath.Join(memoryDir, "MEMORY.md")
	existingMemory, _ := os.ReadFile(memoryPath)

	// Build conversation transcript
	var transcript strings.Builder
	for _, m := range messages {
		if m.Role == "system" {
			continue
		}
		text := messageText(m)
		if text == "" {
			continue
		}
		fmt.Fprintf(&transcript, "[%s]: %s\n\n", m.Role, text)
	}

	if transcript.Len() == 0 {
		return nil
	}

	// Build the user message with existing memory context
	var userMsg strings.Builder
	if len(existingMemory) > 0 {
		fmt.Fprintf(&userMsg, "## Existing Memory (do not duplicate)\n\n%s\n\n---\n\n", string(existingMemory))
	}
	fmt.Fprintf(&userMsg, "## Conversation to Extract From\n\n%s", transcript.String())

	req := client.CompletionRequest{
		Messages: []client.Message{
			{Role: "system", Content: client.NewTextContent(persistPrompt)},
			{Role: "user", Content: client.NewTextContent(userMsg.String())},
		},
		ModelTier:   "small",
		Temperature: 0.2,
		MaxTokens:   1000,
	}

	resp, err := c.Complete(ctx, req)
	if err != nil {
		return fmt.Errorf("persist learnings failed: %w", err)
	}

	result := strings.TrimSpace(resp.OutputText)
	if result == "" || strings.EqualFold(result, "NONE") {
		return nil
	}

	// Wrap with header so auto-persisted entries are distinguishable
	timestamp := time.Now().Format("2006-01-02 15:04")
	entry := fmt.Sprintf("\n## Auto-persisted (%s)\n\n%s", timestamp, result)
	return BoundedAppend(memoryDir, entry)
}

// MaxMemoryLines is the maximum number of lines in MEMORY.md before overflow
// to a detail file. Exported so callers (memory_append tool) share the constant.
const MaxMemoryLines = maxMemoryLines

// BoundedAppend appends content to MEMORY.md in memoryDir, respecting the
// line limit. If appending would exceed maxMemoryLines, content is written
// to a timestamped detail file and a one-line pointer is added to MEMORY.md.
// Read, decide, and write all happen under a single flock so concurrent
// callers cannot both pass the line-count check simultaneously.
func BoundedAppend(memoryDir, content string) error {
	if err := os.MkdirAll(memoryDir, 0755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}

	memoryPath := filepath.Join(memoryDir, "MEMORY.md")
	lockPath := memoryPath + ".lock"

	// Acquire exclusive lock — hold for the entire read+decide+write cycle.
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open lock: %w", err)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) //nolint:errcheck

	// Read existing file under lock
	existing, _ := os.ReadFile(memoryPath)
	// Build the exact content that would be written to MEMORY.md, including
	// the optional leading newline separator.
	writeContent := content
	if len(existing) > 0 && !strings.HasPrefix(content, "\n") {
		writeContent = "\n" + writeContent
	}

	projectedLines := countLines(append(append([]byte{}, existing...), []byte(writeContent)...))

	// If appending would exceed the limit, write overflow to a detail file
	// and add a one-line pointer in MEMORY.md instead.
	if projectedLines > maxMemoryLines {
		detailFile, err := writeDetailFile(memoryDir, content)
		if err != nil {
			return fmt.Errorf("write detail file: %w", err)
		}
		timestamp := time.Now().Format("2006-01-02")
		writeContent = fmt.Sprintf("\n- [%s] See [%s](%s) for details\n",
			timestamp, detailFile, detailFile)
	}

	// Append under lock
	f, err := os.OpenFile(memoryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(writeContent)
	return err
}

// writeDetailFile creates a timestamped detail file in memoryDir and returns
// the filename (not full path, for use in markdown links).
func writeDetailFile(memoryDir, content string) (string, error) {
	b := make([]byte, 3)
	rand.Read(b)
	suffix := hex.EncodeToString(b)

	timestamp := time.Now().Format("2006-01-02")
	filename := fmt.Sprintf("auto-%s-%s.md", timestamp, suffix)
	path := filepath.Join(memoryDir, filename)

	body := fmt.Sprintf("# Auto-persisted Learnings (%s)\n\n%s\n", timestamp, content)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		return "", err
	}
	return filename, nil
}

// countLines counts the number of lines in content.
func countLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	n := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		n++ // last line without trailing newline
	}
	return n
}

// ConsolidateMemory merges auto-persisted detail files and auto sections in
// MEMORY.md into a single deduplicated block via an LLM call. User-written
// memory is preserved verbatim.
// Runs only when ≥12 auto-*.md files exist and last GC was ≥7 days ago.
// Safe for concurrent use (flock on MEMORY.md.lock).
func ConsolidateMemory(ctx context.Context, c Completer, memoryDir string) error {
	if memoryDir == "" {
		return nil
	}

	// Check threshold: need ≥12 auto-*.md files
	autoFiles, err := filepath.Glob(filepath.Join(memoryDir, "auto-*.md"))
	if err != nil || len(autoFiles) < consolidateThreshold {
		return nil
	}

	// Check cooldown: skip if .memory_gc was touched < 7 days ago
	markerPath := filepath.Join(memoryDir, ".memory_gc")
	if info, err := os.Stat(markerPath); err == nil {
		if time.Since(info.ModTime()) < consolidateCooldown {
			return nil
		}
	}

	// Acquire flock (same lock as BoundedAppend)
	memoryPath := filepath.Join(memoryDir, "MEMORY.md")
	lockPath := memoryPath + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("consolidate: open lock: %w", err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("consolidate: flock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) //nolint:errcheck

	// Read and split MEMORY.md into user vs auto content
	existing, _ := os.ReadFile(memoryPath)
	userContent, autoFromMemory := splitMemory(string(existing))

	// Read all auto-*.md file contents
	var autoContent strings.Builder
	var consumedFiles []string
	if autoFromMemory != "" {
		autoContent.WriteString(autoFromMemory)
		autoContent.WriteString("\n\n")
	}
	for _, f := range autoFiles {
		data, readErr := os.ReadFile(f)
		if readErr != nil {
			// Still mark as consumed — if a file is permanently unreadable,
			// leaving it blocks future consolidations from making progress.
			consumedFiles = append(consumedFiles, f)
			continue
		}
		consumedFiles = append(consumedFiles, f)
		autoContent.WriteString(string(data))
		autoContent.WriteString("\n")
	}

	if autoContent.Len() == 0 {
		return nil
	}

	// LLM consolidation call
	if c == nil {
		return nil // no completer (skip-logic tests)
	}

	req := client.CompletionRequest{
		Messages: []client.Message{
			{Role: "system", Content: client.NewTextContent(consolidatePrompt)},
			{Role: "user", Content: client.NewTextContent(autoContent.String())},
		},
		ModelTier:   "small",
		Temperature: 0.2,
		MaxTokens:   2000,
	}

	resp, err := c.Complete(ctx, req)
	if err != nil {
		return fmt.Errorf("consolidate: LLM call failed: %w", err)
	}

	consolidated := strings.TrimSpace(resp.OutputText)

	// Rebuild MEMORY.md: user content + consolidated auto section
	var result strings.Builder
	if userContent != "" {
		result.WriteString(userContent)
	}
	if consolidated != "" && !strings.EqualFold(consolidated, "NONE") {
		if result.Len() > 0 {
			result.WriteString("\n\n")
		}
		timestamp := time.Now().Format("2006-01-02 15:04")
		fmt.Fprintf(&result, "## Auto-consolidated (%s)\n\n%s\n", timestamp, consolidated)
	}

	// Atomic write: temp file + rename
	tmpPath := memoryPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(result.String()), 0644); err != nil {
		return fmt.Errorf("consolidate: write temp: %w", err)
	}
	if err := os.Rename(tmpPath, memoryPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("consolidate: rename: %w", err)
	}

	// Delete consumed auto-*.md files
	for _, f := range consumedFiles {
		os.Remove(f)
	}

	// Touch marker — failure here means consolidation would re-trigger next run,
	// wasting an LLM call. Return the error so callers can log it.
	if err := os.WriteFile(markerPath, []byte(time.Now().Format(time.RFC3339)), 0644); err != nil {
		return fmt.Errorf("consolidate: write marker: %w", err)
	}

	return nil
}

// splitMemory separates MEMORY.md content into user-written lines and
// auto-persisted lines. Auto content starts at "## Auto-persisted" or
// "## Auto-consolidated" headers, and includes "See [auto-" pointer lines.
// Returns trimmed strings for each section.
func splitMemory(content string) (userContent, autoContent string) {
	lines := strings.Split(content, "\n")
	var user, auto []string
	inAuto := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect auto section headers
		if strings.HasPrefix(trimmed, "## Auto-persisted") || strings.HasPrefix(trimmed, "## Auto-consolidated") {
			inAuto = true
			auto = append(auto, line)
			continue
		}

		// Detect auto-*.md pointer lines — only match the exact format
		// produced by BoundedAppend: "- [date] See [auto-YYYY-MM-DD-hex.md](...) for details"
		if strings.HasPrefix(trimmed, "- [") && strings.Contains(trimmed, "See [auto-") && strings.HasSuffix(trimmed, "for details") {
			auto = append(auto, line)
			continue
		}

		// A new non-auto ## header ends the auto section
		if inAuto && strings.HasPrefix(trimmed, "## ") {
			inAuto = false
		}

		if inAuto {
			auto = append(auto, line)
		} else {
			user = append(user, line)
		}
	}

	return strings.TrimSpace(strings.Join(user, "\n")),
		strings.TrimSpace(strings.Join(auto, "\n"))
}
