package daemon

import (
	"fmt"
	"sync"

	"github.com/Kocoro-lab/shan/internal/client"
)

func SessionKey(agent, channel, threadID string) string {
	if agent == "" {
		agent = "default"
	}
	return fmt.Sprintf("%s:%s:%s", agent, channel, threadID)
}

type SessionCache struct {
	mu       sync.RWMutex
	sessions map[string][]client.Message
}

func NewSessionCache() *SessionCache {
	return &SessionCache{
		sessions: make(map[string][]client.Message),
	}
}

func (sc *SessionCache) Get(key string) []client.Message {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	msgs, ok := sc.sessions[key]
	if !ok {
		return nil
	}
	cp := make([]client.Message, len(msgs))
	copy(cp, msgs)
	return cp
}

func (sc *SessionCache) Append(key string, msgs ...client.Message) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.sessions[key] = append(sc.sessions[key], msgs...)
}
