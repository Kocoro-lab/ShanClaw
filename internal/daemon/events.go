package daemon

import (
	"encoding/json"
	"sync"
)

// Event types emitted by the daemon.
const (
	EventMessageReceived  = "message_received"
	EventAgentReply       = "agent_reply"
	EventApprovalRequest  = "approval_request"
	EventApprovalResolved = "approval_resolved"
	EventAgentError       = "agent_error"
	EventHeartbeatAlert   = "heartbeat_alert"
)

// Event is a daemon lifecycle event pushed to SSE subscribers.
type Event struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// EventBus is a simple pub/sub bus for daemon events.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[<-chan Event]chan Event
}

// NewEventBus creates a new EventBus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[<-chan Event]chan Event),
	}
}

// Subscribe returns a channel that receives all emitted events.
// Caller must call Unsubscribe when done.
func (b *EventBus) Subscribe() <-chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subscribers[ch] = ch
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber. No further events will be sent to ch.
// The channel is not closed; callers should stop reading after Unsubscribe.
func (b *EventBus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	delete(b.subscribers, ch)
	b.mu.Unlock()
}

// Emit sends an event to all subscribers. Non-blocking: if a subscriber's
// buffer is full, the event is dropped for that subscriber.
func (b *EventBus) Emit(evt Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers {
		select {
		case ch <- evt:
		default:
			// subscriber too slow, drop
		}
	}
}
