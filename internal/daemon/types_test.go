package daemon

import (
	"encoding/json"
	"testing"
)

func TestServerMessage_UnmarshalMessage(t *testing.T) {
	raw := `{"type":"message","message_id":"msg-001","payload":{"channel":"slack","thread_id":"t1","sender":"alice","text":"hello","timestamp":"2026-03-08T00:00:00Z"}}`
	var sm ServerMessage
	if err := json.Unmarshal([]byte(raw), &sm); err != nil {
		t.Fatal(err)
	}
	if sm.Type != MsgTypeMessage {
		t.Errorf("type = %q, want %q", sm.Type, MsgTypeMessage)
	}
	if sm.MessageID != "msg-001" {
		t.Errorf("message_id = %q, want %q", sm.MessageID, "msg-001")
	}
	var payload MessagePayload
	if err := json.Unmarshal(sm.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Channel != ChannelSlack || payload.Text != "hello" {
		t.Errorf("payload = %+v", payload)
	}
}

func TestServerMessage_UnmarshalClaimAck(t *testing.T) {
	raw := `{"type":"claim_ack","message_id":"msg-001","payload":{"granted":true}}`
	var sm ServerMessage
	if err := json.Unmarshal([]byte(raw), &sm); err != nil {
		t.Fatal(err)
	}
	var ack ClaimAckPayload
	if err := json.Unmarshal(sm.Payload, &ack); err != nil {
		t.Fatal(err)
	}
	if !ack.Granted {
		t.Error("expected granted=true")
	}
}

func TestDaemonMessage_MarshalClaim(t *testing.T) {
	dm := DaemonMessage{Type: MsgTypeClaim, MessageID: "msg-001"}
	data, err := json.Marshal(dm)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	json.Unmarshal(data, &decoded)
	if decoded["type"] != "claim" {
		t.Errorf("type = %v, want claim", decoded["type"])
	}
	if decoded["message_id"] != "msg-001" {
		t.Errorf("message_id = %v", decoded["message_id"])
	}
}

func TestDaemonMessage_MarshalReply(t *testing.T) {
	payload := ReplyPayload{Channel: ChannelSlack, ThreadID: "t1", Text: "result", Format: FormatText}
	payloadBytes, _ := json.Marshal(payload)
	dm := DaemonMessage{Type: MsgTypeReply, MessageID: "msg-001", Payload: payloadBytes}
	data, err := json.Marshal(dm)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DaemonMessage
	json.Unmarshal(data, &decoded)
	if decoded.Type != MsgTypeReply {
		t.Errorf("type = %q", decoded.Type)
	}
	var rp ReplyPayload
	json.Unmarshal(decoded.Payload, &rp)
	if rp.Text != "result" || rp.Format != FormatText {
		t.Errorf("reply payload = %+v", rp)
	}
}

func TestApprovalResolvedPayload_Serialization(t *testing.T) {
	p := ApprovalResolvedPayload{
		RequestID:  "apr_abc123",
		Decision:   DecisionAllow,
		ResolvedBy: "ptfrog",
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ApprovalResolvedPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RequestID != "apr_abc123" {
		t.Errorf("request_id = %q, want %q", decoded.RequestID, "apr_abc123")
	}
	if decoded.Decision != DecisionAllow {
		t.Errorf("decision = %q, want %q", decoded.Decision, DecisionAllow)
	}
	if decoded.ResolvedBy != "ptfrog" {
		t.Errorf("resolved_by = %q, want %q", decoded.ResolvedBy, "ptfrog")
	}
}

func TestApprovalResponse_ResolvedByOmitEmpty(t *testing.T) {
	resp := ApprovalResponse{RequestID: "apr_test", Decision: DecisionDeny}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	json.Unmarshal(data, &decoded)
	if _, ok := decoded["resolved_by"]; ok {
		t.Error("resolved_by should be omitted when empty")
	}

	resp.ResolvedBy = "slack"
	data, _ = json.Marshal(resp)
	json.Unmarshal(data, &decoded)
	if decoded["resolved_by"] != "slack" {
		t.Errorf("resolved_by = %v, want %q", decoded["resolved_by"], "slack")
	}
}

func TestIsSystemChannel(t *testing.T) {
	if !IsSystemChannel(ChannelSystem) {
		t.Error("system channel not detected")
	}
	if IsSystemChannel(ChannelSlack) {
		t.Error("slack should not be system channel")
	}
}
