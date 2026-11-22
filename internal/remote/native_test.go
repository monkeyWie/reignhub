package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/relay"
)

func TestNativeDirectQUIC(t *testing.T) {
	token := strings.Repeat("r", 32)
	room := relay.New(token)
	defer room.Close()
	room.RelayAllowed = func(string, string, string) bool { return false }
	// Reject all WS data sockets to verify both legs of the actual RPC.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p2p/relay" {
			http.Error(w, "disabled", 503)
			return
		}
		room.ServeHTTP(w, r)
	}))
	defer server.Close()
	p := Pairing{Relay: "ws" + strings.TrimPrefix(server.URL, "http"), Host: relay.ID(), Key: strings.Repeat("ab", 32), RelayToken: token}
	fake := &testProvider{log: provider.NewEventLog()}
	status := NewHostStatus(p.Relay)
	d := &control.Dispatcher{Providers: map[string]provider.Provider{"test": fake}, Network: status.Snapshot}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go RunHostObserved(ctx, p, Options{DisableTCP: true, DisableWebRTC: true}, d, status)
	waitFor(t, func() bool { return status.Snapshot().State == "connected" })
	client, err := Connect(ctx, p, Options{DisableTCP: true, DisableWebRTC: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.Path() != "P2P · QUIC" {
		t.Fatal(client.Path())
	}
	var netState control.NetworkStatus
	if err := client.Call(ctx, control.Request{Method: "network"}, &netState); err != nil || len(netState.Peers) != 1 || netState.Peers[0].Path != "P2P · QUIC" {
		t.Fatal(netState, err)
	}
	var sessions provider.SessionPage
	if err := client.Call(ctx, control.Request{Method: "sessions", Provider: "test"}, &sessions); err != nil || len(sessions.Sessions) != 1 {
		t.Fatal(err)
	}
	if err := client.Call(ctx, control.Request{Method: "send", Provider: "test", Session: "original-thread", Text: "continue"}, nil); err != nil || fake.sends.Load() != 1 {
		t.Fatal(fake.sends.Load(), err)
	}
}
