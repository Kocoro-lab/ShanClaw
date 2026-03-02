package tui

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
)

// Matches 2+ consecutive blank-looking lines (may contain whitespace or ANSI escapes)
var blankLineRe = regexp.MustCompile(`(\n[ \t]*(\x1b\[[0-9;]*m)*[ \t]*){3,}`)

// Cached renderer and the width it was built for.
var (
	cachedRenderer *glamour.TermRenderer
	cachedWidth    int
)

// compactStyle is a Claude Code-inspired style: no margins, minimal spacing,
// bold headings without color backgrounds, compact lists.
var compactStyle = ansi.StyleConfig{
	Document: ansi.StyleBlock{
		// No Color — use terminal's default foreground (white on dark backgrounds).
		// Setting an explicit color (e.g. 252) dims all text below terminal default.
		Margin: uintPtr(0),
	},
	BlockQuote: ansi.StyleBlock{
		Indent:      uintPtr(1),
		IndentToken: stringPtr("│ "),
		StylePrimitive: ansi.StylePrimitive{
			Italic: boolPtr(true),
		},
	},
	Paragraph: ansi.StyleBlock{},
	List: ansi.StyleList{
		LevelIndent: 2,
	},
	Heading: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	H1: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold:      boolPtr(true),
			Italic:    boolPtr(true),
			Underline: boolPtr(true),
		},
	},
	H2: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	H3: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	H4: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	H5: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	H6: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Bold: boolPtr(true),
		},
	},
	Strikethrough: ansi.StylePrimitive{
		CrossedOut: boolPtr(true),
	},
	Emph: ansi.StylePrimitive{
		Italic: boolPtr(true),
	},
	Strong: ansi.StylePrimitive{
		Bold: boolPtr(true),
	},
	HorizontalRule: ansi.StylePrimitive{
		Color:  stringPtr("240"),
		Format: "--------",
	},
	Item: ansi.StylePrimitive{
		BlockPrefix: "• ",
	},
	Enumeration: ansi.StylePrimitive{
		BlockPrefix: ". ",
	},
	Task: ansi.StyleTask{
		Ticked:   "[✓] ",
		Unticked: "[ ] ",
	},
	Link: ansi.StylePrimitive{
		Color:     stringPtr("30"),
		Underline: boolPtr(true),
	},
	LinkText: ansi.StylePrimitive{
		Bold: boolPtr(true),
	},
	Image: ansi.StylePrimitive{
		Color:     stringPtr("212"),
		Underline: boolPtr(true),
	},
	Code: ansi.StyleBlock{
		StylePrimitive: ansi.StylePrimitive{
			Color: stringPtr("203"),
		},
	},
	CodeBlock: ansi.StyleCodeBlock{
		StyleBlock: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr("244"),
			},
			Margin: uintPtr(0),
		},
		Chroma: &ansi.Chroma{
			Text:              ansi.StylePrimitive{Color: stringPtr("#C4C4C4")},
			Error:             ansi.StylePrimitive{Color: stringPtr("#F1F1F1"), BackgroundColor: stringPtr("#F05B5B")},
			Comment:           ansi.StylePrimitive{Color: stringPtr("#676767")},
			CommentPreproc:    ansi.StylePrimitive{Color: stringPtr("#FF875F")},
			Keyword:           ansi.StylePrimitive{Color: stringPtr("#00AAFF")},
			KeywordReserved:   ansi.StylePrimitive{Color: stringPtr("#FF5FD2")},
			KeywordNamespace:  ansi.StylePrimitive{Color: stringPtr("#FF5F87")},
			KeywordType:       ansi.StylePrimitive{Color: stringPtr("#6E6ED8")},
			Operator:          ansi.StylePrimitive{Color: stringPtr("#EF8080")},
			Punctuation:       ansi.StylePrimitive{Color: stringPtr("#E8E8A8")},
			Name:              ansi.StylePrimitive{Color: stringPtr("#C4C4C4")},
			NameBuiltin:       ansi.StylePrimitive{Color: stringPtr("#FF8EC7")},
			NameTag:           ansi.StylePrimitive{Color: stringPtr("#B083EA")},
			NameAttribute:     ansi.StylePrimitive{Color: stringPtr("#7A7AE6")},
			NameClass:         ansi.StylePrimitive{Color: stringPtr("#F1F1F1"), Underline: boolPtr(true), Bold: boolPtr(true)},
			NameDecorator:     ansi.StylePrimitive{Color: stringPtr("#FFFF87")},
			NameFunction:      ansi.StylePrimitive{Color: stringPtr("#00D787")},
			LiteralNumber:     ansi.StylePrimitive{Color: stringPtr("#6EEFC0")},
			LiteralString:     ansi.StylePrimitive{Color: stringPtr("#C69669")},
			LiteralStringEscape: ansi.StylePrimitive{Color: stringPtr("#AFFFD7")},
			GenericDeleted:    ansi.StylePrimitive{Color: stringPtr("#FD5B5B")},
			GenericEmph:       ansi.StylePrimitive{Italic: boolPtr(true)},
			GenericInserted:   ansi.StylePrimitive{Color: stringPtr("#00D787")},
			GenericStrong:     ansi.StylePrimitive{Bold: boolPtr(true)},
			GenericSubheading: ansi.StylePrimitive{Color: stringPtr("#777777")},
		},
	},
	Table:  ansi.StyleTable{},
}

// getRenderer returns a glamour renderer sized to the given terminal width.
// The renderer is cached and only rebuilt when the width changes.
func getRenderer(width int) *glamour.TermRenderer {
	if width <= 0 {
		width = 120
	}
	if cachedRenderer != nil && cachedWidth == width {
		return cachedRenderer
	}
	styleJSON, err := json.Marshal(compactStyle)
	if err != nil {
		return nil
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(styleJSON),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	cachedRenderer = r
	cachedWidth = width
	return r
}

// renderMarkdown renders markdown text with ANSI styling.
// Width should be the current terminal width (for correct table rendering).
// Falls back to plain text if the renderer is unavailable.
func renderMarkdown(text string, width int) string {
	r := getRenderer(width)
	if r == nil || text == "" {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	// Collapse excessive blank lines (glamour may still produce some)
	out = blankLineRe.ReplaceAllString(out, "\n\n")
	out = strings.TrimRight(out, "\n ")
	return out
}

func stringPtr(s string) *string { return &s }
func uintPtr(u uint) *uint       { return &u }
func boolPtr(b bool) *bool       { return &b }
