// Package device owns persistent device identity and sealed pairing envelopes.
package device

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type Identity struct {
	Info         ClientInfo `json:"info,omitempty"`
	ID           string     `json:"id"`
	ConnectionID string     `json:"connectionId"`
	Name         string     `json:"name"`
	Private      string     `json:"private"`
	Public       string     `json:"public"`
	Token        string     `json:"token"`
	Service      string     `json:"service"`
}

func NewIdentity(name string) (Identity, error) {
	k, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		return Identity{}, e
	}
	return Identity{Name: name, Private: hex.EncodeToString(k.Bytes()), Public: hex.EncodeToString(k.PublicKey().Bytes())}, nil
}
func Random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func Fingerprint(public string) string {
	h := sha256.Sum256([]byte(public))
	return hex.EncodeToString(h[:])
}

// Pairing envelopes use X25519 + HKDF-SHA256 + AES-256-GCM with a request-bound context.
// The service is trusted for initial public-key discovery; subsequent peers pin keys.
func box(private, public, context string) (cipher.AEAD, error) {
	key, e := sharedKey(private, public, context)
	if e != nil {
		return nil, e
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}

func sharedKey(private, public, context string) ([]byte, error) {
	raw, e := hex.DecodeString(private)
	if e != nil {
		return nil, e
	}
	k, e := ecdh.X25519().NewPrivateKey(raw)
	if e != nil {
		return nil, e
	}
	raw, e = hex.DecodeString(public)
	if e != nil {
		return nil, e
	}
	p, e := ecdh.X25519().NewPublicKey(raw)
	if e != nil {
		return nil, e
	}
	shared, e := k.ECDH(p)
	if e != nil {
		return nil, e
	}
	return derive(shared, context), nil
}
func Seal(private, public, context string, value any) (string, error) {
	a, e := box(private, public, context)
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(value)
	if e != nil {
		return "", e
	}
	n := make([]byte, a.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(a.Seal(n, n, b, []byte(context))), nil
}
func Open(private, public, context, packet string, out any) error {
	a, e := box(private, public, context)
	if e != nil {
		return e
	}
	b, e := base64.StdEncoding.DecodeString(packet)
	if e != nil || len(b) < a.NonceSize() {
		return errors.New("invalid pairing envelope")
	}
	b, e = a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], []byte(context))
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}

var fileMu sync.Mutex

func Save(path string, v any) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".state-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), path)
}
func Load(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
