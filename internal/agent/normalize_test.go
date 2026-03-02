package agent

import (
	"testing"
)

func TestNormalizeWebQuery_StripsDatesAndFillers(t *testing.T) {
	tests := []struct {
		name     string
		argsJSON string
		want     string
	}{
		{
			name:     "strips fillers and dates",
			argsJSON: `{"query":"world news today March 2 2026 major headlines"}`,
			want:     "world",
		},
		{
			name:     "keeps source names, strips fillers and dates",
			argsJSON: `{"query":"world news March 2 2026 top headlines Reuters AP BBC"}`,
			want:     "ap bbc reuters world",
		},
		{
			name:     "no fillers or dates, just sorts",
			argsJSON: `{"query":"golang concurrency patterns tutorial"}`,
			want:     "concurrency golang patterns tutorial",
		},
		{
			name:     "all stripped returns sentinel",
			argsJSON: `{"query":"latest news today headlines"}`,
			want:     "[empty]",
		},
		{
			name:     "non-JSON returns empty",
			argsJSON: `not json at all`,
			want:     "",
		},
		{
			name:     "empty query returns empty",
			argsJSON: `{"query":""}`,
			want:     "",
		},
		{
			name:     "strips ISO date format",
			argsJSON: `{"query":"golang release 2026-03-02 features"}`,
			want:     "features golang release",
		},
		{
			name:     "strips day-month-year date",
			argsJSON: `{"query":"golang release 2 March 2026 features"}`,
			want:     "features golang release",
		},
		{
			name:     "strips standalone year",
			argsJSON: `{"query":"best golang frameworks 2026"}`,
			want:     "best frameworks golang",
		},
		{
			name:     "uses q key",
			argsJSON: `{"q":"golang tutorial latest"}`,
			want:     "golang tutorial",
		},
		{
			name:     "uses url key",
			argsJSON: `{"url":"https://example.com/search?q=test"}`,
			want:     "https://example.com/search?q=test",
		},
		{
			name:     "strips punctuation from tokens",
			argsJSON: `{"query":"golang, concurrency; patterns!"}`,
			want:     "concurrency golang patterns",
		},
		{
			name:     "removes short tokens after cleanup",
			argsJSON: `{"query":"a b golang c d patterns"}`,
			want:     "golang patterns",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeWebQuery(tt.argsJSON)
			if got != tt.want {
				t.Errorf("normalizeWebQuery(%q) = %q, want %q", tt.argsJSON, got, tt.want)
			}
		})
	}
}

func TestNormalizeWebQuery_VariantsSameHash(t *testing.T) {
	// Two queries about the same topic with different filler/date noise
	// should produce the same normalized form and thus the same hash.
	q1 := `{"query":"golang concurrency patterns 2026"}`
	q2 := `{"query":"latest golang concurrency patterns March 2 2026"}`

	n1 := normalizeWebQuery(q1)
	n2 := normalizeWebQuery(q2)

	if n1 != n2 {
		t.Errorf("expected same normalized form, got %q vs %q", n1, n2)
	}

	h1 := hashArgs(n1)
	h2 := hashArgs(n2)
	if h1 != h2 {
		t.Errorf("expected same hash, got %q vs %q", h1, h2)
	}
}

func TestNormalizeWebQuery_DifferentTopicsDifferentHash(t *testing.T) {
	q1 := `{"query":"golang concurrency patterns"}`
	q2 := `{"query":"rust memory safety"}`

	n1 := normalizeWebQuery(q1)
	n2 := normalizeWebQuery(q2)

	if n1 == n2 {
		t.Errorf("different topics should have different normalized forms, both got %q", n1)
	}
}

func TestExtractResultSignature(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "multiple URLs with paths, deduped and sorted",
			content: "Found results from https://golang.org/doc and https://blog.golang.org/concurrency and https://golang.org/doc",
			want:    "https://blog.golang.org/concurrency,https://golang.org/doc",
		},
		{
			name:    "no URLs returns empty",
			content: "No links here, just text about golang.",
			want:    "",
		},
		{
			name:    "different paths = different signatures",
			content: "https://reuters.com/climate/article1 https://reuters.com/economics/report2",
			want:    "https://reuters.com/climate/article1,https://reuters.com/economics/report2",
		},
		{
			name:    "strips trailing punctuation",
			content: "See https://example.com/page), also https://test.org/doc.",
			want:    "https://example.com/page,https://test.org/doc",
		},
		{
			name:    "strips query strings",
			content: "https://example.com/search?q=test&page=2 https://example.com/search?q=other",
			want:    "https://example.com/search",
		},
		{
			name:    "lowercased",
			content: "https://GoLang.Org/DOC https://EXAMPLE.COM/Path",
			want:    "https://example.com/path,https://golang.org/doc",
		},
		{
			name:    "empty content returns empty",
			content: "",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractResultSignature(tt.content)
			if got != tt.want {
				t.Errorf("extractResultSignature() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractResultSignature_SameURLsDifferentOrder(t *testing.T) {
	content1 := "https://b.com/page https://a.com/page"
	content2 := "https://a.com/page https://b.com/page"

	sig1 := extractResultSignature(content1)
	sig2 := extractResultSignature(content2)

	if sig1 != sig2 {
		t.Errorf("same URLs in different order should produce same signature, got %q vs %q", sig1, sig2)
	}
}

func TestExtractResultSignature_DifferentPathsDifferentSig(t *testing.T) {
	// Same domain but different paths should NOT match — this is the key fix
	sig1 := extractResultSignature("https://reuters.com/climate/report1")
	sig2 := extractResultSignature("https://reuters.com/economics/report2")

	if sig1 == sig2 {
		t.Errorf("different paths should produce different signatures, both got %q", sig1)
	}
}

func TestNormalizeWebQuery_AllFillersSameSentinel(t *testing.T) {
	// All-filler queries should match each other via the sentinel
	n1 := normalizeWebQuery(`{"query":"today news update"}`)
	n2 := normalizeWebQuery(`{"query":"latest news headlines"}`)

	if n1 != n2 {
		t.Errorf("all-filler queries should produce same sentinel, got %q vs %q", n1, n2)
	}
	if n1 != "[empty]" {
		t.Errorf("all-filler queries should return [empty], got %q", n1)
	}
}
