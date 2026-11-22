package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/monkeyWie/reignhub/internal/nativepath"
)

// Enrollment is returned only to the authenticated owner of this role.
// The node checks a fresh proof of possession on every transport connection.
type Enrollment struct {
	nativepath.Endpoint
	Namespace string `json:"namespace"`
	Issuer    string `json:"issuer,omitempty"`
	Grant     string `json:"grant,omitempty"`
	Host      string `json:"host"`
	Device    string `json:"device"`
	Role      string `json:"role"`
	Key       string `json:"key"`
}
type Routes struct {
	Alternates []*Enrollment `json:"alternates,omitempty"`
	Grant      string        `json:"grant"`
	Expires    int64         `json:"expires,omitempty"`
	Node       *Enrollment   `json:"node,omitempty"`
}

func signingMAC(key []byte, parts ...string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.Join(parts, ".")))
	return m.Sum(nil)
}
func memberKey(secret []byte, namespace, issuer, host, device, role string) []byte {
	return signingMAC(secret, "reign-member-v2", namespace, issuer, host, device, role)
}
func (s *Server) Enroll(namespace, host, device, role, authority string, key ed25519.PrivateKey) *Enrollment {
	if !idPattern.MatchString(host) || !idPattern.MatchString(device) {
		return nil
	}
	count := len(s.ExternalRelays)
	if s.nativePort != "" {
		count++
	}
	if count == 0 {
		return nil
	}
	n, _ := strconv.ParseUint(host[:8], 16, 32)
	idx := int(n % uint64(count))
	var node RelayNode
	if idx < len(s.ExternalRelays) {
		node = s.ExternalRelays[idx]
	} else {
		address := s.nativePublic
		if address == "" {
			name, _, err := net.SplitHostPort(authority)
			if err != nil {
				name = authority
			}
			address = net.JoinHostPort(name, s.nativePort)
		}
		node = RelayNode{nativepath.Endpoint{Address: address, Pin: s.nativePin}, authorizationKey(key)}
	}
	return &Enrollment{Namespace: namespace, Endpoint: node.Endpoint, Host: host, Device: device, Role: role, Key: hex.EncodeToString(memberKey(node.secret, namespace, "", host, device, role))}
}

type RouteCredentials struct{ atomic.Pointer[Routes] }

func (e *Enrollment) SameNode(other *Enrollment) bool {
	if e == nil || other == nil {
		return e == other
	}
	return e.Endpoint == other.Endpoint && e.Namespace == other.Namespace && e.Issuer == other.Issuer && e.Key == other.Key && e.Role == other.Role && e.Host == other.Host && e.Device == other.Device
}

// Nodes returns a bounded candidate set with one membership per node identity.
func (r *Routes) Nodes() []*Enrollment {
	if r == nil {
		return nil
	}
	var out []*Enrollment
	seen := map[string]bool{}
	add := func(n *Enrollment) {
		if n != nil && !seen[n.Pin] && len(out) < 3 {
			seen[n.Pin] = true
			out = append(out, n)
		}
	}
	add(r.Node)
	for _, n := range r.Alternates {
		add(n)
	}
	return out
}
func (r *Routes) member(e *Enrollment) *Enrollment {
	for _, n := range r.Nodes() {
		if n.SameNode(e) {
			return n
		}
	}
	return nil
}
func (s *RouteCredentials) Current(e *Enrollment) *Enrollment {
	if s != nil {
		if n := s.Load().member(e); n != nil {
			return n
		}
	}
	return e
}
func (s *RouteCredentials) Renew(ctx context.Context, l *nativepath.Link, e *Enrollment) {
	if s == nil || e == nil || e.Issuer == "" {
		return
	}
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		last := e.Grant
		for {
			select {
			case <-ctx.Done():
				return
			case <-l.Done():
				return
			case <-tick.C:
			}
			n := s.Load().member(e)
			if n == nil {
				l.Close()
				return
			}
			if next := n.Grant; next != last {
				if err := l.Send(ctx, nativepath.GrantLane, []byte(next)); err != nil {
					l.Close()
					return
				}
				last = next
			}
		}
	}()
}
