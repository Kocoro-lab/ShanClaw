package context

import (
	"testing"

	"github.com/Kocoro-lab/shan/internal/client"
)

func TestEstimateTokens(t *testing.T) {
	t.Run("empty messages", func(t *testing.T) {
		got := EstimateTokens(nil)
		if got != 0 {
			t.Errorf("EstimateTokens(nil) = %d, want 0", got)
		}
	})

	t.Run("single text message", func(t *testing.T) {
		msgs := []client.Message{
			{Role: "user", Content: client.NewTextContent("hello world")}, // 11 chars
		}
		got := EstimateTokens(msgs)
		// 11 chars / 3.5 ≈ 3.14, ceil = 4, + 4 overhead = 8
		want := 8
		if got != want {
			t.Errorf("EstimateTokens = %d, want %d", got, want)
		}
	})

	t.Run("multiple messages accumulate", func(t *testing.T) {
		msgs := []client.Message{
			{Role: "system", Content: client.NewTextContent("You are a helpful assistant.")}, // 28 chars
			{Role: "user", Content: client.NewTextContent("hello")},                          // 5 chars
			{Role: "assistant", Content: client.NewTextContent("Hi there!")},                  // 9 chars
		}
		got := EstimateTokens(msgs)
		// (28/3.5 + 4) + (5/3.5 + 4) + (9/3.5 + 4) = 12+4 + 2+4 + 3+4 = 29
		// ceil(28/3.5)=8, ceil(5/3.5)=2, ceil(9/3.5)=3 → 8+4 + 2+4 + 3+4 = 25
		want := 25
		if got != want {
			t.Errorf("EstimateTokens = %d, want %d", got, want)
		}
	})

	t.Run("block content with tool results", func(t *testing.T) {
		blocks := []client.ContentBlock{
			{Type: "text", Text: "result data here"}, // 16 chars
		}
		msgs := []client.Message{
			{Role: "user", Content: client.NewBlockContent(blocks)},
		}
		got := EstimateTokens(msgs)
		// ceil(16/3.5) + 4 = 5 + 4 = 9
		want := 9
		if got != want {
			t.Errorf("EstimateTokens = %d, want %d", got, want)
		}
	})
}

func TestShouldCompact(t *testing.T) {
	t.Run("below threshold returns false", func(t *testing.T) {
		// 85% of 128000 = 108800
		got := ShouldCompact(50000, 1000, 128000)
		if got {
			t.Error("ShouldCompact should be false when well below threshold")
		}
	})

	t.Run("input plus output above threshold returns true", func(t *testing.T) {
		// 100000 + 10000 = 110000 > 108800
		got := ShouldCompact(100000, 10000, 128000)
		if !got {
			t.Error("ShouldCompact should be true when input+output exceeds 85% of context window")
		}
	})

	t.Run("exactly at threshold returns true", func(t *testing.T) {
		// 85% of 128000 = 108800
		got := ShouldCompact(100000, 8800, 128000)
		if !got {
			t.Error("ShouldCompact should be true at exactly the threshold")
		}
	})

	t.Run("zero context window returns false", func(t *testing.T) {
		got := ShouldCompact(100000, 10000, 0)
		if got {
			t.Error("ShouldCompact should be false with zero context window")
		}
	})
}

func TestShapeHistory(t *testing.T) {
	// Helper to build a sequence of user/assistant turn pairs
	makeTurns := func(n int) []client.Message {
		var msgs []client.Message
		for i := 0; i < n; i++ {
			msgs = append(msgs,
				client.Message{Role: "user", Content: client.NewTextContent("user msg " + string(rune('A'+i)))},
				client.Message{Role: "assistant", Content: client.NewTextContent("assistant msg " + string(rune('A'+i)))},
			)
		}
		return msgs
	}

	t.Run("short history unchanged", func(t *testing.T) {
		system := client.Message{Role: "system", Content: client.NewTextContent("system prompt")}
		turns := makeTurns(3)
		all := append([]client.Message{system}, turns...)

		got := ShapeHistory(all, "summary text", 128000)

		// History is small, no shaping needed — should return original unchanged
		if len(got) != len(all) {
			t.Errorf("ShapeHistory should not shape short history, got %d messages, want %d", len(got), len(all))
		}
	})

	t.Run("long history is shaped with summary", func(t *testing.T) {
		system := client.Message{Role: "system", Content: client.NewTextContent("system prompt")}
		turns := makeTurns(30) // 60 messages
		all := append([]client.Message{system}, turns...)

		got := ShapeHistory(all, "summary of dropped turns", 128000)

		// Should have: system + first user msg + summary + last N turn pairs
		if len(got) < 5 {
			t.Fatalf("ShapeHistory returned too few messages: %d", len(got))
		}

		// First message is system
		if got[0].Role != "system" {
			t.Errorf("first message should be system, got %s", got[0].Role)
		}

		// Second message is the first user message (primer)
		if got[1].Role != "user" {
			t.Errorf("second message should be first user message, got %s", got[1].Role)
		}
		if got[1].Content.Text() != "user msg A" {
			t.Errorf("second message should be original first user msg, got %q", got[1].Content.Text())
		}

		// Third message is the summary injection
		if got[2].Role != "user" {
			t.Errorf("third message should be summary (user role), got %s", got[2].Role)
		}
		if got[2].Content.Text() == "" {
			t.Error("summary message should not be empty")
		}

		// Last message should be the last assistant message from original
		lastOrig := all[len(all)-1]
		lastShaped := got[len(got)-1]
		if lastShaped.Content.Text() != lastOrig.Content.Text() {
			t.Errorf("last message should be preserved, got %q want %q",
				lastShaped.Content.Text(), lastOrig.Content.Text())
		}

		// Should be shorter than original
		if len(got) >= len(all) {
			t.Errorf("shaped history should be shorter: got %d, original %d", len(got), len(all))
		}
	})

	t.Run("budget-aware shrinking", func(t *testing.T) {
		system := client.Message{Role: "system", Content: client.NewTextContent("system prompt")}
		// Create turns with large content to force budget-aware shrinking
		var turns []client.Message
		bigContent := make([]byte, 10000)
		for i := range bigContent {
			bigContent[i] = 'x'
		}
		for i := 0; i < 30; i++ {
			turns = append(turns,
				client.Message{Role: "user", Content: client.NewTextContent(string(bigContent))},
				client.Message{Role: "assistant", Content: client.NewTextContent(string(bigContent))},
			)
		}
		all := append([]client.Message{system}, turns...)

		// Small context window forces aggressive shrinking
		got := ShapeHistory(all, "summary", 5000)

		// Should keep minimum: system + first user + summary + at least 3 pairs (6 msgs)
		// Total minimum = 1 + 1 + 1 + 6 = 9
		if len(got) > len(all) {
			t.Errorf("shaped should not grow: got %d, original %d", len(got), len(all))
		}

		// Verify minimum floor of 3 recent pairs is maintained
		// Count non-system, non-primer, non-summary messages
		recentCount := len(got) - 3 // subtract system + primer + summary
		if recentCount < 6 {        // 3 pairs = 6 messages minimum
			t.Errorf("should keep at least 3 recent pairs (6 msgs), got %d recent msgs", recentCount)
		}
	})

	t.Run("few messages but over token budget still shapes", func(t *testing.T) {
		system := client.Message{Role: "system", Content: client.NewTextContent("system prompt")}
		// 10 turn pairs (20 msgs) — below old message-count gate of 43
		// but each message is huge, blowing the token budget
		bigContent := make([]byte, 50000)
		for i := range bigContent {
			bigContent[i] = 'x'
		}
		var turns []client.Message
		for i := 0; i < 10; i++ {
			turns = append(turns,
				client.Message{Role: "user", Content: client.NewTextContent(string(bigContent))},
				client.Message{Role: "assistant", Content: client.NewTextContent(string(bigContent))},
			)
		}
		all := append([]client.Message{system}, turns...)

		// Estimated tokens: ~10 * 2 * (50000/3.5 + 4) ≈ 285k, way over 5000
		got := ShapeHistory(all, "summary of prior work", 5000)

		// Should be shaped (shorter than original)
		if len(got) >= len(all) {
			t.Errorf("few-but-large messages should still be shaped: got %d, original %d", len(got), len(all))
		}

		// Should contain summary
		found := false
		for _, m := range got {
			if m.Content.Text() == "Previous context summary: summary of prior work" {
				found = true
				break
			}
		}
		if !found {
			t.Error("should contain summary message when token budget forces shaping")
		}
	})

	t.Run("empty summary skips injection", func(t *testing.T) {
		system := client.Message{Role: "system", Content: client.NewTextContent("system prompt")}
		turns := makeTurns(30)
		all := append([]client.Message{system}, turns...)

		got := ShapeHistory(all, "", 128000)

		// With empty summary and large window, should still shape but no summary message
		for _, m := range got {
			if m.Role == "user" && m.Content.Text() != "" {
				// Make sure no "Previous context summary" message exists
				text := m.Content.Text()
				if len(text) > 25 && text[:25] == "Previous context summary:" {
					t.Error("should not inject empty summary")
				}
			}
		}
	})
}
