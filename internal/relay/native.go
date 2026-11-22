//go:build !js

package relay

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"github.com/coder/websocket"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/nativepath"
)

type NativeListener = nativepath.Listener

func (s *Server) ListenNative(address, public string, key ed25519.PrivateKey) (*NativeListener, error) {
	secret := authorizationKey(key)
	n, err := nativepath.Listen(s.ctx, address, key, func(ctx context.Context, l *nativepath.Link) { s.serveNative(ctx, l, secret, key, false) })
	if err == nil {
		_, port, _ := net.SplitHostPort(n.Addr().String())
		s.nativePublic, s.nativePort, s.nativePin = public, port, nativepath.Pin(key)
	}
	return n, err
}

func (s *Server) serveNative(ctx context.Context, l *nativepath.Link, secret []byte, key ed25519.PrivateKey, browser bool) {
	defer l.Close()
	stop := context.AfterFunc(ctx, l.Close)
	defer stop()
	setup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := l.Setup(setup)
	var req memberRequest
	if err == nil && len(raw) <= 12000 && json.Unmarshal(raw, &req) == nil && req.Op != "" && (!browser || req.Op == "connect") {
		s.meet.serve(ctx, setup, l, secret, key, req)
	}
}

// ServeNode accepts only explicit proof-of-possession credentials. Browsers
// may connect from their controller origin and can only open controller legs.
func (s *Server) ServeNode(w http.ResponseWriter, r *http.Request, key ed25519.PrivateKey) {
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Protocol")) != nativepath.NodeProtocol {
		http.Error(w, "node protocol required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if s.closed || s.nodeSockets >= 512 {
		s.mu.Unlock()
		http.Error(w, "node unavailable", http.StatusServiceUnavailable)
		return
	}
	s.nodeSockets++
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.nodeSockets--; s.mu.Unlock(); s.wg.Done() }()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{nativepath.NodeProtocol}, InsecureSkipVerify: true})
	if err != nil {
		return
	}
	s.serveNative(s.ctx, nativepath.WebSocketLink(ws), authorizationKey(key), key, r.Header.Get("Origin") != "")
}
