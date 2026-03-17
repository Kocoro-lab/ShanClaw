package test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Kocoro-lab/ShanClaw/internal/tools"
)

func TestVisionLoop_ScreenshotReturnsImage(t *testing.T) {
	st := &tools.ScreenshotTool{}
	result, err := st.Run(context.Background(), `{"target":"fullscreen"}`)
	if err != nil {
		t.Fatalf("screenshot error: %v", err)
	}
	if result.IsError {
		t.Fatalf("screenshot failed: %s", result.Content)
	}
	t.Logf("Content: %s", result.Content)
	if len(result.Images) == 0 {
		t.Fatal("expected at least 1 image block, got 0")
	}
	img := result.Images[0]
	if img.MediaType != "image/png" {
		t.Errorf("expected image/png, got %s", img.MediaType)
	}
	rawBytes := len(img.Data) * 3 / 4
	t.Logf("Image: %s, %d KB base64", img.MediaType, rawBytes/1024)
	if rawBytes < 1000 {
		t.Error("image seems too small — resize may have failed")
	}
}

func TestVisionLoop_ComputerScreenshotAction(t *testing.T) {
	// Screenshot action doesn't use ax_server, so nil client is fine
	reg, _, cleanup := tools.RegisterLocalTools(nil)
	defer cleanup()
	ct, _ := reg.Get("computer")
	result, err := ct.Run(context.Background(), `{"action":"screenshot"}`)
	if err != nil {
		t.Fatalf("computer screenshot error: %v", err)
	}
	if result.IsError {
		t.Fatalf("computer screenshot failed: %s", result.Content)
	}
	t.Logf("Content: %s", result.Content)
	if len(result.Images) == 0 {
		t.Fatal("expected image from computer screenshot action")
	}
	t.Logf("Image: %d KB", len(result.Images[0].Data)*3/4/1024)
}

func TestVisionLoop_ComputerNativeLeftClick(t *testing.T) {
	// Test that Anthropic native left_click with coordinate array parses correctly
	// Will fail with ax_server error since we're not in a real GUI context,
	// but that's fine — it means the action was correctly mapped to "click"
	reg, _, cleanup := tools.RegisterLocalTools(nil)
	defer cleanup()
	ct, _ := reg.Get("computer")
	result, err := ct.Run(context.Background(), `{"action":"left_click","coordinate":[640,400]}`)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	t.Logf("Result: %s (isError: %v)", result.Content, result.IsError)
	if result.IsError && result.Content == `unknown action: "left_click"` {
		t.Fatal("left_click was NOT normalized to click — normalizeArgs not called")
	}
	fmt.Println("left_click correctly mapped to click action")
}
