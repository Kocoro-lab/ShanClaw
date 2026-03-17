package agent

import (
	"context"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
)

type ToolInfo struct {
	Name        string
	Description string
	Parameters  map[string]any
	Required    []string
}

type ImageBlock struct {
	MediaType string // e.g. "image/png"
	Data      string // base64-encoded
}

// ErrorCategory classifies the nature of a tool failure so the agent
// can make informed retry decisions.
type ErrorCategory string

const (
	// ErrCategoryTransient indicates a timeout or network error. Retry may help.
	ErrCategoryTransient ErrorCategory = "transient"
	// ErrCategoryValidation indicates the tool arguments were invalid. Fix before retrying.
	ErrCategoryValidation ErrorCategory = "validation"
	// ErrCategoryBusiness indicates a policy or constraint violation. Do not retry.
	ErrCategoryBusiness ErrorCategory = "business"
	// ErrCategoryPermission indicates access was denied. Escalate to user.
	ErrCategoryPermission ErrorCategory = "permission"
)

type ToolResult struct {
	Content       string
	IsError       bool
	ErrorCategory ErrorCategory // empty when IsError is false
	IsRetryable   bool          // true only for transient errors
	Images        []ImageBlock
}

// TransientError returns a ToolResult for timeout/network failures where retry may help.
func TransientError(msg string) ToolResult {
	return ToolResult{
		Content:       "[transient error] " + msg,
		IsError:       true,
		ErrorCategory: ErrCategoryTransient,
		IsRetryable:   true,
	}
}

// ValidationError returns a ToolResult for invalid tool arguments.
func ValidationError(msg string) ToolResult {
	return ToolResult{
		Content:       "[validation error] " + msg,
		IsError:       true,
		ErrorCategory: ErrCategoryValidation,
	}
}

// BusinessError returns a ToolResult for policy/constraint violations that must not be retried.
func BusinessError(msg string) ToolResult {
	return ToolResult{
		Content:       "[business error] " + msg,
		IsError:       true,
		ErrorCategory: ErrCategoryBusiness,
	}
}

// PermissionError returns a ToolResult for access denied scenarios requiring escalation.
func PermissionError(msg string) ToolResult {
	return ToolResult{
		Content:       "[permission error] " + msg,
		IsError:       true,
		ErrorCategory: ErrCategoryPermission,
	}
}

type Tool interface {
	Info() ToolInfo
	Run(ctx context.Context, args string) (ToolResult, error)
	RequiresApproval() bool
}

// NativeToolProvider is an optional interface for tools that use a provider's
// native tool schema (e.g., Anthropic's computer_20251124) instead of the
// standard function-calling format.
type NativeToolProvider interface {
	NativeToolDef() *client.NativeToolDef
}

// SafeChecker is an optional interface tools can implement to indicate
// certain invocations are safe and don't need approval.
type SafeChecker interface {
	IsSafeArgs(argsJSON string) bool
}

type ToolRegistry struct {
	tools map[string]Tool
	order []string
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

func (r *ToolRegistry) Register(t Tool) {
	name := t.Info().Name
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

func (r *ToolRegistry) Clone() *ToolRegistry {
	clone := NewToolRegistry()
	for _, name := range r.order {
		tool := r.tools[name]
		clone.tools[name] = tool
		clone.order = append(clone.order, name)
	}
	return clone
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *ToolRegistry) All() []Tool {
	tools := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		tools = append(tools, r.tools[name])
	}
	return tools
}

// Remove removes a tool from the registry by name.
func (r *ToolRegistry) Remove(name string) {
	if _, ok := r.tools[name]; !ok {
		return
	}
	delete(r.tools, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			return
		}
	}
}

// Names returns the ordered list of tool names.
func (r *ToolRegistry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Len returns the number of registered tools.
func (r *ToolRegistry) Len() int {
	return len(r.tools)
}

// FilterByAllow returns a new registry containing only the named tools.
// Tools not found are silently skipped.
func (r *ToolRegistry) FilterByAllow(allow []string) *ToolRegistry {
	filtered := NewToolRegistry()
	for _, name := range allow {
		if t, ok := r.tools[name]; ok {
			filtered.Register(t)
		}
	}
	return filtered
}

// FilterByDeny returns a new registry with the named tools removed.
func (r *ToolRegistry) FilterByDeny(deny []string) *ToolRegistry {
	denySet := make(map[string]struct{}, len(deny))
	for _, name := range deny {
		denySet[name] = struct{}{}
	}
	filtered := NewToolRegistry()
	for _, name := range r.order {
		if _, blocked := denySet[name]; !blocked {
			filtered.Register(r.tools[name])
		}
	}
	return filtered
}

func (r *ToolRegistry) Schemas() []client.Tool {
	schemas := make([]client.Tool, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]

		// Check for native tool definition
		if native, ok := t.(NativeToolProvider); ok {
			def := native.NativeToolDef()
			if def != nil {
				schemas = append(schemas, client.Tool{
					Type:            def.Type,
					Name:            def.Name,
					DisplayWidthPx:  def.DisplayWidthPx,
					DisplayHeightPx: def.DisplayHeightPx,
				})
				continue
			}
		}

		// Standard function tool
		info := t.Info()
		params := info.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		if info.Required != nil {
			params["required"] = info.Required
		}
		schemas = append(schemas, client.Tool{
			Type: "function",
			Function: client.FunctionDef{
				Name:        info.Name,
				Description: info.Description,
				Parameters:  params,
			},
		})
	}
	return schemas
}
