package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/monkeyWie/reignhub/internal/transport"
	"github.com/pion/webrtc/v4"
)

type Client struct {
	routes       *connectivity.PathPool[*Client]
	hostPlatform string
	wire         *wire
	mu           sync.Mutex
	pending      map[string]*pendingCall
	done         chan struct{}
	cache        map[string]snapshot
	cacheBytes   int
}

type pendingCall struct {
	method    string
	idle      *transport.ReadIdle
	ctx       context.Context
	responses *transport.Inbox[control.Response]
}

func Connect(ctx context.Context, p Pairing, opts Options) (*Client, error) {
	if opts.Progress != nil {
		ctx = connectivity.WithProgress(ctx, opts.Progress)
	}
	return connectivity.RetrySetup(ctx, func(setup context.Context) (*Client, error) {
		return connectOnce(setup, p, opts)
	})
}

func connectOnce(ctx context.Context, p Pairing, opts Options) (*Client, error) {
	if opts.Progress == nil {
		opts.Progress = connectivity.Observer(ctx)
	} else {
		ctx = connectivity.WithProgress(ctx, opts.Progress)
	}
	ws, dataWS, welcome, err := dialService(ctx, p, "client", connectivity.StageTimeout, opts)
	peer, allowRelay := welcome.Peer, welcome.AllowRelay
	if err != nil {
		return nil, err
	}
	c, err := newClientPeer(ws, peer, p, opts)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	w := c.wire
	w.relayLink = &relayLink{}
	w.relayLink.socket.Store(dataWS)
	go func() {
		<-w.ctx.Done()
		_ = ws.CloseNow()
		close(c.done)
	}()
	heartbeat := newSocketHeartbeat()
	go heartbeat.run(w.ctx, ws, true, opts.Progress)
	if allowRelay && !opts.directOnly {
		// The pairing RPC's context ends after Connect returns. Optional relay
		// setup belongs to the live wire and must not gate handshake or ICE.
		relayCtx := connectivity.WithProgress(w.ctx, opts.Progress)
		go w.relayLink.run(relayCtx, p, "client", peer, nil)
	}
	go func() {
		defer ws.CloseNow()
		for {
			_, raw, err := ws.Read(w.ctx)
			if err != nil {
				w.signalLost.Store(true)
				w.progress("signal-lost", "signal", "信令连接已断开", err.Error(), "warning")
				if strings.HasPrefix(w.path(), "P2P") {
					w.progress("signal-independent", "signal", "信令暂不可用，保留现有直连", w.path()+" · 数据通信继续使用已建立的加密通道", "info")
				}
				return
			}
			heartbeat.received()
			var f relay.Frame
			if json.Unmarshal(raw, &f) == nil {
				w.receive(f)
			}
		}
	}()
	connectivity.Report(ctx, "handshake", "正在与被控电脑建立加密握手", connectivity.StageTimeout)
	handshakeCtx, stopHandshake := context.WithTimeout(ctx, connectivity.StageTimeout)
	err = w.handshake(handshakeCtx)
	stopHandshake()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("配对握手失败: %w", err)
	}
	connectivity.Note(ctx, "handshake", "handshake", "端到端加密握手完成", "已验证设备配对密钥", "success")
	message := "正在尝试 P2P 直连；暂无可用的备用中继通道"
	hasRelay := allowRelay
	if hasRelay {
		message = "优先尝试 P2P 直连，必要时自动使用中继"
	}
	if opts.RelayOnly {
		if !hasRelay {
			c.Close()
			return nil, errors.New("当前服务未提供中继权限，请关闭“仅用中继”后尝试直连，或使用支持中继的服务")
		}
		message = "正在验证中继连接"
	}
	connectivity.Report(ctx, "direct", message, connectivity.StageTimeout)
	go w.offer()
	if err = w.waitPath(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("连接路径验证失败: %w", err)
	}
	connectivity.Report(ctx, "verify", "正在验证设备响应", connectivity.StageTimeout)
	go c.watchPaths()
	verifyCtx, stopVerify := context.WithTimeout(ctx, connectivity.StageTimeout)
	defer stopVerify()
	// Prove possession of the pairing key before presenting a connected UI.
	var statuses control.NetworkStatus
	if err = c.Call(verifyCtx, control.Request{Method: "network"}, &statuses); err != nil {
		c.Close()
		return nil, fmt.Errorf("配对验证失败: %w", err)
	}
	if opts.RememberAddresses != nil {
		opts.RememberAddresses(w.advertised)
	}
	c.hostPlatform = statuses.Platform
	w.meter.Connected()
	return c, nil
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

// Business RPCs have no total deadline. End their wait when every usable data
// path has actually gone away, even if the signaling socket remains open.
func (c *Client) recoveringPath() bool {
	c.wire.mu.Lock()
	pc, dc := c.wire.pc, c.wire.dc
	c.wire.mu.Unlock()
	return pc != nil && dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen && pc.ConnectionState() == webrtc.PeerConnectionStateDisconnected
}

// Resume probes the original authenticated data path without re-pairing or
// replaying any command. A brief ICE interruption can heal on this same peer.
func (c *Client) Resume(ctx context.Context) error {
	if c.routes != nil {
		v := c.routes.Use()
		if v == nil {
			return errors.New("连接已关闭")
		}
		return v.Resume(ctx)
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for c.Path() == "Disconnected" {
		if !c.recoveringPath() {
			return errors.New("连接已断开")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return errors.New("连接已断开")
		case <-tick.C:
		}
	}
	var statuses json.RawMessage
	err := c.Call(ctx, control.Request{Method: "network"}, &statuses)
	return err
}

func (c *Client) watchPaths() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var lost time.Time
	for {
		select {
		case <-c.done:
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
			if !c.recoveringPath() || time.Since(lost) >= 20*time.Second {
				c.wire.progress("paths-lost", "transport", "所有数据路径不可用，开始恢复连接", fmt.Sprintf("recoveringPath=%t elapsed=%s", c.recoveringPath(), time.Since(lost).Round(time.Millisecond)), "warning")
				_ = c.Close()
				return
			}
		}
	}
}

func (c *Client) Path() string {
	if c.routes != nil {
		if v := c.routes.Active(); v != nil {
			return v.Path()
		}
		return "Disconnected"
	}
	select {
	case <-c.done:
		return "Disconnected"
	default:
	}
	path := c.wire.path()
	if strings.HasPrefix(path, "P2P") {
		return path
	}
	if c.wire.independent == nil && c.wire.relaySocket() == nil {
		return "Disconnected"
	}
	return path
}
func (c *Client) Close() error {
	if c.routes != nil {
		c.routes.Close()
		return nil
	}
	c.wire.close()
	if c.wire.independent != nil {
		c.wire.independent.link.Close()
		return nil
	}
	c.wire.relayLink.close()
	return c.wire.ws.CloseNow()
}
func (c *Client) Call(ctx context.Context, r control.Request, out any) (callErr error) {
	if c.routes != nil {
		v := c.routes.Use()
		if v == nil {
			return errors.New("连接已关闭")
		}
		err := v.Call(ctx, r, out)
		if errors.Is(err, context.DeadlineExceeded) || v.Path() == "Disconnected" {
			c.routes.Check()
		}
		return err
	}
	done := c.wire.meter.Begin(r.Method)
	defer func() { done(callErr) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cacheKey := ""
	var cached snapshot
	if control.ReadOnly(r.Method) {
		r.ID = ""
		r.Revision = ""
		encoded, _ := json.Marshal(r)
		cacheKey = string(encoded)
		c.mu.Lock()
		cached = c.cache[cacheKey]
		c.mu.Unlock()
		if cached.revision != "" && (!r.Progress || (r.Method == "transcript" && r.HistoryItems > 0)) {
			r.Progress = false
			r.Revision = cached.revision
		}
	}
	if r.ID == "" {
		r.ID = fmt.Sprintf("%s-%d", relay.ID(), nextRequest.Add(1))
	}
	idle := transport.NewReadIdle(control.CancellableRead(r.Method))
	defer idle.Stop()
	ch := transport.NewInbox[control.Response]()
	defer ch.Close()
	c.mu.Lock()
	c.pending[r.ID] = &pendingCall{ctx: ctx, responses: ch, idle: idle, method: r.Method}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, r.ID); c.mu.Unlock(); c.wire.discardResponse(r.ID) }()
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("request too large")
	}
	if err = c.wire.sendContext(transport.WithMethod(ctx, r.Method), r.ID, raw); err != nil {
		// A completed frame can reach the host before its sender reports success.
		c.cancelRead(r)
		return err
	}
	idle.Touch()
	var history provider.TranscriptBuffer
	historyBytes := 0
	for {
		select {
		case <-ch.Ready:
			result, ok, err := ch.Take()
			if err != nil {
				c.cancelRead(r)
				return err
			}
			if !ok {
				continue
			}
			if result.Partial {
				if !r.Progress || r.Method != "transcript" {
					return errors.New("unexpected partial response")
				}
				historyBytes += len(result.Result)
				if historyBytes > 16<<20 {
					return errors.New("消息历史超过 16 MiB，请缩小查询范围")
				}
				var fragment provider.Transcript
				if err := json.Unmarshal(result.Result, &fragment); err != nil {
					return err
				}
				if err := history.Add(fragment); err != nil {
					c.cancelRead(r)
					return err
				}
				if err := provider.ObserveTranscript(ctx, fragment); err != nil {
					return err
				}
				continue
			}
			if result.Error != "" {
				if result.Code == control.ConnectionAuthorizationExpired {
					c.wire.progress("authorization-renewal", "retry", "连接授权需要续期，重新建立连接", "复用已保存的设备配对，无需重新登录；不会自动重放写操作", "warning")
					// Stop reporting an expired direct path as connected. The hub's
					// existing reconnection policy can now obtain a fresh service lease.
					c.wire.close()
					return errors.New("连接授权需要续期，请在连接恢复后重试")
				}
				return &control.ResponseError{Code: result.Code, Message: result.Error}
			}
			if history.Seen {
				var metadata provider.Transcript
				if err := json.Unmarshal(result.Result, &metadata); err != nil {
					return err
				}
				if err := history.Add(metadata); err != nil {
					return err
				}
				result.Result, err = json.Marshal(history.Transcript)
				if err != nil {
					return err
				}
			}
			if result.NotModified {
				if cacheKey == "" || cached.revision == "" || result.Revision != cached.revision {
					return errors.New("invalid conditional response")
				}
				result.Result = cached.data
			} else if cacheKey != "" && result.Revision != "" {
				c.remember(cacheKey, snapshot{revision: result.Revision, data: result.Result})
			}
			if out != nil {
				return json.Unmarshal(result.Result, out)
			}
			return nil
		case <-idle.C:
			if !idle.Expired() {
				continue
			}
			c.cancelRead(r)
			return errors.New("设备读取连续 30 秒没有进展，已取消本次读取，请重试")
		case <-ctx.Done():
			c.cancelRead(r)
			if control.ReadOnly(r.Method) {
				return fmt.Errorf("等待设备读取响应超时或已取消: %w", ctx.Err())
			}
			return fmt.Errorf("请求超时或取消；操作结果可能尚未返回，请刷新后再决定是否重试: %w", ctx.Err())
		case <-c.done:
			return errors.New("relay 已断开；请重新连接，刷新后再重试操作")
		}
	}
}

type snapshot struct {
	revision string
	data     json.RawMessage
}

func (c *Client) remember(key string, entry snapshot) {
	const limit = 16 << 20
	if len(entry.data) > limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = make(map[string]snapshot)
	}
	if previous, ok := c.cache[key]; ok {
		c.cacheBytes -= len(previous.data)
		delete(c.cache, key)
	}
	for len(c.cache) >= 32 || c.cacheBytes+len(entry.data) > limit {
		for k, v := range c.cache {
			c.cacheBytes -= len(v.data)
			delete(c.cache, k)
			break
		}
	}
	c.cache[key] = entry
	c.cacheBytes += len(entry.data)
}

func (c *Client) Metrics() map[string]any {
	if c.routes != nil {
		if v := c.routes.Active(); v != nil {
			return v.Metrics()
		}
		return nil
	}
	return c.wire.meter.Snapshot()
}

func newClientPeer(ws *websocket.Conn, peer string, p Pairing, opts Options) (*Client, error) {
	c := &Client{pending: make(map[string]*pendingCall), done: make(chan struct{})}
	w, err := newWire(context.Background(), ws, peer, p.Key, false, opts, func(data []byte) {
		var r control.Response
		if json.Unmarshal(data, &r) != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[r.ID]
		c.mu.Unlock()
		if ch != nil && ch.ctx.Err() == nil {
			ch.responses.PushSequence(r, len(data), r.Sequence)
		}

	})
	if err != nil {

		return nil, err
	}
	c.wire = w
	w.wantsResponse = func(id string) bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		pending := c.pending[id]
		if pending == nil {
			return false
		}
		pending.idle.Touch()
		if pending.method != "network" {
			w.meter.Progress()
		}
		return true
	}

	return c, nil
}
