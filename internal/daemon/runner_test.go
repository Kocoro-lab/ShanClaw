package daemon

import (
	"encoding/json"
	"testing"
)

func TestRunAgentRequest_Validate_EmptyText(t *testing.T) {
	req := RunAgentRequest{Text: ""}
	if err := req.Validate(); err == nil {
		t.Fatal("expected error for empty text")
	}
}

func TestRunAgentRequest_Validate_WhitespaceOnly(t *testing.T) {
	req := RunAgentRequest{Text: "   "}
	if err := req.Validate(); err == nil {
		t.Fatal("expected error for whitespace-only text")
	}
}

func TestRunAgentRequest_Validate_NonEmpty(t *testing.T) {
	req := RunAgentRequest{Text: "hello"}
	if err := req.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAgentRequest_Validate_WithAgent(t *testing.T) {
	req := RunAgentRequest{Text: "do something", Agent: "ops-bot"}
	if err := req.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAgentRequest_Validate_WithSessionID(t *testing.T) {
	req := RunAgentRequest{Text: "do something", SessionID: "sess-123"}
	if err := req.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAgentRequest_Ephemeral(t *testing.T) {
	req := RunAgentRequest{
		Text:      "test",
		Agent:     "test-agent",
		Source:    "heartbeat",
		Ephemeral: true,
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("valid ephemeral request should not fail: %v", err)
	}
}

func TestRunAgentRequest_ModelOverride(t *testing.T) {
	req := RunAgentRequest{
		Text:          "test",
		ModelOverride: "small",
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("valid model override request should not fail: %v", err)
	}
}

func TestComputeRouteKey_BypassRouting(t *testing.T) {
	req := RunAgentRequest{Agent: "my-agent", BypassRouting: true}
	if got := ComputeRouteKey(req); got != "" {
		t.Errorf("ComputeRouteKey with BypassRouting=true returned %q, want empty", got)
	}
}

func TestComputeRouteKey_AgentWithoutBypass(t *testing.T) {
	req := RunAgentRequest{Agent: "my-agent"}
	if got := ComputeRouteKey(req); got != "agent:my-agent" {
		t.Errorf("ComputeRouteKey returned %q, want %q", got, "agent:my-agent")
	}
}

func TestRunAgentRequestSource(t *testing.T) {
	req := RunAgentRequest{
		Text:   "hello",
		Agent:  "test",
		Source: "slack",
	}
	data, _ := json.Marshal(req)
	var decoded RunAgentRequest
	json.Unmarshal(data, &decoded)
	if decoded.Source != "slack" {
		t.Fatalf("expected source 'slack', got %q", decoded.Source)
	}
}
