//go:build !js

package relay

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/nativepath"
)

// The fast transport completes its handshake and receives the membership
// proof but never acknowledges it. The other transport serves the real node.
func TestRelayAdmissionFallsBackAfterProof(t *testing.T) {
	for _, stalled := range []string{"QUIC", "TCP"} {
		t.Run(stalled, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
			defer cancel()
			key := nativepath.Key([]byte("isolated relay"), "node")
			secret := authorizationKey(key)
			gates := map[string]chan struct{}{"register": make(chan struct{}), "accept": make(chan struct{})}
			var node rendezvous
			listener, err := nativepath.Listen(ctx, "127.0.0.1:0", key, func(life context.Context, l *nativepath.Link) {
				defer l.Close()
				raw, err := l.Setup(life)
				var req memberRequest
				if err != nil || json.Unmarshal(raw, &req) != nil {
					return
				}
				if gate := gates[req.Op]; gate != nil {
					if l.Network == stalled {
						if l.Send(life, 0, []byte(ID())) != nil {
							return
						}
						if proof, err := l.Setup(life); err != nil || len(proof) != 32 {
							return
						}
						close(gate)
						_, _ = l.Setup(life) // wait for the client to discard the stalled leg
						return
					}
					select {
					case <-gate:
					case <-life.Done():
						return
					}
				}
				node.serve(life, life, l, secret, key, req)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			host, device, namespace := ID(), ID(), ID()
			enroll := func(id, role string) Enrollment {
				return Enrollment{Endpoint: nativepath.Endpoint{Address: listener.Addr().String(), Pin: nativepath.Pin(key)}, Host: host, Device: id, Namespace: namespace, Role: role,
					Key: hex.EncodeToString(memberKey(secret, namespace, "", host, id, role))}
			}
			h, c := enroll(host, "host"), enroll(device, "client")
			setup, stop := context.WithCancel(ctx)
			reg, _, err := h.Dial(setup, "register", Invitation{Host: host, Device: host})
			stop()
			if err != nil {
				t.Fatal(err)
			}
			defer reg.Close()
			if reg.Network == stalled {
				t.Fatalf("registered over stalled %s", stalled)
			}
			invite := Invitation{Host: host, Device: device, Peer: ID()}
			controller, err := nativepath.Dial(ctx, c.Endpoint, "TCP")
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()
			connected := make(chan error, 1)
			go func() { connected <- c.Open(ctx, controller, "connect", invite) }()
			raw, err := reg.Setup(ctx)
			var got Invitation
			if err != nil || json.Unmarshal(raw, &got) != nil || got != invite {
				t.Fatal("missing invitation on surviving registration", err)
			}
			setup, stop = context.WithCancel(ctx)
			accept, peer, err := h.Dial(setup, "accept", got)
			stop()
			if err != nil {
				t.Fatal(err)
			}
			defer accept.Close()
			if peer != invite.Peer || accept.Network == stalled {
				t.Fatal("wrong accepted leg")
			}
			if err := <-connected; err != nil {
				t.Fatal(err)
			}
			for _, legs := range [][2]*nativepath.Link{{controller, accept}, {accept, controller}} {
				if err := legs[0].Send(ctx, 0, []byte("fixture")); err != nil {
					t.Fatal(err)
				}
				if got, err := legs[1].Setup(ctx); err != nil || string(got) != "fixture" {
					t.Fatal("relay did not forward after admission", err)
				}
			}
		})
	}
}

func TestCancelRelayAdmissionWithUnresponsiveNode(t *testing.T) {
	key := nativepath.Key([]byte("isolated unresponsive relay"), "node")
	entered := make(chan struct{}, 2)
	closed := make(chan struct{}, 2)
	listener, err := nativepath.Listen(t.Context(), "127.0.0.1:0", key, func(ctx context.Context, l *nativepath.Link) {
		defer l.Close()
		if _, err := l.Setup(ctx); err != nil {
			return
		}
		if l.Network == "QUIC" {
			if l.Send(ctx, 0, []byte(ID())) != nil {
				return
			}
			if _, err := l.Setup(ctx); err != nil {
				return
			}
		}
		entered <- struct{}{}
		_, _ = l.Setup(ctx)
		closed <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	member := Enrollment{Endpoint: nativepath.Endpoint{Address: listener.Addr().String(), Pin: nativepath.Pin(key)}, Key: hex.EncodeToString(make([]byte, 32))}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, err := member.Dial(ctx, "register", Invitation{})
		result <- err
	}()
	wait, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	for range 2 {
		select {
		case <-entered:
		case <-wait.Done():
			t.Fatal("transports did not both reach admission")
		}
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-wait.Done():
		t.Fatal("cancellation blocked")
	}
	for range 2 {
		select {
		case <-closed:
		case <-wait.Done():
			t.Fatal("abandoned transport still open")
		}
	}
}
