package daemon

import "encoding/json"

// Server -> Daemon message types
const (
	MsgTypeConnected = "connected"
	MsgTypeMessage   = "message"
	MsgTypeClaimAck  = "claim_ack"
	MsgTypeSystem    = "system"
)

// Daemon -> Server message types
const (
	MsgTypeClaim      = "claim"
	MsgTypeReply      = "reply"
	MsgTypeProgress   = "progress"
	MsgTypeDisconnect = "disconnect"
)

// Approval protocol (bidirectional relay via Cloud)
const (
	MsgTypeApprovalRequest  = "approval_request"
	MsgTypeApprovalResponse = "approval_response"
	MsgTypeApprovalResolved = "approval_resolved"
)

// ApprovalRequest is sent by daemon when a tool needs user approval.
type ApprovalRequest struct {
	Channel   string `json:"channel"`
	ThreadID  string `json:"thread_id"`
	RequestID string `json:"request_id"`
	Tool      string `json:"tool"`
	Args      string `json:"args"`
	Agent     string `json:"agent"`
}

// ApprovalResponse is received from the client (via Cloud relay).
type ApprovalResponse struct {
	RequestID  string           `json:"request_id"`
	Decision   ApprovalDecision `json:"decision"`    // "allow", "deny", "always_allow"
	ResolvedBy string           `json:"resolved_by,omitempty"` // populated by Cloud
}

// ApprovalResolvedPayload is sent daemon→Cloud when Ptfrog resolves first.
type ApprovalResolvedPayload struct {
	RequestID  string           `json:"request_id"`
	Decision   ApprovalDecision `json:"decision"`
	ResolvedBy string           `json:"resolved_by"` // "ptfrog", "slack", "line"
}

// Channel types
const (
	ChannelSlack    = "slack"
	ChannelLINE     = "line"
	ChannelTeams    = "teams"
	ChannelWeChat   = "wechat"
	ChannelWeb      = "web"
	ChannelSchedule = "schedule"
	ChannelSystem   = "system"
)

// Reply format types
const (
	FormatText     = "text"
	FormatMarkdown = "markdown"
)

// ServerMessage is the envelope for all server-to-daemon messages.
type ServerMessage struct {
	Type      string          `json:"type"`
	MessageID string          `json:"message_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// DaemonMessage is the envelope for all daemon-to-server messages.
type DaemonMessage struct {
	Type      string          `json:"type"`
	MessageID string          `json:"message_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// MessagePayload is what the daemon's agent loop processes.
type MessagePayload struct {
	Channel   string `json:"channel"`
	ThreadID  string `json:"thread_id"`
	Sender    string `json:"sender"`
	Text      string `json:"text"`
	AgentName string `json:"agent_name,omitempty"`
	MessageID string `json:"-"` // set locally from envelope, not from JSON
	Timestamp string `json:"timestamp"`
	Source    string `json:"source,omitempty"` // populated by Cloud; "slack", "line", "webhook"
}

// ReplyPayload is sent back after agent completes.
type ReplyPayload struct {
	Channel  string `json:"channel"`
	ThreadID string `json:"thread_id"`
	Text     string `json:"text"`
	Format   string `json:"format,omitempty"`
}

// ClaimAckPayload is sent to confirm or deny a claim.
type ClaimAckPayload struct {
	Granted bool `json:"granted"`
}

// IsSystemChannel returns true for channels that don't expect agent processing.
func IsSystemChannel(channel string) bool {
	return channel == ChannelSystem
}
