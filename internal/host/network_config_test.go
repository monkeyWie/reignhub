package host_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/clienthub"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/deviceservice"
	"github.com/monkeyWie/reignhub/internal/host"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/remote"
	"github.com/monkeyWie/reignhub/internal/testagent"
)

func TestHostRetriesMissingTrustConfigBeforeGoingOnline(t *testing.T) {
	service, err := deviceservice.New(filepath.Join(t.TempDir(), "service.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	var mode, hostDials atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p2p/config" {
			switch mode.Load() {
			case 0:
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			case 1:
				_ = json.NewEncoder(w).Encode(map[string]any{"serviceId": service.ServiceID, "grantPublicKey": ""})
				return
			}
		}
		if r.URL.Path == "/p2p/signal" && r.URL.Query().Get("role") == "host" {
			hostDials.Add(1)
		}
		service.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServiceURL = server.URL
	agent := testagent.New()
	m := host.New(cfg, &control.Dispatcher{Providers: map[string]provider.Provider{"demo": agent}}, func(config.Config) remote.Options { return remote.Options{RelayOnly: true} })
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	eventually(t, func() bool { n := m.Network(); return n.State == "error" && strings.Contains(n.Detail, "503") })
	info := invoke(t, m, "host.info", nil).(map[string]any)
	originalID := info["deviceId"]
	if originalID == "" || hostDials.Load() != 0 {
		t.Fatal("host went online without trust identity")
	}
	mode.Store(1)
	eventually(t, func() bool { n := m.Network(); return n.State == "error" && strings.Contains(n.Detail, "授权公钥") })
	if hostDials.Load() != 0 {
		t.Fatal("host advertised an unusable authenticated connection")
	}
	mode.Store(2)
	eventually(t, func() bool { return m.Network().State == "connected" })
	info = invoke(t, m, "host.info", nil).(map[string]any)
	if info["deviceId"] != originalID {
		t.Fatal("config retry replaced the registered device")
	}
	invoke(t, m, "host.password", map[string]any{"password": "fixture-password"})
	hub, err := clienthub.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	saved, err := hub.Connect(ctx, server.URL, info["connectionId"].(string), info["public"].(string), "fixture-password", "config recovery controller", true)
	if err != nil {
		t.Fatalf("signed device verification failed after config recovery: %v", err)
	}
	if _, err := hub.Call(ctx, saved.ID, control.Request{Method: "providers"}); err != nil {
		t.Fatal(err)
	}
}
