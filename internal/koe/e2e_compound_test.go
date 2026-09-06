//go:build darwin && !ios && cgo

package koe

// Live Realtime contract for the interaction main could not perform: cancel one
// running task and start two independent tasks in a single model Response. Text
// input removes microphone/VAD variance while retaining the live model, production
// response sender, response-id authority, parallel tool events, dispatcher, ledger,
// HTTP daemon contract, and bounded continuation loop.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestKoeCompoundParallelE2E(t *testing.T) {
	if os.Getenv("KOE_E2E") != "1" {
		t.Skip("compound parallel E2E: set KOE_E2E=1 (mints via the running daemon)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	daemonBase := os.Getenv("KOE_DAEMON_URL")
	if daemonBase == "" {
		daemonBase = "http://127.0.0.1:7533"
	}
	ek, err := NewDaemonClient(daemonBase).MintViaDaemon(ctx, e2eModelName())
	if err != nil {
		t.Fatalf("mint via daemon: %v", err)
	}

	var backendMu sync.Mutex
	var backend []DoTaskRequest
	var cancels []CancelRequest
	release := make(chan struct{})
	var releaseOnce sync.Once
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/message":
			body, _ := io.ReadAll(r.Body)
			var req DoTaskRequest
			_ = json.Unmarshal(body, &req)
			backendMu.Lock()
			backend = append(backend, req)
			backendMu.Unlock()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"reply": "completed", "spoken_summary": "completed"})
		case "/cancel":
			body, _ := io.ReadAll(r.Body)
			var req CancelRequest
			_ = json.Unmarshal(body, &req)
			backendMu.Lock()
			cancels = append(cancels, req)
			backendMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer mock.Close()
	defer releaseOnce.Do(func() { close(release) })

	audio, err := NewAudioIO()
	if err != nil {
		t.Fatalf("NewAudioIO: %v", err)
	}
	state := NewCallState("burst-compound-e2e", "")
	disp := NewDispatcher(NewDaemonClient(mock.URL), NewAgentResolver(nil, NoopSemanticMatcher{}), state, nil)
	rc, err := newPeerConnection(audio)
	if err != nil {
		t.Fatalf("newPeerConnection: %v", err)
	}
	defer rc.Close()
	send := func(v any) error {
		body, _ := json.Marshal(v)
		return rc.dc.SendText(string(body))
	}
	h := newEventHandler(disp, state, audio, send)
	go h.runResponseSender(ctx)

	connected := make(chan struct{})
	configured := make(chan struct{})
	var connOnce, cfgOnce sync.Once
	var eventMu sync.Mutex
	responseCalls := map[string][]string{}
	responseCreated := 0
	var errorsSeen []string
	persona := "You are Kocoro, a concise voice assistant. Execute requested tool actions immediately. A do_task returns running and never blocks another action. If one user turn asks to cancel a task and start multiple independent tasks, emit the cancel and every do_task together in the SAME response using parallel function calls; do not split them across responses."
	rc.pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			connOnce.Do(func() { close(connected) })
		}
	})
	rc.dc.OnOpen(func() { _ = send(sessionConfig(persona, "marin", false)) })
	rc.dc.OnMessage(func(m webrtc.DataChannelMessage) {
		var ev struct {
			Type       string `json:"type"`
			Name       string `json:"name"`
			ResponseID string `json:"response_id"`
		}
		_ = json.Unmarshal(m.Data, &ev)
		h.handleEvent(ctx, m.Data)
		switch ev.Type {
		case "session.updated":
			cfgOnce.Do(func() { close(configured) })
		case "response.created":
			eventMu.Lock()
			responseCreated++
			eventMu.Unlock()
		case "response.function_call_arguments.done":
			eventMu.Lock()
			responseCalls[ev.ResponseID] = append(responseCalls[ev.ResponseID], ev.Name)
			eventMu.Unlock()
		case "error", "response.failed":
			eventMu.Lock()
			errorsSeen = append(errorsSeen, string(m.Data))
			eventMu.Unlock()
		}
	})
	if err := rc.dialOpenAI(ctx, ek); err != nil {
		t.Fatalf("dial OpenAI: %v", err)
	}
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

	runTextTurn := func(text string) {
		t.Helper()
		turnID := h.inputCommitSeq.Add(1)
		h.toolLoop.noteUserCommit(turnID)
		if err := send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
			"type": "message", "role": "user",
			"content": []map[string]any{{"type": "input_text", "text": text}},
		}}); err != nil {
			t.Fatalf("send text turn: %v", err)
		}
		h.queueLoopResponse(responseCreateRequest{
			purpose: responsePurposeUser, turnID: turnID,
			toolMode: responseToolsEnabled, dropIfPreempted: true,
		})
	}

	runTextTurn("Start a long-running task to monitor current Tesla stock news. Use do_task with relationship new.")
	waitCompoundE2E(t, ctx, func() bool {
		return len(state.RunningTasks()) == 1
	}, "initial task did not enter the running ledger")
	waitCompoundIdle(t, ctx, h, "initial task response/continuation did not settle")
	eventMu.Lock()
	initialResponseCount := responseCreated
	eventMu.Unlock()
	if initialResponseCount != 1 {
		t.Fatalf("deferred do_task created %d Responses before its result, want exactly 1", initialResponseCount)
	}
	initial := state.RunningTasks()[0]

	runTextTurn("Cancel task " + initial.ID + ", and at the same time start two separate independent tasks: check the current weather in Tokyo, and check the current weather in Osaka. Execute all three actions now in this same response.")
	waitCompoundE2E(t, ctx, func() bool {
		tasks := state.AllTasks()
		if len(tasks) != 3 {
			return false
		}
		return tasks[0].State == TaskCancelled && tasks[1].State == TaskRunning && tasks[2].State == TaskRunning
	}, "compound cancel + two new tasks did not reach the ledger")
	waitCompoundE2E(t, ctx, func() bool {
		backendMu.Lock()
		defer backendMu.Unlock()
		return len(backend) == 3 && len(cancels) == 1
	}, "compound actions did not all reach the daemon HTTP boundary")

	eventMu.Lock()
	byResponse := make(map[string][]string, len(responseCalls))
	for id, calls := range responseCalls {
		byResponse[id] = append([]string(nil), calls...)
	}
	eventsErr := append([]string(nil), errorsSeen...)
	eventMu.Unlock()
	foundCompound := false
	for _, calls := range byResponse {
		sort.Strings(calls)
		if strings.Join(calls, ",") == "cancel,do_task,do_task" {
			foundCompound = true
			break
		}
	}
	if !foundCompound {
		t.Fatalf("live model did not emit cancel + two do_task calls in one response; calls=%v errors=%v", byResponse, eventsErr)
	}

	backendMu.Lock()
	requests := append([]DoTaskRequest(nil), backend...)
	cancelReqs := append([]CancelRequest(nil), cancels...)
	backendMu.Unlock()
	if len(requests) != 3 || len(cancelReqs) != 1 {
		t.Fatalf("backend calls: message=%d cancel=%d requests=%+v cancels=%+v", len(requests), len(cancelReqs), requests, cancelReqs)
	}
	if cancelReqs[0].RouteKey != routeKeyFor(initial.Agent, initial.ThreadID) {
		t.Fatalf("cancel route=%q, want initial task route=%q", cancelReqs[0].RouteKey, routeKeyFor(initial.Agent, initial.ThreadID))
	}
	if requests[1].ThreadID == requests[2].ThreadID {
		t.Fatalf("parallel independent tasks share a lane: %+v", requests[1:])
	}
	t.Logf("VERDICT: PASS — one live Response emitted cancel + two do_task calls; task states=%+v lanes=%q/%q", state.AllTasks(), requests[1].ThreadID, requests[2].ThreadID)
}

func waitCompoundIdle(t *testing.T, ctx context.Context, h *eventHandler, failure string) {
	t.Helper()
	var idleSince time.Time
	waitCompoundE2E(t, ctx, func() bool {
		idle := !h.respBusy.Load() && len(h.loopRespReq) == 0
		if !idle {
			idleSince = time.Time{}
			return false
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
			return false
		}
		return time.Since(idleSince) >= 200*time.Millisecond
	}, failure)
}

func waitCompoundE2E(t *testing.T, ctx context.Context, ok func() bool, failure string) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ok() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(failure)
		case <-ticker.C:
		}
	}
}
