package host_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/clienthub"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/deviceservice"
	"github.com/monkeyWie/reignhub/internal/host"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/remote"
	"github.com/monkeyWie/reignhub/internal/store"
	"github.com/monkeyWie/reignhub/internal/testagent"
)

func TestPairedReconnectAfterHostRestart(t *testing.T) {
	service, err := deviceservice.New(filepath.Join(t.TempDir(), "registry.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServiceURL = server.URL
	fake := testagent.New()
	db, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := &control.Dispatcher{Providers: map[string]provider.Provider{"demo": fake}, Execute: db.Execute}
	start := func() (*host.Manager, func()) {
		m := host.New(cfg, d, func(config.Config) remote.Options { return remote.Options{RelayOnly: true} })
		life, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); m.Run(life) }()
		eventually(t, func() bool { return m.Network().State == "connected" })
		return m, func() { stop(); <-done }
	}
	m, stop := start()
	info := invoke(t, m, "host.info", nil).(map[string]any)
	invoke(t, m, "host.password", map[string]any{"password": "fixture-password"})
	hub, err := clienthub.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	saved, err := hub.Connect(ctx, server.URL, info["connectionId"].(string), info["public"].(string), "fixture-password", "fixture controller", true)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	if _, err := hub.Call(ctx, saved.ID, control.Request{ID: "one-user-command", Method: "send", Provider: "demo", Session: "existing", Text: "test"}); err != nil {
		stop()
		t.Fatal(err)
	}
	stop()
	eventually(t, func() bool {
		devices, _ := hub.List()
		return len(devices) == 1 && devices[0]["connected"] == false && devices[0]["autoReconnect"] == true
	})
	m, stop = start()
	defer stop()
	restored, err := hub.Reconnect(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Key != saved.Key || restored.Host != saved.Host {
		t.Fatal("reconnect replaced pairing")
	}
	if _, err := hub.Call(ctx, saved.ID, control.Request{Method: "providers"}); err != nil {
		t.Fatal(err)
	}
	if fake.Sends != 1 {
		t.Fatalf("reconnect replayed user commands: %d", fake.Sends)
	}
	if pending := invoke(t, m, "host.info", nil).(map[string]any)["pending"].([]map[string]any); len(pending) != 0 {
		t.Fatal("reconnect requested fresh approval")
	}
	if err := hub.Disconnect(saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Reconnect(ctx, saved.ID); err == nil {
		t.Fatal("manual disconnect allowed automatic reconnect")
	}
	devices, _ := hub.List()
	if devices[0]["autoReconnect"] != false {
		t.Fatal("manual disconnect was not persisted")
	}
}
