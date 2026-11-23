package remote

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/monkeyWie/reignhub/internal/terminal"
)

type terminalAgent struct {
	*testProvider
	dir string
}

func (p terminalAgent) Transcript(_ context.Context, id, _ string) (provider.Transcript, error) {
	return provider.Transcript{Session: provider.Session{ID: id, CWD: p.dir}}, nil
}

type wireTerminal struct {
	mu    sync.Mutex
	text  string
	dirty bool
	done  chan struct{}
	once  sync.Once
	read  chan struct{}
}

func (e *wireTerminal) Resize(terminal.Size) {}
func (e *wireTerminal) Send(b []byte) {
	e.mu.Lock()
	e.text += string(b)
	e.dirty = true
	e.mu.Unlock()
}
func (e *wireTerminal) Frame(reset bool) []byte {
	if !reset && e.read != nil {
		select {
		case e.read <- struct{}{}:
		default:
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !reset && !e.dirty {
		return nil
	}
	e.dirty = false
	return []byte(e.text)
}
func (e *wireTerminal) Done() <-chan struct{} { return e.done }
func (e *wireTerminal) ExitCode() int         { return 0 }
func (e *wireTerminal) Close() error          { e.once.Do(func() { close(e.done) }); return nil }

func TestTerminalRemoteRecoveryAndRevocation(t *testing.T) {
	for _, opts := range []Options{{RelayOnly: true}, {DisableWebRTC: true, DisableQUIC: true}} {
		name := "relay"
		if !opts.RelayOnly {
			name = "tcp"
		}
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			token := strings.Repeat("fixture", 6)
			service := relay.New(token)
			defer service.Close()
			service.Authorize = func(credential, host, role string) (string, bool) { return "controller-fixture", credential == token }
			server := httptest.NewServer(service)
			defer server.Close()
			pair := Pairing{Relay: "ws" + strings.TrimPrefix(server.URL, "http"), Host: relay.ID(), Key: strings.Repeat("ab", 32), RelayToken: token}
			engine := &wireTerminal{done: make(chan struct{}), text: "remote prompt", dirty: true, read: make(chan struct{}, 1)}
			var creates atomic.Int32
			manager := terminal.New(ctx, func(context.Context, string, terminal.Size, string) (terminal.Engine, error) {
				creates.Add(1)
				return engine, nil
			})
			defer manager.Close()
			var allowed atomic.Bool
			allowed.Store(true)
			d := &control.Dispatcher{Terminals: manager, RemoteTerminalAllowed: allowed.Load, Providers: map[string]provider.Provider{"test": terminalAgent{&testProvider{}, t.TempDir()}}}
			monitor := NewHostStatus(pair.Relay)
			go serveHostObserved(ctx, pair, opts, d, monitor)
			waitFor(t, func() bool { return monitor.Snapshot().State == "connected" })
			connect := func() *Client {
				c, err := Connect(ctx, pair, opts)
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			c := connect()
			defer c.Close()
			if !opts.RelayOnly {
				waitFor(t, func() bool { return strings.Contains(c.Path(), "TCP") })
			}
			call := func(ctx context.Context, c *Client, method string, p terminal.Params) (terminal.Snapshot, error) {
				raw, _ := json.Marshal(p)
				var out terminal.Snapshot
				err := c.Call(ctx, control.Request{Method: method, Provider: "test", Session: "fixture", Params: raw}, &out)
				return out, err
			}
			p := terminal.Params{ID: strings.Repeat("cd", 32), Size: terminal.Size{Cols: 80, Rows: 24}}
			first, err := call(ctx, c, "terminal.open", p)
			if err != nil {
				t.Fatal(err)
			}
			p.Attachment, p.After = first.Attachment, first.Sequence
			readCtx, cancelRead := context.WithCancel(ctx)
			readDone := make(chan error, 1)
			go func() { _, err := call(readCtx, c, "terminal.read", p); readDone <- err }()
			probe, probeStop := context.WithTimeout(ctx, time.Second)
			if err := c.Call(probe, control.Request{Method: "network"}, nil); err != nil {
				t.Fatal("terminal blocked navigation", err)
			}
			if err := c.Call(probe, control.Request{Method: "providers"}, nil); err != nil {
				t.Fatal("terminal blocked ordinary request lane", err)
			}
			probeStop()
			cancelRead()
			select {
			case <-readDone:
			case <-time.After(time.Second):
				t.Fatal("read cancellation stuck")
			}
			p.Data, p.InputSeq = []byte(" unchanged across reconnect"), 1
			if _, err = call(ctx, c, "terminal.input", p); err != nil {
				t.Fatal(err)
			}
			_ = c.Close()
			select {
			case <-engine.Done():
				t.Fatal("disconnect killed shell")
			default:
			}
			next := connect()
			defer next.Close()
			attached, err := call(ctx, next, "terminal.attach", p)
			if err != nil || !strings.Contains(string(attached.Data), "unchanged") || creates.Load() != 1 {
				t.Fatal("resume failed", attached, err)
			}
			p.Attachment, p.After = attached.Attachment, attached.Sequence
			select {
			case <-engine.read:
			default:
			}
			pending := make(chan error, 1)
			go func() { _, err := call(ctx, next, "terminal.read", p); pending <- err }()
			select {
			case <-engine.read:
			case <-time.After(time.Second):
				t.Fatal("pending read did not reach host")
			}
			allowed.Store(false)
			engine.Send([]byte("must not reach revoked controller"))
			select {
			case err := <-pending:
				if err == nil {
					t.Fatal("revocation leaked an in-flight response")
				}
			case <-time.After(time.Second):
				t.Fatal("pending read did not finish")
			}
			if _, err := call(ctx, next, "terminal.read", p); err == nil {
				t.Fatal("revoked reader accepted")
			}
			if _, err := call(ctx, next, "terminal.input", p); err == nil {
				t.Fatal("revoked writer accepted")
			}
			if _, err := call(ctx, next, "terminal.closeStatus", p); err == nil {
				t.Fatal("revoked controller could check terminal close status")
			}
			allowed.Store(true)
			if _, err := call(ctx, next, "terminal.attach", p); err != nil {
				t.Fatal("permission recovery failed", err)
			}
			if _, err := call(ctx, next, "terminal.close", p); err != nil {
				t.Fatal("terminate failed", err)
			}
			if _, err := call(ctx, next, "terminal.closeStatus", p); err != nil {
				t.Fatal("close confirmation failed over the encrypted link", err)
			}
		})
	}
}
