package agent

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// ToolFamilies maps tool names to their logical family for grouping
// related tools in loop detection (e.g., web_search + web_fetch = "web").
var ToolFamilies = map[string]string{
	"web_search": "web",
	"web_fetch":  "web",
}

// fillerWords are common query padding that don't affect semantic meaning.
var fillerWords = map[string]bool{
	"today":     true,
	"yesterday": true,
	"latest":    true,
	"recent":    true,
	"top":       true,
	"major":     true,
	"breaking":  true,
	"headlines": true,
	"news":      true,
	"current":   true,
	"update":    true,
	"updates":   true,
}

// isoDatePattern matches YYYY-MM-DD dates.
var isoDatePattern = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)

// monthDayYearPattern matches "March 2 2026" or "March 02 2026".
var monthDayYearPattern = regexp.MustCompile(`(?i)\b(?:January|February|March|April|May|June|July|August|September|October|November|December)\s+\d{1,2}\s+\d{4}\b`)

// dayMonthYearPattern matches "2 March 2026" or "02 March 2026".
var dayMonthYearPattern = regexp.MustCompile(`(?i)\b\d{1,2}\s+(?:January|February|March|April|May|June|July|August|September|October|November|December)\s+\d{4}\b`)

// standaloneYearPattern matches 4-digit years (2000-2099) as standalone tokens.
var standaloneYearPattern = regexp.MustCompile(`\b20\d{2}\b`)

// urlPattern matches http/https URLs (domain + path, excluding query strings).
// Captures the full URL minus trailing punctuation and query params.
var urlPattern = regexp.MustCompile(`https?://[^\s"'<>\])\},]+`)

// normalizeWebQuery extracts a search query from JSON args, strips dates and
// filler words, sorts remaining tokens, and returns a canonical form.
// Two queries about the same topic with different date/filler noise will
// produce the same normalized string and thus the same hash.
func normalizeWebQuery(argsJSON string) string {
	// Try to extract the query string from known JSON keys.
	var raw map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return ""
	}

	query := ""
	for _, key := range []string{"query", "q", "queries", "url", "urls"} {
		if v, ok := raw[key]; ok {
			switch val := v.(type) {
			case string:
				query = val
			case []any:
				// Take first element if it's a string array.
				if len(val) > 0 {
					if s, ok := val[0].(string); ok {
						query = s
					}
				}
			}
			if query != "" {
				break
			}
		}
	}

	if query == "" {
		return ""
	}

	// For URL values, return as-is (no token stripping).
	if strings.HasPrefix(query, "http://") || strings.HasPrefix(query, "https://") {
		return query
	}

	// Strip date patterns.
	query = isoDatePattern.ReplaceAllString(query, " ")
	query = monthDayYearPattern.ReplaceAllString(query, " ")
	query = dayMonthYearPattern.ReplaceAllString(query, " ")
	query = standaloneYearPattern.ReplaceAllString(query, " ")

	// Tokenize, strip punctuation, filter fillers and short tokens.
	tokens := strings.Fields(query)
	var cleaned []string
	for _, tok := range tokens {
		// Trim punctuation from edges.
		tok = strings.TrimFunc(tok, func(r rune) bool {
			return unicode.IsPunct(r) || unicode.IsSymbol(r)
		})
		tok = strings.ToLower(tok)
		if len(tok) < 2 {
			continue
		}
		if fillerWords[tok] {
			continue
		}
		cleaned = append(cleaned, tok)
	}

	sort.Strings(cleaned)
	if len(cleaned) == 0 {
		// All tokens were filler/dates — return a sentinel so all-filler
		// queries match each other (prevents bypassing topic detection).
		return "[empty]"
	}
	return strings.Join(cleaned, " ")
}

// extractResultSignature extracts unique URLs (domain+path) from text content,
// strips query strings and trailing punctuation, then hashes the sorted set.
// More granular than domain-only: reuters.com/climate ≠ reuters.com/economics.
func extractResultSignature(content string) string {
	matches := urlPattern.FindAllString(content, -1)
	if len(matches) == 0 {
		return ""
	}

	seen := make(map[string]bool)
	var urls []string
	for _, u := range matches {
		// Strip query string
		if idx := strings.IndexByte(u, '?'); idx != -1 {
			u = u[:idx]
		}
		// Strip fragment
		if idx := strings.IndexByte(u, '#'); idx != -1 {
			u = u[:idx]
		}
		// Trim trailing punctuation that leaked in
		u = strings.TrimRight(u, ".,;:!)")
		u = strings.ToLower(u)
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}

	sort.Strings(urls)
	return strings.Join(urls, ",")
}
