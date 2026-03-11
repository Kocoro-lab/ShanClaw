package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"regexp"
	"runtime"
	"strings"

	_ "image/jpeg"

	"github.com/Kocoro-lab/shan/internal/agent"
)

type refEntry struct {
	path string
	role string
	pid  int
}

type appContext struct {
	App            string `json:"app"`
	Window         string `json:"window"`
	URL            string `json:"url,omitempty"`
	FocusedElement string `json:"focused_element,omitempty"`
}

func formatContext(ctx *appContext) string {
	if ctx == nil {
		return ""
	}
	msg := fmt.Sprintf("\n[context: %s — %s", ctx.App, ctx.Window)
	if ctx.URL != "" {
		msg += fmt.Sprintf(" (%s)", ctx.URL)
	}
	if ctx.FocusedElement != "" {
		msg += fmt.Sprintf(", focused: %s", ctx.FocusedElement)
	}
	msg += "]"
	return msg
}

type AccessibilityTool struct {
	client  *AXClient
	refs    map[string]refEntry
	lastPID int
}

type accessibilityArgs struct {
	Action       string   `json:"action"`
	App          string   `json:"app,omitempty"`
	MaxDepth     int      `json:"max_depth,omitempty"`
	Budget       int      `json:"semantic_budget,omitempty"`
	Filter       string   `json:"filter,omitempty"`
	Ref          string   `json:"ref,omitempty"`
	Value        *string  `json:"value,omitempty"`
	Query        string   `json:"query,omitempty"`
	Role         string   `json:"role,omitempty"`
	Identifier   string   `json:"identifier,omitempty"`
	DX           int      `json:"dx,omitempty"`
	DY           int      `json:"dy,omitempty"`
	Roles        []string `json:"roles,omitempty"`
	MaxLabels    int      `json:"max_labels,omitempty"`
}

func (t *AccessibilityTool) Info() agent.ToolInfo {
	return agent.ToolInfo{
		Name:        "accessibility",
		Description: "Interact with macOS apps via the accessibility tree. Workflow: (1) Use 'annotate' to get a labeled screenshot with numbered elements, (2) click/type by ref. Actions: read_tree, click, press, set_value, get_value, find, scroll, annotate. Always specify 'app' parameter with the exact app name. For web content in browsers, prefer the browser tool instead.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":          map[string]any{"type": "string", "description": "Action: read_tree, click, press, set_value, get_value, find, scroll, annotate"},
				"app":             map[string]any{"type": "string", "description": "Target app name (defaults to frontmost app)"},
				"max_depth":       map[string]any{"type": "integer", "description": "Tree depth (default: 25 semantic budget, layout containers cost 0)"},
				"semantic_budget": map[string]any{"type": "integer", "description": "Semantic depth budget (default: 25, layout containers cost 0 depth)"},
				"filter":          map[string]any{"type": "string", "description": "Filter: all (default) or interactive (for read_tree)"},
				"ref":             map[string]any{"type": "string", "description": "Element ref from read_tree (e.g. e14, for click/press/set_value/get_value/scroll)"},
				"value":           map[string]any{"type": "string", "description": "Value to set (for set_value)"},
				"query":           map[string]any{"type": "string", "description": "Text to search for (for find, case-insensitive substring)"},
				"role":            map[string]any{"type": "string", "description": "AX role filter (for find, e.g. AXButton)"},
				"identifier":     map[string]any{"type": "string", "description": "AX identifier to find (exact match, for find)"},
				"dx":              map[string]any{"type": "integer", "description": "Horizontal scroll amount in pixels (for scroll)"},
				"dy":              map[string]any{"type": "integer", "description": "Vertical scroll amount in pixels (for scroll, positive=down)"},
				"roles":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Filter by AX roles (for annotate, e.g. [\"AXButton\", \"AXTextField\"])"},
				"max_labels":      map[string]any{"type": "integer", "description": "Max elements to annotate (default: 50, for annotate)"},
			},
		},
		Required: []string{"action"},
	}
}

func (t *AccessibilityTool) RequiresApproval() bool { return true }

func (t *AccessibilityTool) Run(ctx context.Context, argsJSON string) (agent.ToolResult, error) {
	if runtime.GOOS != "darwin" || t.client == nil {
		return agent.ToolResult{Content: "accessibility tool is only available on macOS", IsError: true}, nil
	}

	var args accessibilityArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}

	if args.Action == "" {
		return agent.ToolResult{Content: "missing required parameter: action", IsError: true}, nil
	}

	switch args.Action {
	case "read_tree":
		return t.readTree(ctx, args)
	case "click", "press":
		return t.performAction(ctx, args.Action, args.Ref)
	case "set_value":
		return t.setValue(ctx, args.Ref, args.Value)
	case "get_value":
		return t.getValue(ctx, args.Ref)
	case "find":
		return t.find(ctx, args)
	case "scroll":
		return t.scroll(ctx, args)
	case "annotate":
		return t.annotate(ctx, args)
	default:
		return agent.ToolResult{
			Content: fmt.Sprintf("unknown action: %q (valid: read_tree, click, press, set_value, get_value, find, scroll, annotate)", args.Action),
			IsError: true,
		}, nil
	}
}

// validAppName checks that an app name contains only safe characters.
var validAppNamePattern = regexp.MustCompile(`^[a-zA-Z0-9 ._\-()]+$`)

func (t *AccessibilityTool) resolvePID(ctx context.Context, appName string) (int, error) {
	if appName == "" {
		return 0, nil // ax_server will use frontmost
	}
	if !validAppNamePattern.MatchString(appName) {
		return 0, fmt.Errorf("invalid app name %q — only letters, numbers, spaces, dots, hyphens, underscores, and parentheses allowed", appName)
	}
	result, err := t.client.Call(ctx, "resolve_pid", map[string]any{"app_name": appName})
	if err != nil {
		return 0, fmt.Errorf("app %q not found or not running", appName)
	}
	var pidResult struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(result, &pidResult); err != nil {
		return 0, fmt.Errorf("could not parse PID for %q", appName)
	}
	return pidResult.PID, nil
}

func (t *AccessibilityTool) readTree(ctx context.Context, args accessibilityArgs) (agent.ToolResult, error) {
	pid, err := t.resolvePID(ctx, args.App)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	params := map[string]any{
		"filter": args.Filter,
	}
	if args.Filter == "" {
		params["filter"] = "all"
	}
	if pid > 0 {
		params["pid"] = pid
	}
	// Semantic budget takes priority over max_depth
	if args.Budget > 0 {
		params["semantic_budget"] = args.Budget
	} else if args.MaxDepth > 0 {
		params["max_depth"] = args.MaxDepth
	}

	result, err := t.client.Call(ctx, "read_tree", params)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("accessibility error: %v", err), IsError: true}, nil
	}

	var treeResult struct {
		App      string                            `json:"app"`
		PID      int                               `json:"pid"`
		Window   string                            `json:"window"`
		Elements []any                             `json:"elements"`
		RefPaths map[string]map[string]string       `json:"ref_paths"`
	}
	if err := json.Unmarshal(result, &treeResult); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("parse error: %v", err), IsError: true}, nil
	}

	t.refs = make(map[string]refEntry)
	t.lastPID = treeResult.PID

	for ref, entry := range treeResult.RefPaths {
		t.refs[ref] = refEntry{
			path: entry["path"],
			role: entry["role"],
			pid:  treeResult.PID,
		}
	}

	// Remove ref_paths from output (agent doesn't need them)
	var outputMap map[string]any
	json.Unmarshal(result, &outputMap)
	delete(outputMap, "ref_paths")

	outputJSON, _ := json.MarshalIndent(outputMap, "", "  ")
	content := string(outputJSON)

	// Truncate if too large
	if len(content) > 8000 {
		if elems, ok := outputMap["elements"].([]any); ok {
			lo, hi := 0, len(elems)
			for lo < hi {
				mid := (lo + hi + 1) / 2
				outputMap["elements"] = elems[:mid]
				trial, _ := json.MarshalIndent(outputMap, "", "  ")
				if len(trial) <= 7800 {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			outputMap["elements"] = elems[:lo]
			outputMap["truncated"] = fmt.Sprintf("showing %d of %d elements — use filter='interactive' or lower semantic_budget", lo, len(elems))
			outputJSON, _ = json.MarshalIndent(outputMap, "", "  ")
			content = string(outputJSON)
		}
	}

	return agent.ToolResult{Content: content}, nil
}

func (t *AccessibilityTool) lookupRef(ref string) (refEntry, error) {
	if ref == "" {
		return refEntry{}, fmt.Errorf("missing required parameter: ref")
	}
	if t.refs == nil || len(t.refs) == 0 {
		return refEntry{}, fmt.Errorf("no refs available — call read_tree first")
	}
	entry, ok := t.refs[ref]
	if !ok {
		return refEntry{}, fmt.Errorf("unknown ref %q — call read_tree to get current refs", ref)
	}
	return entry, nil
}

func (t *AccessibilityTool) performAction(ctx context.Context, action string, ref string) (agent.ToolResult, error) {
	entry, err := t.lookupRef(ref)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	params := map[string]any{
		"pid":  entry.pid,
		"path": entry.path,
	}
	if entry.role != "" {
		params["expected_role"] = entry.role
	}

	result, err := t.client.Call(ctx, action, params)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("accessibility error: %v", err), IsError: true}, nil
	}

	var actionResult struct {
		Result  string      `json:"result"`
		Context *appContext `json:"context,omitempty"`
	}
	json.Unmarshal(result, &actionResult)
	return agent.ToolResult{Content: actionResult.Result + formatContext(actionResult.Context)}, nil
}

func (t *AccessibilityTool) setValue(ctx context.Context, ref string, value *string) (agent.ToolResult, error) {
	entry, err := t.lookupRef(ref)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if value == nil {
		return agent.ToolResult{Content: "set_value requires 'value' parameter", IsError: true}, nil
	}

	params := map[string]any{
		"pid":   entry.pid,
		"path":  entry.path,
		"value": *value,
	}
	if entry.role != "" {
		params["expected_role"] = entry.role
	}

	result, err := t.client.Call(ctx, "set_value", params)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("accessibility error: %v", err), IsError: true}, nil
	}

	var actionResult struct {
		Result  string      `json:"result"`
		Context *appContext `json:"context,omitempty"`
	}
	json.Unmarshal(result, &actionResult)
	return agent.ToolResult{Content: actionResult.Result + formatContext(actionResult.Context)}, nil
}

func (t *AccessibilityTool) getValue(ctx context.Context, ref string) (agent.ToolResult, error) {
	entry, err := t.lookupRef(ref)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	params := map[string]any{
		"pid":  entry.pid,
		"path": entry.path,
	}

	result, err := t.client.Call(ctx, "get_value", params)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("accessibility error: %v", err), IsError: true}, nil
	}

	var actionResult struct {
		Result  string      `json:"result"`
		Role    string      `json:"role"`
		Context *appContext `json:"context,omitempty"`
	}
	json.Unmarshal(result, &actionResult)
	msg := actionResult.Result
	if actionResult.Role != "" {
		msg = fmt.Sprintf("%s (role: %s)", msg, actionResult.Role)
	}
	msg += formatContext(actionResult.Context)
	return agent.ToolResult{Content: msg}, nil
}

func (t *AccessibilityTool) find(ctx context.Context, args accessibilityArgs) (agent.ToolResult, error) {
	pid, err := t.resolvePID(ctx, args.App)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	params := map[string]any{}
	if pid > 0 {
		params["pid"] = pid
	}
	if args.Query != "" {
		params["query"] = args.Query
	}
	if args.Role != "" {
		params["role"] = args.Role
	}
	if args.Identifier != "" {
		params["identifier"] = args.Identifier
	}

	result, err := t.client.Call(ctx, "find", params)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("find error: %v", err), IsError: true}, nil
	}

	outputJSON, _ := json.MarshalIndent(json.RawMessage(result), "", "  ")
	content := string(outputJSON)
	if len(content) > 8000 {
		content = content[:7900] + "\n... [truncated]"
	}
	return agent.ToolResult{Content: content}, nil
}

func (t *AccessibilityTool) annotate(ctx context.Context, args accessibilityArgs) (agent.ToolResult, error) {
	pid, err := t.resolvePID(ctx, args.App)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	params := map[string]any{}
	if pid > 0 {
		params["pid"] = pid
	}
	if len(args.Roles) > 0 {
		params["roles"] = args.Roles
	}
	if args.MaxLabels > 0 {
		params["max_labels"] = args.MaxLabels
	}

	result, err := t.client.Call(ctx, "annotate", params)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("annotate error: %v", err), IsError: true}, nil
	}

	// Parse the annotation result
	var annotateResult struct {
		App         string                       `json:"app"`
		PID         int                          `json:"pid"`
		Window      string                       `json:"window"`
		Annotations []annotationEntry            `json:"annotations"`
		RefPaths    map[string]map[string]string `json:"ref_paths"`
	}
	if err := json.Unmarshal(result, &annotateResult); err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("parse error: %v", err), IsError: true}, nil
	}

	// Store refs so the agent can click by ref after annotating
	t.refs = make(map[string]refEntry)
	t.lastPID = annotateResult.PID
	for ref, entry := range annotateResult.RefPaths {
		t.refs[ref] = refEntry{
			path: entry["path"],
			role: entry["role"],
			pid:  annotateResult.PID,
		}
	}

	// Build text index
	lines := make([]string, 0, len(annotateResult.Annotations)+1)
	lines = append(lines, fmt.Sprintf("App: %s | Window: %s | %d elements", annotateResult.App, annotateResult.Window, len(annotateResult.Annotations)))
	for _, a := range annotateResult.Annotations {
		title := a.Title
		if title == "" {
			title = "(untitled)"
		}
		lines = append(lines, fmt.Sprintf("[%d] ref=%s %s %q (%.0f, %.0f, %.0f x %.0f)", a.Label, a.Ref, a.Role, title, a.X, a.Y, a.Width, a.Height))
	}
	content := strings.Join(lines, "\n")

	// Take a screenshot and draw annotation markers on it
	screenshotPath, imgBlock, captureErr := CaptureAndEncode(DefaultAPIWidth)
	var images []agent.ImageBlock
	if captureErr == nil {
		// Get screen dimensions for coordinate mapping
		screenW, screenH, dimErr := GetScreenDimensions()
		if dimErr == nil && len(annotateResult.Annotations) > 0 {
			annotatedBlock, annotErr := drawAnnotations(screenshotPath, annotateResult.Annotations, screenW, screenH)
			if annotErr == nil {
				imgBlock = annotatedBlock
			}
		}
		images = append(images, imgBlock)
		// Clean up original screenshot temp file
		os.Remove(screenshotPath)
	}

	return agent.ToolResult{
		Content: content,
		Images:  images,
	}, nil
}

func (t *AccessibilityTool) scroll(ctx context.Context, args accessibilityArgs) (agent.ToolResult, error) {
	pid := t.lastPID
	var path *string
	if args.Ref != "" {
		entry, err := t.lookupRef(args.Ref)
		if err != nil {
			return agent.ToolResult{Content: err.Error(), IsError: true}, nil
		}
		pid = entry.pid
		path = &entry.path
	}

	params := map[string]any{
		"dx": args.DX,
		"dy": args.DY,
	}
	if pid > 0 {
		params["pid"] = pid
	}
	if path != nil {
		params["path"] = *path
	}

	result, err := t.client.Call(ctx, "scroll", params)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("scroll error: %v", err), IsError: true}, nil
	}

	var actionResult struct {
		Result  string      `json:"result"`
		Context *appContext `json:"context,omitempty"`
	}
	json.Unmarshal(result, &actionResult)
	return agent.ToolResult{Content: actionResult.Result + formatContext(actionResult.Context)}, nil
}

type annotationEntry struct {
	Label  int     `json:"label"`
	Ref    string  `json:"ref"`
	Role   string  `json:"role"`
	Title  string  `json:"title,omitempty"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// drawAnnotations loads a screenshot image and draws numbered markers at each
// annotation's center position. Returns the annotated image as an ImageBlock.
func drawAnnotations(imgPath string, annotations []annotationEntry, screenW, screenH int) (agent.ImageBlock, error) {
	f, err := os.Open(imgPath)
	if err != nil {
		return agent.ImageBlock{}, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return agent.ImageBlock{}, err
	}

	bounds := img.Bounds()
	annotated := image.NewRGBA(bounds)
	draw.Draw(annotated, bounds, img, image.Point{}, draw.Src)

	// Scale: screen coordinates -> image coordinates
	scaleX := float64(bounds.Dx()) / float64(screenW)
	scaleY := float64(bounds.Dy()) / float64(screenH)

	for _, a := range annotations {
		// Center of element in screen coords -> image coords
		cx := int((a.X + a.Width/2) * scaleX)
		cy := int((a.Y + a.Height/2) * scaleY)
		drawMarker(annotated, cx, cy, a.Label)
	}

	// Write annotated image to a temp file
	outFile, err := os.CreateTemp("", "shannon-annotated-*.png")
	if err != nil {
		return agent.ImageBlock{}, err
	}
	defer outFile.Close()

	if err := png.Encode(outFile, annotated); err != nil {
		os.Remove(outFile.Name())
		return agent.ImageBlock{}, err
	}

	block, err := EncodeImage(outFile.Name())
	os.Remove(outFile.Name()) // clean up temp file after encoding
	if err != nil {
		return agent.ImageBlock{}, err
	}
	return block, nil
}

// drawMarker draws a filled circle with a contrasting border at (x, y) on the image.
func drawMarker(img *image.RGBA, x, y, label int) {
	radius := 10
	bounds := img.Bounds()
	red := color.RGBA{R: 255, G: 50, B: 50, A: 230}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}

	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			dist := math.Sqrt(float64(dx*dx + dy*dy))
			if dist > float64(radius) {
				continue
			}
			px, py := x+dx, y+dy
			if px < bounds.Min.X || px >= bounds.Max.X || py < bounds.Min.Y || py >= bounds.Max.Y {
				continue
			}
			if dist > float64(radius-2) {
				img.Set(px, py, white)
			} else {
				img.Set(px, py, red)
			}
		}
	}

	// Draw label number using simple pixel font
	drawLabelNumber(img, x, y, label, white)
}

// digitPatterns contains 5x7 bitmap patterns for digits 0-9.
// Each digit is a 5-wide, 7-tall grid stored as 7 bytes where bits 4..0 represent columns.
var digitPatterns = [10][7]byte{
	{0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E}, // 0
	{0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E}, // 1
	{0x0E, 0x11, 0x01, 0x06, 0x08, 0x10, 0x1F}, // 2
	{0x0E, 0x11, 0x01, 0x06, 0x01, 0x11, 0x0E}, // 3
	{0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02}, // 4
	{0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E}, // 5
	{0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E}, // 6
	{0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08}, // 7
	{0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E}, // 8
	{0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C}, // 9
}

// drawLabelNumber renders a number at position (cx, cy) using a simple bitmap font.
func drawLabelNumber(img *image.RGBA, cx, cy, num int, col color.RGBA) {
	s := fmt.Sprintf("%d", num)
	totalW := len(s) * 6 // 5px wide + 1px gap per digit
	startX := cx - totalW/2
	startY := cy - 3 // center vertically (7px tall / 2)
	bounds := img.Bounds()

	for i, ch := range s {
		d := int(ch - '0')
		if d < 0 || d > 9 {
			continue
		}
		ox := startX + i*6
		for row := 0; row < 7; row++ {
			bits := digitPatterns[d][row]
			for colIdx := 0; colIdx < 5; colIdx++ {
				if bits&(1<<uint(4-colIdx)) != 0 {
					px, py := ox+colIdx, startY+row
					if px >= bounds.Min.X && px < bounds.Max.X && py >= bounds.Min.Y && py < bounds.Max.Y {
						img.Set(px, py, col)
					}
				}
			}
		}
	}
}
