package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monkeyWie/reignhub/internal/nativepath"
)

// The operator signs this callback address inside the admission grant. Private
// enrollments have no usage callback. Sign each report with the node identity.
type relayUsage struct {
	http       *http.Client
	url        string
	signer     ed25519.PrivateKey
	invite     Invitation
	holder     string
	mu         sync.Mutex
	validUntil time.Time
	window     time.Time
	probeBytes int
}

func (u *relayUsage) report(ctx context.Context, op string) error {
	endpoint, err := url.Parse(u.url)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.Path != "/api/relay/usage" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("invalid relay admission service")
	}
	at := time.Now().Unix()
	pin := nativepath.Pin(u.signer)
	message := strings.Join([]string{"reign-relay-use-v1", "https://" + endpoint.Host, op, pin, u.invite.Host, u.invite.Device, u.holder, strconv.FormatInt(at, 10)}, "\n")
	sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(u.signer, []byte(message)))
	b, err := json.Marshal(map[string]any{"host": u.invite.Host, "client": u.invite.Device, "node": pin, "holder": u.holder, "operation": op, "at": at, "signature": sig})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", u.url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := u.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("relay usage admission denied")
	}
	return nil
}

var usageHTTP = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (u *relayUsage) admit(ctx context.Context, b []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now()
	if now.Before(u.validUntil) {
		return nil
	}
	// During setup only bounded signaling and health checks can pass without a slot.
	if len(b) > 0 && (b[0] == 2 || b[0] == 3 || b[0] == '{') {
		if now.Sub(u.window) >= time.Minute {
			u.window = now
			u.probeBytes = 0
		}
		u.probeBytes += len(b)
		if u.probeBytes <= 65536 {
			return nil
		}
	}
	if err := u.report(ctx, "use"); err != nil {
		return err
	}
	u.validUntil = now.Add(45 * time.Second)
	return nil
}
func (u *relayUsage) release(ctx context.Context) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.validUntil.IsZero() {
		_ = u.report(ctx, "release")
		u.validUntil = time.Time{}
	}
}
