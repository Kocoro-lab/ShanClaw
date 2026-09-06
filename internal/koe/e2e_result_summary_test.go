//go:build darwin && !ios && cgo

package koe

// Live proof for the new result boundary. It gives Realtime a deliberately rich
// Kocoro final reply (Markdown, URL, numbers, and a deliverable) through the
// production ResultMailbox path, then verifies the native S2S model produces one
// concise, grounded Chinese spoken projection instead of reading the payload.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pion/webrtc/v4"
)

func TestKoeNativeResultSummaryE2E(t *testing.T) {
	if os.Getenv("KOE_E2E") != "1" {
		t.Skip("native result summary E2E: set KOE_E2E=1 (uses OPENAI_API_KEY or the running daemon mint relay)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ek, err := mintE2EEphemeral(ctx)
	if err != nil {
		t.Fatalf("mint Realtime token: %v", err)
	}
	audio, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	rc, err := newPeerConnection(audio)
	if err != nil {
		t.Fatalf("newPeerConnection: %v", err)
	}
	defer rc.Close()
	send := func(v any) error {
		body, _ := json.Marshal(v)
		return rc.dc.SendText(string(body))
	}

	mailbox := NewResultMailbox()
	h := newEventHandlerWithMailbox(nil, NewCallState("burst-summary-e2e", ""), audio, send, mailbox, nil)
	h.language = "zh"
	go h.runResponseSender(ctx)

	connected := make(chan struct{})
	configured := make(chan struct{})
	var connOnce, cfgOnce sync.Once
	var mu sync.Mutex
	var transcripts []string
	var apiErrors []string
	rc.pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			connOnce.Do(func() { close(connected) })
		}
	})
	rc.dc.OnOpen(func() {
		_ = send(sessionConfig("You are Kocoro, a concise native voice assistant. Speak in the language of the current user turn.", "marin", false))
	})
	rc.dc.OnMessage(func(m webrtc.DataChannelMessage) {
		var ev struct {
			Type       string `json:"type"`
			Transcript string `json:"transcript"`
		}
		_ = json.Unmarshal(m.Data, &ev)
		h.handleEvent(ctx, m.Data)
		switch ev.Type {
		case "session.updated":
			cfgOnce.Do(func() { close(configured) })
		case "response.output_audio_transcript.done":
			mu.Lock()
			transcripts = append(transcripts, ev.Transcript)
			mu.Unlock()
			t.Logf("[native result speech] %q", ev.Transcript)
		case "error", "response.failed":
			mu.Lock()
			apiErrors = append(apiErrors, string(m.Data))
			mu.Unlock()
		}
	})
	if err := rc.dialOpenAI(ctx, ek); err != nil {
		t.Fatalf("dial OpenAI: %v", err)
	}
	go rc.pumpSendTrack(ctx)
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("peer connection did not connect")
	}
	select {
	case <-configured:
	case <-ctx.Done():
		t.Fatal("session did not configure")
	}

	if err := send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "message", "role": "user",
		"content": []map[string]any{{"type": "input_text", "text": "帮我调研今天的 AI 新闻，结果好了以后直接告诉我重点。"}},
	}}); err != nil {
		t.Fatalf("inject user context: %v", err)
	}
	fullReply := `## 今日 AI 新闻

1. **Helio 发布 Atlas-7 推理芯片**：官方称相同吞吐下能耗降低 37%，首批计划在 7 月 30 日出货，重点面向端侧智能体。
2. **Cedar Lab 发布 M3 多模态模型**：支持图像和语音输入，并开放模型权重；团队强调它更适合本地隐私场景。
3. **Northstar 完成新一轮融资**：融资额为 1.2 亿美元，将用于扩建日本的数据中心。

需要注意：这些数字来自各公司公告，尚未经过独立基准复核。完整来源和对照表：https://example.invalid/ai-news-report`
	mailbox.Enqueue(SayResult{
		TaskID: "t01", Task: "调研今天的 AI 新闻", Revision: 1, Status: "ok", Reply: fullReply,
		Deliverables: []Deliverable{{ID: "d1", Filename: "ai-news-report.html", Title: "AI 新闻对照报告", MIME: "text/html", ByteSize: 8192}},
	}, false)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		joined := strings.Join(transcripts, " ")
		errs := append([]string(nil), apiErrors...)
		mu.Unlock()
		if joined != "" && mailbox.pending() == 0 {
			lower := strings.ToLower(joined)
			facts := 0
			for _, signature := range []string{"37", "atlas", "m3", "多模态", "1.2", "融资"} {
				if strings.Contains(lower, signature) {
					facts++
				}
			}
			if facts < 2 {
				t.Fatalf("native summary lost the result's key facts (facts=%d): %q", facts, joined)
			}
			assertSingleKocoroVoice(t, joined)
			han, latin := resultSpeechScriptCounts(joined)
			if han < 8 || han*2 < latin {
				t.Fatalf("native summary is not predominantly Chinese (han=%d latin=%d): %q", han, latin, joined)
			}
			if strings.Contains(lower, "https://") || strings.Contains(joined, "##") || strings.Contains(joined, "**") {
				t.Fatalf("native summary read markup or URL aloud: %q", joined)
			}
			if utf8.RuneCountInString(joined) >= utf8.RuneCountInString(fullReply) {
				t.Fatalf("native summary did not compress the complete reply: speech=%d reply=%d", utf8.RuneCountInString(joined), utf8.RuneCountInString(fullReply))
			}
			t.Logf("VERDICT: native summary grounded facts=%d, speech_runes=%d, full_reply_runes=%d, responses=%d",
				facts, utf8.RuneCountInString(joined), utf8.RuneCountInString(fullReply), len(transcripts))
			return
		}
		if len(errs) > 0 {
			t.Fatalf("Realtime rejected native result delivery: %s", strings.Join(errs, "\n"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("native result summary timed out: transcripts=%v errors=%v mailbox_pending=%d", transcripts, apiErrors, mailbox.pending())
}

func mintE2EEphemeral(ctx context.Context) (string, error) {
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		return mintEphemeral(ctx, apiKey, e2eModelName())
	}
	daemonBase := os.Getenv("KOE_DAEMON_URL")
	if daemonBase == "" {
		daemonBase = "http://127.0.0.1:7533"
	}
	return NewDaemonClient(daemonBase).MintViaDaemon(ctx, e2eModelName())
}

func resultSpeechScriptCounts(s string) (han, latin int) {
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.Is(unicode.Latin, r):
			latin++
		}
	}
	return han, latin
}

func assertSingleKocoroVoice(t *testing.T, speech string) {
	t.Helper()
	normalized := strings.ToLower(speech)
	normalized = strings.ReplaceAll(normalized, "kocoro desktop", "")
	for _, banned := range []string{
		"kocoro's result",
		"kocoro found",
		"ask kocoro",
		"kocoro 的结果",
		"kocoro查",
		"让 kocoro",
	} {
		if strings.Contains(normalized, banned) {
			t.Fatalf("native summary framed Kocoro as a separate worker or result source (%q): %q", banned, speech)
		}
	}
}
