// This fixture never loads user config or starts a real Agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/monkeyWie/reignhub/internal/remote"
)

type fixture struct{ provider.Provider }

func (fixture) Status(context.Context) provider.Status {
	return provider.Status{ID: "fixture", State: "ready"}
}
func (fixture) Sessions(context.Context, string) (provider.SessionPage, error) {
	return provider.SessionPage{Sessions: []provider.Session{{ID: "nat-fixture", Preview: strings.Repeat("x", 1<<20)}}}, nil
}

func main() {
	mode := flag.String("mode", "client", "server, host or client")
	address := flag.String("server", "172.30.40.1", "test service IP")
	expect := flag.String("expect", "tcp", "expected path: tcp or relay")
	flag.Parse()
	if *mode == "server" {
		s, err := connectivity.ListenTCPSTUN(":26331")
		if err != nil {
			log.Fatal(err)
		}
		defer s.Close()
		log.Fatal(http.ListenAndServe(":26330", relay.New(strings.Repeat("r", 32))))
	}
	p := remote.Pairing{Relay: "ws://" + *address + ":26330", Host: strings.Repeat("h", 32), Key: strings.Repeat("ab", 32), RelayToken: strings.Repeat("r", 32)}
	opts := remote.Options{DisableWebRTC: true, TCPSTUN: []string{*address + ":26331"}}
	opts.TCPTrace = log.Printf
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if *mode == "host" {
		monitor := remote.NewHostStatus(p.Relay)
		d := &control.Dispatcher{Providers: map[string]provider.Provider{"fixture": fixture{}}, Network: monitor.Snapshot}
		remote.RunHostObserved(ctx, p, opts, d, monitor)
		return
	}
	var c *remote.Client
	for ctx.Err() == nil {
		attempt, stop := context.WithTimeout(ctx, time.Second)
		client, err := remote.Connect(attempt, p, opts)
		stop()
		if err == nil {
			c = client
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if c == nil {
		log.Fatal("cannot connect to test host")
	}
	defer c.Close()
	want := "P2P · TCP"
	if *expect == "relay" {
		want = "Relay · WebSocket"
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			if c.Path() != want {
				log.Fatalf("blocked TCP unexpectedly connected: %s", c.Path())
			}
			time.Sleep(50 * time.Millisecond)
		}
	} else {
		deadline := time.Now().Add(20 * time.Second)
		for c.Path() != want && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if c.Path() != want {
		log.Fatalf("unexpected path: %s (want %s)", c.Path(), want)
	}
	var page provider.SessionPage
	if err := c.Call(ctx, control.Request{Method: "sessions", Provider: "fixture"}, &page); err != nil {
		log.Fatal(err)
	}
	if len(page.Sessions) != 1 || len(page.Sessions[0].Preview) != 1<<20 {
		log.Fatal("TCP data corrupted")
	}
	var network control.NetworkStatus
	if err := c.Call(ctx, control.Request{Method: "network"}, &network); err != nil {
		log.Fatal(err)
	}
	matched := false
	for _, peer := range network.Peers {
		if peer.Path == want {
			matched = true
		}
	}
	if !matched {
		log.Fatalf("host did not select same channel: %+v", network)
	}
	fmt.Printf("TCP_NAT_OK: path=%s, 1 MiB received, WebRTC disabled\n", want)
}
