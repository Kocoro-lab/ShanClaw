package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/Kocoro-lab/ShanClaw/internal/agent"
)

// backend tracks which browser engine is active.
type browserBackend int

const (
	backendNone     browserBackend = iota
	backendPinchtab                // pinchtab HTTP API
	backendChromedp                // embedded chromedp (fallback)
)

type BrowserTool struct {
	mu      sync.Mutex
	backend browserBackend

	// pinchtab
	pt    *pinchtabClient
	tabID string // active tab in pinchtab

	// chromedp fallback
	ctx    context.Context
	cancel context.CancelFunc
	active bool
}

type browserArgs struct {
	Action   string `json:"action"`
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Text     string `json:"text,omitempty"`
	Key      string `json:"key,omitempty"`
	Value    string `json:"value,omitempty"`
	Script   string `json:"script,omitempty"`
	Query    string `json:"query,omitempty"`
	Filter   string `json:"filter,omitempty"`
	Timeout  int    `json:"timeout,omitempty"`
}

func (t *BrowserTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name: "browser",
		Description: "Control a headless browser with an isolated profile. " +
			"FIRST CHOICE for any web page interaction: navigating, clicking, reading, scraping, screenshots of web content. " +
			"Only skip this for pages requiring user login/authentication — use GUI tools for those. " +
			"Actions: navigate, click, type, scroll, screenshot, read_page, execute_js, wait, snapshot, find, close. " +
			"Use 'snapshot' to get the accessibility tree with element refs (e1, e2, ...), then use 'ref' parameter with click/type/scroll actions. " +
			"Use 'find' to search for elements by natural language description.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":   map[string]any{"type": "string", "description": "Action: navigate, click, type, scroll, screenshot, read_page, execute_js, wait, snapshot, find, close"},
				"url":      map[string]any{"type": "string", "description": "URL to navigate to (for navigate action)"},
				"selector": map[string]any{"type": "string", "description": "CSS selector (for click, type, read_page, scroll, wait)"},
				"ref":      map[string]any{"type": "string", "description": "Element ref from snapshot, e.g. 'e5' (for click, type, scroll — alternative to selector)"},
				"text":     map[string]any{"type": "string", "description": "Text to type (for type action)"},
				"key":      map[string]any{"type": "string", "description": "Key to press, e.g. 'Enter' (for press action via click with key)"},
				"value":    map[string]any{"type": "string", "description": "Value to select (for select action via click with value)"},
				"script":   map[string]any{"type": "string", "description": "JavaScript to execute (for execute_js action)"},
				"query":    map[string]any{"type": "string", "description": "Natural language search query (for find action)"},
				"filter":   map[string]any{"type": "string", "description": "Filter mode: 'interactive' or 'all' (for snapshot action, default: interactive)"},
				"timeout":  map[string]any{"type": "integer", "description": "Timeout in seconds (default: 30)"},
			},
		},
		Required: []string{"action"},
	}
}

func (t *BrowserTool) RequiresApproval() bool { return true }

func (t *BrowserTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	var args browserArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	if args.Action == "" {
		return agent.ToolResult{Content: "missing required parameter: action", IsError: true}, nil
	}

	timeout := 30 * time.Second
	if args.Timeout > 0 {
		timeout = time.Duration(args.Timeout) * time.Second
	}

	// close doesn't need a running backend
	if args.Action == "close" {
		return t.closeBrowser()
	}

	// Validate required params before starting a browser
	if err := t.validateArgs(args); err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	// Ensure a backend is available (pinchtab preferred, chromedp fallback)
	if err := t.ensureBackend(ctx); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("failed to start browser: %v", err), IsError: true}, nil
	}

	switch args.Action {
	case "navigate":
		return t.navigate(ctx, args, timeout)
	case "click":
		return t.click(ctx, args, timeout)
	case "type":
		return t.typeText(ctx, args, timeout)
	case "scroll":
		return t.scroll(ctx, args, timeout)
	case "screenshot":
		return t.screenshot(ctx, args, timeout)
	case "read_page":
		return t.readPage(ctx, args, timeout)
	case "execute_js":
		return t.executeJS(ctx, args, timeout)
	case "wait":
		return t.waitVisible(ctx, args, timeout)
	case "snapshot":
		return t.snapshotAction(ctx, args)
	case "find":
		return t.findAction(ctx, args)
	default:
		// unreachable — validateArgs catches unknown actions
		return agent.ToolResult{Content: fmt.Sprintf("unknown action: %q", args.Action), IsError: true}, nil
	}
}

// validateArgs checks required params before starting a browser.
func (t *BrowserTool) validateArgs(args browserArgs) error {
	switch args.Action {
	case "navigate":
		if args.URL == "" {
			return fmt.Errorf("navigate action requires 'url' parameter")
		}
	case "click":
		if args.Ref == "" && args.Selector == "" {
			return fmt.Errorf("click action requires 'ref' or 'selector' parameter")
		}
	case "type":
		if args.Ref == "" && args.Selector == "" {
			return fmt.Errorf("type action requires 'ref' or 'selector' parameter")
		}
	case "wait":
		if args.Selector == "" {
			return fmt.Errorf("wait action requires 'selector' parameter")
		}
	case "execute_js":
		if args.Script == "" {
			return fmt.Errorf("execute_js action requires 'script' parameter")
		}
	case "find":
		if args.Query == "" {
			return fmt.Errorf("find action requires 'query' parameter")
		}
	case "scroll", "screenshot", "read_page", "snapshot":
		// no required params
	default:
		return fmt.Errorf("unknown action: %q (valid: navigate, click, type, scroll, screenshot, read_page, execute_js, wait, snapshot, find, close)", args.Action)
	}
	return nil
}

// ensureBackend picks pinchtab if available, else falls back to chromedp.
func (t *BrowserTool) ensureBackend(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Already have a working backend?
	switch t.backend {
	case backendPinchtab:
		if t.pt.available(ctx) {
			return nil
		}
		// pinchtab died — clear stale tab ID, try to restart or fall through to chromedp
		t.tabID = ""
		t.backend = backendNone
	case backendChromedp:
		if t.ctx != nil && t.ctx.Err() == nil {
			return nil
		}
		// chromedp context dead — reset
		if t.cancel != nil {
			t.cancel()
		}
		t.ctx = nil
		t.cancel = nil
		t.active = false
		t.backend = backendNone
	}

	// Try pinchtab first
	if t.pt == nil {
		t.pt = newPinchtabClient()
	}
	if err := t.pt.ensure(ctx); err == nil {
		t.backend = backendPinchtab
		return nil
	}

	// Fall back to chromedp
	return t.startChromedp()
}

func (t *BrowserTool) startChromedp() error {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	if err := chromedp.Run(browserCtx); err != nil {
		browserCancel()
		allocCancel()
		return fmt.Errorf("failed to start browser: %w", err)
	}

	t.ctx = browserCtx
	t.cancel = func() {
		browserCancel()
		allocCancel()
	}
	t.active = true
	t.backend = backendChromedp
	return nil
}

func (t *BrowserTool) isPinchtab() bool {
	return t.backend == backendPinchtab
}

// --- Actions ---

func (t *BrowserTool) navigate(_ context.Context, args browserArgs, timeout time.Duration) (agent.ToolResult, error) {
	if t.isPinchtab() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		// Always open a new tab to isolate navigation from previous tasks
		resp, err := t.pt.navigate(ctx, ptNavigateReq{URL: args.URL, NewTab: true})
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("navigate error: %v", err), IsError: true}, nil
		}
		if resp.TabID != "" {
			t.tabID = resp.TabID
		}
		return agent.ToolResult{Content: fmt.Sprintf("Navigated to: %s\nTitle: %s", resp.URL, resp.Title)}, nil
	}

	// chromedp
	tCtx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()

	var title string
	err := chromedp.Run(tCtx,
		chromedp.Navigate(args.URL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Title(&title),
	)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("navigate error: %v", err), IsError: true}, nil
	}
	return agent.ToolResult{Content: fmt.Sprintf("Navigated to: %s\nTitle: %s", args.URL, title)}, nil
}

func (t *BrowserTool) click(_ context.Context, args browserArgs, timeout time.Duration) (agent.ToolResult, error) {
	if t.isPinchtab() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		kind := "click"
		if args.Key != "" {
			kind = "press"
		} else if args.Value != "" {
			kind = "select"
		}
		req := ptActionReq{TabID: t.tabID, Kind: kind, Ref: args.Ref, Selector: args.Selector, Key: args.Key, Value: args.Value}
		resp, err := t.pt.action(ctx, req)
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("click error: %v", err), IsError: true}, nil
		}
		target := args.Ref
		if target == "" {
			target = args.Selector
		}
		_ = resp
		return agent.ToolResult{Content: fmt.Sprintf("Clicked: %s", target)}, nil
	}

	// chromedp (selector only)
	if args.Selector == "" {
		return agent.ToolResult{Content: "chromedp fallback requires 'selector' (refs not supported without pinchtab)", IsError: true}, nil
	}
	tCtx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()
	if err := chromedp.Run(tCtx, chromedp.Click(args.Selector)); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("click error: %v", err), IsError: true}, nil
	}
	return agent.ToolResult{Content: fmt.Sprintf("Clicked: %s", args.Selector)}, nil
}

func (t *BrowserTool) typeText(_ context.Context, args browserArgs, timeout time.Duration) (agent.ToolResult, error) {
	if t.isPinchtab() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		req := ptActionReq{TabID: t.tabID, Kind: "type", Ref: args.Ref, Selector: args.Selector, Text: args.Text}
		_, err := t.pt.action(ctx, req)
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("type error: %v", err), IsError: true}, nil
		}
		target := args.Ref
		if target == "" {
			target = args.Selector
		}
		return agent.ToolResult{Content: fmt.Sprintf("Typed into: %s", target)}, nil
	}

	// chromedp
	if args.Selector == "" {
		return agent.ToolResult{Content: "chromedp fallback requires 'selector'", IsError: true}, nil
	}
	tCtx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()
	if err := chromedp.Run(tCtx, chromedp.SendKeys(args.Selector, args.Text)); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("type error: %v", err), IsError: true}, nil
	}
	return agent.ToolResult{Content: fmt.Sprintf("Typed into: %s", args.Selector)}, nil
}

func (t *BrowserTool) scroll(_ context.Context, args browserArgs, timeout time.Duration) (agent.ToolResult, error) {
	if t.isPinchtab() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		req := ptActionReq{TabID: t.tabID, Kind: "scroll", Ref: args.Ref, Selector: args.Selector}
		if args.Ref == "" && args.Selector == "" {
			req.ScrollY = 800 // scroll down by default
		}
		_, err := t.pt.action(ctx, req)
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("scroll error: %v", err), IsError: true}, nil
		}
		target := args.Ref
		if target == "" {
			target = args.Selector
		}
		if target == "" {
			target = "page"
		}
		return agent.ToolResult{Content: fmt.Sprintf("Scrolled: %s", target)}, nil
	}

	// chromedp
	tCtx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()

	if args.Selector != "" {
		if err := chromedp.Run(tCtx, chromedp.ScrollIntoView(args.Selector)); err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("scroll error: %v", err), IsError: true}, nil
		}
		return agent.ToolResult{Content: fmt.Sprintf("Scrolled to: %s", args.Selector)}, nil
	}

	var scrollHeight int
	if err := chromedp.Run(tCtx,
		chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight); document.body.scrollHeight`, &scrollHeight),
	); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("scroll error: %v", err), IsError: true}, nil
	}
	return agent.ToolResult{Content: fmt.Sprintf("Scrolled to bottom (height: %d)", scrollHeight)}, nil
}

func (t *BrowserTool) screenshot(_ context.Context, _ browserArgs, timeout time.Duration) (agent.ToolResult, error) {
	if t.isPinchtab() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		// Note: pinchtab v0.7.6 captures viewport only (no full-page support).
		// For full-page, the LLM can scroll + take multiple screenshots.
		data, err := t.pt.screenshot(ctx, t.tabID)
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("screenshot error: %v", err), IsError: true}, nil
		}

		// Save to temp file, resize for vision loop
		f, err := os.CreateTemp("", "browser-screenshot-*.jpg")
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("failed to create temp file: %v", err), IsError: true}, nil
		}
		f.Write(data)
		f.Close()

		// Best-effort resize — skip if image is too small or sips fails
		ResizeImage(f.Name(), DefaultAPIWidth)

		block, err := EncodeImage(f.Name())
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("encode error: %v", err), IsError: true}, nil
		}
		return agent.ToolResult{
			Content: fmt.Sprintf("Screenshot saved to: %s", f.Name()),
			Images:  []agent.ImageBlock{block},
		}, nil
	}

	// chromedp
	tCtx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()

	var buf []byte
	if err := chromedp.Run(tCtx, chromedp.FullScreenshot(&buf, 90)); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("screenshot error: %v", err), IsError: true}, nil
	}

	f, err := os.CreateTemp("", "browser-screenshot-*.png")
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("failed to create temp file: %v", err), IsError: true}, nil
	}
	f.Write(buf)
	f.Close()

	// Best-effort resize
	ResizeImage(f.Name(), DefaultAPIWidth)

	block, err := EncodeImage(f.Name())
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("encode error: %v", err), IsError: true}, nil
	}
	return agent.ToolResult{
		Content: fmt.Sprintf("Screenshot saved to: %s", f.Name()),
		Images:  []agent.ImageBlock{block},
	}, nil
}

func (t *BrowserTool) readPage(_ context.Context, args browserArgs, timeout time.Duration) (agent.ToolResult, error) {
	if t.isPinchtab() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		resp, err := t.pt.text(ctx, t.tabID)
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("read_page error: %v", err), IsError: true}, nil
		}
		text := resp.Text
		const maxLen = 10240
		if len(text) > maxLen {
			text = text[:maxLen] + "\n... [truncated to 10KB]"
		}
		return agent.ToolResult{Content: fmt.Sprintf("URL: %s\nTitle: %s\n\n%s", resp.URL, resp.Title, text)}, nil
	}

	// chromedp
	tCtx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()

	selector := "html"
	if args.Selector != "" {
		selector = args.Selector
	}

	var textContent string
	err := chromedp.Run(tCtx, chromedp.Evaluate(
		fmt.Sprintf(`document.querySelector(%q)?.innerText || ""`, selector),
		&textContent,
	))
	if err != nil {
		// Fall back to outerHTML
		var html string
		chromedp.Run(tCtx, chromedp.OuterHTML(selector, &html))
		textContent = html
	}

	const maxLen = 10240
	if len(textContent) > maxLen {
		textContent = textContent[:maxLen] + "\n... [truncated to 10KB]"
	}
	return agent.ToolResult{Content: textContent}, nil
}

func (t *BrowserTool) executeJS(_ context.Context, args browserArgs, timeout time.Duration) (agent.ToolResult, error) {
	if t.isPinchtab() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		resp, err := t.pt.evaluate(ctx, t.tabID, args.Script)
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("execute_js error: %v", err), IsError: true}, nil
		}
		output := fmt.Sprintf("%v", resp.Result)
		const maxLen = 10240
		if len(output) > maxLen {
			output = output[:maxLen] + "\n... [truncated to 10KB]"
		}
		return agent.ToolResult{Content: output}, nil
	}

	// chromedp
	tCtx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()

	var result any
	if err := chromedp.Run(tCtx, chromedp.Evaluate(args.Script, &result)); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("execute_js error: %v", err), IsError: true}, nil
	}
	output := fmt.Sprintf("%v", result)
	const maxLen = 10240
	if len(output) > maxLen {
		output = output[:maxLen] + "\n... [truncated to 10KB]"
	}
	return agent.ToolResult{Content: output}, nil
}

func (t *BrowserTool) waitVisible(_ context.Context, args browserArgs, timeout time.Duration) (agent.ToolResult, error) {
	if t.isPinchtab() {
		// Use JS polling via evaluate
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		script := fmt.Sprintf(`
			await new Promise((resolve, reject) => {
				const el = document.querySelector(%q);
				if (el) return resolve(true);
				const obs = new MutationObserver(() => {
					if (document.querySelector(%q)) { obs.disconnect(); resolve(true); }
				});
				obs.observe(document.body, {childList: true, subtree: true});
				setTimeout(() => { obs.disconnect(); reject('timeout'); }, %d);
			})
		`, args.Selector, args.Selector, int(timeout.Milliseconds()))
		_, err := t.pt.evaluate(ctx, t.tabID, script)
		if err != nil {
			return agent.ToolResult{Content: fmt.Sprintf("wait error: %v", err), IsError: true}, nil
		}
		return agent.ToolResult{Content: fmt.Sprintf("Element visible: %s", args.Selector)}, nil
	}

	// chromedp
	tCtx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()
	if err := chromedp.Run(tCtx, chromedp.WaitVisible(args.Selector)); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("wait error: %v", err), IsError: true}, nil
	}
	return agent.ToolResult{Content: fmt.Sprintf("Element visible: %s", args.Selector)}, nil
}

// --- New pinchtab-only actions ---

func (t *BrowserTool) snapshotAction(_ context.Context, args browserArgs) (agent.ToolResult, error) {
	if !t.isPinchtab() {
		return agent.ToolResult{
			Content: "snapshot action requires pinchtab (not available, using chromedp fallback). Use read_page instead.",
			IsError: true,
		}, nil
	}

	filter := args.Filter
	if filter == "" {
		filter = "interactive"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := t.pt.snapshot(ctx, t.tabID, filter)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("snapshot error: %v", err), IsError: true}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("URL: %s\nTitle: %s\nElements: %d\n\n", resp.URL, resp.Title, resp.Count))

	for _, n := range resp.Nodes {
		indent := strings.Repeat("  ", n.Depth)
		line := fmt.Sprintf("%s[%s] %s: %s", indent, n.Ref, n.Role, n.Name)
		if n.Value != "" {
			line += fmt.Sprintf(" = %q", n.Value)
		}
		if n.Focused {
			line += " (focused)"
		}
		if n.Disabled {
			line += " (disabled)"
		}
		sb.WriteString(line + "\n")
	}

	content := sb.String()
	const maxLen = 20480 // snapshot can be larger
	if len(content) > maxLen {
		content = content[:maxLen] + "\n... [truncated]"
	}

	return agent.ToolResult{Content: content}, nil
}

func (t *BrowserTool) findAction(_ context.Context, args browserArgs) (agent.ToolResult, error) {
	if !t.isPinchtab() {
		return agent.ToolResult{
			Content: "find action requires pinchtab (not available, using chromedp fallback). Use execute_js or read_page instead.",
			IsError: true,
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := t.pt.find(ctx, ptFindReq{Query: args.Query, TabID: t.tabID, TopK: 5})
	if err != nil {
		// /find may not exist in older pinchtab versions — suggest snapshot instead
		if strings.Contains(err.Error(), "404") {
			return agent.ToolResult{
				Content: "find is not available in this pinchtab version. Use 'snapshot' to get element refs, then click/type by ref.",
				IsError: true,
			}, nil
		}
		return agent.ToolResult{Content: fmt.Sprintf("find error: %v", err), IsError: true}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Best match: %s (confidence: %s, score: %.2f)\n\n", resp.BestRef, resp.Confidence, resp.Score))
	for _, m := range resp.Matches {
		sb.WriteString(fmt.Sprintf("  [%s] %s: %s (score: %.2f)\n", m.Ref, m.Role, m.Name, m.Score))
	}

	return agent.ToolResult{Content: sb.String()}, nil
}

func (t *BrowserTool) closeBrowser() (agent.ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.backend == backendNone {
		return agent.ToolResult{Content: "Browser is not running"}, nil
	}

	t.cleanup()
	return agent.ToolResult{Content: "Browser closed"}, nil
}

// Cleanup shuts down the browser. Safe to call multiple times.
func (t *BrowserTool) Cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanup()
}

// cleanup must be called with mu held.
func (t *BrowserTool) cleanup() {
	switch t.backend {
	case backendPinchtab:
		if t.pt != nil {
			t.pt.close()
		}
		t.tabID = ""
	case backendChromedp:
		if t.cancel != nil {
			t.cancel()
		}
		t.ctx = nil
		t.cancel = nil
		t.active = false
	}
	t.backend = backendNone
}
