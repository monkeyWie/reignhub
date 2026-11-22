// Package access defines signed connection grants without knowing users,
// subscriptions, OAuth providers, prices, or commercial database tables.
package access

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const Audience = "reign-connect-v1"

type Grant struct {
	RelayUsageURL string   `json:"relayUsageUrl,omitempty"`
	Audience      string   `json:"aud"`
	Namespace     string   `json:"namespace"`
	HostID        string   `json:"hostId"`
	DeviceID      string   `json:"deviceId"`
	Role          string   `json:"role"`
	Scopes        []string `json:"scopes"`
	Products      []string `json:"products"`
	IssuedAt      int64    `json:"iat"`
	ExpiresAt     int64    `json:"exp"`
	ID            string   `json:"jti"`
}

func Sign(key ed25519.PrivateKey, g Grant) (string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", errors.New("invalid signing key")
	}
	if err := g.validate(time.Now()); err != nil {
		return "", err
	}
	b, e := json.Marshal(g)
	if e != nil {
		return "", e
	}
	payload := base64.RawURLEncoding.EncodeToString(b)
	sig := ed25519.Sign(key, []byte("reign/grant/v1/"+payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
func Verify(key ed25519.PublicKey, token string, now time.Time) (Grant, error) {
	var g Grant
	if len(key) != ed25519.PublicKeySize || len(token) > 8192 {
		return g, errors.New("invalid grant")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return g, errors.New("invalid grant")
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil || !ed25519.Verify(key, []byte("reign/grant/v1/"+parts[0]), sig) {
		return g, errors.New("invalid grant signature")
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return g, e
	}
	if e = json.Unmarshal(b, &g); e != nil {
		return g, e
	}
	return g, g.validate(now)
}
func (g Grant) validate(now time.Time) error {
	durable := g.ExpiresAt == 0 && g.Allows(g.Namespace, g.HostID, g.DeviceID, "offline")
	if g.Audience != Audience || g.Namespace == "" || g.HostID == "" || g.DeviceID == "" || g.ID == "" || g.Role != "host" && g.Role != "client" || (!durable && g.ExpiresAt <= now.Unix()) || g.IssuedAt > now.Unix()+30 || (!durable && g.ExpiresAt <= g.IssuedAt) {
		return errors.New("invalid or expired grant")
	}
	return nil
}
func (g Grant) Allows(namespace, host, device, scope string) bool {
	if g.Namespace != namespace || g.HostID != host || g.DeviceID != device {
		return false
	}
	for _, s := range g.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// nil means unrestricted (self-hosted); an empty array permits no Agent.
func (g Grant) AllowsProduct(provider string) bool {
	if g.Products == nil {
		return true
	}
	product := provider
	if strings.HasPrefix(product, "codex") {
		product = "codex"
	}
	for _, v := range g.Products {
		if v == product {
			return true
		}
	}
	return false
}
