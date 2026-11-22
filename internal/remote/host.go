package remote

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/monkeyWie/reignhub/internal/transport"
)

// RunHost reconnects the authenticated signal lease independently of existing
// authenticated direct data paths.
func RunHost(ctx context.Context, p Pairing, opts Options, d *control.Dispatcher) {
	RunHostObserved(ctx, p, opts, d, nil)
}
func RunHostObserved(ctx context.Context, p Pairing, opts Options, d *control.Dispatcher, status *HostStatus) {
	defer status.DisconnectAll()
	defer status.set("stopped", "后台连接已停止")
	for ctx.Err() == nil {
		status.set("connecting", "正在连接中继服务器")
		if err := serveHostObserved(ctx, p, opts, d, status); err != nil && ctx.Err() == nil {
			log.Printf("relay host: %v", err)
			status.set("error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func serveHost(ctx context.Context, p Pairing, opts Options, d *control.Dispatcher) error {
	return serveHostObserved(ctx, p, opts, d, nil)
}
func serveHostObserved(ctx context.Context, p Pairing, opts Options, d *control.Dispatcher, status *HostStatus) error {
	ws, dataWS, welcome, err := dialService(ctx, p, "host", connectivity.StageTimeout, opts)
	opts.status = status
	hostPeer, allowRelay := welcome.Peer, welcome.AllowRelay
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	if dataWS != nil {
		defer dataWS.CloseNow()
	}
	if dataWS != nil {
		status.set("connected", "信令与备用中继已连接，等待控制端配对")
	} else if allowRelay {
		status.set("connected", "信令已连接，直连可用，备用中继在后台准备")
	} else {
		status.set("connected", "信令已连接，可尝试 P2P 直连；暂无可用中继")
	}
	life, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeat := newSocketHeartbeat()
	go heartbeat.run(life, ws, true, opts.Progress)
	var replyRelay *relayLink
	if allowRelay {
		replyRelay = &relayLink{}
		replyRelay.socket.Store(dataWS)
		defer replyRelay.close()
		go replyRelay.run(life, p, "host", hostPeer, status)
	}
	peers := map[string]*wire{}
	defer func() {
		for _, w := range peers {
			if ctx.Err() == nil && w.hasDirectPath() {
				w.detachSignal(status)
			} else {
				w.close()
				status.remove(w.peer, w)
			}
		}
	}()
	for {
		_, raw, err := ws.Read(life)
		if err != nil {
			return err
		}
		heartbeat.received()
		var f relay.Frame
		if json.Unmarshal(raw, &f) != nil || f.Peer == "" {
			continue
		}
		if f.Kind == "lease" && f.Peer == hostPeer {
			continue
		}
		if f.Kind == "bye" {
			if w := peers[f.Peer]; w != nil {
				if w.hasDirectPath() {
					w.detachSignal(status)
				} else {
					w.close()
					delete(peers, f.Peer)
					status.remove(f.Peer, w)
				}
			}
			continue
		}
		w := peers[f.Peer]
		if w == nil {
			for id, old := range peers {
				if old.ctx.Err() != nil {
					delete(peers, id)
				}
			}
			if len(peers) >= 16 {
				continue
			}
			key := p.Key
			deviceID := f.Device
			if opts.ResolveKey != nil {
				var ok bool
				key, ok = opts.ResolveKey(deviceID)
				if !ok {
					continue
				}
			}
			peer, err := newHostPeer(ctx, ws, f.Peer, key, p.Host, deviceID, opts, d)
			if err != nil {
				continue
			}
			peer.device = deviceID
			peer.relayLink = replyRelay
			peer.grant = f.Grant
			w = peer
			if !status.reserve(f.Peer, w) {
				w.close()
				continue
			}
			peers[f.Peer] = w
		}
		if f.Grant != "" {
			w.grantMu.Lock()
			w.grant = f.Grant
			w.grantMu.Unlock()
		}
		w.receive(f)
	}
}

func newHostPeer(ctx context.Context, ws *websocket.Conn, peerID, key, hostID, deviceID string, opts Options, d *control.Dispatcher) (*wire, error) {
	var peer *wire
	var err error
	limiter := make(chan struct{}, 8)
	probes := make(chan struct{}, 2)
	terminalSlots := make(chan struct{}, 4)
	var reads hostReads
	peer, err = newWire(ctx, ws, peerID, key, true, opts, func(data []byte) {
		if opts.ResolveKey != nil {
			if liveKey, ok := opts.ResolveKey(deviceID); !ok || liveKey != key {
				return
			}
		}
		var req control.Request
		if len(data) > 1<<20 || json.Unmarshal(data, &req) != nil || req.ID == "" {
			return
		}
		if req.Method == "peer.grant" && opts.ResolveKey != nil {
			var ticket string
			response := control.Response{ID: req.ID, Result: json.RawMessage(`{}`)}
			if json.Unmarshal(req.Params, &ticket) != nil || peer.authorizeTicket(ticket, hostID, deviceID, req) != nil {
				response = authorizationFailure(req.ID, errConnectionAuthorization)
			} else {
				peer.grantMu.Lock()
				peer.offlineGrant = ticket
				peer.grantMu.Unlock()
			}
			raw, _ := json.Marshal(response)
			go peer.sendContext(peer.ctx, req.ID, raw)
			return
		}
		if err := peer.authorize(hostID, deviceID, req); err != nil {
			encoded, _ := json.Marshal(authorizationFailure(req.ID, err))
			go peer.sendContext(peer.ctx, req.ID, encoded)
			return
		}
		if req.Method == "peer.select" {
			var generation uint64
			response := control.Response{ID: req.ID, Result: json.RawMessage(`{}`)}
			if json.Unmarshal(req.Params, &generation) != nil || !opts.status.selectPath(peer, generation) {
				response.Error = "连接路径已被替换"
			}
			raw, _ := json.Marshal(response)
			go peer.sendContext(peer.ctx, req.ID, raw)
			return
		}
		if req.Method == control.CancelRead {
			reads.cancel(req.Cancel)
			return
		}
		// Agent work can outlive a fixed RPC budget. Cancel with the peer
		// instead; connection liveness is checked independently.
		callCtx, stop := context.WithCancel(peer.ctx)
		callCtx = transport.WithMethod(callCtx, req.Method)
		identity := ""
		if deviceID != "" {
			identity = hostID + "/" + deviceID
		}
		callCtx = control.WithRemoteIdentity(callCtx, identity)
		watch := req.Method == "watch"
		slots := limiter
		if req.Method == "network" {
			slots = probes // Liveness must remain measurable while Agent reads stall.
		}
		if req.Method == "terminal.read" {
			slots = terminalSlots
		}
		busy := func() {
			stop()
			encoded, _ := json.Marshal(control.Response{ID: req.ID, Code: "HOST_BUSY", Error: "设备读取繁忙，请稍后重试"})
			go peer.sendContext(peer.ctx, req.ID, encoded)
		}
		if !watch {
			select {
			case slots <- struct{}{}:
			default:
				busy()
				return
			}
		}
		read, ok := reads.add(req, stop)
		if !ok {
			if !watch {
				<-slots
			}
			busy()
			return
		}
		go func() {
			defer reads.remove(req.ID, read)
			if !watch {
				defer func() { <-slots }()
			}
			defer stop()
			originalID := req.ID
			if deviceID != "" {
				req.ID = deviceID + "/" + req.ID
			}
			var partsSent uint32
			if req.Progress && req.Method == "transcript" {
				callCtx = provider.WithTranscriptObserver(callCtx, func(fragment provider.Transcript) error {
					if opts.ResolveKey != nil {
						if liveKey, ok := opts.ResolveKey(deviceID); !ok || liveKey != key {
							return errors.New("远控授权已过期")
						}
					}
					if err := peer.authorize(hostID, deviceID, req); err != nil {
						return err
					}
					raw, err := json.Marshal(fragment)
					if err != nil {
						return err
					}
					fragmentResponse := control.Response{ID: originalID, Partial: true, Result: raw}
					if peer.native.negotiated.Load() {
						fragmentResponse.Sequence = partsSent + 1
					}
					encoded, err := json.Marshal(fragmentResponse)
					if err != nil {
						return err
					}
					if len(encoded) > 16<<20 {
						return errors.New("消息超过 16 MiB")
					}
					if err := peer.sendContext(callCtx, originalID, encoded); err != nil {
						return err
					}
					partsSent++
					return nil
				})
			}
			var result control.Response
			if req.Method == "peer.info" && opts.OnClientInfo != nil && opts.ResolveKey != nil {
				var info device.ClientInfo
				err := json.Unmarshal(req.Params, &info)
				if err == nil {
					err = opts.OnClientInfo(deviceID, info.Normalize())
				}
				if err != nil {
					result.Error = err.Error()
				} else {
					result.Result = json.RawMessage(`{}`)
				}
			} else {
				result = d.HandleRemote(callCtx, req)
			}
			if partsSent > 0 && result.Error == "" {
				if peer.native.negotiated.Load() {
					result.Sequence = partsSent + 1
				}
				var metadata provider.Transcript
				if json.Unmarshal(result.Result, &metadata) == nil {
					metadata.Turns = nil
					result.Result, _ = json.Marshal(metadata)
				}
			}
			result.ID = originalID
			if opts.ResolveKey != nil {
				if liveKey, ok := opts.ResolveKey(deviceID); !ok || liveKey != key {
					return
				}
			}
			if err := peer.authorize(hostID, deviceID, req); err != nil {
				result = authorizationFailure(originalID, err)
			}
			encoded, _ := json.Marshal(result)
			if len(encoded) > 16<<20 {
				encoded, _ = json.Marshal(control.Response{ID: originalID, Error: "响应超过 16 MiB，请缩小查询范围"})
			}
			_ = peer.sendContext(callCtx, originalID, encoded)
		}()
	})
	return peer, err
}
