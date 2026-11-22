//go:build js && wasm

package webapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/connectivity"
)

// Opt-in only: use a dedicated testsupport/workerfixture, never a user's host.
func deployedBrowserStability(t *testing.T) {
	origin, code := os.Getenv("REIGNHUB_TEST_WORKER_ORIGIN"), os.Getenv("REIGNHUB_TEST_FIXTURE_CODE")
	if origin == "" || code == "" {
		t.Skip("requires an isolated deployed Worker fixture")
	}
	a := New(&memoryStore{v: map[string][]byte{}})
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ctx = connectivity.WithProgress(ctx, func(p connectivity.Progress) {
		if p.State == "warning" || p.Stage == "connected" {
			t.Log(p.Message, p.Detail)
		}
	})
	v, err := a.Call(ctx, "client.connect", Object{"service": origin, "code": code, "name": "Isolated Web stability test", "remember": true}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	device := arr(v)[0]
	target := str(device, "id")
	statuses, err := a.Call(ctx, "client.presence", nil, "", nil)
	if err != nil || len(arr(statuses)) != 1 || arr(statuses)[0]["online"] != true {
		t.Fatalf("server directory presence via browser Fetch: %v, %v", statuses, err)
	}
	for round := 0; round < 60; round++ {
		if _, err = a.Call(ctx, "client.sync", Object{"provider": "codex-app"}, target, nil); err != nil {
			t.Fatalf("sync %d: %v", round, err)
		}
		if _, err = a.Call(ctx, "client.call", nil, target, Object{"method": "transcript", "provider": "codex-app", "session": "isolated"}); err != nil {
			t.Fatalf("transcript %d: %v", round, err)
		}
		if round%10 == 0 {
			t.Logf("sync %d connected: %s", round, a.client(target).Path())
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

func TestBrowserOfflineNodeReconnect(t *testing.T) {
	origin, code := os.Getenv("REIGNHUB_TEST_WORKER_ORIGIN"), os.Getenv("REIGNHUB_TEST_FIXTURE_CODE")
	if os.Getenv("REIGNHUB_TEST_OFFLINE_NODE") != "1" || !strings.HasPrefix(origin, "http://127.0.0.1:") || code == "" {
		t.Skip("requires the isolated loopback Worker and WSS node fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 55*time.Second)
	defer cancel()
	store := &Store{}
	first := New(store)
	defer first.Close()
	devices, err := first.Call(ctx, "client.connect", Object{"service": origin, "code": code, "remember": true, "relayOnly": true}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := str(arr(devices)[0], "id")
	for {
		list, err := first.list()
		if err != nil {
			t.Fatal(err)
		}
		item := arr(list)[0]
		if item["routes"] != nil || item["key"] != nil {
			t.Fatal("credential in UI result")
		}
		if item["independentRoutes"] == true {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("no browser enrollment")
		case <-time.After(50 * time.Millisecond):
		}
	}
	first.Close()
	r, err := http.Get(origin + "/fixture-offline")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", origin+"/healthz", nil)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			break
		}
		r.Body.Close()
		if r.StatusCode >= 500 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture still online")
		case <-time.After(50 * time.Millisecond):
		}
	}
	next := New(&Store{})
	defer next.Close()
	for _, only := range []bool{true, false} {
		if !only {
			saved, err := next.saved()
			if err != nil {
				t.Fatal(err)
			}
			saved[0]["relayOnly"] = false
			if err = store.SaveEnvironment(ctx, saved[0]); err != nil {
				t.Fatal(err)
			}
			next.client(id).Close()
		}
		_, err = next.Call(ctx, "client.reconnect", Object{"id": id}, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		c := next.client(id)
		want := "Relay · WSS"
		if !only {
			want = "P2P · WebRTC"
		}
		for c.Path() != want {
			select {
			case <-ctx.Done():
				t.Fatalf("wanted %s, got %s", want, c.Path())
			case <-time.After(20 * time.Millisecond):
			}
		}
		v, err := next.Call(ctx, "client.call", nil, id, Object{"method": "sessions", "provider": "fixture", "fresh": true})
		if err != nil || !strings.Contains(fmt.Sprint(v), "isolated") {
			t.Fatal("live read through "+want, err)
		}
		t.Log("Worker offline, fresh browser RPC via " + want)
	}
}
