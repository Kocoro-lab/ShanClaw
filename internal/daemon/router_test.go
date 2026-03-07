package daemon

import (
	"testing"
)

func TestSessionKey(t *testing.T) {
	tests := []struct {
		agent, channel, thread string
		want                   string
	}{
		{"ops-bot", "slack", "thread_abc", "ops-bot:slack:thread_abc"},
		{"", "telegram", "chat_123", "default:telegram:chat_123"},
	}
	for _, tt := range tests {
		got := SessionKey(tt.agent, tt.channel, tt.thread)
		if got != tt.want {
			t.Errorf("SessionKey(%q,%q,%q) = %q, want %q", tt.agent, tt.channel, tt.thread, got, tt.want)
		}
	}
}
