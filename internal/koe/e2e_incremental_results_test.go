//go:build darwin && !ios && cgo

package koe

// Live Realtime regression for staggered parallel results. The first task result
// is claimed before the second lands, exactly like two independent daemon runs
// completing at slightly different times. Each response must describe only its
// newly delivered batch: absence from the first batch is not evidence that the
// other task failed, is missing, or is still running.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestKoeDefaultCompoundRequestUsesOneDoTaskE2E(t *testing.T) {
	if os.Getenv("KOE_E2E") != "1" {
		t.Skip("default compound routing E2E: set KOE_E2E=1 (uses OPENAI_API_KEY or the running daemon mint relay)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ek, err := mintE2EEphemeral(ctx)
	if err != nil {
		t.Fatalf("mint Realtime token: %v", err)
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	requests := make(chan DoTaskRequest, 3)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req DoTaskRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		requests <- req
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"reply": "东京今天晴，适合轻薄衣物，午后注意防晒。"})
	}))
	defer mock.Close()

	audio, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	state := NewCallState("burst-default-compound-e2e", "")
	disp := NewDispatcher(NewDaemonClient(mock.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)
	rc, err := newPeerConnection(audio)
	if err != nil {
		t.Fatalf("newPeerConnection: %v", err)
	}
	defer rc.Close()

	send := func(v any) error {
		body, _ := json.Marshal(v)
		return rc.sendText(string(body))
	}
	h := newEventHandler(disp, state, audio, send)
	h.language = "zh"
	go h.runResponseSender(ctx)

	connected := make(chan struct{})
	configured := make(chan struct{})
	var connOnce, cfgOnce sync.Once
	rc.pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			connOnce.Do(func() { close(connected) })
		}
	})
	persona := "You are Kocoro, a concise Chinese voice assistant. For current weather use do_task. " +
		ParallelTaskInstructions + " Before a do_task call say only 我查一下."
	rc.dc.OnOpen(func() { _ = send(sessionConfig(persona, "marin", false)) })
	rc.dc.OnMessage(func(m webrtc.DataChannelMessage) {
		var ev struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(m.Data, &ev)
		h.handleEvent(ctx, m.Data)
		if ev.Type == "session.updated" {
			cfgOnce.Do(func() { close(configured) })
		}
	})
	if err := rc.dialOpenAI(ctx, ek); err != nil {
		t.Fatalf("dial OpenAI: %v", err)
	}
	go rc.pumpSendTrack(ctx)
	waitIncrementalSignal(t, ctx, connected, "peer connection did not connect")
	waitIncrementalSignal(t, ctx, configured, "session did not configure")

	turnID := h.inputCommitSeq.Add(1)
	h.toolLoop.noteUserCommit(turnID)
	if err := send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "message", "role": "user",
		"content": []map[string]any{{"type": "input_text", "text": "查询今天东京的天气，并根据天气整理穿衣和出行建议。"}},
	}}); err != nil {
		t.Fatalf("send user turn: %v", err)
	}
	h.queueLoopResponse(responseCreateRequest{
		purpose: responsePurposeUser, turnID: turnID,
		toolMode: responseToolsEnabled, dropIfPreempted: true,
	})

	var first DoTaskRequest
	select {
	case first = <-requests:
	case <-ctx.Done():
		t.Fatal("model did not dispatch the compound task")
	}
	lower := strings.ToLower(first.Text)
	hasWeather := strings.Contains(first.Text, "天气") || strings.Contains(lower, "weather")
	hasAdvice := strings.Contains(first.Text, "穿衣") || strings.Contains(first.Text, "出行") ||
		strings.Contains(lower, "clothing") || strings.Contains(lower, "travel") || strings.Contains(lower, "advice")
	if !hasWeather || !hasAdvice {
		t.Fatalf("single compound task lost requested scope: %+v", first)
	}
	waitCompoundIdle(t, ctx, h, "default compound response did not settle")
	select {
	case second := <-requests:
		t.Fatalf("non-parallel compound request dispatched a second task: first=%+v second=%+v", first, second)
	case <-time.After(1500 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	t.Logf("VERDICT: non-parallel multi-part request dispatched exactly one complete do_task: %q", first.Text)
}

func TestKoeStaggeredParallelDeliveryE2E(t *testing.T) {
	if os.Getenv("KOE_E2E") != "1" {
		t.Skip("staggered full-path E2E: set KOE_E2E=1 (uses OPENAI_API_KEY or the running daemon mint relay)")
	}
	runKoeStaggeredParallelDeliveryE2E(t, ProviderOpenAI)
}

func TestKoeQwenStaggeredParallelDeliveryE2E(t *testing.T) {
	if os.Getenv("KOE_QWEN_E2E") != "1" {
		t.Skip("Qwen staggered full-path E2E: set KOE_QWEN_E2E=1 (uses the running daemon SDP relay)")
	}
	runKoeStaggeredParallelDeliveryE2E(t, ProviderQwen)
}

func runKoeStaggeredParallelDeliveryE2E(t *testing.T, provider RealtimeProvider) {
	t.Helper()
	timeout := 150 * time.Second
	if provider == ProviderQwen {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var ek string
	var err error
	if provider == ProviderOpenAI {
		ek, err = mintE2EEphemeral(ctx)
		if err != nil {
			t.Fatalf("mint Realtime token: %v", err)
		}
	}

	weatherRelease := make(chan struct{})
	newsRelease := make(chan struct{})
	var weatherOnce, newsOnce sync.Once
	requests := make(chan DoTaskRequest, 2)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/message" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req DoTaskRequest
		_ = json.Unmarshal(body, &req)
		requests <- req
		lower := strings.ToLower(req.Text)
		switch {
		case strings.Contains(req.Text, "天气") || strings.Contains(lower, "weather"):
			select {
			case <-weatherRelease:
			case <-r.Context().Done():
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"reply": "东京今天晴，最高气温36摄氏度，午后西部山区有短时雷阵雨风险。",
			})
		case strings.Contains(req.Text, "新闻") || strings.Contains(lower, "news"):
			select {
			case <-newsRelease:
			case <-r.Context().Done():
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"reply": "今天的重要科技新闻是 Helio 发布 Atlas-7 推理芯片，官方称同等吞吐下能耗降低37%。",
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "failed", "failure_code": "unexpected_task", "reason": req.Text,
			})
		}
	}))
	defer func() {
		weatherOnce.Do(func() { close(weatherRelease) })
		newsOnce.Do(func() { close(newsRelease) })
		mock.Close()
	}()

	audio, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	state := NewCallState("burst-staggered-e2e-"+string(provider), "")
	disp := NewDispatcher(NewDaemonClient(mock.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)
	var videoSource *RealtimeVideoSource
	if provider == ProviderQwen {
		// The active-call path below is text-injected, so the source is never read;
		// attaching it still proves the real Qwen SDP relay accepts Koe's H264 video
		// m-line alongside the existing audio and DataChannel transports.
		videoSource = &RealtimeVideoSource{
			Codec: VideoCodecH264,
			ReadFrame: func(context.Context) ([]byte, error) {
				return nil, errors.New("unexpected video read in text-only E2E")
			},
		}
	}
	rc, err := newPeerConnectionForProviderWithVideo(audio, provider, videoSource)
	if err != nil {
		t.Fatalf("newPeerConnection: %v", err)
	}
	defer rc.Close()

	var resultInjections atomic.Int32
	var functionOutputs atomic.Int32
	var h *eventHandler
	send := func(v any) error {
		body, _ := json.Marshal(v)
		if strings.Contains(string(body), "kocoro.task_results.v1") {
			resultInjections.Add(1)
		}
		if strings.Contains(string(body), `"type":"function_call_output"`) {
			functionOutputs.Add(1)
		}
		return rc.dc.SendText(string(body))
	}
	h = newEventHandler(disp, state, audio, send)
	h.provider = string(provider)
	if provider == ProviderQwen {
		h.model = DefaultQwenRealtimeModel
	} else {
		h.model = DefaultOpenAIRealtimeModel
	}
	h.language = "zh"
	go h.runResponseSender(ctx)

	connected := make(chan struct{})
	dataChannelOpen := make(chan struct{})
	configured := make(chan struct{})
	var connOnce, dcOnce, cfgOnce sync.Once
	var eventMu sync.Mutex
	var transcripts []string
	var apiErrors []string
	rc.pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			connOnce.Do(func() { close(connected) })
		}
	})
	persona := "You are Kocoro, a concise Chinese voice assistant. For current weather and news, call do_task. " +
		ParallelTaskInstructions + " Before the calls say only 我查一下 and nothing else."
	var configOnce sync.Once
	sendConfig := func() {
		configOnce.Do(func() {
			_ = send(realtimeSessionPayload(
				provider,
				persona,
				DefaultOpenAIRealtimeVoice,
				DefaultQwenRealtimeVoice,
				ConnectOptions{},
				rc.videoTrack != nil,
			))
		})
	}
	handleMessage := func(m webrtc.DataChannelMessage) {
		var ev struct {
			Type       string `json:"type"`
			Transcript string `json:"transcript"`
		}
		_ = json.Unmarshal(m.Data, &ev)
		if provider == ProviderQwen && ev.Type == "session.created" {
			sendConfig()
		}
		h.handleEvent(ctx, m.Data)
		switch ev.Type {
		case "session.updated":
			cfgOnce.Do(func() { close(configured) })
		case "response.output_audio_transcript.done", "response.audio_transcript.done":
			eventMu.Lock()
			transcripts = append(transcripts, ev.Transcript)
			index := len(transcripts)
			eventMu.Unlock()
			t.Logf("[full-path speech %d] %q", index, ev.Transcript)
		case "error", "response.failed":
			eventMu.Lock()
			apiErrors = append(apiErrors, string(m.Data))
			eventMu.Unlock()
		}
	}
	wireDataChannel := func(dc *webrtc.DataChannel, configureOnOpen bool) {
		dc.OnOpen(func() {
			dcOnce.Do(func() { close(dataChannelOpen) })
			if configureOnOpen {
				sendConfig()
			}
		})
		dc.OnMessage(handleMessage)
	}
	wireDataChannel(rc.dc, provider == ProviderOpenAI)
	if provider == ProviderQwen {
		rc.pc.OnDataChannel(func(dc *webrtc.DataChannel) {
			if dc.Label() == "txt" {
				rc.setDataChannel(dc)
			}
			wireDataChannel(dc, false)
		})
	}
	if provider == ProviderQwen {
		daemonURL := os.Getenv("KOE_E2E_DAEMON_URL")
		if daemonURL == "" {
			daemonURL = "http://127.0.0.1:7533"
		}
		relay := NewDaemonClient(daemonURL)
		relay.SetToken(strings.TrimSpace(os.Getenv("KOE_DAEMON_TOKEN")))
		err = rc.dialQwen(ctx, func(exchangeCtx context.Context, offer string) (string, error) {
			answer, exchangeErr := relay.ExchangeSDPViaDaemon(exchangeCtx, string(ProviderQwen), DefaultQwenRealtimeModel, offer)
			if exchangeErr == nil {
				for _, line := range strings.Split(answer, "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "m=video") || strings.HasPrefix(line, "a=rtpmap:") || strings.HasPrefix(line, "a=fmtp:") {
						t.Logf("Qwen answer SDP: %s", line)
					}
				}
			}
			return answer, exchangeErr
		})
	} else {
		err = rc.dialOpenAI(ctx, ek)
	}
	if err != nil {
		t.Fatalf("dial %s: %v", provider, err)
	}
	go rc.pumpSendTrack(ctx)
	waitIncrementalSignal(t, ctx, connected, "peer connection did not connect")
	waitIncrementalSignal(t, ctx, dataChannelOpen, "Realtime data channel did not open")
	waitIncrementalSignal(t, ctx, configured, "session did not configure")

	turnID := h.inputCommitSeq.Add(1)
	h.toolLoop.noteUserCommit(turnID)
	if err := send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "message", "role": "user",
		"content": []map[string]any{{"type": "input_text", "text": "同时查询今天东京的天气和今天的重要新闻。"}},
	}}); err != nil {
		t.Fatalf("send user turn: %v", err)
	}
	h.queueLoopResponse(responseCreateRequest{
		purpose: responsePurposeUser, turnID: turnID,
		toolMode: responseToolsEnabled, dropIfPreempted: true,
	})

	seen := make(map[string]DoTaskRequest)
	for len(seen) < 2 {
		select {
		case req := <-requests:
			lower := strings.ToLower(req.Text)
			hasWeather := strings.Contains(req.Text, "天气") || strings.Contains(lower, "weather")
			hasNews := strings.Contains(req.Text, "新闻") || strings.Contains(lower, "news")
			switch {
			case hasWeather && hasNews:
				t.Fatalf("parallel call repeated the full compound request instead of one disjoint scope: %+v", req)
			case hasWeather:
				seen["weather"] = req
			case hasNews:
				seen["news"] = req
			default:
				t.Fatalf("model dispatched an unexpected task: %+v", req)
			}
		case <-ctx.Done():
			t.Fatalf("model did not dispatch both tasks: %+v", seen)
		}
	}
	if seen["weather"].ThreadID == seen["news"].ThreadID {
		t.Fatalf("parallel tasks shared a daemon lane: %+v", seen)
	}
	waitCompoundIdle(t, ctx, h, "initial parallel-tool response did not settle")
	eventMu.Lock()
	initialSpeech := append([]string(nil), transcripts...)
	eventMu.Unlock()
	if provider == ProviderQwen && len(initialSpeech) == 0 {
		// The provider may omit the optional acknowledgement and go directly to
		// the requested tool calls. Silence is preferable to delivery narration.
	} else if len(initialSpeech) != 1 || strings.Trim(initialSpeech[0], " \t\r\n，。！？,.!?") != "我查一下" {
		t.Fatalf("task handoff speech=%q, want one bare acknowledgement and no delivery narration", initialSpeech)
	}

	weatherOnce.Do(func() { close(weatherRelease) })
	waitUntil(t, func() bool { return h.resultMailbox.pending() == 1 }, "weather result did not land")
	time.Sleep(250 * time.Millisecond)
	if got := resultInjections.Load(); got != 0 {
		t.Fatalf("partial parallel group injected %d result item(s), want 0", got)
	}
	if provider == ProviderQwen {
		if got := functionOutputs.Load(); got != 0 {
			t.Fatalf("partial Qwen group submitted %d function outputs, want 0", got)
		}
	} else if got := functionOutputs.Load(); got != 2 {
		// OpenAI receives one immediate status=running output per background task;
		// these release the model's tool loop and are not final result delivery.
		t.Fatalf("OpenAI running acknowledgements=%d, want 2", got)
	}
	newsOnce.Do(func() { close(newsRelease) })

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		eventMu.Lock()
		allSpeech := append([]string(nil), transcripts...)
		errs := append([]string(nil), apiErrors...)
		eventMu.Unlock()
		if len(errs) > 0 {
			t.Fatalf("Realtime error: %s", strings.Join(errs, "\n"))
		}
		var resultSpeech []string
		for _, speech := range allSpeech {
			if strings.Contains(speech, "36") && strings.Contains(speech, "37") {
				resultSpeech = append(resultSpeech, speech)
			}
		}
		if len(resultSpeech) >= 1 && h.resultMailbox.pending() == 0 {
			if provider == ProviderQwen {
				if got := functionOutputs.Load(); got != 2 {
					t.Fatalf("Qwen function outputs=%d, want 2", got)
				}
			} else if got := resultInjections.Load(); got != 1 {
				t.Fatalf("OpenAI result injections=%d, want 1", got)
			}
			t.Logf("VERDICT: %s dispatched two daemon tasks on distinct lanes; staggered results produced one combined update", provider)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	t.Fatalf("full-path staggered delivery timed out: transcripts=%v errors=%v tasks=%+v pending=%d", transcripts, apiErrors, seen, h.resultMailbox.pending())
}

func TestKoeIncrementalParallelResultsE2E(t *testing.T) {
	if os.Getenv("KOE_E2E") != "1" {
		t.Skip("incremental parallel-result E2E: set KOE_E2E=1 (uses OPENAI_API_KEY or the running daemon mint relay)")
	}
	trials := koeEnvInt("KOE_E2E_TRIALS", 3)
	if trials < 1 {
		trials = 1
	}
	for trial := 1; trial <= trials; trial++ {
		t.Run(fmt.Sprintf("trial_%02d", trial), func(t *testing.T) {
			runIncrementalParallelResultTrial(t, trial)
		})
	}
}

func runIncrementalParallelResultTrial(t *testing.T, trial int) {
	t.Helper()
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

	firstBatchInjected := make(chan struct{}, 1)
	var injectionMu sync.Mutex
	injections := 0
	send := func(v any) error {
		body, _ := json.Marshal(v)
		if strings.Contains(string(body), "kocoro.task_results.v1") {
			injectionMu.Lock()
			injections++
			if injections == 1 {
				signalNonBlocking(firstBatchInjected)
			}
			injectionMu.Unlock()
		}
		return rc.dc.SendText(string(body))
	}

	mailbox := NewResultMailbox()
	h := newEventHandlerWithMailbox(nil, NewCallState(fmt.Sprintf("burst-incremental-%d", trial), ""), audio, send, mailbox, nil)
	go h.runResponseSender(ctx)

	connected := make(chan struct{})
	configured := make(chan struct{})
	var connOnce, cfgOnce sync.Once
	var eventMu sync.Mutex
	var transcripts []string
	var apiErrors []string
	rc.pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			connOnce.Do(func() { close(connected) })
		}
	})
	rc.dc.OnOpen(func() {
		_ = send(sessionConfig("You are Kocoro, a concise native Chinese voice assistant.", "marin", false))
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
			eventMu.Lock()
			transcripts = append(transcripts, ev.Transcript)
			index := len(transcripts)
			eventMu.Unlock()
			t.Logf("[incremental speech %d] %q", index, ev.Transcript)
		case "error", "response.failed":
			eventMu.Lock()
			apiErrors = append(apiErrors, string(m.Data))
			eventMu.Unlock()
		}
	})
	if err := rc.dialOpenAI(ctx, ek); err != nil {
		t.Fatalf("dial OpenAI: %v", err)
	}
	go rc.pumpSendTrack(ctx)
	waitIncrementalSignal(t, ctx, connected, "peer connection did not connect")
	waitIncrementalSignal(t, ctx, configured, "session did not configure")

	if err := send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "message", "role": "user",
		"content": []map[string]any{{"type": "input_text", "text": "同时查询今天东京的天气和今天的重要新闻，结果可以分别告诉我。"}},
	}}); err != nil {
		t.Fatalf("inject user context: %v", err)
	}

	mailbox.Enqueue(SayResult{
		TaskID: "weather", Task: "查询今天东京的天气", Revision: 1, Status: "ok",
		Reply: "东京今天晴，最高气温36摄氏度，午后西部山区有短时雷阵雨风险。",
	}, false)
	waitIncrementalSignal(t, ctx, firstBatchInjected, "weather result was not claimed")
	// Reproduce the observed race: the second daemon task lands shortly after the
	// first result has already become an in-flight Realtime response.
	time.Sleep(250 * time.Millisecond)
	mailbox.Enqueue(SayResult{
		TaskID: "news", Task: "查询今天的重要新闻", Revision: 1, Status: "ok",
		Reply: "今天的重要科技新闻是 Helio 发布 Atlas-7 推理芯片，官方称同等吞吐下能耗降低37%。",
	}, false)

	deadline := time.Now().Add(70 * time.Second)
	for time.Now().Before(deadline) {
		eventMu.Lock()
		got := append([]string(nil), transcripts...)
		errs := append([]string(nil), apiErrors...)
		eventMu.Unlock()
		if len(errs) > 0 {
			t.Fatalf("Realtime error: %s", strings.Join(errs, "\n"))
		}
		if len(got) >= 2 && mailbox.pending() == 0 {
			assertIncrementalResultSpeech(t, got[:2])
			injectionMu.Lock()
			gotInjections := injections
			injectionMu.Unlock()
			if gotInjections != 2 {
				t.Fatalf("result injections=%d, want 2 independent incremental batches", gotInjections)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	t.Fatalf("incremental results timed out: transcripts=%v errors=%v pending=%d", transcripts, apiErrors, mailbox.pending())
}

func waitIncrementalSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal(failure)
	}
}

func assertIncrementalResultSpeech(t *testing.T, transcripts []string) {
	t.Helper()
	first := strings.ToLower(transcripts[0])
	second := strings.ToLower(transcripts[1])
	if !strings.Contains(first, "东京") || !strings.Contains(first, "36") {
		t.Fatalf("first update lost weather facts: %q", transcripts[0])
	}
	if strings.Contains(first, "新闻") {
		t.Fatalf("first update commented on an omitted concurrent news task: %q", transcripts[0])
	}
	if !strings.Contains(second, "37") || (!strings.Contains(second, "helio") && !strings.Contains(second, "芯片")) {
		t.Fatalf("second update lost news facts: %q", transcripts[1])
	}
	if strings.Contains(second, "36") {
		t.Fatalf("second update repeated the earlier weather batch: %q", transcripts[1])
	}
}
