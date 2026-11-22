package host_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/clienthub"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/deviceservice"
	"github.com/monkeyWie/reignhub/internal/host"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/monkeyWie/reignhub/internal/remote"
	"github.com/monkeyWie/reignhub/internal/testagent"
)

// Exercise the public pairing and reconnect flow with fresh processes' state.
// Stopping the HTTP listener prevents presence, signaling and WebSocket relay.
func TestPairedRestartWithoutControl(t *testing.T) {
	for _, tc := range []struct {
		name, path   string
		relay, stale bool
	}{{"cached-address", "P2P · ", false, false}, {"relay-without-udp", "Relay · TCP", true, false}, {"rediscover-via-node", "P2P · ", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			service, err := deviceservice.New(filepath.Join(t.TempDir(), "control.db"), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, key, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			address := "127.0.0.1:0"
			startNode := func() func() {
				node := relay.New("")
				listener, err := node.ListenNative(address, "", key)
				if err != nil {
					t.Fatal(err)
				}
				address = listener.Addr().String()
				stop := sync.OnceFunc(func() { node.Close(); listener.Close() })
				t.Cleanup(stop)
				return stop
			}
			var stopNode func()
			var dropped atomic.Int32
			if tc.relay || tc.stale {
				stopNode = startNode()
				// The UDP socket never responds. TCP to the same node stays usable.
				sink, err := net.ListenPacket("udp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer sink.Close()
				go func() {
					var b [2048]byte
					for {
						if _, _, err := sink.ReadFrom(b[:]); err != nil {
							return
						}
						dropped.Add(1)
					}
				}()
				nodes, err := relay.ResolveNodes([]relay.NodeConfig{{Addresses: []string{"tcp:" + address, "udp:" + sink.LocalAddr().String()}, Credential: relay.Credential(key)}})
				if err != nil {
					t.Fatal(err)
				}
				service.ConfigureNative(nodes, false)
			}
			server := httptest.NewServer(service)
			stopControl := sync.OnceFunc(func() { _ = service.Close(); server.Close() })
			defer stopControl()
			cfg, err := config.Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cfg.ServiceURL = server.URL
			startHost := func() (*host.Manager, func()) {
				life, end := context.WithCancel(ctx)
				fake := testagent.New()
				d := &control.Dispatcher{Providers: map[string]provider.Provider{"demo": fake}}
				m := host.New(cfg, d, func(config.Config) remote.Options { return remote.Options{RelayOnly: tc.relay} })
				done := make(chan struct{})
				go func() { defer close(done); m.Run(life) }()
				stop := sync.OnceFunc(func() { end(); <-done })
				t.Cleanup(stop)
				return m, stop
			}
			m, stopHost := startHost()
			eventually(t, func() bool { return m.Network().State == "connected" })
			info := invoke(t, m, "host.info", nil).(map[string]any)
			invoke(t, m, "host.password", map[string]any{"password": "fixture-password"})
			dir := t.TempDir()
			openHub := func() *clienthub.Hub {
				h, e := clienthub.New(dir)
				if e != nil {
					t.Fatal(e)
				}
				return h
			}
			h := openHub()
			saved, err := h.Connect(ctx, server.URL, info["connectionId"].(string), info["public"].(string), "fixture-password", "isolated controller", true, tc.relay)
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool {
				var stored clienthub.Saved
				return h.DB.LoadEnvironment(saved.ID, &stored) == nil && stored.Routes.Grant != "" && (!(tc.relay || tc.stale) || stored.Routes.Node != nil)
			})
			grants := invoke(t, m, "host.info", nil).(map[string]any)["grants"].([]map[string]any)
			controller := grants[0]["client"].(string)
			if err = h.Close(); err != nil {
				t.Fatal(err)
			}
			stopHost()
			stopControl()
			if tc.relay || tc.stale {
				// Restart the node using the same persistent signing identity and address.
				stopNode()
				startNode()
			}
			m, _ = startHost()
			h = openHub()
			defer func() { _ = h.Close() }()
			if tc.stale {
				var stale clienthub.Saved
				if err := h.DB.LoadEnvironment(saved.ID, &stale); err != nil {
					t.Fatal(err)
				}
				stale.Direct = nil
				stale.Networks = nil
				if err := h.DB.SaveEnvironment(saved.ID, stale); err != nil {
					t.Fatal(err)
				}
			}
			// Wait for local identity restore, as the application does on startup.
			eventually(t, func() bool { return invoke(t, m, "host.info", nil).(map[string]any)["deviceId"] == info["deviceId"] })
			started := time.Now()
			if _, err = h.Reconnect(ctx, saved.ID); err != nil {
				t.Fatal("offline restart", err)
			}
			t.Log("first usable path after host identity restore", time.Since(started))
			if tc.relay {
				var durations []time.Duration
				for range 12 {
					if err = h.Close(); err != nil {
						t.Fatal(err)
					}
					h = openHub()
					started := time.Now()
					if _, err = h.Reconnect(ctx, saved.ID); err != nil {
						t.Fatal(err)
					}
					if _, err = h.Call(ctx, saved.ID, control.Request{Method: "network", Fresh: true}); err != nil {
						t.Fatal(err)
					}
					durations = append(durations, time.Since(started))
				}
				if dropped.Load() == 0 {
					t.Fatal("UDP did not participate")
				}
				slices.Sort(durations)
				t.Logf("offline TCP reconnect plus network RPC, 12 samples: p50=%s p95=%s", durations[5], durations[11])
			}
			if _, err = h.Call(ctx, saved.ID, control.Request{Method: "sessions", Provider: "demo", Fresh: true}); err != nil {
				t.Fatal(err)
			}
			if tc.stale {
				eventually(t, func() bool {
					rows, err := h.List()
					return err == nil && len(rows) == 1 && strings.HasPrefix(rows[0]["path"].(string), "P2P · ")
				})
			}
			t.Log("preferred path verified", time.Since(started))
			rows, err := h.List()
			if err != nil || len(rows) != 1 || !strings.HasPrefix(rows[0]["path"].(string), tc.path) {
				t.Fatalf("unexpected selected path (want %s)", tc.path)
			}
			raw, _ := json.Marshal(map[string]string{"client": controller})
			_, _ = m.Local(ctx, control.Request{Method: "host.revoke", Params: raw})
			if _, err = h.Call(ctx, saved.ID, control.Request{Method: "sessions", Provider: "demo", Fresh: true}); err == nil {
				t.Fatal("revoked device retained access")
			}
			t.Log("revocation verified", time.Since(started))
			retry, stop := context.WithTimeout(ctx, 1200*time.Millisecond)
			defer stop()
			if _, err = h.Reconnect(retry, saved.ID); err == nil {
				t.Fatal("revoked device reconnected")
			}
		})
	}
}
