package tools

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/Kocoro-lab/shan/internal/agent"
)

const (
	DefaultAPIWidth  = 1280
	DefaultAPIHeight = 800
	MaxScreenshotDim = 1200
)

// EncodeImage reads a PNG/JPEG file and returns it as a base64-encoded ImageBlock.
func EncodeImage(path string) (agent.ImageBlock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agent.ImageBlock{}, err
	}

	mediaType := "image/png"
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
		mediaType = "image/jpeg"
	}

	return agent.ImageBlock{
		MediaType: mediaType,
		Data:      base64.StdEncoding.EncodeToString(data),
	}, nil
}

// ResizeImage resizes an image so its longest edge is at most maxDim pixels.
// Uses macOS sips command.
func ResizeImage(path string, maxDim int) error {
	out, err := exec.Command("sips", "--resampleHeightWidthMax", strconv.Itoa(maxDim), path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sips resize: %v\n%s", err, string(out))
	}
	return nil
}

// CaptureAndEncode takes a fullscreen screenshot (-x flag for no sound), resizes, and base64-encodes.
// Returns the file path and encoded image block.
func CaptureAndEncode(maxDim int) (string, agent.ImageBlock, error) {
	f, err := os.CreateTemp("", "shannon-capture-*.png")
	if err != nil {
		return "", agent.ImageBlock{}, fmt.Errorf("create temp file: %v", err)
	}
	path := f.Name()
	f.Close()

	out, err := exec.Command("screencapture", "-x", path).CombinedOutput()
	if err != nil {
		os.Remove(path)
		return "", agent.ImageBlock{}, fmt.Errorf("screencapture: %v\n%s", err, string(out))
	}

	if maxDim > 0 {
		if err := ResizeImage(path, maxDim); err != nil {
			os.Remove(path)
			return "", agent.ImageBlock{}, err
		}
	}

	block, err := EncodeImage(path)
	if err != nil {
		os.Remove(path)
		return "", agent.ImageBlock{}, err
	}

	return path, block, nil
}

// GetScreenDimensions returns the logical screen dimensions of the main display.
// Uses system_profiler on macOS. Parses "Resolution:" or "UI Looks like:" lines.
func GetScreenDimensions() (width, height int, err error) {
	out, err := exec.Command("system_profiler", "SPDisplaysDataType").CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("system_profiler: %v", err)
	}
	return parseScreenDimensions(string(out))
}

// resolutionRe matches "WxH" or "W x H" with optional surrounding text.
var resolutionRe = regexp.MustCompile(`(\d+)\s*x\s*(\d+)`)

func parseScreenDimensions(output string) (int, int, error) {
	// Prefer "UI Looks like:" (logical resolution on Retina) over raw "Resolution:".
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "UI Looks like:") {
			m := resolutionRe.FindStringSubmatch(trimmed)
			if m != nil {
				w, _ := strconv.Atoi(m[1])
				h, _ := strconv.Atoi(m[2])
				return w, h, nil
			}
		}
	}

	// Fall back to "Resolution:" line.
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Resolution:") {
			m := resolutionRe.FindStringSubmatch(trimmed)
			if m != nil {
				w, _ := strconv.Atoi(m[1])
				h, _ := strconv.Atoi(m[2])
				return w, h, nil
			}
		}
	}

	return 0, 0, fmt.Errorf("no display resolution found in system_profiler output")
}

// ScaleCoordinates maps coordinates from API space to logical screen space.
func ScaleCoordinates(apiX, apiY, apiW, apiH, screenW, screenH int) (int, int) {
	x := apiX * screenW / apiW
	y := apiY * screenH / apiH
	return x, y
}

// ClampCoordinates ensures coordinates are within display bounds (0 to max-1).
func ClampCoordinates(x, y, maxW, maxH int) (int, int) {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x >= maxW {
		x = maxW - 1
	}
	if y >= maxH {
		y = maxH - 1
	}
	return x, y
}
