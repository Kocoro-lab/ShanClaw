package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeJSON_IdenticalArgumentsAreCanonicalized(t *testing.T) {
	a := normalizeJSON(json.RawMessage(`{"command":"date","path":"/tmp"}`))
	b := normalizeJSON(json.RawMessage(`{ "path": "/tmp", "command": "date" }`))

	if a != b {
		t.Fatalf("expected canonical JSON to match, got %q and %q", a, b)
	}
	if a != `{"command":"date","path":"/tmp"}` {
		t.Fatalf("expected deterministic key order, got %q", a)
	}
}

func TestNormalizeJSON_EmptyAndWhitespaceInputs(t *testing.T) {
	tests := []json.RawMessage{
		nil,
		{},
		[]byte(""),
		[]byte("   \n\t"),
	}

	for i, tc := range tests {
		got := normalizeJSON(tc)
		if got != "{}" {
			t.Fatalf("case %d: expected {}, got %q", i, got)
		}
	}
}

func TestNormalizeJSON_InvalidJSONFallsBackToTrimmedRaw(t *testing.T) {
	raw := json.RawMessage(`{ "command": "date",`)
	expected := strings.TrimSpace(string(raw))
	got := normalizeJSON(raw)
	if got != expected {
		t.Fatalf("expected trimmed fallback %q, got %q", expected, got)
	}
}

