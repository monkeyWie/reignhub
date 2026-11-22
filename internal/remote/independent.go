package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/wlynxg/anet"
)

type routeRequest struct {
	Host      string `json:"host"`
	Device    string `json:"device"`
	Peer      string `json:"peer"`
	Grant     string `json:"grant"`
	RelayOnly bool   `json:"relayOnly"`
}

// Each candidate has fresh nonces, its own authenticated peer and request inbox.
type independentPath struct {
	link *nativepath.Link
	name string
}

func (w *wire) readIndependent() {
	w.independent.link.Receive(func(lane byte, b []byte) error {
		if len(b) < 2 {
			return errors.New("empty route record")
		}
		switch b[0] {
		case 1, 3:
			w.onChunk(b[1:])
		case 2:
			if lane != 0 {
				return errors.New("invalid signaling lane")
			}
			var f relay.Frame
			if json.Unmarshal(b[1:], &f) != nil || f.Peer != w.peer || (f.Kind != "hello" && f.Kind != "signal") {
				return errors.New("invalid route negotiation")
			}
			w.receive(f)
		default:
			return errors.New("unknown route record")
		}
		return w.ctx.Err()
	})
	if w.hasDirectPath() {
		w.detachSignal(nil)
	} else {
		w.close()
	}
}

func ServePaired(ctx context.Context, l *nativepath.Link, host string, invite *relay.Invitation, opts Options, d *control.Dispatcher, status *HostStatus) {
	defer l.Close()
	stopLink := context.AfterFunc(ctx, l.Close)
	defer stopLink()
	setup, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := l.Setup(setup)
	var req routeRequest
	if err != nil || len(raw) > 12000 || json.Unmarshal(raw, &req) != nil || req.Host != host || len(req.Peer) != 32 || opts.ResolveKey == nil {
		return
	}
	if _, err := hex.DecodeString(req.Peer); err != nil {
		return
	}
	if invite == nil && (req.RelayOnly || opts.RelayOnly) {
		return
	}
	if invite != nil && (req.Device != invite.Device || req.Peer != invite.Peer) {
		return
	}
	key, ok := opts.ResolveKey(req.Device)
	if !ok {
		return
	}
	opts.status = status
	opts.RelayOnly = req.RelayOnly || opts.RelayOnly
	w, err := newHostPeer(ctx, nil, req.Peer, key, host, req.Device, opts, d)
	if err != nil {
		return
	}
	w.grant, w.device = req.Grant, req.Device
	name := "P2P · " + l.Network
	if invite != nil {
		name = "Relay · " + l.Network
	}
	w.independent = &independentPath{l, name}
	defer w.close()
	if w.authorize(host, req.Device, control.Request{Method: "network"}) != nil {
		return
	}
	if !status.reserve(req.Peer, w) {
		return
	}
	defer status.remove(req.Peer, w)
	// An authenticated hello must follow the enrollment record promptly.
	timer := time.AfterFunc(8*time.Second, l.Close)
	defer timer.Stop()
	go func() {
		select {
		case <-w.ready:
			timer.Stop()
		case <-w.ctx.Done():
		}
	}()
	stop := context.AfterFunc(w.ctx, l.Close)
	defer stop()
	w.readIndependent()
	<-w.ctx.Done()
}
func ListenPaired(ctx context.Context, address string, key ed25519.PrivateKey, host string, opts Options, d *control.Dispatcher, status *HostStatus) (*nativepath.Listener, error) {
	return nativepath.Listen(ctx, address, key, func(ctx context.Context, l *nativepath.Link) { ServePaired(ctx, l, host, nil, opts, d, status) })
}
func DirectAddresses(port, pin string) []nativepath.Endpoint {
	var out []nativepath.Endpoint
	addresses, _ := anet.InterfaceAddrs()
	for _, a := range addresses {
		ip, _, err := net.ParseCIDR(a.String())
		if err != nil || !ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, nativepath.Endpoint{Address: net.JoinHostPort(ip.String(), port), Pin: pin})
		if len(out) == 8 {
			break
		}
	}
	return out
}
func runRegistered(ctx context.Context, e *relay.Enrollment, host string, opts Options, d *control.Dispatcher, status *HostStatus) {
	slots := make(chan struct{}, 16)
	var sessions sync.WaitGroup
	defer sessions.Wait()
	for ctx.Err() == nil {
		setup, cancel := context.WithTimeout(ctx, 5*time.Second)
		l, err := nativepath.DialFirst(setup, e.Endpoint)
		cancel()
		if err == nil {
			stop := context.AfterFunc(ctx, l.Close)
			opts.Credentials.Current(e).Register(ctx, l, func(i relay.Invitation) {
				if opts.ResolveKey == nil {
					return
				}
				if _, ok := opts.ResolveKey(i.Device); !ok {
					return
				}
				select {
				case slots <- struct{}{}:
				default:
					return
				}
				sessions.Go(func() {
					defer func() { <-slots }()
					setup, cancel := context.WithTimeout(ctx, 10*time.Second)
					defer cancel()
					path, err := nativepath.DialFirst(setup, e.Endpoint)
					if err != nil {
						return
					}
					defer path.Close()
					member := opts.Credentials.Current(e)
					if member.Open(setup, path, "accept", i) == nil {
						opts.Credentials.Renew(ctx, path, member)
						ServePaired(ctx, path, host, &i, opts, d, status)
					}
				})
			}, opts.Credentials)
			stop()
			l.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
func RegisterPaired(ctx context.Context, e *relay.Enrollment, host string, opts Options, d *control.Dispatcher, status *HostStatus) {
	if e != nil && e.Host == host && e.Role == "host" {
		runRegistered(ctx, e, host, opts, d, status)
	}
}

func connectPaired(ctx context.Context, p Pairing, opts Options, device, grant string, endpoint nativepath.Endpoint, network string, node *relay.Enrollment) (*Client, error) {
	var l *nativepath.Link
	var err error
	if network == "" {
		l, err = nativepath.DialFirst(ctx, endpoint)
	} else {
		l, err = nativepath.Dial(ctx, endpoint, network)
	}
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, l.Close)
	defer stop()
	peer := relay.ID()
	if node != nil {
		err = node.Open(ctx, l, "connect", relay.Invitation{Host: p.Host, Device: device, Peer: peer})
		if err != nil {
			l.Close()
			return nil, err
		}
	}
	c, err := newClientPeer(nil, peer, p, opts)
	if err != nil {
		l.Close()
		return nil, err
	}
	w := c.wire
	name := "P2P · " + network
	if node != nil {
		name = "Relay · " + l.Network
	}
	w.independent = &independentPath{l, name}
	go func() { <-w.ctx.Done(); l.Close(); close(c.done) }()
	raw, _ := json.Marshal(routeRequest{p.Host, device, peer, grant, opts.RelayOnly})
	if err = l.Send(ctx, 0, raw); err != nil {
		c.Close()
		return nil, err
	}
	go w.readIndependent()
	if err = w.handshake(ctx); err != nil {
		c.Close()
		return nil, err
	}
	if opts.directOnly {
		go w.offer()
		if err = w.waitPath(ctx); err != nil {
			c.Close()
			return nil, err
		}
	}
	var info control.NetworkStatus
	if err = c.Call(ctx, control.Request{Method: "network"}, &info); err != nil {
		c.Close()
		return nil, err
	}
	c.hostPlatform = info.Platform
	if opts.RememberAddresses != nil {
		addresses := w.advertised
		if node == nil {
			addresses = append([]nativepath.Endpoint{endpoint}, addresses...)
		}
		opts.RememberAddresses(addresses)
	}
	w.meter.Connected()
	if node != nil {
		opts.Credentials.Renew(w.ctx, l, node)
	}

	w.progress("path:"+name, "transport", name, endpoint.AddressFor(network), "success")
	return c, nil
}
func routeRank(path string) int {
	switch {
	case strings.HasPrefix(path, "P2P · "):
		return 3
	case strings.HasPrefix(path, "Relay · "):
		return 1
	}
	return 0
}

// NetworkKey scopes address hints by the native controller's interface prefixes.
func NetworkKey() string {
	addresses, _ := anet.InterfaceAddrs()
	var prefixes []string
	for _, address := range addresses {
		ip, subnet, err := net.ParseCIDR(address.String())
		if err == nil && ip.IsGlobalUnicast() {
			prefixes = append(prefixes, subnet.String())
		}
	}
	sort.Strings(prefixes)
	sum := sha256.Sum256([]byte(strings.Join(prefixes, ",")))
	return hex.EncodeToString(sum[:8])
}
