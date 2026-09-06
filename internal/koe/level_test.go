//go:build darwin && !ios && cgo

package koe

import (
	"bufio"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRMSLevel(t *testing.T) {
	if g := rmsLevel(nil); g != 0 {
		t.Errorf("rmsLevel(nil) = %v, want 0", g)
	}
	if g := rmsLevel(make([]int16, 960)); g != 0 {
		t.Errorf("rmsLevel(silence) = %v, want 0", g)
	}
	full := make([]int16, 960)
	for i := range full {
		full[i] = 32767
	}
	if g := rmsLevel(full); math.Abs(g-1) > 0.001 {
		t.Errorf("rmsLevel(full-scale) = %v, want ~1", g)
	}
	// A half-scale sine: RMS = amplitude / sqrt(2), normalized to full scale.
	sine := make([]int16, 4800)
	for i := range sine {
		sine[i] = int16(16384 * math.Sin(2*math.Pi*440*float64(i)/48000))
	}
	want := 16384.0 / math.Sqrt2 / 32768.0 // ~0.3536
	if g := rmsLevel(sine); math.Abs(g-want) > 0.02 {
		t.Errorf("rmsLevel(half-scale sine) = %v, want ~%v", g, want)
	}
}

func TestAudioLevelAccessors(t *testing.T) {
	a := &AudioIO{}
	if g := a.InputLevel(); g != 0 {
		t.Errorf("initial InputLevel = %v, want 0", g)
	}
	if g := a.OutputLevel(); g != 0 {
		t.Errorf("initial OutputLevel = %v, want 0", g)
	}
	a.setInputLevel(0.42)
	a.setOutputLevel(0.73)
	if g := a.InputLevel(); math.Abs(g-0.42) > 1e-9 {
		t.Errorf("InputLevel = %v, want 0.42", g)
	}
	if g := a.OutputLevel(); math.Abs(g-0.73) > 1e-9 {
		t.Errorf("OutputLevel = %v, want 0.73", g)
	}
}

// TestEmitVoiceLevelSSE pins the D3w wire shape: voice_state carries an additive
// `level` field. The Desktop sprite decoder mirrors this.
func TestEmitVoiceLevelSSE(t *testing.T) {
	s := NewControlServer(nil, nil, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for s.subscriberCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	s.EmitVoiceLevel("speaking", 0.5)

	br := bufio.NewReader(resp.Body)
	readDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(readDeadline) {
		line, rerr := br.ReadString('\n')
		if rerr != nil {
			break
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			got := strings.TrimSpace(data)
			want := `{"type":"voice_state","state":"speaking","level":0.5}`
			if got != want {
				t.Errorf("SSE = %q, want %q", got, want)
			}
			return
		}
	}
	t.Error("no voice_state level event received")
}
