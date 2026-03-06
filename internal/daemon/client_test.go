package daemon

import (
	"context"
	"testing"
	"time"
)

func TestRunWithReconnect_CancelledContextExitsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient("ws://localhost:99999/nonexistent", "key", func(msg IncomingMessage) {})

	done := make(chan struct{})
	go func() {
		client.RunWithReconnect(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Good
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithReconnect did not exit within 2s after cancel")
	}
}
