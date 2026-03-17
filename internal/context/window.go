package context

import (
	"math"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

const (
	// charsPerToken is the conservative estimation ratio.
	// 3.5 chars/token handles mixed English/code/CJK better than 4.
	charsPerToken = 3.5

	// overheadPerMessage accounts for role, formatting, and separator tokens.
	overheadPerMessage = 4

	// compactThreshold is the fraction of context window that triggers compaction.
	compactThreshold = 0.85

	// defaultKeepLast is the default number of recent turn pairs to keep.
	defaultKeepLast = 20

	// minKeepLast is the minimum recent turn pairs to keep, even under budget pressure.
	minKeepLast = 3
)

// MinShapeable returns the minimum number of messages needed for shaping to
// have any effect: system + first user + at least minKeepLast turn pairs.
func MinShapeable() int {
	return 3 + minKeepLast*2 // 9
}

// EstimateTokens returns a heuristic token count for a slice of messages.
// Uses chars/3.5 + 4 overhead per message.
func EstimateTokens(messages []client.Message) int {
	total := 0
	for _, m := range messages {
		chars := countChars(m)
		tokens := int(math.Ceil(float64(chars) / charsPerToken))
		total += tokens + overheadPerMessage
	}
	return total
}

// ShouldCompact returns true if the total tokens (input + output) exceed
// 85% of the context window.
func ShouldCompact(inputTokens, outputTokens, contextWindow int) bool {
	if contextWindow <= 0 {
		return false
	}
	threshold := int(float64(contextWindow) * compactThreshold)
	return inputTokens+outputTokens >= threshold
}

// ShapeHistory builds a sliding window over messages:
//
//	[system] + [first user message] + [summary] + [last N turn pairs]
//
// If the history is short enough to fit without shaping, it's returned as-is.
// After shaping, if estimated tokens still exceed the context window,
// keepLast is reduced iteratively down to minKeepLast.
func ShapeHistory(messages []client.Message, summary string, contextWindow int) []client.Message {
	// Skip shaping if too few messages to meaningfully shape (need system + first user + at least minKeepLast pairs)
	if len(messages) <= 3+minKeepLast*2 {
		return messages
	}
	// Skip if both message count is low AND estimated tokens fit in budget
	if len(messages) <= 3+defaultKeepLast*2 && (contextWindow <= 0 || EstimateTokens(messages) < contextWindow) {
		return messages
	}

	// Extract system message (index 0) and first user message
	system := messages[0]
	firstUser := messages[1]

	// All remaining messages after system + first user
	rest := messages[2:]

	keepLast := defaultKeepLast
	for keepLast >= minKeepLast {
		shaped := buildShaped(system, firstUser, summary, rest, keepLast)
		if contextWindow <= 0 || EstimateTokens(shaped) < contextWindow {
			return shaped
		}
		keepLast--
	}

	// Floor: return with minKeepLast even if over budget
	return buildShaped(system, firstUser, summary, rest, minKeepLast)
}

// buildShaped assembles the shaped message array.
func buildShaped(system, firstUser client.Message, summary string, rest []client.Message, keepLast int) []client.Message {
	keepMsgs := keepLast * 2 // turn pairs = user + assistant
	if keepMsgs > len(rest) {
		keepMsgs = len(rest)
	}

	recent := rest[len(rest)-keepMsgs:]

	result := make([]client.Message, 0, 3+len(recent))
	result = append(result, system, firstUser)

	if summary != "" {
		result = append(result, client.Message{
			Role:    "user",
			Content: client.NewTextContent("Previous context summary: " + summary),
		})
	}

	result = append(result, recent...)
	return result
}

// imageTokenEstimate is the approximate token cost of an image block.
// Anthropic charges ~1600 tokens for a typical image; 1000 is a conservative floor.
const imageTokenChars = 3500 // 1000 tokens * 3.5 chars/token

// countChars counts total characters in a message's content.
// Images are estimated as a fixed char cost since their base64 data is not
// representative of actual token usage.
func countChars(m client.Message) int {
	if m.Content.HasBlocks() {
		total := 0
		for _, b := range m.Content.Blocks() {
			switch b.Type {
			case "text":
				total += len([]rune(b.Text))
			case "tool_use":
				total += len([]rune(b.Name)) + len(b.Input)
			case "tool_result":
				total += countToolResultChars(b)
			case "image":
				total += imageTokenChars
			}
		}
		return total
	}
	return len([]rune(m.Content.Text()))
}

// countToolResultChars counts chars in a tool_result, including nested blocks.
func countToolResultChars(b client.ContentBlock) int {
	switch v := b.ToolContent.(type) {
	case string:
		return len([]rune(v))
	case []client.ContentBlock:
		total := 0
		for _, nb := range v {
			switch nb.Type {
			case "text":
				total += len([]rune(nb.Text))
			case "image":
				total += imageTokenChars
			}
		}
		return total
	}
	return 0
}
