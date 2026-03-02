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

// urlPattern matches http/https URLs for domain extraction.
var urlPattern = regexp.MustCompile(`https?://([^/\s]+)`)

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
	return strings.Join(cleaned, " ")
}

// extractResultSignature extracts unique domains from URLs in text content.
// Used to detect when different queries return the same set of sources.
func extractResultSignature(content string) string {
	matches := urlPattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return ""
	}

	seen := make(map[string]bool)
	var domains []string
	for _, m := range matches {
		domain := strings.ToLower(m[1])
		if !seen[domain] {
			seen[domain] = true
			domains = append(domains, domain)
		}
	}

	sort.Strings(domains)
	return strings.Join(domains, ",")
}
