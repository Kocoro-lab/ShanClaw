package tools

import (
	"context"
	"testing"
)

func TestBrowser_Info(t *testing.T) {
	tool := &BrowserTool{}
	info := tool.Info()

	if info.Name != "browser" {
		t.Errorf("expected name 'browser', got %q", info.Name)
	}
	if len(info.Required) != 1 || info.Required[0] != "action" {
		t.Errorf("expected required [action], got %v", info.Required)
	}

	props, ok := info.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties to be map[string]any")
	}

	expectedParams := []string{"action", "url", "selector", "text", "script", "timeout"}
	for _, p := range expectedParams {
		if _, exists := props[p]; !exists {
			t.Errorf("expected parameter %q in properties", p)
		}
	}
}

func TestBrowser_RequiresApproval(t *testing.T) {
	tool := &BrowserTool{}
	if !tool.RequiresApproval() {
		t.Error("expected RequiresApproval to return true")
	}
}

func TestBrowser_InvalidJSON(t *testing.T) {
	tool := &BrowserTool{}
	result, err := tool.Run(context.Background(), `not valid json`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid JSON")
	}
	if !contains(result.Content, "invalid arguments") {
		t.Errorf("expected 'invalid arguments' in content, got: %s", result.Content)
	}
}

func TestBrowser_MissingAction(t *testing.T) {
	tool := &BrowserTool{}
	result, err := tool.Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing action")
	}
	if !contains(result.Content, "missing required parameter: action") {
		t.Errorf("expected missing action message, got: %s", result.Content)
	}
}

func TestBrowser_UnknownAction(t *testing.T) {
	tool := &BrowserTool{}
	result, err := tool.Run(context.Background(), `{"action": "fly"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for unknown action")
	}
	if !contains(result.Content, "unknown action") {
		t.Errorf("expected 'unknown action' in content, got: %s", result.Content)
	}
}

func TestBrowser_NavigateMissingURL(t *testing.T) {
	tool := &BrowserTool{}
	result, err := tool.Run(context.Background(), `{"action": "navigate"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for navigate without URL")
	}
	if !contains(result.Content, "requires 'url'") {
		t.Errorf("expected url required message, got: %s", result.Content)
	}
}

func TestBrowser_ClickMissingSelector(t *testing.T) {
	tool := &BrowserTool{}
	result, err := tool.Run(context.Background(), `{"action": "click"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for click without selector")
	}
	if !contains(result.Content, "requires") {
		t.Errorf("expected requires message, got: %s", result.Content)
	}
}

func TestBrowser_TypeMissingSelector(t *testing.T) {
	tool := &BrowserTool{}
	result, err := tool.Run(context.Background(), `{"action": "type"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for type without selector")
	}
	if !contains(result.Content, "requires") {
		t.Errorf("expected requires message, got: %s", result.Content)
	}
}

func TestBrowser_WaitMissingSelector(t *testing.T) {
	tool := &BrowserTool{}
	result, err := tool.Run(context.Background(), `{"action": "wait"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for wait without selector")
	}
	if !contains(result.Content, "requires 'selector'") {
		t.Errorf("expected selector required message, got: %s", result.Content)
	}
}

func TestBrowser_ExecuteJSMissingScript(t *testing.T) {
	tool := &BrowserTool{}
	result, err := tool.Run(context.Background(), `{"action": "execute_js"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for execute_js without script")
	}
	if !contains(result.Content, "requires 'script'") {
		t.Errorf("expected script required message, got: %s", result.Content)
	}
}

func TestBrowser_CloseWhenNotRunning(t *testing.T) {
	tool := &BrowserTool{}
	result, err := tool.Run(context.Background(), `{"action": "close"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected no error when closing non-running browser, got: %s", result.Content)
	}
	if !contains(result.Content, "not running") {
		t.Errorf("expected 'not running' message, got: %s", result.Content)
	}
}

func TestBrowser_InfoDescription(t *testing.T) {
	tool := &BrowserTool{}
	info := tool.Info()
	if !contains(info.Description, "isolated profile") {
		t.Errorf("expected description to mention isolated profile, got: %s", info.Description)
	}
}
