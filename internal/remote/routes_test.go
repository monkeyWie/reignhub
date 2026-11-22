package remote

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/relay"
)

type pendingSendProvider struct {
	*testProvider
	entered chan struct{}
	release chan struct{}
}

func (p *pendingSendProvider) Send(ctx context.Context, id, text string) error {
	p.sends.Add(1)
	close(p.entered)
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestFirstUsableRouteDrainsPendingWriteOnUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	room := relay.New(strings.Repeat("r", 32))
	srv := httptest.NewServer(room)
	defer srv.Close()
	defer room.Close()
	host, device := relay.ID(), relay.ID()
	p := Pairing{Host: host, Relay: "ws" + strings.TrimPrefix(srv.URL, "http"), RelayToken: strings.Repeat("r", 32), Key: strings.Repeat("ab", 32)}
	fake := &pendingSendProvider{testProvider: &testProvider{log: provider.NewEventLog()}, entered: make(chan struct{}), release: make(chan struct{})}
	d := &control.Dispatcher{Providers: map[string]provider.Provider{"test": fake}}
	status := NewHostStatus(p.Relay)
	opts := Options{DisableTCP: true, DisableWebRTC: true, DisableQUIC: true}
	go RunHostObserved(ctx, p, opts, d, status)
	waitFor(t, func() bool { return status.Snapshot().State == "connected" })
	key := nativepath.Key([]byte("isolated"), "host")
	hold := make(chan struct{})
	listener, err := nativepath.Listen(ctx, "127.0.0.1:0", key, func(life context.Context, l *nativepath.Link) {
		select {
		case <-hold:
		case <-life.Done():
			return
		}
		o := opts
		o.ResolveKey = func(id string) (string, bool) { return p.Key, id == device }
		ServePaired(life, l, host, nil, o, d, status)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	direct := []nativepath.Endpoint{{Address: net.JoinHostPort("127.0.0.1", port), Pin: nativepath.Pin(key)}}
	c, err := ConnectRoutes(ctx, p, opts, device, relay.Routes{Grant: "fixture-without-service-signer"}, direct)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Path() != "Relay · WebSocket" {
		t.Fatal("first usable path", c.Path())
	}
	result := make(chan error, 1)
	go func() {
		result <- c.Call(ctx, control.Request{ID: "pending-command", Method: "send", Session: "original-thread", Provider: "test", Text: "continue"}, nil)
	}()
	select {
	case <-fake.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(hold)
	waitFor(t, func() bool { return c.Path() == "P2P · TCP" })
	if err := c.Call(ctx, control.Request{Method: "providers"}, nil); err != nil {
		t.Fatal(err)
	}
	close(fake.release)
	if err := <-result; err != nil || fake.sends.Load() != 1 {
		t.Fatalf("in-flight write after switch: %v, executions %d", err, fake.sends.Load())
	}
}
