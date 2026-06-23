package aria2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Notification is a server-pushed aria2 event received over WebSocket.
type Notification struct {
	Method string // e.g. "aria2.onDownloadStart"
	GID    string
}

// wsRequest is a JSON-RPC notification frame from the server.
type wsRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  []wsEventParam `json:"params"`
}

type wsEventParam struct {
	GID string `json:"gid"`
}

// WSClient connects to aria2's WebSocket JSON-RPC endpoint and streams
// download-related notification events.  It automatically reconnects
// with exponential backoff when the connection drops.
type WSClient struct {
	endpoint string

	mu     sync.Mutex
	conn   *websocket.Conn // active connection, set during read loop
	events chan Notification
	done   chan struct{}
	closed bool
}

// NewWSClient creates a WSClient from an HTTP JSON-RPC endpoint URL and
// the RPC secret.  It derives the WebSocket URL by replacing the scheme.
func NewWSClient(httpEndpoint string) (*WSClient, error) {
	u, err := url.Parse(httpEndpoint)
	if err != nil {
		return nil, fmt.Errorf("wsclient: invalid endpoint: %w", err)
	}
	u.Scheme = "ws"
	return &WSClient{
		endpoint: u.String(),
		events:   make(chan Notification, 64),
		done:     make(chan struct{}),
	}, nil
}

// Connect starts the background read/reconnect loop.  It returns
// immediately; events arrive on the channel returned by Events.
func (c *WSClient) Connect(ctx context.Context) {
	go c.loop(ctx)
}

// Events returns a receive-only channel of Notification.  The channel
// is closed when Close is called or the parent context is cancelled.
func (c *WSClient) Events() <-chan Notification {
	return c.events
}

// Close shuts down the background loop, interrupts any in-flight read,
// and closes the events channel.  It is safe to call multiple times.
func (c *WSClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	close(c.done)
	if c.conn != nil {
		c.conn.CloseNow()
	}
	return nil
}

func (c *WSClient) loop(ctx context.Context) {
	defer close(c.events)

	const (
		initialBackoff = time.Second
		maxBackoff     = 30 * time.Second
	)

	backoff := initialBackoff

	for {
		select {
		case <-c.done:
			return
		case <-ctx.Done():
			return
		default:
		}

		hadMessages, err := c.dialAndRead(ctx)
		if c.isClosed() {
			return
		}

		var wait time.Duration
		switch {
		case err == nil:
			// Clean close — reconnect immediately.
			backoff = initialBackoff
		case hadMessages:
			// Session was healthy; reset backoff before retrying.
			backoff = initialBackoff
			wait = initialBackoff
		default:
			wait = backoff
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		select {
		case <-c.done:
			return
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (c *WSClient) dialAndRead(ctx context.Context) (bool, error) {
	conn, _, err := websocket.Dial(ctx, c.endpoint, nil)
	if err != nil {
		return false, err
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		conn.CloseNow()
	}()

	hadMessages := false

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			var closeErr websocket.CloseError
			if errors.As(err, &closeErr) && closeErr.Code == websocket.StatusNormalClosure {
				return hadMessages, nil
			}
			return hadMessages, err
		}

		var req wsRequest
		if err := json.Unmarshal(data, &req); err != nil {
			continue
		}

		// Server-pushed notifications have no "id" and carry a
		// method name starting with "aria2.on".
		if req.Method == "" {
			continue
		}

		notif := Notification{Method: req.Method}
		if len(req.Params) > 0 {
			notif.GID = req.Params[0].GID
		}

		select {
		case c.events <- notif:
			hadMessages = true
		case <-c.done:
			return hadMessages, nil
		}
	}
}

func (c *WSClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}
