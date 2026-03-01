package tools

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func createMinimalPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func createMinimalJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{0, 255, 0, 255})
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

func TestEncodeImage(t *testing.T) {
	pngData := createMinimalPNG()
	path := filepath.Join(t.TempDir(), "test.png")
	if err := os.WriteFile(path, pngData, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	block, err := EncodeImage(path)
	if err != nil {
		t.Fatalf("EncodeImage error: %v", err)
	}
	if block.MediaType != "image/png" {
		t.Errorf("expected MediaType 'image/png', got %q", block.MediaType)
	}

	decoded, err := base64.StdEncoding.DecodeString(block.Data)
	if err != nil {
		t.Fatalf("base64 decode error: %v", err)
	}
	if !bytes.Equal(decoded, pngData) {
		t.Error("decoded base64 data does not match original PNG bytes")
	}
}

func TestEncodeImage_FileNotFound(t *testing.T) {
	_, err := EncodeImage("/nonexistent/file.png")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestEncodeImage_JPEG(t *testing.T) {
	jpegData := createMinimalJPEG()
	path := filepath.Join(t.TempDir(), "test.jpg")
	if err := os.WriteFile(path, jpegData, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	block, err := EncodeImage(path)
	if err != nil {
		t.Fatalf("EncodeImage error: %v", err)
	}
	if block.MediaType != "image/jpeg" {
		t.Errorf("expected MediaType 'image/jpeg', got %q", block.MediaType)
	}
}

func TestEncodeImage_JPEG_Uppercase(t *testing.T) {
	jpegData := createMinimalJPEG()
	path := filepath.Join(t.TempDir(), "test.JPEG")
	if err := os.WriteFile(path, jpegData, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	block, err := EncodeImage(path)
	if err != nil {
		t.Fatalf("EncodeImage error: %v", err)
	}
	if block.MediaType != "image/jpeg" {
		t.Errorf("expected MediaType 'image/jpeg', got %q", block.MediaType)
	}
}

func TestGetScreenDimensions(t *testing.T) {
	w, h, err := GetScreenDimensions()
	if err != nil {
		t.Skipf("skipping: no display available (%v)", err)
	}
	if w <= 0 {
		t.Errorf("expected width > 0, got %d", w)
	}
	if h <= 0 {
		t.Errorf("expected height > 0, got %d", h)
	}
}

func TestParseScreenDimensions_Resolution(t *testing.T) {
	output := `Graphics/Displays:

    Apple M2 Pro:

      Chipset Model: Apple M2 Pro
      Type: GPU
      Bus: Built-In
      Total Number of Cores: 19
      Vendor: Apple (0x106b)
      Metal Support: Metal 3
      Displays:
        Color LCD:
          Display Type: Built-In Retina LCD
          Resolution: 1512 x 982
          Main Display: Yes
          Mirror: Off
          Online: Yes
          Automatically Adjust Brightness: Yes
          Connection Type: Internal
`
	w, h, err := parseScreenDimensions(output)
	if err != nil {
		t.Fatalf("parseScreenDimensions error: %v", err)
	}
	if w != 1512 {
		t.Errorf("expected width 1512, got %d", w)
	}
	if h != 982 {
		t.Errorf("expected height 982, got %d", h)
	}
}

func TestParseScreenDimensions_UILooksLike(t *testing.T) {
	output := `Graphics/Displays:

    Apple M1:

      Chipset Model: Apple M1
      Displays:
        Color LCD:
          Display Type: Built-In Retina LCD
          Resolution: 2560 x 1600 (Retina)
          UI Looks like: 1440 x 900 @ 120.00Hz
          Main Display: Yes
`
	w, h, err := parseScreenDimensions(output)
	if err != nil {
		t.Fatalf("parseScreenDimensions error: %v", err)
	}
	if w != 1440 {
		t.Errorf("expected width 1440, got %d", w)
	}
	if h != 900 {
		t.Errorf("expected height 900, got %d", h)
	}
}

func TestParseScreenDimensions_RetinaStripped(t *testing.T) {
	output := `Graphics/Displays:

    Apple M2:

      Displays:
        Color LCD:
          Resolution: 2880 x 1800 (Retina)
          Main Display: Yes
`
	w, h, err := parseScreenDimensions(output)
	if err != nil {
		t.Fatalf("parseScreenDimensions error: %v", err)
	}
	if w != 2880 {
		t.Errorf("expected width 2880, got %d", w)
	}
	if h != 1800 {
		t.Errorf("expected height 1800, got %d", h)
	}
}

func TestParseScreenDimensions_NoDisplay(t *testing.T) {
	output := `Graphics/Displays:

    Apple M2 Pro:

      Chipset Model: Apple M2 Pro
`
	_, _, err := parseScreenDimensions(output)
	if err == nil {
		t.Error("expected error for output with no display info")
	}
}

func TestScaleCoordinates(t *testing.T) {
	// API: 1280x800, Screen: 1440x900
	x, y := ScaleCoordinates(640, 400, 1280, 800, 1440, 900)
	if x != 720 {
		t.Errorf("expected x=720, got %d", x)
	}
	if y != 450 {
		t.Errorf("expected y=450, got %d", y)
	}
}

func TestScaleCoordinates_Identity(t *testing.T) {
	x, y := ScaleCoordinates(100, 200, 1280, 800, 1280, 800)
	if x != 100 {
		t.Errorf("expected x=100, got %d", x)
	}
	if y != 200 {
		t.Errorf("expected y=200, got %d", y)
	}
}

func TestScaleCoordinates_Origin(t *testing.T) {
	x, y := ScaleCoordinates(0, 0, 1280, 800, 1920, 1080)
	if x != 0 {
		t.Errorf("expected x=0, got %d", x)
	}
	if y != 0 {
		t.Errorf("expected y=0, got %d", y)
	}
}

func TestScaleCoordinates_MaxCorner(t *testing.T) {
	x, y := ScaleCoordinates(1280, 800, 1280, 800, 1920, 1080)
	if x != 1920 {
		t.Errorf("expected x=1920, got %d", x)
	}
	if y != 1080 {
		t.Errorf("expected y=1080, got %d", y)
	}
}

func TestClampCoordinates(t *testing.T) {
	x, y := ClampCoordinates(-10, 1000, 1280, 800)
	if x != 0 {
		t.Errorf("expected x=0, got %d", x)
	}
	if y != 799 {
		t.Errorf("expected y=799, got %d", y)
	}
}

func TestClampCoordinates_InBounds(t *testing.T) {
	x, y := ClampCoordinates(500, 400, 1280, 800)
	if x != 500 {
		t.Errorf("expected x=500, got %d", x)
	}
	if y != 400 {
		t.Errorf("expected y=400, got %d", y)
	}
}

func TestClampCoordinates_BothNegative(t *testing.T) {
	x, y := ClampCoordinates(-5, -10, 1920, 1080)
	if x != 0 {
		t.Errorf("expected x=0, got %d", x)
	}
	if y != 0 {
		t.Errorf("expected y=0, got %d", y)
	}
}

func TestClampCoordinates_BothOverflow(t *testing.T) {
	x, y := ClampCoordinates(2000, 1500, 1280, 800)
	if x != 1279 {
		t.Errorf("expected x=1279, got %d", x)
	}
	if y != 799 {
		t.Errorf("expected y=799, got %d", y)
	}
}

func TestClampCoordinates_ExactBoundary(t *testing.T) {
	x, y := ClampCoordinates(1280, 800, 1280, 800)
	if x != 1279 {
		t.Errorf("expected x=1279, got %d", x)
	}
	if y != 799 {
		t.Errorf("expected y=799, got %d", y)
	}
}
