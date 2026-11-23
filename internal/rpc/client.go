// Package rpc implements Codex's bidirectional JSON-RPC transport without
// exposing native protocol types to the application's provider interface.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type Message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s (RPC %d)", e.Message, e.Code) }

type Observer func(Message, func(context.Context, any) error)

type Client struct {
	conn     *websocket.Conn
	read     func() ([]byte, error)
	writeRaw func(context.Context, []byte) error
	closeRaw func() error
	next     atomic.Uint64
	mu       sync.Mutex
	pending  map[string]chan Message
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	observer Observer
}

func Dial(ctx context.Context, endpoint string, observer Observer) (*Client, error) {
	return dial(ctx, endpoint, nil, observer)
}

// DialAuthenticated connects to an app-server with WebSocket capability auth.
func DialAuthenticated(ctx context.Context, endpoint, token string, observer Observer) (*Client, error) {
	return dial(ctx, endpoint, http.Header{"Authorization": {"Bearer " + token}}, observer)
}

func dial(ctx context.Context, endpoint string, headers http.Header, observer Observer) (*Client, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	if strings.HasPrefix(endpoint, "unix://") {
		path := strings.TrimPrefix(endpoint, "unix://")
		httpClient.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}
		endpoint = "ws://localhost/"
	}
	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: httpClient, HTTPHeader: headers})
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return nil, err
	}
	conn.SetReadLimit(16 << 20)
	life, cancel := context.WithCancel(context.Background())
	c := &Client{conn: conn, pending: make(map[string]chan Message), ctx: life, cancel: cancel, done: make(chan struct{}), observer: observer}
	go c.readLoop()
	var initialized json.RawMessage
	err = c.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "reignhub", "title": "ReignHub", "version": "0.1.0"}, "capabilities": map[string]bool{"experimentalApi": true}}, &initialized)
	if err == nil {
		err = c.Notify(ctx, "initialized", map[string]any{})
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
func (c *Client) Close() error          { c.cancel(); return c.closeTransport() }
func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) closeTransport() error {
	if c.closeRaw != nil {
		return c.closeRaw()
	}
	return c.conn.CloseNow()
}

func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	id := strconv.FormatUint(c.next.Add(1), 10)
	ch := make(chan Message, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.write(ctx, map[string]any{"id": json.Number(id), "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return msg.Error
		}
		if result != nil {
			if err := json.Unmarshal(msg.Result, result); err != nil {
				return fmt.Errorf("decode %s: %w", method, err)
			}
		}
		return nil
	case <-c.done:
		return errors.New("Codex connection closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) Notify(ctx context.Context, method string, params any) error {
	return c.write(ctx, map[string]any{"method": method, "params": params})
}
func (c *Client) write(ctx context.Context, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if c.writeRaw != nil {
		return c.writeRaw(ctx, b)
	}
	return c.conn.Write(ctx, websocket.MessageText, b)
}

func (c *Client) readLoop() {
	defer close(c.done)
	defer c.cancel()
	defer c.closeTransport()
	for {
		var data []byte
		var err error
		if c.read != nil {
			data, err = c.read()
		} else {
			_, data, err = c.conn.Read(c.ctx)
		}
		if err != nil {
			return
		}
		var m Message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.Method == "" && len(m.ID) > 0 {
			c.mu.Lock()
			ch := c.pending[string(m.ID)]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default:
				}
			}
			continue
		}
		if m.Method == "" {
			continue
		}
		reply := func(ctx context.Context, result any) error {
			return c.write(ctx, map[string]any{"id": json.RawMessage(m.ID), "result": result})
		}
		if c.observer != nil {
			c.observer(m, reply)
		} else if len(m.ID) > 0 {
			ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
			_ = c.write(ctx, map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "Unsupported client request"}})
			cancel()
		}
	}
}
