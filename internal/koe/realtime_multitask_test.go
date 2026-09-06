//go:build darwin && !ios && cgo

package koe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDoTaskImmediateAckAndParallelLanes(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	posts := make(chan DoTaskRequest, 2)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req DoTaskRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		posts <- req
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reply": "result for " + req.Text, "spoken_summary": "result for " + req.Text,
		})
	}))
	defer func() {
		releaseAll()
		mock.Close()
	}()

	state := NewCallState("burst-m", "")
	dispatcher := NewDispatcher(NewDaemonClient(mock.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)
	mailbox := NewResultMailbox()
	mailbox.BeginBurst(state.BurstID())
	var mu sync.Mutex
	var outputs []SayResult
	h := newEventHandlerWithMailbox(dispatcher, state, nil, func(v any) error {
		payload, _ := json.Marshal(v)
		var frame struct {
			Type string `json:"type"`
			Item struct {
				Type   string `json:"type"`
				Output string `json:"output"`
			} `json:"item"`
		}
		_ = json.Unmarshal(payload, &frame)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case frame.Type == "conversation.item.create" && frame.Item.Type == "function_call_output":
			var result SayResult
			_ = json.Unmarshal([]byte(frame.Item.Output), &result)
			outputs = append(outputs, result)
		}
		return nil
	}, mailbox, nil)

	h.handleFunctionCall(context.Background(), "call-a", "do_task", []byte(`{"task":"check Tokyo weather","relationship":"new"}`))
	h.handleFunctionCall(context.Background(), "call-b", "do_task", []byte(`{"task":"sort unread email","relationship":"new"}`))

	first := waitDoTaskPost(t, posts)
	second := waitDoTaskPost(t, posts)
	if first.ThreadID == second.ThreadID {
		t.Fatalf("parallel independent tasks shared lane %q", first.ThreadID)
	}
	mu.Lock()
	if len(outputs) != 2 || outputs[0].Status != "running" || outputs[1].Status != "running" ||
		outputs[0].TaskID == outputs[1].TaskID {
		mu.Unlock()
		t.Fatalf("immediate running acks wrong: %+v", outputs)
	}
	mu.Unlock()

	releaseAll()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		outputCount := len(outputs)
		mu.Unlock()
		if mailbox.pending() == 2 {
			if outputCount != 2 {
				t.Fatalf("final results must not reuse consumed call ids: outputs=%d", outputCount)
			}
			mailbox.mu.Lock()
			firstReply := mailbox.entries[0].result.Reply
			secondReply := mailbox.entries[1].result.Reply
			mailbox.mu.Unlock()
			if firstReply == "" || secondReply == "" || firstReply == secondReply {
				t.Fatalf("complete parallel results missing: %q / %q", firstReply, secondReply)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("results did not land: mailbox=%d", mailbox.pending())
}

func TestSameResponseParallelTasksBecomeOneSpeakableBatch(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	weatherRelease := make(chan struct{})
	newsRelease := make(chan struct{})
	var weatherOnce, newsOnce sync.Once
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req DoTaskRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Text {
		case "weather":
			<-weatherRelease
		case "news":
			<-newsRelease
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reply": req.Text + " done", "spoken_summary": req.Text + " done",
		})
	}))
	defer func() {
		weatherOnce.Do(func() { close(weatherRelease) })
		newsOnce.Do(func() { close(newsRelease) })
		mock.Close()
	}()

	state := NewCallState("burst-group", "")
	dispatcher := NewDispatcher(NewDaemonClient(mock.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)
	mailbox := NewResultMailbox()
	mailbox.BeginBurst(state.BurstID())
	h := newEventHandlerWithMailbox(dispatcher, state, nil, func(any) error { return nil }, mailbox, nil)

	h.handleFunctionCallForResponse(context.Background(), "response-parallel", "call-weather", "do_task", []byte(`{"task":"weather","relationship":"new"}`), false)
	h.handleFunctionCallForResponse(context.Background(), "response-parallel", "call-news", "do_task", []byte(`{"task":"news","relationship":"new"}`), true)
	h.handleEvent(context.Background(), []byte(`{"type":"response.done","response":{"id":"response-parallel","status":"completed"}}`))

	weatherOnce.Do(func() { close(weatherRelease) })
	waitUntil(t, func() bool { return mailbox.pending() == 1 }, "first grouped task result did not land")
	if got := mailbox.claimForBurst("connection", state.BurstID()); len(got) != 0 {
		t.Fatalf("first staggered result became independently speakable: %+v", got)
	}

	newsOnce.Do(func() { close(newsRelease) })
	waitUntil(t, func() bool { return mailbox.pending() == 2 }, "second grouped task result did not land")
	got := mailbox.claimForBurst("connection", state.BurstID())
	if len(got) != 2 {
		t.Fatalf("complete parallel result claim=%d, want 2: %+v", len(got), got)
	}
}

func TestQwenParallelTaskGroupSubmitsAllOutputsBeforeOneContinuation(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	t.Setenv("KOE_QWEN_TASK_GROUP_SEAL_MS", "10")
	weatherRelease := make(chan struct{})
	newsRelease := make(chan struct{})
	var weatherOnce, newsOnce sync.Once
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req DoTaskRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Text {
		case "weather":
			<-weatherRelease
		case "news":
			<-newsRelease
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reply":          req.Text + " key facts " + strings.Repeat("detail ", 100) + "TAIL-MUST-BE-BOUNDED",
			"spoken_summary": req.Text + " concise",
		})
	}))
	defer func() {
		weatherOnce.Do(func() { close(weatherRelease) })
		newsOnce.Do(func() { close(newsRelease) })
		mock.Close()
	}()

	state := NewCallState("burst-qwen-group", "")
	dispatcher := NewDispatcher(NewDaemonClient(mock.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)
	mailbox := NewResultMailbox()
	mailbox.BeginBurst(state.BurstID())
	captured := &captureSender{}
	var h *eventHandler
	h = newEventHandlerWithMailbox(dispatcher, state, nil, func(v any) error {
		if err := captured.send(v); err != nil {
			return err
		}
		payload, _ := json.Marshal(v)
		var frame struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(payload, &frame)
		if frame.Type == "response.create" {
			h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"qwen-result"}}`))
		}
		return nil
	}, mailbox, nil)
	h.provider = string(ProviderQwen)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.runResponseSender(ctx)

	h.handleFunctionCallForResponse(ctx, "response-qwen-parallel", "call-weather", "do_task", []byte(`{"task":"weather","relationship":"new"}`), false)
	h.handleFunctionCallForResponse(ctx, "response-qwen-parallel", "call-news", "do_task", []byte(`{"task":"news","relationship":"new"}`), true)
	// Qwen may keep the response open until every function output arrives. The
	// group must therefore close after the tool-call stream goes quiet instead of
	// requiring response.done, or both sides wait forever.

	weatherOnce.Do(func() { close(weatherRelease) })
	waitUntil(t, func() bool { return mailbox.pending() == 1 }, "first Qwen result did not land")
	if got := captured.countType("conversation.item.create"); got != 0 {
		t.Fatalf("partial Qwen group submitted %d function outputs, want 0", got)
	}
	if got := captured.countType("response.create"); got != 0 {
		t.Fatalf("partial Qwen group requested %d continuations, want 0", got)
	}

	newsOnce.Do(func() { close(newsRelease) })
	waitUntil(t, func() bool {
		return captured.countType("conversation.item.create") == 2 && captured.countType("response.create") == 1
	}, "complete Qwen group was not submitted as one continuation")
	if !captured.sentContains(`"call_id":"call-weather"`) || !captured.sentContains(`"call_id":"call-news"`) {
		t.Fatalf("Qwen batch lost provider call ids: %v", captured.types())
	}
	if !captured.sentContains("TAIL-MUST-BE-BOUNDED") {
		t.Fatal("Qwen function outputs lost the full daemon replies")
	}
	if captured.sentContains("weather concise") || captured.sentContains("news concise") || captured.sentContains("spoken_summary") {
		t.Fatal("Qwen function outputs revived the retired spoken-summary contract")
	}
	if !captured.sentContains("weather key facts") || !captured.sentContains("news key facts") {
		t.Fatal("Qwen function outputs lost daemon result facts")
	}
}

func TestQwenTaskGroupDoesNotResubmitOutputsWhenContinuationAckIsUnknown(t *testing.T) {
	state := NewCallState("burst-qwen-outcome-unknown", "")
	mailbox := NewResultMailbox()
	mailbox.BeginBurst(state.BurstID())
	weather := mailbox.BeginTaskResult(state.BurstID(), "response-parallel", "call-weather")
	news := mailbox.BeginTaskResult(state.BurstID(), "response-parallel", "call-news")
	mailbox.SealTaskResponse(state.BurstID(), "response-parallel")
	mailbox.EnqueueTaskResult(weather, SayResult{TaskID: "weather", Status: "ok", Reply: "Sunny."}, false)
	mailbox.EnqueueTaskResult(news, SayResult{TaskID: "news", Status: "ok", Reply: "Shipped."}, false)

	captured := &captureSender{}
	var cancel context.CancelFunc
	h := newEventHandlerWithMailbox(nil, state, nil, func(v any) error {
		if err := captured.send(v); err != nil {
			return err
		}
		payload, _ := json.Marshal(v)
		var frame struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(payload, &frame)
		if frame.Type == "response.create" && cancel != nil {
			cancel()
		}
		return nil
	}, mailbox, nil)
	h.provider = string(ProviderQwen)
	// This test drives the mailbox directly instead of through
	// handleFunctionCallForResponse, so register the call ids as this
	// transport's own — otherwise they classify as recovered-from-reconnect
	// and take the context-injection path instead of function outputs.
	h.sessionCallIDs.Store("call-weather", struct{}{})
	h.sessionCallIDs.Store("call-news", struct{}{})

	ctx, cancelFirst := context.WithCancel(context.Background())
	cancel = cancelFirst
	h.sendResultBatch(ctx)
	if got := captured.countType("conversation.item.create"); got != 2 {
		t.Fatalf("initial function output count=%d, want 2", got)
	}

	ctx, cancelSecond := context.WithCancel(context.Background())
	cancel = cancelSecond
	h.sendResultBatch(ctx)
	if got := captured.countType("conversation.item.create"); got != 2 {
		t.Fatalf("continuation outcome retry resubmitted function outputs: count=%d, want 2", got)
	}
}

func waitDoTaskPost(t *testing.T, posts <-chan DoTaskRequest) DoTaskRequest {
	t.Helper()
	select {
	case req := <-posts:
		return req
	case <-time.After(time.Second):
		t.Fatal("do_task did not reach daemon")
		return DoTaskRequest{}
	}
}

// A daemon result whose execution run the ledger REFUSES (conflicting run
// identity) has no successor goroutine to clean up after it. The rejection
// must still clear the busy state — a bare return pinned the call at
// "thinking" with the user mic un-restored for the rest of the call — while
// never delivering the conflicting result to the mailbox.
func TestRejectedExecutionRunClearsPendingStateWithoutDelivery(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req DoTaskRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reply":          "done",
			"spoken_summary": "done",
			"execution_run": map[string]any{
				// Different run_id from the request's and a dangling parent:
				// RecordExecutionRun's append branch rejects it.
				"run_id":        "ker1_conflicting",
				"parent_run_id": "ker1_nonexistent",
				"profile": map[string]any{
					"requested_mode":    "full",
					"effective_mode":    "full",
					"resolution_reason": "test",
				},
			},
		})
	}))
	defer mock.Close()

	state := NewCallState("burst-rej", "")
	dispatcher := NewDispatcher(NewDaemonClient(mock.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)
	mailbox := NewResultMailbox()
	mailbox.BeginBurst(state.BurstID())
	var mu sync.Mutex
	var states []string
	h := newEventHandlerWithMailbox(dispatcher, state, nil, func(any) error { return nil }, mailbox, nil)
	h.onVoiceState = func(s string) {
		mu.Lock()
		defer mu.Unlock()
		states = append(states, s)
	}

	h.handleFunctionCall(context.Background(), "call-rej", "do_task", []byte(`{"task":"check something","relationship":"new"}`))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		last := ""
		if len(states) > 0 {
			last = states[len(states)-1]
		}
		mu.Unlock()
		if !h.asyncTaskPending.Load() && last == "listening" {
			if mailbox.pending() != 0 {
				t.Fatalf("rejected execution run was delivered to the mailbox: pending=%d", mailbox.pending())
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("rejected execution run left call wedged: pending=%t states=%v mailbox=%d",
		h.asyncTaskPending.Load(), states, mailbox.pending())
}

// A rejected run's cleanup must be lane-aware: while an INDEPENDENT task is
// still in flight, the rejection must not flip the call to "listening" or
// clear the pending flag — the surviving task's own completion owns that
// transition, and its result must still land.
func TestRejectedExecutionRunKeepsBusyStateWhileAnotherTaskRuns(t *testing.T) {
	t.Setenv("KOE_TASK_LEDGER", "1")
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	rejectedReturned := make(chan struct{})
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req DoTaskRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Text == "long running job" {
			<-release
			_ = json.NewEncoder(w).Encode(map[string]any{
				"reply": "long job done", "spoken_summary": "long job done",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reply":          "conflicting done",
			"spoken_summary": "conflicting done",
			"execution_run": map[string]any{
				"run_id":        "ker1_conflicting2",
				"parent_run_id": "ker1_nonexistent2",
				"profile": map[string]any{
					"requested_mode":    "full",
					"effective_mode":    "full",
					"resolution_reason": "test",
				},
			},
		})
		close(rejectedReturned)
	}))
	defer func() {
		releaseAll()
		mock.Close()
	}()

	state := NewCallState("burst-par", "")
	dispatcher := NewDispatcher(NewDaemonClient(mock.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)
	mailbox := NewResultMailbox()
	mailbox.BeginBurst(state.BurstID())
	var mu sync.Mutex
	var states []string
	h := newEventHandlerWithMailbox(dispatcher, state, nil, func(any) error { return nil }, mailbox, nil)
	h.onVoiceState = func(s string) {
		mu.Lock()
		defer mu.Unlock()
		states = append(states, s)
	}

	h.handleFunctionCall(context.Background(), "call-long", "do_task", []byte(`{"task":"long running job","relationship":"new"}`))
	h.handleFunctionCall(context.Background(), "call-conflict", "do_task", []byte(`{"task":"conflicting job","relationship":"new"}`))

	select {
	case <-rejectedReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("rejected task's daemon call never returned")
	}
	// Give the rejection goroutine time to run its (wrong, if buggy) cleanup.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !h.asyncTaskPending.Load() {
			t.Fatal("rejection cleared asyncTaskPending while another task was still running")
		}
		mu.Lock()
		for _, s := range states {
			if s == "listening" {
				mu.Unlock()
				t.Fatalf("rejection emitted listening while another task was still running: %v", states)
			}
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}

	releaseAll()
	waitUntil(t, func() bool { return mailbox.pending() == 1 }, "surviving task's result must land in the mailbox")
}

// A transport replacement preserves mailbox entries whose function call_ids
// belong to the OLD provider conversation. Submitting those as
// function_call_output is rejected by the replacement session (it has no such
// call), losing the result — recovered entries must go out as provider-neutral
// context injection instead, like the OpenAI path.
func TestQwenRecoveredResultsUseContextInjectionNotStaleCallIDs(t *testing.T) {
	state := NewCallState("burst-qwen-recovered", "")
	mailbox := NewResultMailbox()
	mailbox.BeginBurst(state.BurstID())
	weather := mailbox.BeginTaskResult(state.BurstID(), "response-old-session", "call-old-weather")
	news := mailbox.BeginTaskResult(state.BurstID(), "response-old-session", "call-old-news")
	mailbox.SealTaskResponse(state.BurstID(), "response-old-session")
	mailbox.EnqueueTaskResult(weather, SayResult{TaskID: "weather", Status: "ok", Reply: "Sunny."}, false)
	mailbox.EnqueueTaskResult(news, SayResult{TaskID: "news", Status: "ok", Reply: "Shipped."}, false)

	captured := &captureSender{}
	var h *eventHandler
	h = newEventHandlerWithMailbox(nil, state, nil, func(v any) error {
		if err := captured.send(v); err != nil {
			return err
		}
		payload, _ := json.Marshal(v)
		var frame struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(payload, &frame)
		if frame.Type == "response.create" {
			h.handleEvent(context.Background(), []byte(`{"type":"response.created","response":{"id":"qwen-recovered"}}`))
		}
		return nil
	}, mailbox, nil)
	h.provider = string(ProviderQwen)
	// Deliberately NOT registered in h.sessionCallIDs: this handler is the
	// replacement transport and never saw these function calls.

	h.sendResultBatch(context.Background())
	if got := captured.countType("conversation.item.create"); got != 1 {
		t.Fatalf("recovered batch sent %d conversation items, want 1 context injection", got)
	}
	if captured.sentContains(`"call_id":"call-old-weather"`) || captured.sentContains(`"call_id":"call-old-news"`) {
		t.Fatal("recovered batch submitted stale call_ids as function_call_output")
	}
	if !captured.sentContains("kocoro.task_results.v1") {
		t.Fatal("recovered batch did not use the context-injection envelope")
	}
	if !captured.sentContains("Sunny.") || !captured.sentContains("Shipped.") {
		t.Fatal("recovered batch lost result content")
	}
	if got := captured.countType("response.create"); got != 1 {
		t.Fatalf("recovered batch requested %d continuations, want 1", got)
	}
}
