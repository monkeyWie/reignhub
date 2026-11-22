package host_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/clienthub"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/host"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/remote"
	"github.com/monkeyWie/reignhub/internal/testagent"
)

// Explicitly opt in to testing a deployed Worker. Both identities are isolated
// and the host exposes only a fake provider, never installed Agent sessions.
func TestDeployedWorkerNativeRelay(t *testing.T) {
	origin := os.Getenv("REIGNHUB_TEST_WORKER_ORIGIN")
	if origin == "" {
		t.Skip("set REIGNHUB_TEST_WORKER_ORIGIN to test a deployed Worker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServiceURL, cfg.DeviceName = origin, "Isolated relay test host"
	agent := testagent.New()
	text := strings.Repeat("隔离测试历史消息🙂 with repeated JSON content.\n", 20000)
	agent.Turns["existing"] = []provider.Turn{{ID: "history", Status: "completed", Items: []provider.Item{{ID: "message", Role: "assistant", Text: text}}}}
	d := &control.Dispatcher{Providers: map[string]provider.Provider{"codex-app": agent}}
	m := host.New(cfg, d, func(config.Config) remote.Options { return remote.Options{RelayOnly: true} })
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	for m.Network().State != "connected" {
		select {
		case <-ctx.Done():
			t.Fatalf("host: %+v", m.Network())
		case <-time.After(100 * time.Millisecond):
		}
	}
	info := invoke(t, m, "host.info", nil).(map[string]any)
	invoke(t, m, "host.password", map[string]any{"password": "isolated-fixture-password"})
	hub, err := clienthub.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	trace := connectivity.WithProgress(ctx, func(p connectivity.Progress) { t.Log(p.Stage, p.Message, p.Detail) })
	saved, err := hub.Connect(trace, origin, info["connectionId"].(string), info["public"].(string), "isolated-fixture-password", "Isolated relay test controller", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []control.Request{{Method: "providers"}, {Method: "sessions", Provider: "codex-app"}, {Method: "transcript", Provider: "codex-app", Session: "existing"}} {
		started := time.Now()
		result, err := hub.Call(ctx, saved.ID, req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		t.Log(req.Method, "response bytes:", len(raw), "roundtrip including cache:", time.Since(started))
		if req.Method == "transcript" {
			var transcript provider.Transcript
			if json.Unmarshal(raw, &transcript) != nil || len(transcript.Turns) != 1 || transcript.Turns[0].Items[0].Text != text {
				t.Fatal("compressed transcript changed its message content")
			}
		}
	}
	// Keep the real relay alive across several heartbeats and a lease renewal.
	// Short connect/read tests miss sockets closed by the first liveness probe.
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for i := 0; i < 8; i++ {
		select {
		case <-ctx.Done():
			t.Fatal("relay longevity:", ctx.Err())
		case <-tick.C:
		}
		probe, stop := context.WithTimeout(ctx, 10*time.Second)
		_, err := hub.Call(probe, saved.ID, control.Request{Method: "providers"})
		stop()
		if err != nil {
			t.Fatalf("relay after %ds: %v (host: %+v)", (i+1)*10, err, m.Network())
		}
		t.Logf("relay still usable after %ds", (i+1)*10)
	}

}
