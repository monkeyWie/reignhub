package remote

import (
	"context"
	"crypto/sha256"
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
	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/monkeyWie/reignhub/internal/transport"
	"github.com/pion/webrtc/v4"
)

type Options struct {
	directOnly        bool
	logicalID         string
	status            *HostStatus
	DirectAddresses   func() []nativepath.Endpoint                `json:"-"`
	RememberAddresses func([]nativepath.Endpoint)                 `json:"-"`
	Credentials       *relay.RouteCredentials                     `json:"-"`
	FetchRoutes       func(context.Context) (relay.Routes, error) `json:"-"`
	OnClientInfo      func(string, device.ClientInfo) error       `json:"-"`
	Progress          func(connectivity.Progress)                 `json:"-"`
	PublicSTUN        []string
	SelfSTUN          []string
	GrantRequired     bool
	GrantPublicKey    string
	ServiceID         string
	RelayName         string
	TCPTrace          func(string, ...any) `json:"-"`
	ICEServers        []webrtc.ICEServer
	TCPSTUN           []string // TCP STUN host:port endpoints, discovered via /p2p/config
	DisableNative     bool
	DisableQUIC       bool
	DisableTCP        bool
	DisableWebRTC     bool
	ResolveKey        func(string) (string, bool) `json:"-"`
	RelayOnly         bool
}
type wire struct {
	selection        atomic.Uint64
	logicalID        string
	independent      *independentPath
	native           nativeState
	meter            transport.Meter
	progressMu       sync.Mutex
	progressLast     map[string]connectivity.Progress
	ws               *websocket.Conn
	signalLost       atomic.Bool
	relayLink        *relayLink
	grantMu          sync.RWMutex
	grant            string
	offlineGrant     string
	crypto           *cryptor
	base             *cryptor
	key, clientNonce string
	ready            chan struct{}
	handshakeError   error
	advertised       []nativepath.Endpoint
	device           string
	peer             string
	host             bool
	opts             Options
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex
	pc               *webrtc.PeerConnection
	dc               *webrtc.DataChannel
	direct           bool
	chunks           map[string]*assembly
	chunkBytes       int
	sender           transport.Sender
	quicBulkSender   transport.Sender
	signalMu         sync.Mutex
	candidates       []webrtc.ICECandidateInit
	tcpMu            sync.Mutex
	tcpSelectMu      sync.Mutex
	tcpAgent         *connectivity.TCPAgent
	tcpStarted       bool
	tcpKey           [32]byte
	tcp              *tcpPath
	onData           func([]byte)
	wantsResponse    func(string) bool
}
type chunk = transport.Chunk
type assembly struct {
	encoding string
	rawSize  int
	request  string
	parts    [][]byte
	count    int
	updated  time.Time
}

// A WebSocket-only relay can be verified eagerly in relay-only mode. With a
// native listener, both fallback setups belong to the live peer instead.
func dialService(ctx context.Context, p Pairing, role string, timeout time.Duration, opts Options) (*websocket.Conn, *websocket.Conn, relay.Frame, error) {
	ws, welcome, err := dialSignal(ctx, p, role, timeout)
	if err != nil || !opts.RelayOnly || !welcome.AllowRelay {
		return ws, nil, welcome, err
	}
	connectivity.Report(ctx, "relay-ready", "准备备用中继通道", timeout)
	setup, stop := context.WithTimeout(ctx, timeout)
	defer stop()
	data, err := openRelaySocket(setup, relayEndpoint(p, role, welcome.Peer), p.RelayToken, welcome.Peer)
	if err != nil {
		_ = ws.CloseNow()
		return nil, nil, relay.Frame{}, err
	}
	return ws, data, welcome, nil
}
func dialSignal(ctx context.Context, p Pairing, role string, timeout time.Duration) (*websocket.Conn, relay.Frame, error) {
	if err := p.Validate(); err != nil {
		return nil, relay.Frame{}, err
	}
	u, _ := url.Parse(p.Relay)
	u.Path = "/p2p/signal"
	q := u.Query()
	q.Set("role", role)
	q.Set("id", p.Host)
	u.RawQuery = q.Encode()
	connectivity.Report(ctx, "signal", "正在建立信令连接", timeout)
	signalCtx, stopSignal := context.WithTimeout(ctx, timeout)
	defer stopSignal()
	ws, resp, err := dialWebSocket(signalCtx, u.String(), p.RelayToken, "signal")
	if err != nil {
		return nil, relay.Frame{}, websocketDialFailure("建立信令连接", resp, err)
	}
	ws.SetReadLimit(2 << 20)
	_, raw, err := ws.Read(signalCtx)
	if err != nil {
		ws.CloseNow()
		return nil, relay.Frame{}, fmt.Errorf("等待信令服务响应失败: %w", err)
	}
	stopSignal()
	var f relay.Frame
	if json.Unmarshal(raw, &f) != nil || f.Kind != "welcome" || f.Peer == "" {
		ws.CloseNow()
		return nil, relay.Frame{}, errors.New("invalid relay handshake")
	}
	if f.Protocol != 1 {
		ws.CloseNow()
		return nil, relay.Frame{}, errors.New("unsupported service protocol")
	}
	connectivity.Note(ctx, "signal", "signal", "信令连接已建立", u.Scheme+"://"+u.Host+"/p2p/signal", "success")
	return ws, f, nil
}

func relayEndpoint(p Pairing, role, peer string) string {
	u, _ := url.Parse(p.Relay)
	u.Path = "/p2p/relay"
	q := u.Query()
	q.Set("id", p.Host)
	q.Set("role", role)
	q.Set("peer", peer)
	u.RawQuery = q.Encode()
	return u.String()
}

func newWire(ctx context.Context, ws *websocket.Conn, peer, key string, host bool, opts Options, onData func([]byte)) (*wire, error) {
	derived := sha256.Sum256([]byte("reign/p2p/peer/" + peer + "/" + key))
	crypto, err := newCryptor(hex.EncodeToString(derived[:]))
	if err != nil {
		return nil, err
	}
	// Handshake packets use random nonces under the long-term PSK. Data keys
	// additionally bind fresh randomness from both ends, even if relay IDs repeat.
	crypto.random = true
	life, cancel := context.WithCancel(ctx)
	return &wire{ws: ws, peer: peer, base: crypto, key: key, clientNonce: relay.ID() + relay.ID(), ready: make(chan struct{}), host: host, opts: opts, ctx: life, cancel: cancel, chunks: make(map[string]*assembly), onData: onData}, nil
}

func (w *wire) aad(kind string, out bool) string {
	direction := "c2h"
	if w.host == out {
		direction = "h2c"
	}
	return "reign/p2p/" + w.peer + "/" + direction + "/" + kind
}
func (w *wire) relaySend(kind string, data []byte) error {
	return w.relaySendContext(w.ctx, kind, data)
}
func (w *wire) relaySendContext(parent context.Context, kind string, data []byte) error {
	crypto := w.cipher(kind)
	if crypto == nil {
		return errors.New("handshake not ready")
	}
	sealed, err := crypto.seal(w.aad(kind, true), data)
	if err != nil {
		return err
	}
	f := relay.Frame{Peer: w.peer, Kind: kind, Body: sealed}
	raw, _ := json.Marshal(f)
	ctx, cancel := context.WithTimeout(parent, connectivity.StageTimeout)
	defer cancel()
	if w.independent != nil {
		return w.independent.link.Send(ctx, 0, append([]byte{2}, raw...))
	}
	return w.ws.Write(ctx, websocket.MessageText, raw)
}
func (w *wire) receive(f relay.Frame) {
	if w.ctx.Err() != nil {
		return
	}
	if f.Kind == "chunk" {
		w.onChunk(f.Body)
		return
	}
	crypto := w.cipher(f.Kind)
	if crypto == nil {
		return
	}
	plain, err := crypto.open(w.aad(f.Kind, false), f.Body)
	if err != nil {
		return
	}
	if f.Kind == "hello" {
		w.hello(plain)
		return
	}
	if f.Kind == "data" {
		w.onData(plain)
		return
	}
	if f.Kind == "signal" && !w.opts.RelayOnly {
		go w.signal(plain)
	}
}
func (w *wire) relaySocket() *websocket.Conn {
	if w.relayLink != nil {
		return w.relayLink.socket.Load()
	}
	return nil
}

func (w *wire) path() string {
	w.mu.Lock()
	direct := w.direct
	w.mu.Unlock()
	if w.native.direct.Load() != nil {
		return "P2P · QUIC"
	}
	if direct {
		return "P2P · WebRTC"
	}
	if w.tcpSnapshot() != nil {
		return "P2P · TCP"
	}
	if w.signalLost.Load() || w.opts.directOnly {
		return "Disconnected"
	}
	if w.independent != nil {
		return w.independent.name
	}
	if w.opts.RelayName != "" {
		return "Relay · " + w.opts.RelayName
	}
	if w.relaySocket() == nil {
		return "Connecting · direct only"
	}
	return "Relay · WebSocket"
}

func (w *wire) close() {
	w.cancel()
	w.closeTCP()
	w.mu.Lock()
	pc := w.pc
	w.direct = false
	w.mu.Unlock()
	if pc != nil {
		_ = pc.Close()
	}
}
func (w *wire) waitPath(ctx context.Context) error {
	if routeRank(w.path()) > 0 {
		return nil
	}
	deadline := time.NewTimer(connectivity.StageTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(15 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("无可用的数据通道")
		case <-tick.C:
			if routeRank(w.path()) > 0 {
				return nil
			}
		}
	}

}
func (w *wire) sendContext(ctx context.Context, request string, data []byte) error {
	if request == "" {
		return errors.New("missing RPC request ID")
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return errors.New("response exceeds 16 MiB")
	}
	priority := transport.PriorityFor(ctx, len(data))
	rawSize := len(data)
	data, encoding, err := transport.Encode(data)
	if err != nil {
		return err
	}
	id := relay.ID()
	total := max(1, (len(data)+transport.ChunkSize-1)/transport.ChunkSize)
	for i := 0; i < total; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		start, end := i*transport.ChunkSize, min((i+1)*transport.ChunkSize, len(data))
		part, _ := json.Marshal(chunk{Request: request, ID: id, Index: i, Total: total, Encoding: encoding, RawSize: rawSize, Data: data[start:end]})
		sender := &w.sender
		// Independent QUIC flow control lets a small live RPC pass a bulk
		// stream whose write is currently blocked by the remote window.
		if priority == transport.Bulk && (w.native.direct.Load() != nil || w.independent != nil && w.independent.link.Network == "QUIC") {
			sender = &w.quicBulkSender
		}
		if err := sender.Send(ctx, w.ctx, priority, func() error { return w.sendPart(ctx, part, priority) }); err != nil {
			return err
		}
	}
	return nil
}

func (w *wire) sendPart(ctx context.Context, part []byte, priority transport.Priority) (sendErr error) {
	defer func() {
		if sendErr == nil {
			w.meter.Sent(len(part))
		}
	}()
	if err := w.waitPath(ctx); err != nil {
		return err
	}
	w.mu.Lock()
	dc, direct, crypto := w.dc, w.direct, w.crypto
	w.mu.Unlock()
	if crypto == nil {
		return errors.New("handshake not ready")
	}
	if !direct || dc == nil {
		direct = false
	}
	tcp := w.tcpSnapshot()
	native := w.native.direct.Load()
	if native == nil && !direct && tcp == nil && (w.signalLost.Load() || w.opts.directOnly) {
		return errors.New("直连路径已断开，且信令尚未恢复")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sealed, err := crypto.seal(w.aad("chunk", true), part)
	if err != nil {
		return err
	}
	if native != nil {
		var lane byte
		if priority == transport.Bulk {
			lane = 1
		}
		if err = native.Send(ctx, lane, sealed); err != nil {
			native.Close()
			return fmt.Errorf("native send failed; outcome uncertain: %w", err)
		}
		return nil
	}
	if !direct && tcp != nil {
		if err = tcp.send(tcpChunk, sealed); err != nil {
			tcp.close()
			return fmt.Errorf("TCP P2P send failed; outcome uncertain, refresh before retrying: %w", err)
		}
		return nil
	}
	if !direct && w.independent != nil {
		lane := byte(0)
		if priority == transport.Bulk {
			lane = 1
		}
		err = w.independent.link.Send(ctx, lane, append([]byte{1}, sealed...))
		if err != nil {
			w.independent.link.Close()
		}
		return err
	}
	if !direct {
		socket := w.relaySocket()
		if socket == nil {
			return errors.New("中继暂不可用，请等待连接恢复后刷新")
		}
		w.progress("path:relay", "relay", "正在使用 WebSocket 中继", "数据通过信令服务的 WebSocket 中继端到端加密传输", "success")
		frame, _ := json.Marshal(relay.Frame{Peer: w.peer, Kind: "chunk", Body: sealed})
		sendCtx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
		err := socket.Write(sendCtx, websocket.MessageText, frame)
		cancel()
		if err != nil {
			return err
		}
		return nil
	}
	deadline := time.Now().Add(8 * time.Second)
	for dc.BufferedAmount() > 512<<10 {
		if time.Now().After(deadline) {
			return errors.New("P2P backpressure timeout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.ctx.Done():
			return w.ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = dc.Send(sealed); err != nil {
		w.mu.Lock()
		w.direct = false
		w.mu.Unlock()
		return fmt.Errorf("P2P send failed; outcome uncertain, refresh before retrying: %w", err)
	}
	return nil
}
func (w *wire) onChunk(data []byte) {
	crypto := w.cipher("chunk")
	if crypto == nil {
		return
	}
	plain, err := crypto.open(w.aad("chunk", false), data)
	if err != nil {
		return
	}
	var c chunk
	if json.Unmarshal(plain, &c) != nil || !c.Valid() {
		return
	}
	w.meter.Received(len(plain))
	if w.wantsResponse != nil && !w.wantsResponse(c.Request) {
		w.discardResponse(c.Request)
		return
	}
	w.mu.Lock()
	for id, a := range w.chunks {
		// Expire abandoned fragments by inactivity, never by total transfer
		// duration: a large snapshot may legitimately take minutes to arrive.
		if time.Since(a.updated) > 2*time.Minute {
			for _, p := range a.parts {
				w.chunkBytes -= len(p)
			}
			delete(w.chunks, id)
		}
	}
	a := w.chunks[c.ID]
	if a == nil {
		if len(w.chunks) >= 64 || w.chunkBytes > 20<<20 {
			w.mu.Unlock()
			return
		}
		a = &assembly{parts: make([][]byte, c.Total), updated: time.Now(), request: c.Request, encoding: c.Encoding, rawSize: c.RawSize}
		w.chunks[c.ID] = a
	}
	if a.encoding != c.Encoding || a.rawSize != c.RawSize || a.request != c.Request || len(a.parts) != c.Total || a.parts[c.Index] != nil {
		w.mu.Unlock()
		return
	}
	a.updated = time.Now()
	a.parts[c.Index] = c.Data
	a.count++
	w.chunkBytes += len(c.Data)
	if a.count != c.Total {
		w.mu.Unlock()
		return
	}
	var full []byte
	for _, part := range a.parts {
		full = append(full, part...)
		w.chunkBytes -= len(part)
	}
	delete(w.chunks, c.ID)
	w.mu.Unlock()
	decoded, err := transport.Decode(full, a.encoding, a.rawSize)
	if err == nil {
		w.onData(decoded)
	}
}
func (w *wire) setup() (*webrtc.PeerConnection, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pc != nil {
		return w.pc, nil
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: connectivity.STUNOnly(w.opts.ICEServers)})
	if err != nil {
		return nil, err
	}
	w.pc = pc
	var candidateCount atomic.Int32
	for _, url := range w.opts.PublicSTUN {
		w.progress("stun:"+url, "udp", "公共 STUN 节点", url+" · 并行探测", "info")
	}
	for _, url := range w.opts.SelfSTUN {
		w.progress("stun:"+url, "udp", "配置的 STUN 节点", url+" · 并行探测", "info")
	}
	w.progress("ice", "udp", "收集 UDP / IPv6 直连候选地址", "并行探测可用地址", "running")
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			if candidateCount.Load() == 0 {
				w.progress("ice", "udp", "未收集到 UDP 直连候选地址", "请检查网卡访问权限和本机网络；继续使用可用中继", "warning")
			} else {
				w.progress("ice", "udp", "UDP 候选地址收集结束", fmt.Sprintf("共 %d 个候选，正在检查可用直连路径", candidateCount.Load()), "success")
			}
			go w.reportSTUNStats(pc)
			return
		}
		candidateCount.Add(1)
		w.progress("candidate:"+candidate.Address+fmt.Sprint(candidate.Port), "udp", "发现 UDP 直连候选", fmt.Sprintf("%s · %s:%d · %s", candidate.Typ.String(), candidate.Address, candidate.Port, candidate.Protocol.String()), "info")
		raw, err := json.Marshal(map[string]any{"type": "candidate", "candidate": candidate.ToJSON()})
		if err == nil {
			_ = w.relaySend("signal", raw)
		}
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnecting {
			w.progress("ice-check", "udp", "正在验证 UDP 直连路径", "交换候选地址并进行连通性检查", "running")
		}
		if s == webrtc.PeerConnectionStateFailed {
			w.progress("ice-check", "udp", "UDP 直连暂未建立", "继续使用可用的 TCP 直连或中继通道", "warning")
		}
		if s == webrtc.PeerConnectionStateConnected {
			w.mu.Lock()
			w.direct = w.dc != nil && w.dc.ReadyState() == webrtc.DataChannelStateOpen
			w.mu.Unlock()
		}
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed || s == webrtc.PeerConnectionStateDisconnected {
			w.mu.Lock()
			w.direct = false
			w.mu.Unlock()
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) { w.attach(dc) })
	return pc, nil
}
func (w *wire) attach(dc *webrtc.DataChannel) {
	if dc.Label() != "reign.p2p" {
		_ = dc.Close()
		return
	}
	w.mu.Lock()
	w.dc = dc
	w.mu.Unlock()
	dc.OnOpen(func() {
		w.mu.Lock()
		w.direct = true
		pc := w.pc
		w.mu.Unlock()
		detail := "端到端加密数据通道已建立"
		if pc != nil && pc.SCTP() != nil && pc.SCTP().Transport() != nil {
			if pair, err := pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair(); err == nil && pair != nil {
				detail = pair.String()
			}
		}
		w.progress("path:webrtc", "direct", "已切换为 WebRTC 直连", detail, "success")
	})
	dc.OnClose(func() { w.mu.Lock(); w.direct = false; w.mu.Unlock() })
	dc.OnMessage(func(msg webrtc.DataChannelMessage) { w.onChunk(msg.Data) })
}
func (w *wire) offer() {
	if w.opts.RelayOnly {
		return
	}
	if w.native.quicEnabled.Load() {
		go w.startQUIC(quicSignal{})
	}
	w.startTCP()
	if w.opts.DisableWebRTC {
		return
	}
	pc, err := w.setup()
	if err != nil {
		w.progress("ice", "udp", "无法初始化 WebRTC 直连", err.Error(), "warning")
		return
	}
	dc, err := pc.CreateDataChannel("reign.p2p", nil)
	if err != nil {
		w.progress("ice", "udp", "无法创建 WebRTC 数据通道", err.Error(), "warning")
		return
	}
	w.attach(dc)
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		w.progress("ice", "udp", "无法创建 WebRTC 协商请求", err.Error(), "warning")
		return
	}
	if err := w.local(pc, offer); err != nil {
		w.progress("ice", "udp", "WebRTC 候选收集或信令发送失败", err.Error(), "warning")
	}
}
func (w *wire) local(pc *webrtc.PeerConnection, sdp webrtc.SessionDescription) error {
	if err := pc.SetLocalDescription(sdp); err != nil {
		return err
	}
	// Trickle ICE: exchange SDP immediately. Public and self-hosted STUN
	// candidates follow as they arrive; an unreachable server cannot stall LAN
	// or IPv6 connectivity behind a full gathering timeout.
	raw, err := json.Marshal(pc.LocalDescription())
	if err != nil {
		return err
	}
	return w.relaySend("signal", raw)
}
func (w *wire) signal(raw []byte) {
	w.signalMu.Lock()
	defer w.signalMu.Unlock()
	var signal struct {
		Type      string                   `json:"type"`
		Candidate *webrtc.ICECandidateInit `json:"candidate"`
	}
	if json.Unmarshal(raw, &signal) != nil {
		return
	}
	if signal.Type == "native-quic" {
		var s quicSignal
		if json.Unmarshal(raw, &s) == nil {
			w.startQUIC(s)
		}
		return
	}
	if signal.Type == "tcp-init" || signal.Type == "tcp-candidate" {
		w.tcpSignal(raw)
		return
	}
	if w.opts.DisableWebRTC {
		return
	}
	if signal.Type == "candidate" {
		if signal.Candidate == nil {
			return
		}
		pc, err := w.setup()
		if err != nil {
			return
		}
		if pc.RemoteDescription() == nil {
			if len(w.candidates) < 256 {
				w.candidates = append(w.candidates, *signal.Candidate)
			}
		} else {
			_ = pc.AddICECandidate(*signal.Candidate)
		}
		return
	}
	var sdp webrtc.SessionDescription
	if json.Unmarshal(raw, &sdp) != nil {
		return
	}
	if w.host && sdp.Type != webrtc.SDPTypeOffer || !w.host && sdp.Type != webrtc.SDPTypeAnswer {
		return
	}
	pc, err := w.setup()
	if err != nil {
		return
	}
	if err = pc.SetRemoteDescription(sdp); err != nil {
		return
	}
	for _, candidate := range w.candidates {
		_ = pc.AddICECandidate(candidate)
	}
	w.candidates = nil
	if w.host {
		answer, err := pc.CreateAnswer(nil)
		if err == nil {
			_ = w.local(pc, answer)
		}
	}
}

var nextRequest atomic.Uint64

type helloMessage struct {
	Logical     string                `json:"connection,omitempty"`
	Direct      []nativepath.Endpoint `json:"direct,omitempty"`
	Native      bool                  `json:"native,omitempty"`
	QUIC        bool                  `json:"quic,omitempty"`
	RPC         int                   `json:"rpc"`
	ClientNonce string                `json:"clientNonce"`
	HostNonce   string                `json:"hostNonce,omitempty"`
}

func (w *wire) cipher(kind string) *cryptor {
	if kind == "hello" {
		return w.base
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.crypto
}
func (w *wire) handshake(ctx context.Context) error {
	raw, _ := json.Marshal(helloMessage{Logical: w.opts.logicalID, RPC: transport.Version, ClientNonce: w.clientNonce, Native: !w.opts.DisableNative, QUIC: !w.opts.DisableQUIC && !w.opts.RelayOnly})
	if err := w.relaySendContext(ctx, "hello", raw); err != nil {
		return err
	}
	select {
	case <-w.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		w.mu.Lock()
		err := w.handshakeError
		w.mu.Unlock()
		if err != nil {
			return err
		}
		return w.ctx.Err()
	}
}
func (w *wire) hello(raw []byte) {
	var hello helloMessage
	if json.Unmarshal(raw, &hello) != nil {
		return
	}
	if hello.RPC != transport.Version {
		err := errors.New("设备协议版本不一致，请同时更新控制端和被控端")
		w.mu.Lock()
		w.handshakeError = err
		w.mu.Unlock()
		w.progress("protocol-mismatch", "handshake", err.Error(), "当前加密 RPC 协议为第 3 版", "warning")
		w.close()
		return
	}
	clientBytes, err := hex.DecodeString(hello.ClientNonce)
	if err != nil || len(clientBytes) != 32 {
		return
	}
	w.mu.Lock()
	if w.crypto != nil {
		w.mu.Unlock()
		return
	}
	if hello.Logical != "" {
		id, err := hex.DecodeString(hello.Logical)
		if err != nil || len(id) != 16 {
			w.mu.Unlock()
			return
		}
	}
	if !w.host && w.opts.logicalID != hello.Logical {
		w.mu.Unlock()
		return
	}
	w.logicalID = hello.Logical
	if w.host {
		if hello.HostNonce != "" {
			w.mu.Unlock()
			return
		}
		hello.HostNonce = relay.ID() + relay.ID()
	} else {
		if hello.ClientNonce != w.clientNonce {
			w.mu.Unlock()
			return
		}
		hostBytes, err := hex.DecodeString(hello.HostNonce)
		if err != nil || len(hostBytes) != 32 {
			w.mu.Unlock()
			return
		}
	}
	crypto, err := sessionCryptor(w.key, w.peer, hello, w.host)
	if err != nil {
		w.mu.Unlock()
		return
	}
	hello.Native = hello.Native && !w.opts.DisableNative
	hello.QUIC = hello.Native && hello.QUIC && !w.opts.DisableQUIC && !w.opts.RelayOnly
	w.native.negotiated.Store(hello.Native)
	w.native.quicEnabled.Store(hello.QUIC)
	if !w.host {
		w.advertised = hello.Direct[:min(len(hello.Direct), 8)]
	}
	w.crypto = crypto
	w.tcpMu.Lock()
	w.tcpKey = sha256.Sum256([]byte("reign/p2p/tcp/" + w.key + "/" + w.peer + "/" + hello.ClientNonce + "/" + hello.HostNonce))
	w.tcpMu.Unlock()
	close(w.ready)
	w.mu.Unlock()
	if w.host {
		if hello.Native && w.opts.DirectAddresses != nil {
			hello.Direct = w.opts.DirectAddresses()
		} else {
			hello.Direct = nil
		}
		response, _ := json.Marshal(hello)
		_ = w.relaySend("hello", response)
	}

}
func sessionCryptor(key, peer string, hello helloMessage, host bool) (*cryptor, error) {
	derived := sha256.Sum256([]byte("reign/p2p/session/" + key + "/" + peer + "/" + hello.ClientNonce + "/" + hello.HostNonce))
	crypto, err := newCryptor(hex.EncodeToString(derived[:]))
	if err != nil {
		return nil, err
	}
	crypto.prefix = [4]byte{1}
	if host {
		crypto.prefix = [4]byte{2}
	}
	return crypto, nil
}

func (w *wire) progress(id, stage, message, detail, state string) {
	if w.opts.Progress == nil || w.ctx.Err() != nil {
		return
	}
	event := connectivity.Progress{ID: id, Stage: stage, Message: message, Detail: detail, State: state}
	w.progressMu.Lock()
	if w.progressLast == nil {
		w.progressLast = map[string]connectivity.Progress{}
	}
	if prior, ok := w.progressLast[id]; ok && prior == event {
		w.progressMu.Unlock()
		return
	}
	if len(w.progressLast) < 128 || strings.HasPrefix(id, "path:") {
		w.progressLast[id] = event
	}
	w.progressMu.Unlock()
	w.opts.Progress(event)
}

func (w *wire) reportSTUNStats(pc *webrtc.PeerConnection) {
	if w.opts.Progress == nil || w.ctx.Err() != nil {
		return
	}
	for _, stat := range pc.GetStats() {
		if c, ok := stat.(webrtc.ICECandidateStats); ok && c.Type == webrtc.StatsTypeLocalCandidate && c.URL != "" {
			w.progress("stun:"+c.URL, "udp", "STUN 节点已返回公网映射", fmt.Sprintf("%s → %s:%d", c.URL, c.IP, c.Port), "success")
		}
	}
}
