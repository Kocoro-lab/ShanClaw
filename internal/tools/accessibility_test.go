package tools

import (
	"context"
	"strings"
	"testing"
)

func TestAccessibility_Info(t *testing.T) {
	tool := &AccessibilityTool{}
	info := tool.Info()
	if info.Name != "accessibility" {
		t.Errorf("expected name 'accessibility', got %q", info.Name)
	}
	if len(info.Required) != 1 || info.Required[0] != "action" {
		t.Errorf("expected required [action], got %v", info.Required)
	}
	props, ok := info.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties map in parameters")
	}
	for _, key := range []string{"action", "app", "max_depth", "filter", "ref", "value"} {
		if _, exists := props[key]; !exists {
			t.Errorf("expected property %q in schema", key)
		}
	}
}

func TestAccessibility_RequiresApproval(t *testing.T) {
	tool := &AccessibilityTool{}
	if !tool.RequiresApproval() {
		t.Error("expected RequiresApproval to return true")
	}
}

func TestAccessibility_InvalidJSON(t *testing.T) {
	tool := &AccessibilityTool{}
	result, err := tool.Run(context.Background(), `not valid json`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid JSON")
	}
}

func TestAccessibility_MissingAction(t *testing.T) {
	tool := &AccessibilityTool{}
	result, err := tool.Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing action")
	}
	if !strings.Contains(result.Content, "missing required parameter: action") {
		t.Errorf("expected missing action error, got: %s", result.Content)
	}
}

func TestAccessibility_UnknownAction(t *testing.T) {
	tool := &AccessibilityTool{}
	result, err := tool.Run(context.Background(), `{"action": "fly"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for unknown action")
	}
	if !strings.Contains(result.Content, "unknown action") {
		t.Errorf("expected 'unknown action' in error, got: %s", result.Content)
	}
}

func TestAccessibility_ClickMissingRef(t *testing.T) {
	tool := &AccessibilityTool{}
	result, err := tool.Run(context.Background(), `{"action": "click"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for click without ref")
	}
}

func TestAccessibility_ClickUnknownRef(t *testing.T) {
	tool := &AccessibilityTool{}
	tool.refs = map[string]refEntry{"e1": {path: "window[0]", pid: 1}}
	result, err := tool.Run(context.Background(), `{"action": "click", "ref": "e99"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error for unknown ref")
	}
	if !strings.Contains(result.Content, "unknown ref") {
		t.Errorf("expected 'unknown ref' error, got: %s", result.Content)
	}
}

func TestAccessibility_SetValueMissingValue(t *testing.T) {
	tool := &AccessibilityTool{}
	tool.refs = map[string]refEntry{"e1": {path: "window[0]/AXTextField[0]", role: "AXTextField", pid: 1}}
	result, err := tool.Run(context.Background(), `{"action": "set_value", "ref": "e1"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error for set_value without value")
	}
}
