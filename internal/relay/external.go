package relay

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/nativepath"
)

var wssOrigin = regexp.MustCompile(`^wss://([a-zA-Z0-9.-]+|\[[a-fA-F0-9:.]+\])(:[0-9]+)?/?$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var namespacePattern = regexp.MustCompile(`^(?:[a-f0-9]{32}){1,2}$`)
var b64 = base64.RawURLEncoding.Strict()

func authorizationKey(key ed25519.PrivateKey) []byte {
	m := hmac.New(sha256.New, key.Seed())
	m.Write([]byte("reign/relay/credential/v1"))
	return m.Sum(nil)
}

// Credential exports the public TLS pin and a domain-separated membership derivation key.
func Credential(key ed25519.PrivateKey) string {
	return "rh1." + nativepath.Pin(key) + "." + b64.EncodeToString(authorizationKey(key))
}

type RelayNode struct {
	nativepath.Endpoint
	secret []byte
}
type NodeConfig struct {
	Addresses  connectivity.TransportAddresses `json:"addresses"`
	Credential string                          `json:"credential"`
}

func ResolveNodes(nodes []NodeConfig) ([]RelayNode, error) {
	if len(nodes) > 32 {
		return nil, errors.New("at most 32 external relays are supported")
	}
	seen := make(map[string]bool)
	out := make([]RelayNode, 0, len(nodes))
	for _, node := range nodes {
		parts := strings.Split(node.Credential, ".")
		if len(parts) != 3 || parts[0] != "rh1" {
			return nil, errors.New("invalid external relay credential")
		}
		pin := parts[1]
		pub, err := b64.DecodeString(pin)
		secret, keyErr := b64.DecodeString(parts[2])
		if err != nil || keyErr != nil || len(pin) != 43 || len(parts[2]) != 43 || len(pub) != 32 || len(secret) != 32 || seen[pin] {
			return nil, errors.New("invalid or duplicate external relay credential")
		}
		if len(node.Addresses) > 32 {
			return nil, errors.New("at most 32 addresses are supported")
		}
		var native connectivity.TransportAddresses
		var wss string
		for _, address := range node.Addresses {
			if !strings.HasPrefix(address, "wss://") {
				native = append(native, address)
				continue
			}
			u, err := url.Parse(address)
			if err != nil || !wssOrigin.MatchString(address) || wss != "" {
				return nil, errors.New("expected one wss:// relay origin")
			}
			port := u.Port()
			if port == "" {
				port = "443"
			}
			host := strings.ToLower(u.Hostname())
			if _, err := (connectivity.TransportAddresses{net.JoinHostPort(host, port)}).Resolve(); err != nil {
				return nil, err
			}
			authority := strings.ToLower(u.Host)
			if u.Port() == "443" {
				authority = strings.TrimSuffix(authority, ":443")
			}
			wss = "wss://" + authority + "/p2p/node"
		}
		addrs, err := native.Resolve()
		if err != nil {
			return nil, err
		}
		if len(addrs.UDP) > 1 || len(addrs.TCP) > 1 || (len(addrs.UDP)+len(addrs.TCP) == 0 && wss == "") {
			return nil, errors.New("each external relay requires an address and supports one endpoint per transport")
		}
		endpoint := nativepath.Endpoint{Pin: pin, WSS: wss}
		if len(addrs.UDP) > 0 {
			endpoint.UDPAddress = addrs.UDP[0]
		}
		if len(addrs.TCP) > 0 {
			endpoint.TCPAddress = addrs.TCP[0]
		}
		if endpoint.UDPAddress == endpoint.TCPAddress {
			endpoint.Address = endpoint.UDPAddress
			endpoint.UDPAddress, endpoint.TCPAddress = "", ""
		}
		seen[pin] = true
		out = append(out, RelayNode{endpoint, secret})
	}
	return out, nil
}
