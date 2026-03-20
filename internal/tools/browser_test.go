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

func TestValidatePageContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool // true = content is empty
	}{
		{"empty string", "", true},
		{"whitespace only", "   \n\t  ", true},
		{"valid content", "Hello world", false},
		{"short but valid", "Please verify", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPageContentEmpty(tt.content)
			if got != tt.want {
				t.Errorf("isPageContentEmpty(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestBrowser_InfoDescription(t *testing.T) {
	tool := &BrowserTool{}
	info := tool.Info()
	if !contains(info.Description, "isolated profile") {
		t.Errorf("expected description to mention isolated profile, got: %s", info.Description)
	}
}

func TestDetectAntiBotPage(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"京东-综合网购首选-正品低价", false},
		{"Google", false},
		{"请验证您的身份", true},
		{"Just a moment...", true},
		{"Verify you are human", true},
		{"Access Denied", true},
		{"Attention Required! | Cloudflare", true},
		{"Are you a robot?", true},
		{"Security Check", true},
		{"Please wait while we verify", true},
		{"Robot Check", true},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := detectAntiBotPage(tt.title)
			if got != tt.want {
				t.Errorf("detectAntiBotPage(%q) = %v, want %v", tt.title, got, tt.want)
			}
		})
	}
}
