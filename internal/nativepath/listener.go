//go:build !js

package nativepath

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
)

type Listener struct {
	tcp    net.Listener
	udp    *quic.Listener
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

func (n *Listener) Close() {
	n.once.Do(func() {
		n.cancel()
		_ = n.tcp.Close()
		_ = n.udp.Close()
		n.wg.Wait()
	})
}
func (n *Listener) Addr() net.Addr { return n.tcp.Addr() }

// Listen accepts pinned TLS/TCP and QUIC on the same port.
func Listen(parent context.Context, address string, key ed25519.PrivateKey, serve func(context.Context, *Link)) (*Listener, error) {
	cfg, err := ServerTLS(key)
	if err != nil {
		return nil, err
	}
	host, requestedPort, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var tcp net.Listener
	var udp *quic.Listener
	var port string
	for attempt := 0; ; attempt++ {
		tcp, err = net.Listen("tcp", address)
		if err != nil {
			return nil, err
		}
		_, port, _ = net.SplitHostPort(tcp.Addr().String())
		udp, err = quic.ListenAddr(net.JoinHostPort(host, port), cfg, QUICConfig())
		if err == nil {
			break
		}
		_ = tcp.Close()
		// TCP's ephemeral allocator cannot see already-bound UDP ports.
		if requestedPort != "0" || attempt >= 7 || !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(parent)
	n := &Listener{tcp: tcp, udp: udp, cancel: cancel}
	slots := make(chan struct{}, 512)
	start := func(serve func(), reject func()) {
		select {
		case slots <- struct{}{}:
			n.wg.Go(func() {
				defer func() { <-slots }()
				serve()
			})
		default:
			reject()
		}
	}
	n.wg.Go(func() {
		for {
			c, err := tcp.Accept()
			if err != nil {
				return
			}
			start(func() {
				conn := tls.Server(c, cfg)
				defer c.Close()
				setup, cancel := context.WithTimeout(ctx, 8*time.Second)
				err := conn.HandshakeContext(setup)
				cancel()
				if err == nil && conn.ConnectionState().NegotiatedProtocol == ALPN {
					link := TCPLink(conn)
					stop := context.AfterFunc(ctx, link.Close)
					defer stop()
					serve(ctx, link)
				}
			}, func() { _ = c.Close() })
		}
	})
	n.wg.Go(func() {
		for {
			c, err := udp.Accept(ctx)
			if err != nil {
				return
			}
			start(func() {
				setup, cancel := context.WithTimeout(ctx, 8*time.Second)
				l, err := QUICLink(setup, c, true)
				cancel()
				if err == nil {
					defer l.Close()
					stop := context.AfterFunc(ctx, l.Close)
					defer stop()
					serve(ctx, l)
				}
			}, func() { _ = c.CloseWithError(1, "busy") })
		}
	})
	return n, nil
}
