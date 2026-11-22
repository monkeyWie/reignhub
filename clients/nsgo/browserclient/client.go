package browserclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/transport"
)

type Object = map[string]any
type Pairing struct {
	Relay, Host, Key, Token string
	RouteName               string
	ICE                     []string
	ICEServers              func(context.Context) []string `json:"-"`
	RelayOnly               bool
	directOnly              bool
	logical                 string
}
type frame struct {
	Probe      bool   `json:"probe,omitempty"`
	AllowRelay bool   `json:"allowRelay"`
	Peer       string `json:"peer"`
	Kind       string `json:"kind"`
	Body       []byte `json:"body"`
}
type chunk = transport.Chunk
type assembly struct {
	encoding    string
	rawSize     int
	request     string
	parts       [][]byte
	count, size int
	updated     time.Time
}
type peer interface {
	Offer() error
	Signal(Object) error
	Send([]byte) error
	Direct() bool
	Close()
}
type cached struct {
	revision string
	result   any
	bytes    int
}
type Client struct {
	routes       *connectivity.PathPool[*Client]
	node         *nativepath.Link
	hostPlatform string
	meter        transport.Meter
	mu           sync.Mutex
	sender       transport.Sender
	ctx          context.Context
	cancel       context.CancelFunc
	ws           *websocket.Conn
	dataWS       atomic.Pointer[websocket.Conn]
	p            Pairing
	id, nonce    string
	base, crypto *cryptor
	ready        chan struct{}
	readyOnce    sync.Once
	closeOnce    sync.Once
	heartbeat    atomic.Int64
	relayBeat    atomic.Int64
	signalLost   atomic.Bool
	progress     func(connectivity.Progress)
	pc           peer
	pending      map[string]*pendingCall
	chunks       map[string]*assembly
	cache        map[string]cached
	cacheBytes   int
}

type pendingCall struct {
	method    string
	idle      *transport.ReadIdle
	ctx       context.Context
	responses *transport.Inbox[Object]
}

func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func sum(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func Connect(ctx context.Context, p Pairing) (*Client, error) {
	return connectivity.RetrySetup(ctx, func(setup context.Context) (*Client, error) {
		return connectOnce(setup, p)
	})
}

func connectOnce(ctx context.Context, p Pairing) (*Client, error) {
	u, e := url.Parse(p.Relay)
	rawKey, ke := hex.DecodeString(p.Key)
	if e != nil || u.User != nil || u.Host == "" || u.Scheme != "wss" && !(u.Scheme == "ws" && CleartextAllowed(u.Hostname())) || ke != nil || len(rawKey) != 32 || len(p.Host) < 16 || p.Token == "" {
		return nil, errors.New("无效或不安全的配对信息")
	}
	u.Path = "/p2p/signal"
	u.RawQuery = url.Values{"id": {p.Host}, "role": {"client"}}.Encode()
	connectivity.Report(ctx, "signal", "正在建立信令连接", connectivity.StageTimeout)
	connectivity.Note(ctx, "signal-endpoint", "signal", "信令服务地址", u.Scheme+"://"+u.Host+u.Path, "info")
	signalCtx, stop := context.WithTimeout(ctx, connectivity.StageTimeout)
	ws, welcome, e := openSocket(signalCtx, u.String(), p.Token)
	stop()
	if e != nil {
		return nil, fmt.Errorf("建立信令连接失败: %w", e)
	}
	connectivity.Note(ctx, "signal", "signal", "信令连接已建立", u.Host, "success")
	// Connection lifetime outlives pairing and all ordinary business requests.
	c, e := newClient(ctx, p, ws, welcome.Peer)
	if e != nil {
		return nil, e
	}
	if welcome.AllowRelay && !p.directOnly {
		u.Path = "/p2p/relay"
		q := u.Query()
		q.Set("peer", c.id)
		u.RawQuery = q.Encode()
		connectivity.Note(ctx, "relay-endpoint", "relay", "中继服务地址", u.Scheme+"://"+u.Host+u.Path, "info")
		if p.RelayOnly {
			relayCtx, stop := context.WithTimeout(ctx, connectivity.StageTimeout)
			data, err := c.openRelay(relayCtx, u.String())
			stop()
			if err != nil {
				c.Close()
				return nil, fmt.Errorf("建立备用中继连接失败: %w", err)
			}
			c.dataWS.Store(data)
		}
		// Optional relay setup follows the connection, not the pairing RPC.
		go c.runRelay(u.String())
	}
	go c.readLoop()
	go c.keepAlive()
	if p.RelayOnly && c.dataWS.Load() == nil {
		c.Close()
		return nil, errors.New("当前服务未提供中继权限，请尝试直连")
	}
	return c.verify(ctx)
}
func newClient(ctx context.Context, p Pairing, ws *websocket.Conn, peerID string) (*Client, error) {
	life, cancel := context.WithCancel(context.Background())
	c := &Client{ctx: life, cancel: cancel, ws: ws, p: p, id: peerID, nonce: id() + id(), ready: make(chan struct{}), pending: map[string]*pendingCall{}, chunks: map[string]*assembly{}, cache: map[string]cached{}, progress: connectivity.Observer(ctx)}
	c.heartbeat.Store(time.Now().UnixNano())
	var e error
	c.base, e = newCryptor(sum("reign/p2p/peer/" + c.id + "/" + p.Key))
	if e != nil {
		c.Close()
		return nil, e
	}
	c.base.random = true
	return c, nil
}
func (c *Client) verify(ctx context.Context) (*Client, error) {
	p := c.p
	connectivity.Report(ctx, "handshake", "正在与被控设备建立加密握手", connectivity.StageTimeout)
	handshake, stop := context.WithTimeout(ctx, connectivity.StageTimeout)
	hello, _ := json.Marshal(Object{"rpc": transport.Version, "clientNonce": c.nonce, "connection": p.logical})
	e := c.relaySend("hello", hello)
	if e == nil {
		select {
		case <-c.ready:
		case <-handshake.Done():
			e = handshake.Err()
		case <-c.ctx.Done():
			e = errors.New("连接已断开")
		}
	}
	stop()
	if e != nil {
		c.Close()
		return nil, fmt.Errorf("配对握手失败: %w", e)
	}
	connectivity.Note(ctx, "handshake", "handshake", "端到端加密握手完成", "已验证设备配对密钥", "success")
	connectivity.Report(ctx, "direct", "优先尝试 WebRTC 直连，必要时使用中继", connectivity.StageTimeout)
	if !p.RelayOnly {
		ice := p.ICE
		if p.ICEServers != nil {
			ice = p.ICEServers(ctx)
		}
		pc, err := newPeer(c, ice)
		if err == nil {
			c.mu.Lock()
			if c.ctx.Err() != nil {
				c.mu.Unlock()
				pc.Close()
				return nil, errors.New("连接已取消")
			}
			c.pc = pc
			c.mu.Unlock()
			// Keep the path budget independent of browser candidate gathering.
			go func() {
				if err := pc.Offer(); err != nil {
					c.note("ice", "udp", "WebRTC 探测未能启动", err.Error(), "warning")
				}
			}()
		} else {
			c.note("ice", "udp", "当前环境无法使用 WebRTC", err.Error(), "warning")
		}
	}
	pathCtx, stop := context.WithTimeout(ctx, connectivity.StageTimeout)
	tick := time.NewTicker(20 * time.Millisecond)
	for {
		c.mu.Lock()
		pc := c.pc
		c.mu.Unlock()
		if pc != nil && pc.Direct() || !p.directOnly && c.relayAvailable() {
			break
		}
		select {
		case <-pathCtx.Done():
			e = fmt.Errorf("直连失败，且没有可用中继: %w", pathCtx.Err())
		case <-c.ctx.Done():
			e = errors.New("连接已断开")
		case <-tick.C:
		}
		if e != nil {
			break
		}
	}
	tick.Stop()
	stop()
	if e != nil {
		c.Close()
		return nil, e
	}
	connectivity.Report(ctx, "verify", "正在验证设备响应", connectivity.StageTimeout)
	go c.watchPaths()
	verify, stop := context.WithTimeout(ctx, connectivity.StageTimeout)
	network, e := c.Call(verify, Object{"method": "network"})
	stop()
	if e != nil {
		c.Close()
		return nil, fmt.Errorf("设备响应验证失败: %w", e)
	}
	if status, ok := network.(Object); ok {
		c.hostPlatform, _ = status["platform"].(string)
	}
	c.note("connected", "connected", "设备连接成功", c.Path(), "success")
	c.meter.Connected()
	return c, nil
}

func (c *Client) openRelay(ctx context.Context, endpoint string) (*websocket.Conn, error) {
	data, f, err := openSocket(ctx, endpoint, c.p.Token)
	if err == nil && f.Peer != c.id {
		_ = closeSocket(data)
		err = errors.New("中继设备身份不匹配")
	}
	return data, err
}

func (c *Client) runRelay(endpoint string) {
	for attempt := 0; c.ctx.Err() == nil && !c.signalLost.Load(); attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(min(attempt, 10)) * time.Second)
			select {
			case <-c.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		socket := c.dataWS.Load()
		if socket == nil {
			c.note("relay-ready", "relay", "后台准备备用中继通道", "与直连协商并行", "running")
			setup, stop := context.WithTimeout(c.ctx, connectivity.StageTimeout)
			data, err := c.openRelay(setup, endpoint)
			stop()
			if err != nil {
				c.note("relay-ready", "relay", "备用中继暂不可用，继续尝试直连", err.Error(), "warning")
				continue
			}
			socket = data
			c.dataWS.Store(socket)
		}
		if c.ctx.Err() != nil {
			c.dataWS.CompareAndSwap(socket, nil)
			_ = closeSocket(socket)
			return
		}
		c.relayBeat.Store(time.Now().UnixNano())
		c.note("relay-ready", "relay", "备用中继通道已就绪", "直连优先，备用通道独立维护", "success")
		life, cancel := context.WithCancel(c.ctx)
		go c.keepRelayAlive(life, socket)
		for {
			if _, _, err := socket.Read(life); err != nil {
				c.note("relay-lost", "relay", "备用中继连接已断开", err.Error(), "warning")
				break
			}
			c.relayBeat.Store(time.Now().UnixNano())
		}
		cancel()
		c.dataWS.CompareAndSwap(socket, nil)
		_ = closeSocket(socket)
		return
	}
}

func (c *Client) keepRelayAlive(life context.Context, socket *websocket.Conn) {
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	lastTick := time.Now()
	for {
		select {
		case <-life.Done():
			return
		case <-tick.C:
			if time.Since(lastTick) > 45*time.Second {
				c.relayBeat.Store(time.Now().UnixNano())
			}
			lastTick = time.Now()
			if time.Since(time.Unix(0, c.relayBeat.Load())) > 85*time.Second {
				c.note("relay-heartbeat", "relay", "中继心跳未收到响应", "85s 内未收到中继控制帧", "warning")
				closeSocket(socket)
				return
			}
			ctx, stop := context.WithTimeout(life, 10*time.Second)
			confirmed, err := pingSocket(ctx, socket)
			stop()
			if err != nil {
				c.note("relay-heartbeat", "relay", "中继心跳发送失败", err.Error(), "warning")
				closeSocket(socket)
				return
			}
			if confirmed {
				c.relayBeat.Store(time.Now().UnixNano())
			}
		}
	}
}
func openSocket(ctx context.Context, endpoint, token string) (*websocket.Conn, frame, error) {
	for attempt := 1; ; attempt++ {
		setup, stop := context.WithTimeout(ctx, connectivity.SocketAttemptTimeout)
		ws, f, err := openSocketOnce(setup, endpoint, token)
		stop()
		if err == nil || attempt >= connectivity.SetupAttempts || ctx.Err() != nil || !connectivity.RetryableSetup(err) {
			return ws, f, err
		}
		connectivity.Note(ctx, fmt.Sprintf("socket-retry:%d", attempt), "signal", "服务连接未能就绪，正在重试", fmt.Sprintf("第 %d/%d 次", attempt+1, connectivity.SetupAttempts), "warning")
		if err := connectivity.RetryPause(ctx, attempt); err != nil {
			return nil, frame{}, err
		}
	}
}
func openSocketOnce(ctx context.Context, endpoint, token string) (*websocket.Conn, frame, error) {
	var f frame
	ws, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{Subprotocols: []string{"reign.p2p", "auth." + base64.RawURLEncoding.EncodeToString([]byte(token))}})
	if err != nil {
		return nil, f, err
	}
	ws.SetReadLimit(2 << 20)
	_, raw, err := ws.Read(ctx)
	if err == nil && (json.Unmarshal(raw, &f) != nil || f.Kind != "welcome" || len(f.Peer) != 32) {
		err = errors.New("无效服务握手")
	}
	if err != nil {
		closeSocket(ws)
		return nil, f, err
	}
	return ws, f, nil
}
func (c *Client) note(id, stage, message, detail, state string) {
	if c.progress != nil && c.ctx.Err() == nil {
		c.progress(connectivity.Progress{ID: id, Stage: stage, Message: message, Detail: detail, State: state})
	}
}
func (c *Client) relayAvailable() bool {
	return !c.p.directOnly && c.ctx.Err() == nil && !c.signalLost.Load() && (c.node != nil || c.dataWS.Load() != nil)
}

func (c *Client) recoveringICE() bool {
	c.mu.Lock()
	pc := c.pc
	c.mu.Unlock()
	p, ok := pc.(interface{ Recovering() bool })
	return ok && p.Recovering()
}

// Resume probes the original authenticated data path without re-pairing or
// replaying any command. A brief ICE interruption can heal on this same peer.
func (c *Client) Resume(ctx context.Context) error {
	if c.routes != nil {
		p := c.routes.Use()
		if p == nil {
			return connectivity.ErrNoRoute
		}
		return p.Resume(ctx)
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for c.Path() == "Disconnected" {
		if !c.recoveringICE() {
			return errors.New("连接已断开")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.ctx.Done():
			return errors.New("连接已断开")
		case <-tick.C:
		}
	}
	_, err := c.Call(ctx, Object{"method": "network"})
	return err
}

func (c *Client) watchPaths() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var lost time.Time
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-tick.C:
			if c.Path() != "Disconnected" {
				lost = time.Time{}
				continue
			}
			if lost.IsZero() {
				lost = time.Now()
			}
			// ICE disconnected is transient: allow the existing DTLS/SCTP
			// channel to recover before destroying it and its encryption state.
			if !c.recoveringICE() || time.Since(lost) >= 20*time.Second {
				c.note("paths-lost", "transport", "所有数据路径不可用，开始恢复连接", fmt.Sprintf("recoveringICE=%t elapsed=%s", c.recoveringICE(), time.Since(lost).Round(time.Millisecond)), "warning")
				c.Close()
				return
			}
		}
	}
}

func (c *Client) keepAlive() {
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	pulses := 0
	lastTick := time.Now()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-tick.C:
			if time.Since(lastTick) > 45*time.Second {
				c.heartbeat.Store(time.Now().UnixNano())
			}
			lastTick = time.Now()
			if time.Since(time.Unix(0, c.heartbeat.Load())) > 85*time.Second {
				c.note("signal-heartbeat", "signal", "信令心跳未收到响应", "85s 内未收到信令控制帧", "warning")
				c.signalLost.Store(true)
				_ = closeSocket(c.ws)
				return
			}
			ctx, stop := context.WithTimeout(c.ctx, 10*time.Second)
			confirmed, err := pingSocket(ctx, c.ws)
			if confirmed {
				c.heartbeat.Store(time.Now().UnixNano())
			}
			pulses++
			if err == nil && pulses%3 == 0 {
				err = c.ws.Write(ctx, websocket.MessageText, []byte(`{"kind":"renew"}`))
			}
			stop()
			if err != nil {
				c.note("signal-heartbeat", "signal", "信令心跳发送失败", err.Error(), "warning")
				c.signalLost.Store(true)
				_ = closeSocket(c.ws)
				return
			}
		}
	}
}
func (c *Client) aad(kind string, out bool) string {
	direction := "h2c"
	if out {
		direction = "c2h"
	}
	return "reign/p2p/" + c.id + "/" + direction + "/" + kind
}
func (c *Client) relaySend(kind string, b []byte) error { return c.relayFrame(kind, b, false) }
func (c *Client) relayFrame(kind string, b []byte, probe bool) error {
	c.mu.Lock()
	crypto := c.crypto
	if kind == "hello" {
		crypto = c.base
	}
	c.mu.Unlock()
	if crypto == nil {
		return errors.New("握手未完成")
	}
	packet, e := crypto.seal(c.aad(kind, true), b)
	if e != nil {
		return e
	}
	raw, _ := json.Marshal(frame{Peer: c.id, Kind: kind, Body: packet, Probe: probe})
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	if c.node != nil {
		if kind == "chunk" {
			record := byte(1)
			if probe {
				record = 3
			}
			return c.node.Send(ctx, 0, append([]byte{record}, packet...))
		}
		return c.node.Send(ctx, 0, append([]byte{2}, raw...))
	}
	socket := c.ws
	if kind == "chunk" || kind == "data" {
		socket = c.dataWS.Load()
		if socket == nil || c.signalLost.Load() {
			return errors.New("直连失败，服务未提供中继权限")
		}
	}
	return socket.Write(ctx, websocket.MessageText, raw)
}
func (c *Client) readLoop() {
	defer closeSocket(c.ws)
	for {
		_, raw, e := c.ws.Read(c.ctx)
		if e != nil {
			c.signalLost.Store(true)
			c.note("signal-lost", "signal", "信令连接已断开", e.Error(), "warning")
			if strings.HasPrefix(c.Path(), "P2P") {
				c.note("signal-independent", "signal", "信令暂不可用，保留现有直连", "数据通信继续使用已建立的加密通道", "info")
			}
			return
		}
		c.heartbeat.Store(time.Now().UnixNano())
		if string(raw) == "reign:pong" {
			continue
		}
		var f frame
		if json.Unmarshal(raw, &f) != nil || f.Peer != c.id {
			continue
		}
		c.handleFrame(f)
	}
}
func (c *Client) handleFrame(f frame) {
	c.mu.Lock()
	crypto := c.crypto
	c.mu.Unlock()
	if f.Kind == "hello" && crypto == nil {
		plain, e := c.base.open(c.aad("hello", false), f.Body)
		if e != nil {
			return
		}
		var hello struct {
			Logical                string `json:"connection"`
			RPC                    int
			ClientNonce, HostNonce string
		}
		if json.Unmarshal(plain, &hello) != nil || hello.Logical != c.p.logical || hello.ClientNonce != c.nonce || len(hello.HostNonce) != 64 {
			return
		}
		if hello.RPC != transport.Version {
			c.note("protocol-mismatch", "handshake", "设备协议版本不一致，请同时更新控制端和被控端", "当前加密 RPC 协议为第 3 版", "warning")
			c.Close()
			return
		}
		if _, e = hex.DecodeString(hello.HostNonce); e != nil {
			return
		}
		next, e := newCryptor(sum("reign/p2p/session/" + c.p.Key + "/" + c.id + "/" + c.nonce + "/" + hello.HostNonce))
		if e != nil {
			return
		}
		next.prefix = [4]byte{1}
		c.mu.Lock()
		c.crypto = next
		c.mu.Unlock()
		c.readyOnce.Do(func() { close(c.ready) })
		return
	}
	if crypto == nil {
		return
	}
	switch f.Kind {
	case "chunk":
		_ = c.receiveChunk(f.Body)
	case "data":
		b, e := crypto.open(c.aad("data", false), f.Body)
		if e == nil {
			c.response(b)
		}
	case "signal":
		b, e := crypto.open(c.aad("signal", false), f.Body)
		if e != nil {
			return
		}
		var v Object
		if json.Unmarshal(b, &v) != nil {
			return
		}
		c.mu.Lock()
		pc := c.pc
		c.mu.Unlock()
		if pc != nil {
			_ = pc.Signal(v)
		}
	}
}
func (c *Client) receiveChunk(packet []byte) error {
	c.mu.Lock()
	crypto := c.crypto
	c.mu.Unlock()
	if crypto == nil {
		return errors.New("握手未完成")
	}
	plain, e := crypto.open(c.aad("chunk", false), packet)
	if e != nil {
		return e
	}
	var part chunk
	if json.Unmarshal(plain, &part) != nil || !part.Valid() {
		return errors.New("无效分片")
	}
	c.meter.Received(len(plain))
	c.mu.Lock()
	if c.pending[part.Request] == nil {
		for id, a := range c.chunks {
			if a.request == part.Request {
				delete(c.chunks, id)
			}
		}
		c.mu.Unlock()
		return nil
	}
	c.pending[part.Request].idle.Touch()
	if c.pending[part.Request].method != "network" {
		c.meter.Progress()
	}
	for k, a := range c.chunks {
		if time.Since(a.updated) > 2*time.Minute {
			delete(c.chunks, k)
		}
	}
	a := c.chunks[part.ID]
	if a == nil {
		if len(c.chunks) >= 64 {
			c.mu.Unlock()
			return errors.New("过多分片")
		}
		a = &assembly{parts: make([][]byte, part.Total), updated: time.Now(), request: part.Request, encoding: part.Encoding, rawSize: part.RawSize}
		c.chunks[part.ID] = a
	}
	if a.encoding != part.Encoding || a.rawSize != part.RawSize || a.request != part.Request || len(a.parts) != part.Total || a.parts[part.Index] != nil {
		c.mu.Unlock()
		return nil
	}
	a.updated = time.Now()
	a.parts[part.Index] = part.Data
	a.count++
	a.size += len(part.Data)
	total := 0
	for _, x := range c.chunks {
		total += x.size
	}
	if a.size > 16<<20 || total > 20<<20 {
		delete(c.chunks, part.ID)
		c.mu.Unlock()
		return errors.New("分片过大")
	}
	if a.count != part.Total {
		c.mu.Unlock()
		return nil
	}
	delete(c.chunks, part.ID)
	out := make([]byte, 0, a.size)
	for _, p := range a.parts {
		out = append(out, p...)
	}
	c.mu.Unlock()
	decoded, err := transport.Decode(out, a.encoding, a.rawSize)
	if err != nil {
		return err
	}
	c.response(decoded)
	return nil
}
func (c *Client) response(b []byte) {
	var v Object
	if json.Unmarshal(b, &v) != nil {
		return
	}
	id, _ := v["id"].(string)
	c.mu.Lock()
	pending := c.pending[id]
	c.mu.Unlock()
	if pending != nil && pending.ctx.Err() == nil {
		pending.responses.Push(v, len(b))
	}

}
func (c *Client) Call(ctx context.Context, request Object) (value any, callErr error) {
	if c.routes != nil {
		v := c.routes.Use()
		if v == nil {
			return nil, connectivity.ErrNoRoute
		}
		value, err := v.Call(ctx, request)
		if errors.Is(err, context.DeadlineExceeded) || v.Path() == "Disconnected" {
			c.routes.Check()
		}
		return value, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := Object{}
	for k, v := range request {
		r[k] = v
	}
	raw, _ := json.Marshal(r)
	cacheKey := string(raw)
	method, _ := r["method"].(string)
	done := c.meter.Begin(method)
	defer func() { done(callErr) }()
	read := control.ReadOnly(method)
	c.mu.Lock()
	previous := c.cache[cacheKey]
	c.mu.Unlock()
	if read && previous.revision != "" && (r["progress"] != true || (method == "transcript" && r["historyItems"] != nil)) {
		delete(r, "progress")
		r["revision"] = previous.revision
	}
	requestID, _ := r["id"].(string)
	if requestID == "" {
		requestID = id()
		r["id"] = requestID
	}
	idle := transport.NewReadIdle(control.CancellableRead(method))
	defer idle.Stop()
	pending := transport.NewInbox[Object]()
	defer pending.Close()
	c.mu.Lock()
	if c.pending[requestID] != nil {
		c.mu.Unlock()
		return nil, errors.New("重复的指令 ID")
	}
	c.pending[requestID] = &pendingCall{ctx: ctx, responses: pending, idle: idle, method: method}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.pending, requestID)
		for id, a := range c.chunks {
			if a.request == requestID {
				delete(c.chunks, id)
			}
		}
	}()
	raw, _ = json.Marshal(r)
	if len(raw) > 1<<20 {
		return nil, errors.New("指令过长")
	}
	cancelRead := func() {
		if !control.CancellableRead(method) {
			return
		}
		go func() {
			cancelCtx, stop := context.WithTimeout(c.ctx, 3*time.Second)
			defer stop()
			cancelID := id()
			raw, _ := json.Marshal(Object{"id": cancelID, "method": control.CancelRead, "cancel": requestID, "provider": r["provider"], "session": r["session"]})
			_ = c.send(cancelCtx, cancelID, control.CancelRead, raw)
		}()
	}
	if e := c.send(ctx, requestID, method, raw); e != nil {
		// The host may already be reading when cancellation wins the sender's
		// completion race. Retire that read even if send returned an error.
		cancelRead()
		return nil, e
	}
	idle.Touch()
	var response Object
	var history provider.TranscriptBuffer
	historyBytes := 0
	for {
		select {
		case <-pending.Ready:
			var ok bool
			var err error
			response, ok, err = pending.Take()
			if err != nil {
				cancelRead()
				return nil, err
			}
			if !ok {
				continue
			}
			if response["partial"] == true {
				if method != "transcript" || r["progress"] != true {
					return nil, errors.New("unexpected partial response")
				}
				raw, err := json.Marshal(response["result"])
				if err != nil {
					return nil, err
				}
				historyBytes += len(raw)
				if historyBytes > 16<<20 {
					return nil, errors.New("消息历史超过 16 MiB，请缩小查询范围")
				}
				var fragment provider.Transcript
				if err := json.Unmarshal(raw, &fragment); err != nil {
					return nil, err
				}
				if err := history.Add(fragment); err != nil {
					cancelRead()
					return nil, err
				}
				if err := provider.ObserveTranscript(ctx, fragment); err != nil {
					return nil, err
				}
				continue
			}
		case <-idle.C:
			if !idle.Expired() {
				continue
			}
			cancelRead()
			return nil, errors.New("设备读取连续 30 秒没有进展，已取消本次读取，请重试")
		case <-ctx.Done():
			cancelRead()
			if read || method == "watch" || method == "attachment.read" {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("结果尚未确认，请刷新后再决定是否重试: %w", ctx.Err())
		case <-c.ctx.Done():
			if read {
				return nil, errors.New("连接已断开，请重新连接")
			}
			return nil, errors.New("连接已断开，指令结果尚未确认")
		}
		break
	}
	if e, _ := response["error"].(string); e != "" {
		code, _ := response["code"].(string)
		if code == control.ConnectionAuthorizationExpired {
			c.note("authorization-renewal", "retry", "连接授权需要续期，重新建立连接", "复用设备配对，无需重新登录", "warning")
			c.Close()
			return nil, errors.New("连接授权需要续期，请在连接恢复后重试")
		}
		return nil, &control.ResponseError{Code: code, Message: e}
	}
	if response["notModified"] == true {
		if response["revision"] != previous.revision || previous.revision == "" {
			return nil, errors.New("无效快照响应")
		}
		return previous.result, nil
	}
	result := response["result"]
	if history.Seen {
		raw, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		var metadata provider.Transcript
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return nil, err
		}
		if err := history.Add(metadata); err != nil {
			return nil, err
		}
		raw, err = json.Marshal(history.Transcript)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
	}
	if read {
		revision, _ := response["revision"].(string)
		b, _ := json.Marshal(result)
		if revision != "" && len(b) <= 16<<20 {
			c.mu.Lock()
			if old, ok := c.cache[cacheKey]; ok {
				c.cacheBytes -= old.bytes
				delete(c.cache, cacheKey)
			}
			for k, v := range c.cache {
				if len(c.cache) < 32 && c.cacheBytes+len(b) <= 16<<20 {
					break
				}
				c.cacheBytes -= v.bytes
				delete(c.cache, k)
			}
			c.cache[cacheKey] = cached{revision, result, len(b)}
			c.cacheBytes += len(b)
			c.mu.Unlock()
		}
	}
	return result, nil
}
func (c *Client) send(ctx context.Context, requestID, method string, raw []byte) error {
	c.mu.Lock()
	pc, crypto := c.pc, c.crypto
	c.mu.Unlock()
	if crypto == nil || c.ctx.Err() != nil {
		return errors.New("连接已关闭")
	}
	direct := pc != nil && pc.Direct()
	priority := transport.PriorityFor(transport.WithMethod(ctx, method), len(raw))
	rawSize := len(raw)
	raw, encoding, err := transport.Encode(raw)
	if err != nil {
		return err
	}
	count := max(1, (len(raw)+transport.ChunkSize-1)/transport.ChunkSize)
	transfer := id()
	for i := 0; i < count; i++ {
		part, _ := json.Marshal(chunk{Request: requestID, ID: transfer, Index: i, Total: count, Encoding: encoding, RawSize: rawSize, Data: raw[i*transport.ChunkSize : min(len(raw), (i+1)*transport.ChunkSize)]})
		if err := c.sender.Send(ctx, c.ctx, priority, func() error { return c.sendPart(part, pc, crypto, direct, transport.IsProbe(method)) }); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) sendPart(part []byte, pc peer, crypto *cryptor, direct, probe bool) (sendErr error) {
	defer func() {
		if sendErr == nil {
			c.meter.Sent(len(part))
		}
	}()
	if direct {
		packet, e := crypto.seal(c.aad("chunk", true), part)
		if e != nil {
			return e
		}
		if e = pc.Send(packet); e != nil {
			return fmt.Errorf("直连发送失败，结果未确认，请先刷新：%w", e)
		}
	} else if c.p.directOnly {
		return errors.New("直连已断开")
	} else if e := c.relayFrame("chunk", part, probe); e != nil {
		return e
	}
	return nil
}

// HostPlatform is the OS reported during encrypted connection verification.
func (c *Client) HostPlatform() string {
	if c.routes != nil {
		if v := c.routes.Active(); v != nil {
			return v.HostPlatform()
		}
		return ""
	}
	return c.hostPlatform
}

func (c *Client) Path() string {
	if c.routes != nil {
		if v := c.routes.Active(); v != nil {
			return v.Path()
		}
		return "Disconnected"
	}
	if c.ctx.Err() != nil {
		return "Disconnected"
	}
	c.mu.Lock()
	pc := c.pc
	c.mu.Unlock()
	if pc != nil && pc.Direct() {
		return "P2P · WebRTC"
	}
	if c.signalLost.Load() || !c.relayAvailable() {
		return "Disconnected"
	}
	if c.p.RouteName != "" {
		return "Relay · " + c.p.RouteName
	}
	return "Relay · WebSocket"
}
func (c *Client) Connected() bool {
	return c.Path() != "Disconnected"
}
func (c *Client) Close() {
	if c.routes != nil {
		c.routes.Close()
		return
	}
	c.cancel()
	c.closeOnce.Do(func() {
		if c.node != nil {
			c.node.Close()
		}
		if c.ws != nil {
			closeSocket(c.ws)
		}
		if socket := c.dataWS.Swap(nil); socket != nil {
			closeSocket(socket)
		}
		c.mu.Lock()
		pc := c.pc
		c.pc = nil
		c.chunks = map[string]*assembly{}
		c.mu.Unlock()
		if pc != nil {
			pc.Close()
		}
	})
}
func (c *Client) signal(v Object) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return c.relaySend("signal", b)
}
func STUN(policy Object) []string {
	_, hasPublic := policy["publicStun"]
	_, hasConfigured := policy["selfStun"]
	if !hasPublic && !hasConfigured {
		return connectivity.DefaultPolicy().PublicSTUN
	}
	values := []string{}
	seen := map[string]bool{}
	for _, key := range []string{"publicStun", "selfStun"} {
		switch v := policy[key].(type) {
		case []any:
			for _, raw := range v {
				if s, ok := raw.(string); ok && strings.HasPrefix(s, "stun:") && !seen[s] && len(values) < 16 {
					seen[s] = true
					values = append(values, s)
				}
			}
		case []string:
			for _, s := range v {
				if strings.HasPrefix(s, "stun:") && !seen[s] && len(values) < 16 {
					seen[s] = true
					values = append(values, s)
				}
			}
		}
	}
	return values
}

func (c *Client) Metrics() map[string]any {
	if c.routes != nil {
		if v := c.routes.Active(); v != nil {
			return v.Metrics()
		}
		return nil
	}
	return c.meter.Snapshot()
}
