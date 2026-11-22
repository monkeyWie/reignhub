package host_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/clienthub"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/host"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/remote"
	"github.com/monkeyWie/reignhub/internal/testagent"
)

// Exercise the account API with a local Better Auth fixture and real direct RPC.
func TestAccountWorkerDirectAndRemoval(t *testing.T) {
	origin := os.Getenv("REIGNHUB_FIXTURE_ORIGIN")
	cookie := os.Getenv("REIGNHUB_FIXTURE_COOKIE")
	if origin == "" || cookie == "" {
		t.Skip("requires a local account Worker fixture")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Fatal("requires a loopback fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	api := func(path string, in any) {
		raw, _ := json.Marshal(in)
		req, err := http.NewRequestWithContext(ctx, "POST", origin+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServiceURL = origin
	d := &control.Dispatcher{Providers: map[string]provider.Provider{"fixture": testagent.New()}}
	m := host.New(cfg, d, func(config.Config) remote.Options { return remote.Options{} })
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	eventually(t, func() bool { return m.Network().State == "connected" })
	info := invoke(t, m, "host.info", nil).(map[string]any)
	hl := invoke(t, m, "host.account.start", nil).(map[string]any)
	api("/api/account/device-login/approve", map[string]any{"code": hl["code"]})
	hub, err := clienthub.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	cl, err := hub.Account(ctx, origin, "Account fixture", "start", "")
	if err != nil {
		t.Fatal(err)
	}
	api("/api/account/device-login/approve", map[string]any{"code": cl.(map[string]any)["code"]})
	saved, err := hub.Connect(ctx, origin, info["connectionId"].(string), info["public"].(string), "", "Account fixture", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccountBinding == "" {
		t.Fatal("manual approval was used")
	}
	if _, err = hub.Call(ctx, saved.ID, control.Request{Method: "providers", Fresh: true}); err != nil {
		t.Fatal(err)
	}
	if g := invoke(t, m, "host.info", nil).(map[string]any)["grants"].([]map[string]any); len(g) != 0 {
		t.Fatal("persisted an account link as a local pairing")
	}
	api("/api/account/devices/remove", map[string]any{"device": info["deviceId"]})
	eventually(t, func() bool { list, e := hub.List(); return e == nil && len(list) == 1 && list[0]["connected"] == false })
	if _, err = hub.Call(ctx, saved.ID, control.Request{Method: "providers", Fresh: true}); err == nil {
		t.Fatal("access survived removal")
	}
}
