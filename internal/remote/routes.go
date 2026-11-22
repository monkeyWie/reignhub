package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/internal/relay"
)

func ConnectRoutes(ctx context.Context, p Pairing, opts Options, device string, cached relay.Routes, direct []nativepath.Endpoint) (*Client, error) {
	pool := connectivity.NewPathPool(ctx, connectivity.PathOps[*Client]{
		Standby: func(ctx context.Context, c *Client) {
			if c.wire.independent != nil {
				_ = c.wire.independent.link.Send(ctx, 0, []byte{4})
				return
			}
			if ws := c.wire.relaySocket(); ws != nil {
				_ = ws.Write(ctx, websocket.MessageText, []byte(`{"kind":"standby"}`))
			}
		},
		Closed: func(c *Client) bool { return c.wire.ctx.Err() != nil },
		Name:   (*Client).Path, Probe: func(ctx context.Context, c *Client) error {
			return c.Call(ctx, control.Request{Method: "network"}, nil)
		},
		Select: func(ctx context.Context, c *Client, g uint64) error {
			raw, _ := json.Marshal(g)
			return c.Call(ctx, control.Request{Method: "peer.select", Params: raw}, nil)
		},
		Close: func(c *Client) { c.Close() },
	})
	life := pool.Context()
	race, stop := context.WithTimeout(life, connectivity.StageTimeout)
	source := &relay.RouteCredentials{}
	source.Store(&cached)
	opts.Credentials = source
	opts.logicalID = relay.ID()
	if opts.Progress == nil {
		opts.Progress = connectivity.Observer(ctx)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failure error
	seen := map[string]bool{}

	start := func(id string, dial func(context.Context) (*Client, error)) {
		mu.Lock()
		if seen[id] {
			mu.Unlock()
			return
		}
		seen[id] = true
		mu.Unlock()
		wg.Go(func() {
			started := time.Now()
			c, err := dial(race)
			if err != nil {
				mu.Lock()
				failure = errors.Join(failure, fmt.Errorf("%s: %w", id, err))
				mu.Unlock()
				connectivity.Note(race, "dial:"+id, "transport", "候选路径未连通", id+" · "+err.Error(), "warning")
				return
			}
			connectivity.Note(race, "dial:"+id, "transport", "已验证候选路径", fmt.Sprintf("%s · %d ms", c.Path(), time.Since(started).Milliseconds()), "success")
			pool.Add(race, c, c.wire.meter.ProbeDuration())
		})
	}
	relayOpts := opts
	relayOpts.RelayOnly = true
	start("signal/relay", func(ctx context.Context) (*Client, error) { return Connect(ctx, p, relayOpts) })
	directModes := map[string]Options{}
	if !opts.RelayOnly {
		for _, name := range []string{"QUIC", "TCP", "WebRTC"} {
			if name == "QUIC" && (opts.DisableQUIC || opts.DisableNative) || name == "TCP" && opts.DisableTCP || name == "WebRTC" && opts.DisableWebRTC {
				continue
			}
			mode := opts
			mode.directOnly = true
			mode.DisableQUIC = name != "QUIC"
			mode.DisableTCP = name != "TCP"
			mode.DisableWebRTC = name != "WebRTC"
			directModes[name] = mode
			start("signal/"+name, func(ctx context.Context) (*Client, error) { return Connect(ctx, p, mode) })
		}
	}
	addRoutes := func(routes relay.Routes) {
		if opts.DisableNative {
			return
		}
		add := func(e nativepath.Endpoint, node *relay.Enrollment) {
			for _, network := range []string{"QUIC", "TCP", "WSS"} {
				addr := e.AddressFor(network)
				if addr == "" || network == "QUIC" && opts.DisableQUIC {
					continue
				}
				kind := "P2P"
				if node != nil {
					kind = "Relay"
				}
				start(kind+"/"+network+"/"+addr+"/"+e.Pin, func(ctx context.Context) (*Client, error) {
					mode := opts
					if node != nil {
						mode = relayOpts
					}
					return connectPaired(ctx, p, mode, device, routes.Grant, e, network, node)
				})
			}
		}
		if !opts.RelayOnly {
			for _, e := range direct[:min(len(direct), 8)] {
				add(e, nil)
			}
		}
		for _, n := range routes.Nodes() {
			if (routes.Expires != 0 && routes.Expires <= time.Now().Unix()) || n.Host != p.Host || n.Device != device || n.Role != "client" {
				continue
			}
			add(n.Endpoint, n)
			for name, mode := range directModes {
				start("node/"+n.Pin+"/"+name, func(ctx context.Context) (*Client, error) {
					return connectPaired(ctx, p, mode, device, routes.Grant, n.Endpoint, "", n)
				})
			}
		}
	}
	addRoutes(cached)
	if opts.FetchRoutes != nil {
		wg.Go(func() {
			fresh, err := opts.FetchRoutes(race)
			if err == nil && race.Err() == nil {
				source.Store(&fresh)
				addRoutes(fresh)
			}
		})
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); pool.Seal(); stop(); close(finished) }()
	select {
	case <-ctx.Done():
		pool.Close()
		return nil, ctx.Err()
	case <-pool.Ready():
	case <-finished:
		if pool.Active() == nil {
			if failure == nil {
				failure = errors.New("设备未确认可用路径")
			}
			pool.Close()
			return nil, failure
		}
	}
	if opts.FetchRoutes != nil {
		go func() {
			tick := time.NewTicker(time.Minute)
			defer tick.Stop()
			for {
				select {
				case <-life.Done():
					return
				case <-tick.C:
				}
				if r := source.Load(); r != nil && r.Grant != "" && r.Expires == 0 {
					continue
				}
				refresh, cancel := context.WithTimeout(life, connectivity.StageTimeout)
				fresh, err := opts.FetchRoutes(refresh)
				cancel()
				if err == nil {
					source.Store(&fresh)
				}
			}
		}()
	}
	return &Client{routes: pool}, nil
}
