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
	"time"

	"github.com/Kocoro-lab/shan/internal/client"
)

const (
	// maxMemoryLines is the maximum number of lines in MEMORY.md.
	// Exceeding this triggers overflow to a detail file.
	maxMemoryLines = 150

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

	// Ensure directory exists
	if err := os.MkdirAll(memoryDir, 0755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}

	// Count existing lines
	existingLines := countLines(existingMemory)

	// Count new lines to add
	newLines := strings.Count(result, "\n") + 1

	// If appending would exceed the limit, write overflow to a detail file
	// and add a one-line pointer in MEMORY.md instead
	// +3 accounts for "## Auto-persisted (date)" header + surrounding blank lines
	if existingLines+newLines+3 > maxMemoryLines {
		detailFile, err := writeDetailFile(memoryDir, result)
		if err != nil {
			return fmt.Errorf("write detail file: %w", err)
		}
		// Add a one-line pointer instead of full content
		timestamp := time.Now().Format("2006-01-02")
		entry := fmt.Sprintf("\n- [%s] See [%s](%s) for auto-persisted learnings\n",
			timestamp, detailFile, detailFile)
		return appendToFile(memoryPath, entry)
	}

	// Append directly to MEMORY.md
	timestamp := time.Now().Format("2006-01-02 15:04")
	entry := fmt.Sprintf("\n\n## Auto-persisted (%s)\n\n%s\n", timestamp, result)
	return appendToFile(memoryPath, entry)
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

// appendToFile appends content to a file, creating it if needed.
func appendToFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
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
