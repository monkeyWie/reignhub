package browserclient

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/internal/relay"
	"sync"
	"time"
)

// Each candidate owns an authenticated peer, encryption counters and request inbox.
func ConnectRoutes(ctx context.Context, p Pairing, device string, cached relay.Routes, fetch func(context.Context) (relay.Routes, error)) (*Client, error) {
	pool := connectivity.NewPathPool(ctx, connectivity.PathOps[*Client]{
		Standby: func(ctx context.Context, c *Client) {
			if c.node != nil {
				_ = c.node.Send(ctx, 0, []byte{4})
				return
			}
			if ws := c.dataWS.Load(); ws != nil {
				_ = ws.Write(ctx, websocket.MessageText, []byte(`{"kind":"standby"}`))
			}
		},
		Closed: func(c *Client) bool { return c.ctx.Err() != nil }, Name: (*Client).Path, Close: (*Client).Close,
		Probe: func(ctx context.Context, c *Client) error {
			_, err := c.Call(ctx, Object{"method": "network"})
			return err
		},
		Select: func(ctx context.Context, c *Client, g uint64) error {
			_, err := c.Call(ctx, Object{"method": "peer.select", "params": g})
			return err
		},
	})
	race, stop := context.WithTimeout(pool.Context(), connectivity.StageTimeout)
	p.logical = id()
	creds := &relay.RouteCredentials{}
	creds.Store(&cached)
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]bool{}
	var failure error
	start := func(name string, connect func() (*Client, error)) {
		mu.Lock()
		if seen[name] {
			mu.Unlock()
			return
		}
		seen[name] = true
		mu.Unlock()
		wg.Go(func() {
			c, err := connect()
			if err != nil {
				mu.Lock()
				failure = errors.Join(failure, err)
				mu.Unlock()
				return
			}
			connectivity.Note(race, "route:"+name, "transport", "已验证连接路径", name, "success")
			pool.Add(race, c, c.meter.ProbeDuration())
		})
	}
	addNode := func(r relay.Routes) {
		for _, n := range r.Nodes() {
			if n == nil || n.WSS == "" || n.Host != p.Host || n.Device != device || n.Role != "client" || r.Grant == "" || r.Expires != 0 && r.Expires <= time.Now().Unix() {
				continue
			}
			for _, direct := range []bool{false, true} {
				if direct && p.RelayOnly {
					continue
				}
				opts := p
				opts.RelayOnly = !direct
				opts.directOnly = direct
				opts.RouteName = "WSS"
				name := "relay:"
				if direct {
					name = "webrtc:"
				}
				start(name+n.WSS, func() (*Client, error) { return connectNode(race, opts, n, r.Grant, creds) })
			}
		}
	}
	for _, direct := range []bool{false, true} {
		if direct && p.RelayOnly {
			continue
		}
		opts := p
		opts.RelayOnly = !direct
		opts.directOnly = direct
		name := "signal-relay"
		if direct {
			name = "signal-webrtc"
		}
		start(name, func() (*Client, error) { return Connect(race, opts) })
	}
	addNode(cached)
	if fetch != nil {
		wg.Go(func() {
			r, err := fetch(race)
			if err == nil {
				creds.Store(&r)
				addNode(r)
			}
		})
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); pool.Seal(); close(finished); stop() }()
	select {
	case <-ctx.Done():
		pool.Close()
		return nil, ctx.Err()
	case <-pool.Ready():
	case <-finished:
		if pool.Active() == nil {
			pool.Close()
			return nil, errors.Join(connectivity.ErrNoRoute, failure)
		}
	}
	if fetch != nil {
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-pool.Context().Done():
					return
				case <-t.C:
				}
				// Self-hosted grants do not expire. Refresh managed leases before expiry.
				if r := creds.Load(); r != nil && r.Grant != "" && r.Expires == 0 {
					continue
				}
				refresh, cancel := context.WithTimeout(pool.Context(), connectivity.StageTimeout)
				r, err := fetch(refresh)
				cancel()
				if err == nil {
					creds.Store(&r)
				}
			}
		}()
	}
	return &Client{routes: pool}, nil
}

func connectNode(ctx context.Context, p Pairing, n *relay.Enrollment, grant string, creds *relay.RouteCredentials) (*Client, error) {
	l, err := nativepath.DialWebSocket(ctx, n.WSS)
	if err != nil {
		return nil, err
	}
	interrupt := context.AfterFunc(ctx, l.Close)
	defer interrupt()
	peerID := id()
	if err = n.Open(ctx, l, "connect", relay.Invitation{Host: p.Host, Device: n.Device, Peer: peerID}); err != nil {
		l.Close()
		return nil, err
	}
	c, err := newClient(ctx, p, nil, peerID)
	if err != nil {
		l.Close()
		return nil, err
	}
	c.node = l
	raw, _ := json.Marshal(Object{"host": p.Host, "device": n.Device, "peer": peerID, "grant": grant, "relayOnly": p.RelayOnly})
	if err = l.Send(ctx, 0, raw); err != nil {
		c.Close()
		return nil, err
	}
	go func() {
		l.Receive(func(lane byte, b []byte) error {
			if len(b) < 2 {
				return errors.New("empty route record")
			}
			switch b[0] {
			case 1, 3:
				return c.receiveChunk(b[1:])
			case 2:
				var f frame
				if lane != 0 || json.Unmarshal(b[1:], &f) != nil || f.Peer != c.id || (f.Kind != "hello" && f.Kind != "signal") {
					return errors.New("invalid route negotiation")
				}
				c.handleFrame(f)
				return nil
			}
			return errors.New("unknown route record")
		})
		c.signalLost.Store(true)
		if !c.Connected() {
			c.Close()
		}
	}()
	_, err = c.verify(ctx)
	if err != nil {
		return nil, err
	}
	creds.Renew(c.ctx, l, n)
	return c, nil
}
