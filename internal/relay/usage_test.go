package relay

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManagedForwardingUsesSignedAdmission(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	var uses, releases atomic.Int32
	var deny atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report struct {
			Host, Client, Node, Holder, Operation, Signature string
			At                                               int64
		}
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(report.Signature)
		data := strings.Join([]string{"reign-relay-use-v1", "https://" + r.Host, report.Operation, report.Node, report.Host, report.Client, report.Holder, strconv.FormatInt(report.At, 10)}, "\n")
		if !ed25519.Verify(pub, []byte(data), sig) {
			t.Error("invalid node signature")
			w.WriteHeader(403)
			return
		}
		if report.Operation == "release" {
			releases.Add(1)
			return
		}
		uses.Add(1)
		if deny.Load() {
			w.WriteHeader(403)
		}
	}))
	defer server.Close()
	u := relayUsage{url: server.URL + "/api/relay/usage", http: server.Client(), signer: key, holder: ID(), invite: Invitation{Host: ID(), Device: ID()}}
	for _, record := range [][]byte{{2, 1}, {3, 1}, {1, 1}, {1, 2}} {
		if err := u.admit(t.Context(), record); err != nil {
			t.Fatal(err)
		}
	}
	if uses.Load() != 1 {
		t.Fatal("expected one admission", uses.Load())
	}
	u.release(t.Context())
	if releases.Load() != 1 {
		t.Fatal("slot was not released")
	}
	deny.Store(true)
	if err := u.admit(t.Context(), []byte{1, 3}); err == nil {
		t.Fatal("forwarding after quota denial")
	}
}
