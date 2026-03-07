package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// MaxConcurrentAgents limits how many agent loops can run simultaneously.
const MaxConcurrentAgents = 5

type IncomingMessage struct {
	Channel   string    `json:"channel"`
	ThreadID  string    `json:"thread_id"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

type OutgoingReply struct {
	Channel  string `json:"channel"`
	ThreadID string `json:"thread_id"`
	Text     string `json:"text"`
}

type Client struct {
	endpoint string
	apiKey   string
	conn     *websocket.Conn
	writeMu  sync.Mutex // gorilla/websocket requires single writer
	onMsg    func(IncomingMessage)
	sem      chan struct{} // bounds concurrent agent dispatches
}

func NewClient(endpoint, apiKey string, onMsg func(IncomingMessage)) *Client {
	return &Client{
		endpoint: endpoint,
		apiKey:   apiKey,
		onMsg:    onMsg,
		sem:      make(chan struct{}, MaxConcurrentAgents),
	}
}

func (c *Client) Connect(ctx context.Context) error {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.apiKey)
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, c.endpoint, header)
	if err != nil {
		return fmt.Errorf("websocket connect: %w", err)
	}
	c.conn = conn
	return nil
}

func (c *Client) Listen(ctx context.Context) error {
	if c.conn == nil {
		return fmt.Errorf("not connected")
	}
	defer c.conn.Close()
	go func() {
		<-ctx.Done()
		c.conn.Close()
	}()
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read: %w", err)
		}
		var msg IncomingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("daemon: invalid message: %v", err)
			continue
		}
		// Dispatch in goroutine with bounded concurrency so the read loop
		// continues processing messages (and pong frames) while agents run.
		c.sem <- struct{}{}
		go func(m IncomingMessage) {
			defer func() { <-c.sem }()
			c.onMsg(m)
		}(msg)
	}
}

func (c *Client) SendReply(reply OutgoingReply) error {
	if c.conn == nil {
		return fmt.Errorf("not connected")
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

func (c *Client) RunWithReconnect(ctx context.Context) {
	backoff := time.Second
	maxBackoff := 30 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := c.Connect(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("daemon: connect failed: %v (retry in %v)", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = time.Second
		log.Println("daemon: connected to Shannon Cloud")
		if err := c.Listen(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("daemon: connection lost: %v (reconnecting)", err)
		}
	}
}
