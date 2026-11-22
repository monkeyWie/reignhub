package servercmd

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/monkeyWie/reignhub/internal/deviceservice"
	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/internal/relay"
)

// Default to an isolated Go control and standalone Go data plane. The same wire
// checks can target a locally configured Worker and external Go node.
func TestExternalNativeRelay(t *testing.T) {
	var standalone *Runtime
	origin := os.Getenv("REIGNHUB_TEST_EXTERNAL_CONTROL")
	if origin == "" {
		node := readTestConfig(t, `{"control":{"enable":false,"listen":"127.0.0.1:0"},"stun":{"listen":"127.0.0.1:0"},"relay":{"native":{"listen":"127.0.0.1:0"}}}`)
		rt, err := Start(node)
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		standalone = rt
		credential, err := deviceservice.ReadRelayCredential(node.State)
		if err != nil {
			t.Fatal(err)
		}
		control := readTestConfig(t, `{"relay":{"enable":false},"control":{"listen":"127.0.0.1:0","externalRelays":[{"addresses":["`+rt.native.Addr().String()+`"],"credential":"`+credential+`"}],"stunServers":["udp:udp.example:3478","tcp:tcp.example:3478"]},"stun":{"listen":"127.0.0.1:0"}}`)
		ctrl, err := Start(control)
		if err != nil {
			t.Fatal(err)
		}
		defer ctrl.Close()
		origin = "http://" + ctrl.HTTPAddr.String()
		policy := device.Network(t.Context(), origin)
		if len(policy.SelfSTUN) != 2 || !slices.Contains(policy.SelfSTUN, "stun:udp.example:3478") ||
			len(policy.TCPSTUN) != 2 || !slices.Contains(policy.TCPSTUN, "tcp.example:3478") {
			t.Fatal("missing local or external discovery endpoints", policy.SelfSTUN, policy.TCPSTUN)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	register := func(isHost bool) device.Identity {
		i, err := device.NewIdentity("external-relay-fixture")
		if err != nil {
			t.Fatal(err)
		}
		i, err = device.Register(ctx, origin, i, isHost)
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	host, client := register(true), register(false)
	id := relay.ID()
	post := func(path, token string, data any) {
		t.Helper()
		if err := device.Post(ctx, origin, token, path, data, nil); err != nil {
			t.Fatal(err)
		}
	}
	post("/p2p/pairings/request", client.Token, map[string]any{"host": host.ID, "id": id, "envelope": "fixture"})
	post("/p2p/pairings/respond", host.Token, map[string]any{"id": id, "allow": true, "reply": "fixture"})

	enroll := func(identity device.Identity) *relay.Enrollment {
		t.Helper()
		var routes relay.Routes
		err := device.Post(ctx, origin, identity.Token, "/p2p/routes", map[string]string{"host": host.ID}, &routes)
		if err != nil || routes.Node == nil || routes.Grant == "" {
			t.Fatal("missing enrollment", err)
		}
		return routes.Node
	}
	h, c := enroll(host), enroll(client)
	if h.Endpoint != c.Endpoint || h.Key == c.Key {
		t.Fatal("role credentials not isolated")
	}
	dial := func(network string) *nativepath.Link {
		l, err := nativepath.Dial(ctx, c.Endpoint, network)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(l.Close)
		return l
	}
	registration := dial("QUIC")
	if err := h.Open(ctx, registration, "register", relay.Invitation{Host: host.ID, Device: host.ID, Peer: relay.ID()}); err != nil {
		t.Fatal(err)
	}
	invite := relay.Invitation{Host: host.ID, Device: client.ID, Peer: relay.ID()}
	if standalone != nil {
		for _, invalid := range []string{"pin", "membership"} {
			t.Run("reject wrong WebSocket "+invalid, func(t *testing.T) {
				bad := *c
				if invalid == "pin" {
					bad.Pin = nativepath.Pin(nativepath.Key([]byte("different fixture node"), "relay"))
				} else {
					bad.Key = h.Key
				}
				check, stop := context.WithTimeout(t.Context(), 3*time.Second)
				defer stop()
				link, err := nativepath.DialWebSocket(check, "ws://"+standalone.HTTPAddr.String()+"/p2p/node")
				if err != nil {
					t.Fatal(err)
				}
				defer link.Close()
				if err := bad.Open(check, link, "connect", invite); err == nil || check.Err() != nil {
					t.Fatal("expected the node identity or membership check to reject the connection promptly", err)
				}
			})
		}
	}
	if err := c.Open(ctx, dial("TCP"), "accept", invite); err == nil {
		t.Fatal("controller credential admitted as host")
	}
	controller := dial("TCP")
	connected := make(chan error, 1)
	go func() { connected <- c.Open(ctx, controller, "connect", invite) }()
	raw, err := registration.Setup(ctx)
	var got relay.Invitation
	if err != nil || json.Unmarshal(raw, &got) != nil || got != invite {
		t.Fatal("missing invitation", err)
	}
	accept := dial("QUIC")
	if err := h.Open(ctx, accept, "accept", got); err != nil {
		t.Fatal(err)
	}
	if err := <-connected; err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]*nativepath.Link{{accept, controller}, {controller, accept}} {
		if err := pair[0].Send(ctx, 0, []byte("opaque fixture record")); err != nil {
			t.Fatal(err)
		}
		raw, err := pair[1].Setup(ctx)
		if err != nil || string(raw) != "opaque fixture record" {
			t.Fatal("mixed transport forwarding", err)
		}
	}
	if standalone != nil {
		standalone.directory.SetRevokedRelayDevices([]string{client.ID})
		check, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		if _, err := controller.Setup(check); err == nil || check.Err() != nil {
			t.Fatal("node did not close revoked controller")
		}
		if err := c.Open(check, dial("TCP"), "connect", invite); err == nil {
			t.Fatal("node accepted revoked membership")
		}
		if err := registration.Send(ctx, 0, []byte("ping")); err != nil {
			t.Fatal(err)
		}
		raw, err := registration.Setup(ctx)
		if err != nil || string(raw) != "pong" {
			t.Fatal("controller revocation closed shared host registration", err)
		}
	}
	post("/p2p/pairings/revoke", host.Token, map[string]string{"client": client.ID})
	if err := device.Post(ctx, origin, client.Token, "/p2p/routes", map[string]string{"host": host.ID}, nil); err == nil {
		t.Fatal("enrolled a revoked device")
	}
}
