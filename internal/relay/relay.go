// Package relay routes opaque encrypted packets. It never receives host pairing keys.
package relay

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Frame struct {
	Protocol   int    `json:"protocol,omitempty"`
	AllowRelay bool   `json:"allowRelay,omitempty"`
	Grant      string `json:"grant,omitempty"`
	Peer       string `json:"peer,omitempty"`
	Device     string `json:"device,omitempty"`
	Kind       string `json:"kind"`
	Body       []byte `json:"body,omitempty"`
}
type connection struct {
	ws     *websocket.Conn
	device string
	peer   string
	data   *websocket.Conn
	life   context.Context
}

func (c *connection) send(f Frame) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	b, _ := json.Marshal(f)
	return c.ws.Write(ctx, websocket.MessageText, b)
}

type relayRoom struct {
	host    *connection
	clients map[string]*connection
}
type Server struct {
	nodeSockets                         int
	meet                                rendezvous
	ExternalRelays                      []RelayNode
	nativePublic, nativePort, nativePin string
	token                               string
	mu                                  sync.Mutex
	rooms                               map[string]*relayRoom
	Authorize                           func(credential, host, role string) (string, bool)
	AuthorizeContext                    func(context.Context, string, string, string) (string, bool)
	Grant                               func(host, device, role string) string
	DisableWebSocket                    bool
	RelayAllowed                        func(host, device, role string) bool
	Origins                             []string
	closed                              bool
	wg                                  sync.WaitGroup
	ctx                                 context.Context
	cancel                              context.CancelFunc
}

func New(token string) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{token: token, rooms: make(map[string]*relayRoom), ctx: ctx, cancel: cancel}
}
func ID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		http.Error(w, "server shutting down", 503)
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	// Validate complete origins here: the WebSocket library accepts host patterns,
	// which cannot express our exact scheme + host + port policy.
	if origin := r.Header.Get("Origin"); origin != "" {
		allowed := origin == "http://"+r.Host || origin == "https://"+r.Host
		for _, o := range s.Origins {
			if o == origin {
				allowed = true
			}
		}
		if !allowed {
			http.Error(w, "origin denied", 403)
			return
		}
	}

	if r.URL.Path != "/p2p/signal" && r.URL.Path != "/p2p/relay" {
		http.NotFound(w, r)
		return
	}
	credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if credential == "" {
		protocols := strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",")
		for i := range protocols {
			protocols[i] = strings.TrimSpace(protocols[i])
		}
		version := false
		for _, p := range protocols {
			if p == "reign.p2p" {
				version = true
			}
		}
		if version {
			for _, p := range protocols {
				if strings.HasPrefix(p, "auth.") {
					if b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(p, "auth.")); err == nil {
						credential = string(b)
					}
				}
			}
		}
	}
	deviceID := ""
	authorized := s.token != "" && subtle.ConstantTimeCompare([]byte(credential), []byte(s.token)) == 1
	if s.Authorize != nil {
		deviceID, authorized = s.Authorize(credential, r.URL.Query().Get("id"), r.URL.Query().Get("role"))
	}
	if s.AuthorizeContext != nil {
		deviceID, authorized = s.AuthorizeContext(r.Context(), credential, r.URL.Query().Get("id"), r.URL.Query().Get("role"))
	}
	if !authorized {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.Header.Get("Origin") != "" && r.URL.Query().Get("role") != "client" {
		http.Error(w, "browser host not allowed", 403)
		return
	}
	id, role := r.URL.Query().Get("id"), r.URL.Query().Get("role")
	if raw, err := hex.DecodeString(id); err != nil || len(raw) != 16 || strings.ToLower(id) != id || (role != "host" && role != "client") {
		http.Error(w, "invalid route", 400)
		return
	}
	allowedRelay := true
	if s.RelayAllowed != nil {
		allowedRelay = s.RelayAllowed(id, deviceID, role)
	}
	allowWebSocket := allowedRelay && !s.DisableWebSocket
	if r.URL.Path == "/p2p/relay" && !allowWebSocket {
		http.Error(w, "relay not allowed", 403)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"reign.p2p"}, InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	stopClose := context.AfterFunc(s.ctx, func() { _ = ws.CloseNow() })
	defer stopClose()
	ws.SetReadLimit(2 << 20)
	if r.URL.Path == "/p2p/relay" {
		peer := r.URL.Query().Get("peer")
		s.mu.Lock()
		room := s.rooms[id]
		var attached *connection
		if room != nil {
			if role == "host" {
				attached = room.host
			} else {
				attached = room.clients[peer]
			}
		}
		if attached == nil || attached.device != deviceID || (role == "host" && peer != attachedPeer(room)) {
			s.mu.Unlock()
			return
		}
		old := attached.data
		attached.data = ws
		s.mu.Unlock()
		if old != nil {
			_ = old.CloseNow()
		}
		defer func() {
			s.mu.Lock()
			if attached.data == ws {
				attached.data = nil
			}
			s.mu.Unlock()
		}()
		_ = (&connection{ws: ws}).send(Frame{Peer: peer, Kind: "welcome", Protocol: 1, AllowRelay: allowWebSocket})
		for {
			_, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			if string(raw) == "reign:ping" {
				if !replyPing(r.Context(), ws) {
					return
				}
				continue
			}
			var f Frame
			if json.Unmarshal(raw, &f) != nil || (f.Kind != "chunk" && f.Kind != "data") {
				return
			}
			s.mu.Lock()
			target := room.host
			if role == "host" {
				target = room.clients[f.Peer]
				f.Device = ""
			} else {
				f.Peer = peer
				f.Device = deviceID
				if s.Grant != nil {
					f.Grant = s.Grant(id, deviceID, role)
				}
			}
			s.mu.Unlock()
			if target != nil {
				if target.send(f) != nil {
					// A controller may disappear while a host reply is in flight.
					// Retire that controller, not the shared host transport.
					_ = target.ws.CloseNow()
					if role != "host" {
						return
					}
				}
			}
		}
	}
	life, end := context.WithCancel(s.ctx)
	defer end()
	c := &connection{ws: ws, device: deviceID, life: life}
	ticket := ""
	if s.Grant != nil {
		ticket = s.Grant(id, deviceID, role)
	}
	peer := ID()
	c.peer = peer
	s.mu.Lock()
	room := s.rooms[id]
	if role == "host" {
		old := room
		if old != nil {
			// An authenticated owner may replace its own half-open socket.
			if old.host.device != deviceID {
				s.mu.Unlock()
				return
			}
		}

		room = &relayRoom{host: c, clients: make(map[string]*connection)}
		s.rooms[id] = room
		if old != nil {
			sockets := []*websocket.Conn{old.host.ws}
			for _, c := range old.clients {
				sockets = append(sockets, c.ws)
			}
			go func() {
				for _, ws := range sockets {
					_ = ws.CloseNow()
				}
			}()
		}
	} else {
		if room == nil || len(room.clients) >= 32 {
			s.mu.Unlock()
			_ = ws.Close(websocket.StatusTryAgainLater, "host unavailable or full")
			return
		}
		room.clients[peer] = c
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		data := c.data
		s.mu.Unlock()
		if data != nil {
			data.CloseNow()
		}
		s.mu.Lock()
		if role == "host" {
			if s.rooms[id] == room {
				delete(s.rooms, id)
			}
		} else {
			delete(room.clients, peer)
		}
		clients := make([]*connection, 0, len(room.clients))
		for _, v := range room.clients {
			clients = append(clients, v)
		}
		s.mu.Unlock()
		if role == "host" {
			for _, v := range clients {
				_ = v.ws.CloseNow()
			}
		} else {
			_ = room.host.send(Frame{Peer: peer, Kind: "bye"})
		}
	}()
	if err = c.send(Frame{Peer: peer, Kind: "welcome", Protocol: 1, AllowRelay: allowWebSocket}); err != nil {
		return
	}
	// Ping bounds half-open connections and keeps common reverse proxies alive.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				authorized := true
				if s.AuthorizeContext != nil {
					_, authorized = s.AuthorizeContext(ctx, credential, id, role)
				} else if s.Authorize != nil {
					_, authorized = s.Authorize(credential, id, role)
				}
				if !authorized {
					_ = ws.Close(websocket.StatusPolicyViolation, "authorization expired or revoked")
					return
				}
				pc, stop := context.WithTimeout(ctx, 10*time.Second)
				err := ws.Ping(pc)
				stop()
				if err != nil {
					_ = ws.CloseNow()
					return
				}
			}
		}
	}()
	for {
		_, raw, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if string(raw) == "reign:ping" {
			if !replyPing(ctx, ws) {
				return
			}
			continue
		}
		var f Frame
		if json.Unmarshal(raw, &f) != nil {
			return
		}
		if f.Kind == "renew" {
			if s.Grant != nil {
				ticket = s.Grant(id, deviceID, role)
			}
			if c.send(Frame{Kind: "lease", Peer: peer, Grant: ticket, AllowRelay: allowWebSocket}) != nil {
				return
			}
			if role == "client" {
				_ = room.host.send(Frame{Kind: "lease", Peer: peer, Device: deviceID, Grant: ticket})
			}
			continue
		}
		if f.Kind != "signal" && f.Kind != "hello" {
			return
		}
		s.mu.Lock()
		target := room.host
		if role == "host" {
			target = room.clients[f.Peer]
			f.Device = ""
		} else {
			f.Peer = peer
			f.Device = deviceID
			f.Grant = ticket
		}
		s.mu.Unlock()
		if target != nil {
			if target.send(f) != nil {
				_ = target.ws.CloseNow()
				if role != "host" {
					return
				}
			}
		}
	}
}

func replyPing(ctx context.Context, ws *websocket.Conn) bool {
	probe, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	return ws.Write(probe, websocket.MessageText, []byte("reign:pong")) == nil
}

// DisconnectDevice closes only the revoked controller's signaling and relay links.
func (s *Server) DisconnectDevice(host, device string) {
	s.mu.Lock()
	var sockets []*websocket.Conn
	if room := s.rooms[host]; room != nil {
		for _, c := range room.clients {
			if c.device == device {
				sockets = append(sockets, c.ws)
			}
		}
	}
	s.mu.Unlock()
	for _, ws := range sockets {
		_ = ws.CloseNow()
	}
}

// Online reads the host's signal socket; it does not create a peer connection.
func (s *Server) Online(host string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rooms[host] != nil
}

func attachedPeer(room *relayRoom) string {
	if room == nil || room.host == nil {
		return ""
	}
	return room.host.peer
}

// Close rejects new sockets and waits for all handlers before the database closes.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}
