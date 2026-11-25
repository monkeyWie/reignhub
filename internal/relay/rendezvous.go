package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/pkg/access"
)

type Invitation struct {
	Host   string `json:"host"`
	Device string `json:"device"` // controller
	Peer   string `json:"peer"`
}
type memberRequest struct {
	Invitation
	Challenge string `json:"challenge,omitempty"`
	Op        string `json:"op"`
	Namespace string `json:"namespace"`
	Issuer    string `json:"issuer,omitempty"`
	Grant     string `json:"grant,omitempty"`
}
type rendezvous struct {
	revoked    map[string]bool
	admitted   map[*nativepath.Link]Invitation
	mu         sync.Mutex
	hosts      map[string]*nativepath.Link
	registered chan struct{}
	waiting    map[string]*meeting
}
type meeting struct {
	invitation Invitation
	accept     chan admittedLeg
	done       chan struct{}
}

func sendObject(ctx context.Context, l *nativepath.Link, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return l.Send(ctx, 0, data)
}
func (e *Enrollment) Open(ctx context.Context, l *nativepath.Link, op string, invite Invitation) error {
	proof, err := e.answerChallenge(ctx, l, op, invite)
	if err != nil {
		return err
	}
	return confirmAdmission(ctx, l, proof)
}

func (e *Enrollment) answerChallenge(ctx context.Context, l *nativepath.Link, op string, invite Invitation) ([]byte, error) {
	req := memberRequest{Invitation: invite, Op: op, Namespace: e.Namespace, Issuer: e.Issuer, Grant: e.Grant}
	if l.Network == "WSS" {
		req.Challenge = ID()
	}
	if err := sendObject(ctx, l, req); err != nil {
		return nil, err
	}
	nonce, err := l.Setup(ctx)
	if err != nil {
		return nil, err
	}
	if l.Network == "WSS" {
		// The public HTTPS certificate may belong to a reverse proxy. Bind the
		// application-layer challenge to the node advertised during enrollment.
		pub, err := b64.DecodeString(e.Pin)
		if err != nil || len(pub) != ed25519.PublicKeySize || len(nonce) != 32+ed25519.SignatureSize || !ed25519.Verify(pub, nodeChallenge(req, nonce[:32]), nonce[32:]) {
			return nil, errors.New("relay node identity mismatch")
		}
		nonce = nonce[:32]
	}
	if !idPattern.Match(nonce) {
		return nil, errors.New("invalid node challenge")
	}
	key, err := hex.DecodeString(e.Key)
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid enrollment")
	}
	return signingMAC(key, "join", op, invite.Host, invite.Device, invite.Peer, string(nonce)), nil
}

func confirmAdmission(ctx context.Context, l *nativepath.Link, proof []byte) error {
	if err := l.Send(ctx, 0, proof); err != nil {
		return err
	}
	ack, err := l.Setup(ctx)
	if err != nil {
		return err
	}
	if string(ack) != "ready" {
		return errors.New("node did not admit connection")
	}
	return nil
}
func nodeChallenge(req memberRequest, nonce []byte) []byte {
	return bytes.Join([][]byte{[]byte("reign-node-v2"), []byte(req.Namespace), []byte(req.Op), []byte(req.Peer), []byte(req.Challenge), nonce}, []byte{0})
}
func (r *rendezvous) serve(ctx, setup context.Context, l *nativepath.Link, secret []byte, signer ed25519.PrivateKey, req memberRequest) {
	i := req.Invitation
	if !namespacePattern.MatchString(req.Namespace) || !idPattern.MatchString(i.Host) || !idPattern.MatchString(i.Device) || !idPattern.MatchString(i.Peer) {
		return
	}
	role, device := "host", i.Host
	switch req.Op {
	case "connect":
		role, device = "client", i.Device
	case "register":
		if i.Device != i.Host {
			return
		}
	case "accept":
	default:
		return
	}
	nonce := ID()
	challenge := []byte(nonce)
	if l.Network == "WSS" {
		if !idPattern.MatchString(req.Challenge) {
			return
		}
		challenge = append(challenge, ed25519.Sign(signer, nodeChallenge(req, challenge))...)
	}
	if l.Send(setup, 0, challenge) != nil {
		return
	}
	proof, err := l.Setup(setup)
	if err != nil || !hmac.Equal(proof, signingMAC(memberKey(secret, req.Namespace, req.Issuer, i.Host, device, role), "join", req.Op, i.Host, i.Device, i.Peer, nonce)) {
		return
	}
	r.mu.Lock()
	if r.revoked[i.Host] || r.revoked[i.Device] {
		r.mu.Unlock()
		return
	}
	if r.admitted == nil {
		r.admitted = make(map[*nativepath.Link]Invitation)
	}
	r.admitted[l] = i
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.admitted, l); r.mu.Unlock() }()
	leg := admittedLeg{link: l}
	var usage *relayUsage
	if req.Issuer != "" {
		pub, err := base64.RawURLEncoding.DecodeString(req.Issuer)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return
		}
		leg.lease = &nodeLease{key: pub, namespace: req.Namespace, host: i.Host, device: device, role: role, close: l.Close}
		if leg.lease.renew(req.Grant) != nil {
			return
		}
		defer leg.lease.stop()
		if req.Op == "connect" && leg.lease.usageURL != "" {
			usage = &relayUsage{http: usageHTTP, url: leg.lease.usageURL, signer: signer, invite: i, holder: ID()}
			defer usage.release(ctx)
		}
	}
	hostKey := req.Namespace + "/" + i.Host
	meetingKey := req.Namespace + "/" + i.Peer
	switch req.Op {
	case "register":
		if l.Send(setup, 0, []byte("ready")) != nil {
			return
		}
		r.mu.Lock()
		if r.hosts == nil {
			r.hosts = make(map[string]*nativepath.Link)
		}
		old := r.hosts[hostKey]
		r.hosts[hostKey] = l
		if r.registered != nil {
			close(r.registered)
			r.registered = nil
		}
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			if r.hosts[hostKey] == l {
				delete(r.hosts, hostKey)
			}
			r.mu.Unlock()
		}()
		if old != nil {
			old.Close()
		}
		leg.receive(func(lane byte, b []byte) error {
			if lane != 0 || string(b) != "ping" {
				return errors.New("invalid registration record")
			}
			return l.Send(ctx, 0, []byte("pong"))
		})
	case "connect":
		m := &meeting{invitation: i, accept: make(chan admittedLeg), done: make(chan struct{})}
		r.mu.Lock()
		host := r.hosts[hostKey]
		if r.waiting == nil {
			r.waiting = make(map[string]*meeting)
		}
		if r.waiting[meetingKey] != nil || len(r.waiting) >= 256 {
			r.mu.Unlock()
			return
		}
		r.waiting[meetingKey] = m
		r.mu.Unlock()
		defer func() { r.mu.Lock(); delete(r.waiting, meetingKey); r.mu.Unlock(); close(m.done) }()
		for host == nil {
			r.mu.Lock()
			host = r.hosts[hostKey]
			if r.registered == nil {
				r.registered = make(chan struct{})
			}
			registered := r.registered
			r.mu.Unlock()
			if host == nil {
				select {
				case <-setup.Done():
					return
				case <-registered:
				}
			}
		}
		if sendObject(setup, host, i) != nil {
			return
		}
		var other admittedLeg
		select {
		case other = <-m.accept:
		case <-setup.Done():
			return
		}
		defer other.link.Close()
		if l.Send(setup, 0, []byte("ready")) != nil {
			return
		}
		var wg sync.WaitGroup
		forward := func(src admittedLeg, dst *nativepath.Link) {
			defer dst.Close()
			src.receive(func(lane byte, b []byte) error {
				if len(b) == 1 && b[0] == 4 && src.link == leg.link {
					if usage != nil {
						usage.release(ctx)
					}
					return nil
				}
				if usage != nil {
					if err := usage.admit(ctx, b); err != nil {
						return err
					}
				}
				return dst.Send(ctx, lane, b)
			})
		}
		wg.Go(func() { forward(other, l) })
		forward(leg, other.link)
		wg.Wait()
	case "accept":
		r.mu.Lock()
		m := r.waiting[meetingKey]
		r.mu.Unlock()
		if m == nil || m.invitation != i {
			return
		}
		if l.Send(setup, 0, []byte("ready")) != nil {
			return
		}
		select {
		case m.accept <- leg:
		case <-setup.Done():
			return
		case <-m.done:
			return
		}
		select {
		case <-ctx.Done():
		case <-m.done:
		}
	}
}

// ReceiveInvitations runs on a link already admitted for registration.
func (e *Enrollment) ReceiveInvitations(ctx context.Context, l *nativepath.Link, notify func(Invitation), creds *RouteCredentials) error {
	creds.Renew(ctx, l, e)
	life, end := context.WithCancel(ctx)
	defer end()
	var mu sync.Mutex
	last := time.Now()
	go func() {
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-life.Done():
				return
			case <-tick.C:
				mu.Lock()
				stale := time.Since(last) > 65*time.Second
				mu.Unlock()
				if stale || l.Send(life, 0, []byte("ping")) != nil {
					l.Close()
					return
				}
			}
		}
	}()
	l.Receive(func(lane byte, b []byte) error {
		if lane != 0 {
			return errors.New("invalid invitation lane")
		}
		mu.Lock()
		last = time.Now()
		mu.Unlock()
		if string(b) == "pong" {
			return nil
		}
		var i Invitation
		if json.Unmarshal(b, &i) != nil || i.Host != e.Host || !idPattern.MatchString(i.Peer) || !idPattern.MatchString(i.Device) {
			return errors.New("invalid invitation")
		}
		notify(i)
		return nil
	})
	return errors.New("node registration disconnected")
}

// Each admitted leg checks its own operator lease before forwarding. Private
// memberships have no lease. A TLS peer cannot delegate its lease to another role.
type admittedLeg struct {
	link  *nativepath.Link
	lease *nodeLease
}

func (a admittedLeg) receive(forward func(byte, []byte) error) {
	a.link.Receive(func(lane byte, b []byte) error {
		if lane != nativepath.GrantLane {
			return forward(lane, b)
		}
		if a.lease == nil {
			return errors.New("unexpected service grant")
		}
		return a.lease.renew(string(b))
	})
}

type nodeLease struct {
	usageURL                      string
	key                           ed25519.PublicKey
	namespace, host, device, role string
	mu                            sync.Mutex
	until                         int64
	timer                         *time.Timer
	close                         func()
}

func (n *nodeLease) renew(ticket string) error {
	now := time.Now()
	g, err := access.Verify(n.key, ticket, now)
	if err != nil || g.Role != n.role || g.ExpiresAt <= now.Unix() || !g.Allows(n.namespace, n.host, n.device, "relay") || !g.Allows(n.namespace, n.host, n.device, "offline") {
		return errors.New("invalid operator grant")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.until != 0 && n.usageURL != g.RelayUsageURL {
		return errors.New("operator changed during session")
	}
	if n.timer != nil {
		if n.until <= now.Unix() {
			return errors.New("operator grant expired")
		}
		if g.ExpiresAt <= n.until {
			return nil
		}
		if !n.timer.Stop() {
			return errors.New("operator grant expired")
		}
	}
	n.usageURL = g.RelayUsageURL
	n.until = g.ExpiresAt
	n.timer = time.AfterFunc(time.Until(time.Unix(n.until, 0)), n.close)
	return nil
}
func (n *nodeLease) stop() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.timer != nil {
		n.timer.Stop()
	}
}

func (s *Server) SetRevokedDevices(ids []string) {
	blocked := make(map[string]bool, len(ids))
	for _, id := range ids {
		blocked[id] = true
	}
	r := &s.meet
	r.mu.Lock()
	r.revoked = blocked
	var closeLinks []*nativepath.Link
	for link, invite := range r.admitted {
		if blocked[invite.Host] || blocked[invite.Device] {
			closeLinks = append(closeLinks, link)
		}
	}
	r.mu.Unlock()
	for _, link := range closeLinks {
		link.Close()
	}
}
