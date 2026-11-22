package relay

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/pkg/access"
)

func TestRelayTransportAddresses(t *testing.T) {
	const shared = "relay.example:26332"
	const tcp = "[2001:db8::1]:443"
	key := nativepath.Key([]byte("test"), "relay")
	pin := nativepath.Pin(key)
	credential := Credential(key)
	for _, tc := range []struct {
		name      string
		entries   []string
		quic, tcp string
	}{
		{"shared", []string{shared}, shared, shared},
		{"split", []string{"tcp:" + tcp, "udp:" + shared}, shared, tcp},
		{"tcp only", []string{"tcp:" + tcp}, "", tcp},
		{"udp only", []string{"udp:" + shared}, shared, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes, err := ResolveNodes([]NodeConfig{{Addresses: tc.entries, Credential: credential}})
			if err != nil {
				t.Fatal(err)
			}
			got := nodes[0]
			if got.Pin != pin || got.AddressFor("QUIC") != tc.quic || got.AddressFor("TCP") != tc.tcp {
				t.Fatalf("incorrect routes: %#v", got.Endpoint)
			}
			if tc.name == "shared" && got.Address != shared {
				t.Fatal("lost shared-address compatibility")
			}
		})
	}
	for _, addresses := range [][]string{{"wss://Relay.Example:443/"}, {shared, "wss://relay.example"}} {
		nodes, err := ResolveNodes([]NodeConfig{{Addresses: addresses, Credential: credential}})
		if err != nil || nodes[0].WSS != "wss://relay.example/p2p/node" {
			t.Fatal("incorrect browser relay", err)
		}
	}
	for _, entries := range [][]string{nil, {"wss://user@relay.example"}, {"wss://relay.example/?x=1"}, {"wss://relay.example:65536"}, {"wss://relay.example/a"}, {"wss://a", "wss://b"}, {"tcp:" + tcp, shared}, {"udp://relay.example:443"}, {"udp:[invalid]:443"}, {"tcp:relay.example:0"}} {
		if _, err := ResolveNodes([]NodeConfig{{Addresses: entries, Credential: credential}}); err == nil {
			t.Errorf("accepted invalid relay addresses %v", entries)
		}
	}
}

func TestOperatorLeaseChecksDeviceRoleAndExpiry(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	n := nodeLease{key: pub, namespace: ID(), host: ID(), device: ID(), role: "client", close: func() {}}
	defer n.stop()
	g := access.Grant{Namespace: n.namespace, HostID: n.host, DeviceID: n.device, Role: n.role, ID: ID(), Audience: access.Audience, IssuedAt: now.Unix(), ExpiresAt: now.Unix() + 180, Scopes: []string{"connect", "offline", "relay"}}
	check := func(grant access.Grant, valid bool) {
		t.Helper()
		ticket, err := access.Sign(key, grant)
		if err != nil {
			t.Fatal(err)
		}
		if (n.renew(ticket) == nil) != valid {
			t.Fatal("incorrect lease admission", grant.Role, grant.Scopes)
		}
	}
	check(g, true)
	g.Role = "host"
	check(g, false)
	g.Role = n.role
	g.ExpiresAt = 0
	check(g, false)
	g.ExpiresAt = now.Unix() + 180
	g.DeviceID = ID()
	check(g, false)
	g.DeviceID = n.device
	g.Scopes = []string{"connect", "offline"}
	check(g, false)
}
