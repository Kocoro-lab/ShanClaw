//go:build darwin && !ios && cgo

package koe

// Paid live text E2E for the complete Koe delegation and result-delivery path.
// Unlike the narrower selector and result-summary tests, this starts an isolated
// daemon built from the current worktree and keeps both live boundaries in one
// run: OpenAI Realtime selects do_task, the production event handler delegates
// to the daemon and its real provider, ResultMailbox returns the completed
// result to the same Realtime session, and the test verifies the final spoken
// transcript plus the persisted daemon session. Text/audio and Fast/Full are
// exercised as one matrix without touching the Desktop-owned daemon on :7533.
//
// It intentionally has its own gate so KOE_E2E=1 does not add a real daemon
// agent turn to the existing live Realtime suite.
//
//	KOE_LIVE_TEXT_FULL_PATH_E2E=1 \
//	PKG_CONFIG_PATH=/opt/homebrew/lib/pkgconfig \
//	go test ./internal/koe -run '^TestKoeLiveFullPathMatrixE2E$' \
//	  -count=1 -v -timeout=12m

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kocoro-lab/ShanClaw/internal/client"
	"github.com/Kocoro-lab/ShanClaw/internal/executionprofile"
	"github.com/pion/webrtc/v4"
)

const (
	liveTextFullPathGate = "KOE_LIVE_TEXT_FULL_PATH_E2E"
	liveFastMarker       = "323"
	liveFullMarker       = "437"
	liveTimeMarker       = "CLOCK_LOCAL_742"
)

type liveFullPathInput string

const (
	liveInputText  liveFullPathInput = "text"
	liveInputAudio liveFullPathInput = "audio"
)

type liveFullPathScenario struct {
	name       string
	input      liveFullPathInput
	prompt     string
	marker     string
	left       []string
	right      []string
	wantTool   string
	wantMode   executionprofile.Mode
	wantReason executionprofile.FullReason
}

type liveTextFullPathDaemonStatus struct {
	Version      string   `json:"version"`
	IsConnected  bool     `json:"is_connected"`
	Capabilities []string `json:"capabilities"`
}

type liveTextFullPathDaemonUsage struct {
	LLMCalls       int     `json:"llm_calls"`
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	TotalTokens    int     `json:"total_tokens"`
	CostUSD        float64 `json:"cost_usd"`
	WebSearchCalls int     `json:"web_search_calls"`
	Model          string  `json:"model"`
}

type liveTextFullPathRealtimeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type liveTextFullPathDoTaskArgs struct {
	Task          string `json:"task"`
	Agent         string `json:"agent"`
	ExecutionMode string `json:"execution_mode"`
	FullReason    string `json:"full_reason"`
}

type liveTextFullPathProbe struct {
	mu     sync.Mutex
	marker string

	doTaskCallIDs          []string
	doTaskArgs             []liveTextFullPathDoTaskArgs
	functionOutputs        map[string]int
	functionOutputStatuses map[string][]string
	resultBatches          []SayResult
	taskResultRequests     int
	taskResultResponseIDs  map[string]struct{}
	taskResultDoneStatuses map[string]string
	transcripts            map[string][]string
	realtimeUsages         []liveTextFullPathRealtimeUsage
	responseDoneCount      int
	apiErrors              []string

	startedAt        time.Time
	doTaskAt         time.Time
	resultBatchAt    time.Time
	taskResultDoneAt time.Time
}

func newLiveTextFullPathProbe(marker string) *liveTextFullPathProbe {
	return &liveTextFullPathProbe{
		marker:                 marker,
		functionOutputs:        make(map[string]int),
		functionOutputStatuses: make(map[string][]string),
		taskResultResponseIDs:  make(map[string]struct{}),
		taskResultDoneStatuses: make(map[string]string),
		transcripts:            make(map[string][]string),
		startedAt:              time.Now(),
	}
}

func (p *liveTextFullPathProbe) observeOutbound(value any) {
	body, err := json.Marshal(value)
	if err != nil {
		return
	}
	var event struct {
		Type string `json:"type"`
		Item struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			CallID  string `json:"call_id"`
			Output  string `json:"output"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
		Response struct {
			Metadata map[string]string `json:"metadata"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &event) != nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case event.Type == "conversation.item.create" && event.Item.Type == "function_call_output":
		p.functionOutputs[event.Item.CallID]++
		var output struct {
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(event.Item.Output), &output) == nil {
			p.functionOutputStatuses[event.Item.CallID] = append(p.functionOutputStatuses[event.Item.CallID], output.Status)
		}
	case event.Type == "conversation.item.create" && event.Item.Type == "message" && event.Item.Role == "system":
		for _, content := range event.Item.Content {
			result, ok := parseLiveTextFullPathResult(content.Text)
			if !ok {
				continue
			}
			p.resultBatches = append(p.resultBatches, result)
			if p.resultBatchAt.IsZero() {
				p.resultBatchAt = time.Now()
			}
		}
	case event.Type == "response.create" && event.Response.Metadata["koe_purpose"] == string(responsePurposeTaskResult):
		p.taskResultRequests++
	}
}

func (p *liveTextFullPathProbe) observeInbound(raw []byte) {
	var event struct {
		Type       string          `json:"type"`
		Name       string          `json:"name"`
		CallID     string          `json:"call_id"`
		ResponseID string          `json:"response_id"`
		Transcript string          `json:"transcript"`
		Arguments  json.RawMessage `json:"arguments"`
		Response   struct {
			ID            string                        `json:"id"`
			Status        string                        `json:"status"`
			Metadata      map[string]string             `json:"metadata"`
			Usage         liveTextFullPathRealtimeUsage `json:"usage"`
			StatusDetails struct {
				Error struct {
					Code    string `json:"code"`
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"status_details"`
		} `json:"response"`
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	switch event.Type {
	case "response.function_call_arguments.done":
		if event.Name == "do_task" {
			p.doTaskCallIDs = append(p.doTaskCallIDs, event.CallID)
			var args liveTextFullPathDoTaskArgs
			_ = json.Unmarshal(unwrapArgs(event.Arguments), &args)
			p.doTaskArgs = append(p.doTaskArgs, args)
			if p.doTaskAt.IsZero() {
				p.doTaskAt = time.Now()
			}
		}
	case "response.created":
		if event.Response.Metadata["koe_purpose"] == string(responsePurposeTaskResult) {
			p.taskResultResponseIDs[event.Response.ID] = struct{}{}
		}
	case "response.output_audio_transcript.done":
		p.transcripts[event.ResponseID] = append(p.transcripts[event.ResponseID], event.Transcript)
	case "response.done":
		p.responseDoneCount++
		p.realtimeUsages = append(p.realtimeUsages, event.Response.Usage)
		if _, ok := p.taskResultResponseIDs[event.Response.ID]; ok {
			p.taskResultDoneStatuses[event.Response.ID] = event.Response.Status
			if p.taskResultDoneAt.IsZero() {
				p.taskResultDoneAt = time.Now()
			}
		}
	case "error", "response.failed":
		failure := event.Error
		if failure.Code == "" && failure.Type == "" && failure.Message == "" {
			failure = event.Response.StatusDetails.Error
		}
		p.apiErrors = append(p.apiErrors, fmt.Sprintf("%s code=%s type=%s message=%s", event.Type, failure.Code, failure.Type, failure.Message))
	}
}

func parseLiveTextFullPathResult(text string) (SayResult, bool) {
	const marker = `{"type":"kocoro.task_results.v1"`
	index := strings.Index(text, marker)
	if index < 0 {
		return SayResult{}, false
	}
	var payload struct {
		Type    string      `json:"type"`
		Results []SayResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(text[index:]), &payload); err != nil || payload.Type != "kocoro.task_results.v1" || len(payload.Results) != 1 {
		return SayResult{}, false
	}
	return payload.Results[0], true
}

func (p *liveTextFullPathProbe) completed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for responseID, status := range p.taskResultDoneStatuses {
		if status != "completed" {
			continue
		}
		if strings.Contains(strings.Join(p.transcripts[responseID], " "), p.marker) {
			return true
		}
	}
	return false
}

func (p *liveTextFullPathProbe) snapshot() liveTextFullPathProbe {
	p.mu.Lock()
	defer p.mu.Unlock()
	return liveTextFullPathProbe{
		doTaskCallIDs:          append([]string(nil), p.doTaskCallIDs...),
		doTaskArgs:             append([]liveTextFullPathDoTaskArgs(nil), p.doTaskArgs...),
		functionOutputs:        cloneStringIntMap(p.functionOutputs),
		functionOutputStatuses: cloneStringSliceMap(p.functionOutputStatuses),
		resultBatches:          append([]SayResult(nil), p.resultBatches...),
		taskResultRequests:     p.taskResultRequests,
		taskResultResponseIDs:  cloneStringSet(p.taskResultResponseIDs),
		taskResultDoneStatuses: cloneStringStringMap(p.taskResultDoneStatuses),
		transcripts:            cloneStringSliceMap(p.transcripts),
		realtimeUsages:         append([]liveTextFullPathRealtimeUsage(nil), p.realtimeUsages...),
		responseDoneCount:      p.responseDoneCount,
		apiErrors:              append([]string(nil), p.apiErrors...),
		startedAt:              p.startedAt,
		doTaskAt:               p.doTaskAt,
		resultBatchAt:          p.resultBatchAt,
		taskResultDoneAt:       p.taskResultDoneAt,
	}
}

func TestKoeLiveFullPathMatrixE2E(t *testing.T) {
	if os.Getenv(liveTextFullPathGate) != "1" {
		t.Skip("paid live Realtime + daemon/provider E2E: set " + liveTextFullPathGate + "=1")
	}
	t.Setenv("KOE_TASK_LEDGER", "1")
	t.Setenv("KOE_RESULT_DELIVERY", "1")
	daemon := startLiveIsolatedDaemon(t)

	statusCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	status := requireLiveTextFullPathDaemon(t, statusCtx, daemon.url)
	defer cancel()
	if status.Version != daemon.version {
		t.Fatalf("isolated daemon version=%q, want current-worktree build %q", status.Version, daemon.version)
	}
	if status.IsConnected {
		t.Fatal("isolated daemon unexpectedly opened the Cloud WS channel")
	}
	t.Logf("[daemon] version=%s pid=%d port=%d connected=%t state=%s", status.Version, daemon.cmd.Process.Pid, daemon.port, status.IsConnected, daemon.stateDir)

	scenarios := []liveFullPathScenario{
		{
			name: "fast_text", input: liveInputText,
			prompt: "这是一项需要实际执行的真实任务：计算 17 × 19，只用一句简短中文告诉我结果。",
			marker: liveFastMarker, left: []string{"17", "seventeen"}, right: []string{"19", "nineteen"},
			wantTool: "calculate",
			wantMode: executionprofile.ModeFast, wantReason: executionprofile.FullReasonNone,
		},
		{
			name: "fast_time_text", input: liveInputText,
			prompt: "这是一项需要实际执行的真实任务：用本地时钟查询 Asia/Tokyo 当前准确日期、时间和星期，并在一句简短中文结果末尾原样附上 CLOCK_LOCAL_742。",
			marker: liveTimeMarker, left: []string{"Asia/Tokyo"}, right: []string{liveTimeMarker},
			wantTool: "current_time",
			wantMode: executionprofile.ModeFast, wantReason: executionprofile.FullReasonNone,
		},
		{
			name: "full_text", input: liveInputText,
			prompt: "请明确使用 Full 模式执行这项真实任务：计算 19 × 23，只用一句简短中文告诉我结果。",
			marker: liveFullMarker, left: []string{"19", "nineteen"}, right: []string{"23", "twenty-three", "twenty three"},
			wantTool: "calculate",
			wantMode: executionprofile.ModeFull, wantReason: executionprofile.FullReasonExplicitFullRequest,
		},
		{
			name: "fast_audio", input: liveInputAudio,
			prompt: "Do this real task now: calculate seventeen times nineteen, and answer with one short sentence.",
			marker: liveFastMarker, left: []string{"17", "seventeen"}, right: []string{"19", "nineteen"},
			wantTool: "calculate",
			wantMode: executionprofile.ModeFast, wantReason: executionprofile.FullReasonNone,
		},
		{
			name: "full_audio", input: liveInputAudio,
			prompt: "Use Full mode for this real task: calculate nineteen times twenty-three, and answer with one short sentence.",
			marker: liveFullMarker, left: []string{"19", "nineteen"}, right: []string{"23", "twenty-three", "twenty three"},
			wantTool: "calculate",
			wantMode: executionprofile.ModeFull, wantReason: executionprofile.FullReasonExplicitFullRequest,
		},
	}

	sessionIDs := make(map[string]string, len(scenarios))
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			sessionIDs[scenario.name] = runLiveFullPathScenario(t, daemon.url, status, scenario)
		})
	}

	oldPID, newPID := daemon.crashAndRestart(t)
	readbackCtx, readbackCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer readbackCancel()
	restarted := requireLiveTextFullPathDaemon(t, readbackCtx, daemon.url)
	if restarted.Version != daemon.version {
		t.Fatalf("restarted isolated daemon version=%q, want %q", restarted.Version, daemon.version)
	}
	for _, scenario := range scenarios {
		sessionID := sessionIDs[scenario.name]
		if sessionID == "" {
			continue
		}
		requireLiveTextFullPathSession(t, readbackCtx, daemon.url, sessionID, scenario)
	}
	t.Logf("PERSISTENCE_READBACK: restarted isolated daemon old_pid=%d new_pid=%d persisted_sessions=%d", oldPID, newPID, len(sessionIDs))
}

func runLiveFullPathScenario(t *testing.T, daemonURL string, status liveTextFullPathDaemonStatus, scenario liveFullPathScenario) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var pcm []int16
	if scenario.input == liveInputAudio {
		pcm = synthSpokenWAV(t, scenario.prompt)
		t.Logf("[wav] %d samples (%.2fs @ 48k mono)", len(pcm), float64(len(pcm))/48000)
	}

	daemonClient := NewDaemonClient(daemonURL)
	ephemeralKey, err := daemonClient.MintViaDaemon(ctx, e2eModelName())
	if err != nil {
		t.Fatalf("mint Realtime token through daemon: %v", err)
	}
	agents, err := daemonClient.ListAgents(ctx)
	if err != nil {
		t.Fatalf("list daemon agents: %v", err)
	}
	audio, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	audio.SetPlaybackEnabled(false)
	rc, err := newPeerConnection(audio)
	if err != nil {
		audio.Stop()
		t.Fatalf("newPeerConnection: %v", err)
	}
	defer func() {
		rc.Close()
		audio.Stop()
	}()

	probe := newLiveTextFullPathProbe(scenario.marker)
	state := NewCallState(fmt.Sprintf("live-text-full-path-%d", time.Now().UnixNano()), "")
	mailbox := NewResultMailbox()
	mailbox.BeginBurst(state.BurstID())
	dispatcher := NewDispatcher(daemonClient, NewAgentResolver(agents, NoopSemanticMatcher{}), state, nil)

	var sendMu sync.Mutex
	send := func(value any) error {
		probe.observeOutbound(value)
		body, err := json.Marshal(value)
		if err != nil {
			return err
		}
		sendMu.Lock()
		defer sendMu.Unlock()
		return rc.dc.SendText(string(body))
	}
	handler := newEventHandlerWithMailbox(dispatcher, state, audio, send, mailbox, nil)
	handler.language = "zh"
	go handler.runResponseSender(ctx)

	configured := make(chan struct{})
	connected := make(chan struct{})
	var configuredOnce, connectedOnce sync.Once
	rc.pc.OnConnectionStateChange(func(connectionState webrtc.PeerConnectionState) {
		if connectionState == webrtc.PeerConnectionStateConnected {
			connectedOnce.Do(func() { close(connected) })
		}
	})
	rc.dc.OnOpen(func() {
		if err := send(sessionConfig(e2ePersona, "marin", false)); err != nil {
			probe.mu.Lock()
			probe.apiErrors = append(probe.apiErrors, "send session config: "+err.Error())
			probe.mu.Unlock()
		}
	})
	rc.dc.OnMessage(func(message webrtc.DataChannelMessage) {
		handler.handleEvent(ctx, message.Data)
		probe.observeInbound(message.Data)
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(message.Data, &event) == nil && event.Type == "session.updated" {
			configuredOnce.Do(func() { close(configured) })
		}
	})
	if err := rc.dialOpenAI(ctx, ephemeralKey); err != nil {
		t.Fatalf("dial OpenAI Realtime: %v", err)
	}
	if scenario.input == liveInputAudio {
		go rc.pumpSendTrack(ctx)
	}
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatalf("wait for WebRTC connection: %v", ctx.Err())
	}
	select {
	case <-configured:
	case <-ctx.Done():
		t.Fatalf("wait for session.updated: %v", ctx.Err())
	}

	probe.mu.Lock()
	probe.startedAt = time.Now()
	probe.mu.Unlock()
	switch scenario.input {
	case liveInputText:
		if err := sendLiveTextFullPathTurn(handler, send, scenario.prompt); err != nil {
			t.Fatalf("send text input: %v", err)
		}
	case liveInputAudio:
		feedWAV(ctx, audio, pcm)
	default:
		t.Fatalf("unknown live input mode %q", scenario.input)
	}

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for !probe.completed() {
		select {
		case <-ctx.Done():
			snapshot := probe.snapshot()
			t.Fatalf("full-path result timed out: %v; calls=%v args=%+v outputs=%v statuses=%v batches=%+v result_requests=%d result_responses=%v done=%v transcripts=%v errors=%v mailbox_pending=%d",
				ctx.Err(), snapshot.doTaskCallIDs, snapshot.doTaskArgs, snapshot.functionOutputs, snapshot.functionOutputStatuses, snapshot.resultBatches,
				snapshot.taskResultRequests, snapshot.taskResultResponseIDs, snapshot.taskResultDoneStatuses,
				snapshot.transcripts, snapshot.apiErrors, mailbox.pending())
		case <-ticker.C:
			snapshot := probe.snapshot()
			if len(snapshot.apiErrors) != 0 {
				t.Fatalf("Realtime failed before completing the full path: %s", strings.Join(snapshot.apiErrors, "; "))
			}
			for callID, statuses := range snapshot.functionOutputStatuses {
				for _, status := range statuses {
					if status != "" && status != "running" {
						t.Fatalf("do_task %s terminated before daemon delegation with status=%q args=%+v", callID, status, snapshot.doTaskArgs)
					}
				}
			}
		}
	}

	snapshot := probe.snapshot()
	if len(snapshot.apiErrors) != 0 {
		t.Fatalf("Realtime errors: %s", strings.Join(snapshot.apiErrors, "; "))
	}
	if len(snapshot.doTaskCallIDs) != 1 || snapshot.doTaskCallIDs[0] == "" {
		t.Fatalf("do_task calls=%v, want exactly one non-empty call_id", snapshot.doTaskCallIDs)
	}
	callID := snapshot.doTaskCallIDs[0]
	if len(snapshot.doTaskArgs) != 1 || snapshot.doTaskArgs[0].ExecutionMode != string(scenario.wantMode) || snapshot.doTaskArgs[0].FullReason != string(scenario.wantReason) {
		t.Fatalf("do_task profile args=%+v, want mode=%s reason=%s", snapshot.doTaskArgs, scenario.wantMode, scenario.wantReason)
	}
	if got := snapshot.functionOutputs[callID]; got != 1 {
		t.Fatalf("function_call_output count for %q=%d, want 1", callID, got)
	}
	if len(snapshot.resultBatches) != 1 {
		t.Fatalf("result batches=%d, want 1: %+v", len(snapshot.resultBatches), snapshot.resultBatches)
	}
	result := snapshot.resultBatches[0]
	if result.Status != "ok" || result.SessionID == "" || !strings.Contains(result.Reply, scenario.marker) {
		t.Fatalf("unexpected daemon result: %+v", result)
	}
	if snapshot.taskResultRequests != 1 || len(snapshot.taskResultResponseIDs) != 1 || len(snapshot.taskResultDoneStatuses) != 1 {
		t.Fatalf("task-result lifecycle requests=%d responses=%v done=%v, want 1/1/1",
			snapshot.taskResultRequests, snapshot.taskResultResponseIDs, snapshot.taskResultDoneStatuses)
	}
	if snapshot.responseDoneCount != 2 || len(snapshot.realtimeUsages) != 2 {
		t.Fatalf("Realtime completed responses=%d usages=%d, want selector and task-result responses", snapshot.responseDoneCount, len(snapshot.realtimeUsages))
	}
	if pending := mailbox.pending(); pending != 0 {
		t.Fatalf("result mailbox pending=%d after completed response.done, want 0", pending)
	}
	tasks := state.AllTasks()
	if len(tasks) != 1 || tasks[0].State != TaskCompleted {
		t.Fatalf("voice task ledger=%+v, want one completed task", tasks)
	}
	executionRun := tasks[0].CurrentExecutionRun()
	if executionRun.Profile.RequestedMode != scenario.wantMode || executionRun.Profile.EffectiveMode != scenario.wantMode {
		t.Fatalf("voice execution profile=%+v, want requested/effective %s", executionRun.Profile, scenario.wantMode)
	}
	daemonUsage := requireLiveTextFullPathSession(t, ctx, daemonURL, result.SessionID, scenario)

	t.Logf("VERDICT: input=%s mode=%s daemon=%s call_id=%s session=%s selector_ms=%d daemon_ms=%d result_voice_ms=%d total_ms=%d",
		scenario.input,
		scenario.wantMode,
		status.Version,
		callID,
		result.SessionID,
		durationMillis(snapshot.startedAt, snapshot.doTaskAt),
		durationMillis(snapshot.doTaskAt, snapshot.resultBatchAt),
		durationMillis(snapshot.resultBatchAt, snapshot.taskResultDoneAt),
		durationMillis(snapshot.startedAt, snapshot.taskResultDoneAt),
	)
	t.Logf("USAGE: daemon_model=%s daemon_calls=%d daemon_input=%d daemon_output=%d daemon_total=%d daemon_cost_usd=%.8f web_search_calls=%d realtime=%+v",
		daemonUsage.Model, daemonUsage.LLMCalls, daemonUsage.InputTokens, daemonUsage.OutputTokens,
		daemonUsage.TotalTokens, daemonUsage.CostUSD, daemonUsage.WebSearchCalls, snapshot.realtimeUsages)
	return result.SessionID
}

func sendLiveTextFullPathTurn(handler *eventHandler, send func(any) error, text string) error {
	if handler == nil || handler.toolLoop == nil {
		return fmt.Errorf("Koe event handler is not initialized")
	}
	if err := send(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": text,
			}},
		},
	}); err != nil {
		return err
	}
	// Text items have no input_audio_buffer.committed event. Establish the same
	// turn authority that the production audio path creates at commit time, then
	// use the serialized response sender so response metadata, tool authority,
	// and response.created acknowledgement remain production-identical.
	turnID := handler.inputCommitSeq.Add(1)
	handler.toolLoop.noteUserCommit(turnID)
	handler.requestResponseWith(responseCreateRequest{
		purpose:  responsePurposeUser,
		turnID:   turnID,
		toolMode: responseToolsEnabled,
	})
	return nil
}

func requireLiveTextFullPathDaemon(t *testing.T, ctx context.Context, daemonURL string) liveTextFullPathDaemonStatus {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(daemonURL, "/")+"/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("daemon status HTTP %d: %s", response.StatusCode, body)
	}
	var status liveTextFullPathDaemonStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatalf("decode daemon status: %v", err)
	}
	if !containsString(status.Capabilities, "koe_fast_profile_v1") {
		t.Fatalf("daemon %s lacks koe_fast_profile_v1", status.Version)
	}
	return status
}

func requireLiveTextFullPathSession(t *testing.T, ctx context.Context, daemonURL, sessionID string, scenario liveFullPathScenario) liveTextFullPathDaemonUsage {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(daemonURL, "/")+"/sessions/"+sessionID, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("load daemon session %s: %v", sessionID, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("load daemon session %s HTTP %d: %s", sessionID, response.StatusCode, body)
	}
	var session struct {
		Usage         liveTextFullPathDaemonUsage `json:"usage"`
		ExecutionRuns []executionprofile.Run      `json:"execution_runs"`
		Messages      []client.Message            `json:"messages"`
	}
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatalf("decode daemon session %s: %v", sessionID, err)
	}
	if len(session.Messages) < 2 {
		t.Fatalf("daemon session %s messages=%d, want at least 2", sessionID, len(session.Messages))
	}
	var sawUserTask, sawAssistantResult, sawExpectedTool bool
	for _, message := range session.Messages {
		content := message.Content.Text()
		for _, block := range message.Content.Blocks() {
			if block.Type == "tool_use" && block.Name == scenario.wantTool {
				sawExpectedTool = true
			}
		}
		switch message.Role {
		case "user":
			sawUserTask = sawUserTask || (containsAnyFold(content, scenario.left) && containsAnyFold(content, scenario.right))
		case "assistant":
			sawAssistantResult = sawAssistantResult || strings.Contains(content, scenario.marker)
		}
	}
	if !sawUserTask || !sawAssistantResult {
		t.Fatalf("daemon session %s did not persist the utility task and its verified result", sessionID)
	}
	if !sawExpectedTool {
		t.Fatalf("daemon session %s did not use local tool %s", sessionID, scenario.wantTool)
	}
	if session.Usage.WebSearchCalls != 0 {
		t.Fatalf("daemon session %s web_search_calls=%d, want 0 for local utility", sessionID, session.Usage.WebSearchCalls)
	}
	if session.Usage.LLMCalls < 1 || session.Usage.TotalTokens == 0 {
		t.Fatalf("daemon session %s usage=%+v, want metered provider work", sessionID, session.Usage)
	}
	if scenario.wantMode == executionprofile.ModeFast && session.Usage.LLMCalls != 2 {
		t.Fatalf("daemon Fast session %s calls=%d, want local utility + final in exactly 2", sessionID, session.Usage.LLMCalls)
	}
	if scenario.wantMode == executionprofile.ModeFull && session.Usage.LLMCalls > 4 {
		t.Fatalf("daemon Full session %s calls=%d, want a bounded run of at most 4", sessionID, session.Usage.LLMCalls)
	}
	if len(session.ExecutionRuns) != 1 {
		t.Fatalf("daemon session %s execution_runs=%d, want 1", sessionID, len(session.ExecutionRuns))
	}
	profile := session.ExecutionRuns[0].Profile
	if profile.RequestedMode != scenario.wantMode || profile.EffectiveMode != scenario.wantMode {
		t.Fatalf("daemon session %s profile=%+v, want requested/effective %s", sessionID, profile, scenario.wantMode)
	}
	return session.Usage
}

func TestKoeLiveTextFullPathHarnessContract(t *testing.T) {
	probe := newLiveTextFullPathProbe(liveFastMarker)
	probe.observeInbound([]byte(`{"type":"response.function_call_arguments.done","name":"do_task","call_id":"call-1"}`))
	probe.observeOutbound(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{"type": "function_call_output", "call_id": "call-1", "output": `{"status":"running"}`},
	})
	probe.observeOutbound(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message", "role": "system",
			"content": []map[string]any{{"type": "input_text", "text": "result\n" + `{"type":"kocoro.task_results.v1","results":[{"status":"ok","reply":"` + liveFastMarker + `","session_id":"session-1"}]}`}},
		},
	})
	probe.observeOutbound(map[string]any{
		"type":     "response.create",
		"response": map[string]any{"metadata": map[string]string{"koe_purpose": "task_result"}},
	})
	probe.observeInbound([]byte(`{"type":"response.done","response":{"id":"selector","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
	probe.observeInbound([]byte(`{"type":"response.created","response":{"id":"result","metadata":{"koe_purpose":"task_result"}}}`))
	probe.observeInbound([]byte(`{"type":"response.output_audio_transcript.done","response_id":"result","transcript":"` + liveFastMarker + `"}`))
	probe.observeInbound([]byte(`{"type":"response.done","response":{"id":"result","status":"completed","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`))

	if !probe.completed() {
		t.Fatal("synthetic completed lifecycle did not satisfy the harness")
	}
	snapshot := probe.snapshot()
	if len(snapshot.doTaskCallIDs) != 1 || snapshot.functionOutputs["call-1"] != 1 || len(snapshot.resultBatches) != 1 || snapshot.taskResultRequests != 1 || snapshot.responseDoneCount != 2 {
		t.Fatalf("unexpected harness snapshot: calls=%v outputs=%v batches=%+v result_requests=%d response_done=%d",
			snapshot.doTaskCallIDs, snapshot.functionOutputs, snapshot.resultBatches, snapshot.taskResultRequests, snapshot.responseDoneCount)
	}
}

func durationMillis(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return -1
	}
	return end.Sub(start).Milliseconds()
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsAnyFold(value string, candidates []string) bool {
	value = strings.ToLower(value)
	for _, candidate := range candidates {
		if strings.Contains(value, strings.ToLower(candidate)) {
			return true
		}
	}
	return false
}

func cloneStringIntMap(source map[string]int) map[string]int {
	clone := make(map[string]int, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneStringStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneStringSet(source map[string]struct{}) map[string]struct{} {
	clone := make(map[string]struct{}, len(source))
	for key := range source {
		clone[key] = struct{}{}
	}
	return clone
}

func cloneStringSliceMap(source map[string][]string) map[string][]string {
	clone := make(map[string][]string, len(source))
	for key, value := range source {
		clone[key] = append([]string(nil), value...)
	}
	return clone
}
