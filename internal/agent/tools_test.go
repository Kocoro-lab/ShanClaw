package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

func TestToolRegistry_Get(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "file_read"})

	tool, ok := reg.Get("file_read")
	if !ok {
		t.Fatal("expected to find file_read")
	}
	if tool.Info().Name != "file_read" {
		t.Errorf("expected 'file_read', got %q", tool.Info().Name)
	}

	_, ok = reg.Get("nonexistent")
	if ok {
		t.Error("expected not found")
	}
}

func TestToolRegistry_Schemas(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&mockTool{name: "file_read"})
	reg.Register(&mockTool{name: "bash"})

	schemas := reg.Schemas()
	if len(schemas) != 2 {
		t.Errorf("expected 2 schemas, got %d", len(schemas))
	}
}

type mockTool struct {
	name string
}

func (m *mockTool) Info() ToolInfo {
	return ToolInfo{
		Name:        m.name,
		Description: "mock tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (m *mockTool) Run(ctx context.Context, args string) (ToolResult, error) {
	return ToolResult{Content: "mock result"}, nil
}

func (m *mockTool) RequiresApproval() bool { return false }

type mockNativeTool struct {
	name string
}

func (m *mockNativeTool) Info() ToolInfo {
	return ToolInfo{Name: m.name, Description: "native tool"}
}
func (m *mockNativeTool) Run(ctx context.Context, args string) (ToolResult, error) {
	return ToolResult{Content: "ok"}, nil
}
func (m *mockNativeTool) RequiresApproval() bool { return false }
func (m *mockNativeTool) NativeToolDef() *client.NativeToolDef {
	return &client.NativeToolDef{
		Type:            "computer_20251124",
		Name:            "computer",
		DisplayWidthPx:  1280,
		DisplayHeightPx: 800,
	}
}

func TestToolRegistry_SchemasIncludesNativeTool(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&mockNativeTool{name: "computer"})
	reg.Register(&mockTool{name: "bash"})

	schemas := reg.Schemas()
	if len(schemas) != 2 {
		t.Fatalf("expected 2 schemas, got %d", len(schemas))
	}
	// Native tool should use its own type
	if schemas[0].Type != "computer_20251124" {
		t.Errorf("expected type 'computer_20251124', got %q", schemas[0].Type)
	}
	if schemas[0].Name != "computer" {
		t.Errorf("expected name 'computer', got %q", schemas[0].Name)
	}
	if schemas[0].DisplayWidthPx != 1280 {
		t.Errorf("expected display_width_px 1280, got %d", schemas[0].DisplayWidthPx)
	}
	// Standard tool should use function type
	if schemas[1].Type != "function" {
		t.Errorf("expected type 'function' for bash, got %q", schemas[1].Type)
	}
}

func TestToolRegistry_Remove(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "a"})
	r.Register(&mockTool{name: "b"})
	r.Register(&mockTool{name: "c"})

	r.Remove("b")

	if _, ok := r.Get("b"); ok {
		t.Error("b should be removed")
	}
	if r.Len() != 2 {
		t.Errorf("Len() = %d, want 2", r.Len())
	}
	names := r.Names()
	if len(names) != 2 || names[0] != "a" || names[1] != "c" {
		t.Errorf("names = %v, want [a c]", names)
	}
}

func TestToolRegistry_RemoveNonexistent(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "a"})
	r.Remove("nonexistent") // should not panic
	if r.Len() != 1 {
		t.Errorf("Len() = %d, want 1", r.Len())
	}
}

func TestToolRegistry_FilterByAllow(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "file_read"})
	r.Register(&mockTool{name: "bash"})
	r.Register(&mockTool{name: "computer"})
	r.Register(&mockTool{name: "browser"})

	filtered := r.FilterByAllow([]string{"file_read", "bash"})
	if filtered.Len() != 2 {
		t.Errorf("filtered Len() = %d, want 2", filtered.Len())
	}
	if _, ok := filtered.Get("computer"); ok {
		t.Error("computer should be filtered out")
	}
	if _, ok := filtered.Get("file_read"); !ok {
		t.Error("file_read should be present")
	}
}

func TestToolRegistry_FilterByDeny(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "file_read"})
	r.Register(&mockTool{name: "bash"})
	r.Register(&mockTool{name: "computer"})
	r.Register(&mockTool{name: "browser"})

	filtered := r.FilterByDeny([]string{"computer", "browser"})
	if filtered.Len() != 2 {
		t.Errorf("filtered Len() = %d, want 2", filtered.Len())
	}
	if _, ok := filtered.Get("computer"); ok {
		t.Error("computer should be denied")
	}
	if _, ok := filtered.Get("file_read"); !ok {
		t.Error("file_read should be present")
	}
}

func TestToolRegistry_CloneIndependence(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "a"})
	r.Register(&mockTool{name: "b"})

	c := r.Clone()
	c.Remove("a")

	if _, ok := r.Get("a"); !ok {
		t.Error("original should still have 'a'")
	}
	if c.Len() != 1 {
		t.Errorf("clone Len() = %d, want 1", c.Len())
	}
}

func TestToolRegistry_RegisterOverwrite(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "a"})
	r.Register(&mockTool{name: "b"})
	r.Register(&mockTool{name: "a"}) // overwrite

	names := r.Names()
	if len(names) != 2 {
		t.Errorf("expected 2 names after overwrite, got %d: %v", len(names), names)
	}
	if r.Len() != 2 {
		t.Errorf("Len() = %d, want 2", r.Len())
	}
	schemas := r.Schemas()
	if len(schemas) != 2 {
		t.Errorf("expected 2 schemas, got %d", len(schemas))
	}
}

func TestToolRegistry_RemoveAndReRegister(t *testing.T) {
	r := NewToolRegistry()
	r.Register(&mockTool{name: "a"})
	r.Register(&mockTool{name: "b"})
	r.Remove("a")
	r.Register(&mockTool{name: "a"})

	names := r.Names()
	if len(names) != 2 {
		t.Errorf("expected 2 names, got %d: %v", len(names), names)
	}
	schemas := r.Schemas()
	if len(schemas) != 2 {
		t.Errorf("expected 2 schemas, got %d", len(schemas))
	}
}

func TestToolResultErrorHelpers(t *testing.T) {
	tests := []struct {
		name        string
		result      ToolResult
		wantIsError bool
		wantCat     ErrorCategory
		wantRetry   bool
		wantPrefix  string
	}{
		{
			name:        "TransientError",
			result:      TransientError("connection timed out"),
			wantIsError: true,
			wantCat:     ErrCategoryTransient,
			wantRetry:   true,
			wantPrefix:  "[transient error]",
		},
		{
			name:        "ValidationError",
			result:      ValidationError("invalid URL format"),
			wantIsError: true,
			wantCat:     ErrCategoryValidation,
			wantRetry:   false,
			wantPrefix:  "[validation error]",
		},
		{
			name:        "BusinessError",
			result:      BusinessError("refund exceeds policy limit"),
			wantIsError: true,
			wantCat:     ErrCategoryBusiness,
			wantRetry:   false,
			wantPrefix:  "[business error]",
		},
		{
			name:        "PermissionError",
			result:      PermissionError("access denied"),
			wantIsError: true,
			wantCat:     ErrCategoryPermission,
			wantRetry:   false,
			wantPrefix:  "[permission error]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.result.IsError != tt.wantIsError {
				t.Errorf("IsError = %v, want %v", tt.result.IsError, tt.wantIsError)
			}
			if tt.result.ErrorCategory != tt.wantCat {
				t.Errorf("ErrorCategory = %q, want %q", tt.result.ErrorCategory, tt.wantCat)
			}
			if tt.result.IsRetryable != tt.wantRetry {
				t.Errorf("IsRetryable = %v, want %v", tt.result.IsRetryable, tt.wantRetry)
			}
			if !strings.HasPrefix(tt.result.Content, tt.wantPrefix) {
				t.Errorf("Content = %q, want prefix %q", tt.result.Content, tt.wantPrefix)
			}
		})
	}
}

func TestToolResult_ZeroValueNotError(t *testing.T) {
	r := ToolResult{Content: "some output"}
	if r.IsError {
		t.Error("zero-value ToolResult must not be an error")
	}
	if r.ErrorCategory != "" {
		t.Errorf("zero-value ErrorCategory must be empty, got %q", r.ErrorCategory)
	}
	if r.IsRetryable {
		t.Error("zero-value IsRetryable must be false")
	}
}

func TestToolResult_ImagesField(t *testing.T) {
	result := ToolResult{
		Content: "Screenshot captured",
		IsError: false,
		Images: []ImageBlock{
			{MediaType: "image/png", Data: "iVBORfakedata"},
		},
	}
	if len(result.Images) != 1 {
		t.Errorf("expected 1 image, got %d", len(result.Images))
	}
	if result.Images[0].MediaType != "image/png" {
		t.Errorf("expected image/png, got %s", result.Images[0].MediaType)
	}
}
