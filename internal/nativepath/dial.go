//go:build !js

package nativepath

import (
	"context"
	"crypto/tls"
	"errors"
	"github.com/quic-go/quic-go"
	"io"
	"time"
)

func QUICConfig() *quic.Config {
	return &quic.Config{MaxIncomingStreams: 2, MaxIncomingUniStreams: -1, KeepAlivePeriod: 15 * time.Second, MaxIdleTimeout: 45 * time.Second, DisablePathMTUDiscovery: true}
}
func QUICLink(ctx context.Context, c *quic.Conn, accept bool) (*Link, error) {
	l := &Link{done: make(chan struct{}), Network: "QUIC", close: func() { _ = c.CloseWithError(0, "") }}
	d, _ := ctx.Deadline()
	for i := range 2 {
		var s *quic.Stream
		var err error
		if accept {
			s, err = c.AcceptStream(ctx)
		} else {
			s, err = c.OpenStreamSync(ctx)
		}
		if err != nil {
			l.Close()
			return nil, err
		}
		_ = s.SetDeadline(d)
		marker := []byte{byte(i)}
		if accept {
			_, err = io.ReadFull(s, marker)
			if marker[0] != byte(i) {
				err = errors.New("invalid QUIC lane")
			}
		} else {
			_, err = s.Write(marker)
		}
		if err != nil {
			l.Close()
			return nil, err
		}
		_ = s.SetDeadline(time.Time{})
		l.lanes = append(l.lanes, s)
	}
	return l, nil
}

func Dial(ctx context.Context, endpoint Endpoint, network string) (*Link, error) {
	address := endpoint.AddressFor(network)
	if address == "" {
		return nil, errors.New("native relay transport unavailable")
	}
	if network == "WSS" {
		return DialWebSocket(ctx, address)
	}
	cfg := ClientTLS(endpoint.Pin)
	if network == "QUIC" {
		c, err := quic.DialAddr(ctx, address, cfg, QUICConfig())
		if err != nil {
			return nil, err
		}
		return QUICLink(ctx, c, false)
	}
	d := tls.Dialer{Config: cfg}
	c, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	return TCPLink(c), nil
}
