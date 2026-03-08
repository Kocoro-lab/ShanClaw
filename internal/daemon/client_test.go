package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRunWithReconnect_CancelledContextExitsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient("ws://localhost:99999/nonexistent", "key", func(msg MessagePayload) string { return "" }, nil)

	done := make(chan struct{})
	go func() {
		client.RunWithReconnect(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithReconnect did not exit within 2s after cancel")
	}
}

func TestClient_SendEnvelope_WritesToConn(t *testing.T) {
	received := make(chan DaemonMessage, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var dm DaemonMessage
		if err := conn.ReadJSON(&dm); err != nil {
			return
		}
		received <- dm
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c := NewClient(wsURL, "", nil, nil)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.sendEnvelope(DaemonMessage{Type: MsgTypeClaim, MessageID: "msg-123"}); err != nil {
		t.Fatal(err)
	}

	select {
	case dm := <-received:
		if dm.Type != MsgTypeClaim || dm.MessageID != "msg-123" {
			t.Errorf("got type=%q id=%q, want type=%q id=%q", dm.Type, dm.MessageID, MsgTypeClaim, "msg-123")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive message")
	}
}

func TestClient_ConnectionState(t *testing.T) {
	c := NewClient("ws://localhost:1/x", "", nil, nil)
	if c.IsConnected() {
		t.Error("should not be connected initially")
	}
	if c.Uptime() < 0 {
		t.Error("uptime should be non-negative")
	}
	if c.ActiveAgent() != "" {
		t.Error("no active agent initially")
	}
}

func TestClient_ClaimHandshake_Granted(t *testing.T) {
	var receivedClaim DaemonMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Send a message to the daemon.
		payload, _ := json.Marshal(MessagePayload{Channel: "slack", Text: "hi", ThreadID: "t1"})
		conn.WriteJSON(ServerMessage{Type: MsgTypeMessage, MessageID: "msg-001", Payload: payload})

		// Read the claim.
		conn.ReadJSON(&receivedClaim)

		// Grant the claim.
		ackPayload, _ := json.Marshal(ClaimAckPayload{Granted: true})
		conn.WriteJSON(ServerMessage{Type: MsgTypeClaimAck, MessageID: "msg-001", Payload: ackPayload})

		// Read messages until we get a reply (may get progress first).
		for {
			var reply DaemonMessage
			if err := conn.ReadJSON(&reply); err != nil {
				return
			}
			if reply.Type == MsgTypeReply {
				var rp ReplyPayload
				json.Unmarshal(reply.Payload, &rp)
				if rp.Text != "agent-result" {
					t.Errorf("reply text = %q, want %q", rp.Text, "agent-result")
				}
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	onMsgCalled := make(chan struct{})
	c := NewClient(wsURL, "", func(msg MessagePayload) string {
		close(onMsgCalled)
		return "agent-result"
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	go c.Listen(ctx)

	select {
	case <-onMsgCalled:
	case <-ctx.Done():
		t.Fatal("onMsg was never called")
	}

	if receivedClaim.Type != MsgTypeClaim || receivedClaim.MessageID != "msg-001" {
		t.Errorf("expected claim for msg-001, got %+v", receivedClaim)
	}
}

func TestClient_ClaimHandshake_Denied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		payload, _ := json.Marshal(MessagePayload{Channel: "slack", Text: "hi"})
		conn.WriteJSON(ServerMessage{Type: MsgTypeMessage, MessageID: "msg-002", Payload: payload})

		var dm DaemonMessage
		conn.ReadJSON(&dm)

		ackPayload, _ := json.Marshal(ClaimAckPayload{Granted: false})
		conn.WriteJSON(ServerMessage{Type: MsgTypeClaimAck, MessageID: "msg-002", Payload: ackPayload})

		// Keep connection open briefly so the client can process the denial.
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	onMsgCalled := false
	c := NewClient(wsURL, "", func(msg MessagePayload) string {
		onMsgCalled = true
		return ""
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	go c.Listen(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()

	if onMsgCalled {
		t.Error("onMsg should NOT be called when claim is denied")
	}
}

func TestClient_SystemMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		payload := json.RawMessage(`"Quota warning: 90% used"`)
		conn.WriteJSON(ServerMessage{Type: MsgTypeSystem, Payload: payload})
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	systemCh := make(chan string, 1)
	c := NewClient(wsURL, "", func(msg MessagePayload) string { return "" }, func(text string) {
		systemCh <- text
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	go c.Listen(ctx)

	select {
	case msg := <-systemCh:
		if msg != "Quota warning: 90% used" {
			t.Errorf("system message = %q, want %q", msg, "Quota warning: 90% used")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("system message not received")
	}
}
