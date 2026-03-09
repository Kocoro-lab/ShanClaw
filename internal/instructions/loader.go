package instructions

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Kocoro-lab/shan/internal/agents"
)

// maxCommandFileChars is the maximum character count for a single custom command file.
const maxCommandFileChars = 8000

// LoadInstructions reads all instruction files and returns combined content.
// shannonDir is the global config directory (e.g. ~/.shannon).
// projectDir is the project-level directory (e.g. .shannon relative to CWD).
// maxTokens is an approximate budget (1 token ~ 4 chars).
// Returns the combined instruction text, truncated if over budget.
func LoadInstructions(shannonDir string, projectDir string, maxTokens int) (string, error) {
	type source struct {
		path     string
		priority int // higher = higher priority
	}

	var sources []source
	priority := 0

	// 1. Global instructions
	if shannonDir != "" {
		sources = append(sources, source{filepath.Join(shannonDir, "instructions.md"), priority})
		priority++

		// 2. Global rules (sorted alphabetically)
		ruleFiles := sortedMDFiles(filepath.Join(shannonDir, "rules"))
		for _, rf := range ruleFiles {
			sources = append(sources, source{rf, priority})
			priority++
		}
	}

	// 3. Project instructions
	if projectDir != "" {
		sources = append(sources, source{filepath.Join(projectDir, "instructions.md"), priority})
		priority++

		// 4. Project rules
		ruleFiles := sortedMDFiles(filepath.Join(projectDir, "rules"))
		for _, rf := range ruleFiles {
			sources = append(sources, source{rf, priority})
			priority++
		}

		// 5. Project local
		sources = append(sources, source{filepath.Join(projectDir, "instructions.local.md"), priority})
		priority++
	}

	// Load file contents in order, tracking lines for deduplication.
	// Lines from higher-priority files take precedence.
	type fileContent struct {
		path     string
		lines    []string
		priority int
	}

	var loaded []fileContent
	for _, src := range sources {
		data, err := readMDFile(src.path)
		if err != nil {
			continue // file doesn't exist or isn't valid — skip
		}
		lines := strings.Split(data, "\n")
		loaded = append(loaded, fileContent{path: src.path, lines: lines, priority: src.priority})
	}

	// Deduplicate: track which non-empty, non-whitespace lines we've seen.
	// Process from highest priority to lowest. Keep only the highest-priority
	// occurrence of each line.
	seenLines := make(map[string]struct{})

	// First pass: collect all lines from highest priority, marking them as seen.
	// We process in reverse order (highest priority first) to build the seen set.
	for i := len(loaded) - 1; i >= 0; i-- {
		fc := &loaded[i]
		deduped := make([]string, 0, len(fc.lines))
		for _, line := range fc.lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				deduped = append(deduped, line)
				continue
			}
			if _, exists := seenLines[trimmed]; !exists {
				seenLines[trimmed] = struct{}{}
				deduped = append(deduped, line)
			}
		}
		fc.lines = deduped
	}

	// Build output in load order (lowest priority first).
	maxChars := maxTokens * 4
	var parts []string
	for _, fc := range loaded {
		content := strings.Join(fc.lines, "\n")
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		part := fmt.Sprintf("<!-- from: %s -->\n%s", fc.path, content)
		parts = append(parts, part)
	}

	result := strings.Join(parts, "\n\n")
	if len(result) > maxChars {
		result = result[:maxChars]
		result += "\n[Instructions truncated — reduce content in lower-priority files]"
	}

	return result, nil
}

// LoadMemory reads the MEMORY.md file from shannonDir/memory/MEMORY.md.
// Returns the first maxLines lines of the file.
// If the file doesn't exist, returns an empty string (not an error).
func LoadMemory(shannonDir string, maxLines int) (string, error) {
	if shannonDir == "" {
		return "", nil
	}
	path := filepath.Join(shannonDir, "memory", "MEMORY.md")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if len(lines) >= maxLines {
			break
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}

	return strings.Join(lines, "\n"), nil
}

// LoadCustomCommands scans for .md files in command directories.
// Returns a map of command name -> file content.
// Project commands override global commands with the same name.
// Built-in command names cannot be overridden and are skipped with a warning to stderr.
func LoadCustomCommands(shannonDir string, projectDir string) (map[string]string, error) {
	commands := make(map[string]string)

	// Load global commands first
	if shannonDir != "" {
		loadCommandDir(filepath.Join(shannonDir, "commands"), commands)
	}

	// Load project commands (overrides global)
	if projectDir != "" {
		loadCommandDir(filepath.Join(projectDir, "commands"), commands)
	}

	return commands, nil
}

// loadCommandDir scans a directory for .md files and adds them to the commands map.
func loadCommandDir(dir string, commands map[string]string) {
	files := sortedMDFiles(dir)
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".md")
		if agents.BuiltinCommands[name] {
			fmt.Fprintf(os.Stderr, "warning: custom command %q skipped — conflicts with built-in command\n", name)
			continue
		}
		data, err := readMDFile(path)
		if err != nil {
			continue
		}
		if len(data) > maxCommandFileChars {
			data = data[:maxCommandFileChars]
		}
		commands[name] = data
	}
}

// sortedMDFiles returns all .md files in dir, sorted alphabetically.
// Returns nil if the directory doesn't exist.
func sortedMDFiles(dir string) []string {
	pattern := filepath.Join(dir, "*.md")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)
	return matches
}

// readMDFile reads a file if it exists, is a .md file, and contains valid UTF-8.
// Returns the file contents or an error.
func readMDFile(path string) (string, error) {
	if filepath.Ext(path) != ".md" {
		return "", fmt.Errorf("not a .md file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("file is not valid UTF-8: %s", path)
	}
	return string(data), nil
}
