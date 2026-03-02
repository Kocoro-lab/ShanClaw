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
			name:     "all stripped leaves empty",
			argsJSON: `{"query":"latest news today headlines"}`,
			want:     "",
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
			name:    "multiple URLs extracts sorted unique domains",
			content: "Found results from https://golang.org/doc and https://blog.golang.org/concurrency and https://golang.org/ref",
			want:    "blog.golang.org,golang.org",
		},
		{
			name:    "no URLs returns empty",
			content: "No links here, just text about golang.",
			want:    "",
		},
		{
			name:    "same domains in different order produce same signature",
			content: "https://example.com/a https://test.org/b https://example.com/c",
			want:    "example.com,test.org",
		},
		{
			name:    "http and https both captured",
			content: "http://old.example.com/page and https://new.example.com/page",
			want:    "new.example.com,old.example.com",
		},
		{
			name:    "domains lowercased",
			content: "https://GoLang.Org/doc https://EXAMPLE.COM/path",
			want:    "example.com,golang.org",
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
				t.Errorf("extractResultSignature(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestExtractResultSignature_OrderIndependent(t *testing.T) {
	content1 := "https://b.com/x https://a.com/y https://c.com/z"
	content2 := "https://c.com/1 https://a.com/2 https://b.com/3"

	sig1 := extractResultSignature(content1)
	sig2 := extractResultSignature(content2)

	if sig1 != sig2 {
		t.Errorf("same domains in different order should produce same signature, got %q vs %q", sig1, sig2)
	}
}
