// Package nativepath carries bounded opaque records over TLS/TCP or two QUIC streams.
package nativepath

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"sync"
	"time"
)

const GrantLane byte = 2

const ALPN = "reign-native/2"

type Endpoint struct {
	Address    string `json:"address,omitempty"`
	UDPAddress string `json:"udpAddress,omitempty"`
	TCPAddress string `json:"tcpAddress,omitempty"`
	WSS        string `json:"wss,omitempty"`
	Pin        string `json:"pin"` // Ed25519 public key, base64url.
}

func (e Endpoint) AddressFor(network string) string {
	switch network {
	case "WSS":
		return e.WSS
	case "QUIC":
		if e.UDPAddress != "" {
			return e.UDPAddress
		}
	case "TCP":
		if e.TCPAddress != "" {
			return e.TCPAddress
		}
	default:
		return ""
	}
	return e.Address
}

// Derive separate TLS identities without using the service signing key in TLS.
func Key(seed []byte, label string) ed25519.PrivateKey {
	m := hmac.New(sha256.New, seed)
	m.Write([]byte("reign/native/tls/v1/" + label))
	return ed25519.NewKeyFromSeed(m.Sum(nil))
}
func Pin(key ed25519.PrivateKey) string {
	return base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}
func ServerTLS(key ed25519.PrivateKey) (*tls.Config, error) {
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS13, NextProtos: []string{ALPN}}, nil
}
func ClientTLS(pin string) *tls.Config {
	// The authenticated discovery channel names the exact public key. TLS
	// proves possession during the handshake without requiring a public CA.
	return &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{ALPN}, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 1 && cs.NegotiatedProtocol == ALPN {
			if key, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey); ok && base64.RawURLEncoding.EncodeToString(key) == pin {
				return nil
			}
		}
		return errors.New("native transport identity mismatch")
	}}
}

type stream interface {
	io.ReadWriter
	SetDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}
type Link struct {
	Network string
	done    chan struct{}
	lanes   []stream
	mu      [2]sync.Mutex
	once    sync.Once
	close   func()
}

func (l *Link) Done() <-chan struct{} { return l.done }
func (l *Link) Close()                { l.once.Do(func() { l.close(); close(l.done) }) }

func (l *Link) Send(ctx context.Context, lane byte, body []byte) error {
	if lane > GrantLane || len(body) == 0 || len(body) > 32768 {
		return errors.New("invalid native record")
	}
	idx := int(lane) % len(l.lanes)
	l.mu[idx].Lock()
	defer l.mu[idx].Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	s := l.lanes[idx]
	_ = s.SetWriteDeadline(deadline)
	frame := make([]byte, 3+len(body))
	frame[0] = lane
	binary.BigEndian.PutUint16(frame[1:], uint16(len(body)))
	copy(frame[3:], body)
	_, err := s.Write(frame)
	return err
}
func (l *Link) read(idx int) (byte, []byte, error) {
	var header [3]byte
	s := l.lanes[idx]
	if _, err := io.ReadFull(s, header[:]); err != nil {
		return 0, nil, err
	}
	size := int(binary.BigEndian.Uint16(header[1:]))
	lane := header[0]
	if size == 0 || size > 32768 || lane > GrantLane || int(lane)%len(l.lanes) != idx {
		return 0, nil, errors.New("invalid native record")
	}
	body := make([]byte, size)
	_, err := io.ReadFull(s, body)
	return lane, body, err
}

// Setup reads one bounded control record before the link carries payloads.
func (l *Link) Setup(ctx context.Context) ([]byte, error) {
	d, _ := ctx.Deadline()
	_ = l.lanes[0].SetDeadline(d)
	defer l.lanes[0].SetDeadline(time.Time{})
	lane, b, err := l.read(0)
	if lane != 0 {
		return nil, errors.New("invalid setup lane")
	}
	return b, err
}
func (l *Link) Receive(fn func(byte, []byte) error) {
	var workers sync.WaitGroup
	for idx := range l.lanes {
		workers.Go(func() {
			defer l.Close()
			for {
				lane, b, err := l.read(idx)
				if err != nil || fn(lane, b) != nil {
					return
				}
			}
		})
	}
	workers.Wait()
}
func TCPLink(c net.Conn) *Link {
	return &Link{done: make(chan struct{}), Network: "TCP", lanes: []stream{c}, close: func() { _ = c.Close() }}
}
