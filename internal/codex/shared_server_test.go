package codex

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/events"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/rpc"
	"github.com/monkeyWie/reignhub/internal/system"
)

func TestSharedCodexServerWithBundledCLI(t *testing.T) {
	cli := os.Getenv("REIGNHUB_TEST_SHARED_CODEX_CLI")
	if cli == "" {
		t.Skip("provide a compatible bundled Codex executable")
	}
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CODEX_API_KEY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	server, err := startSharedServer(ctx, cli, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.close)
	if server.connected() {
		t.Fatal("Hub initialization counted as desktop readiness")
	}
	desktop := &sharedDesktop{path: "/desktop-fixture"}
	server.desktop.Store(desktop)
	rejected := func(url, origin string, status int) {
		t.Helper()
		header := http.Header{}
		if origin != "" {
			header.Set("Origin", origin)
		}
		conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
		if conn != nil {
			conn.CloseNow()
		}
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if err == nil || response == nil || response.StatusCode != status {
			t.Fatal("unexpected WebSocket admission result", err)
		}
	}
	rejected(server.upstream, "", http.StatusUnauthorized)
	rejected(server.address+"/wrong-path", "", http.StatusNotFound)
	rejected(server.address+server.hubPath, "", http.StatusNotFound)
	rejected(server.address+desktop.path, "http://localhost", http.StatusNotFound)

	client, err := rpc.Dial(ctx, server.address+desktop.path, nil)
	if err != nil {
		t.Fatal("desktop fixture did not initialize")
	}
	t.Cleanup(func() { _ = client.Close() })
	waitDesktop(t, ctx, server, true)
	var created struct {
		Thread struct{ ID string }
	}
	if err := client.Call(ctx, "thread/start", map[string]any{
		"cwd": filepath.Clean(dir), "ephemeral": true,
	}, &created); err != nil || created.Thread.ID == "" {
		t.Fatal("desktop could not create an isolated test thread", err)
	}
	var read struct {
		Thread struct{ ID string }
	}
	err = server.client.Call(ctx, "thread/read", map[string]any{"threadId": created.Thread.ID}, &read)
	if err != nil || read.Thread.ID != created.Thread.ID {
		t.Fatal("Hub and desktop were not connected to the same server", err)
	}
	// A second connection can address the live, empty durable thread even
	// though Codex refuses to resume it until a first user message exists.
	var fresh struct{ Thread thread }
	if err := client.Call(ctx, "thread/start", map[string]any{"cwd": dir}, &fresh); err != nil {
		t.Fatal(err)
	}
	p := New(config.Config{}, "", events.New())
	p.client, p.shared = server.client, &sharedConnection{}
	got, err := p.threadClient(ctx, fresh.Thread.ID)
	if err != nil || got != server.client || p.isCreatedThread(fresh.Thread.ID) {
		t.Fatalf("lost the empty shared thread or claimed a subscription: %v", err)
	}
	history, err := p.Transcript(ctx, fresh.Thread.ID, "")
	if err != nil || history.Session.ID != fresh.Thread.ID || len(history.Turns) != 0 {
		t.Fatalf("empty shared transcript: %v", err)
	}
	_ = client.Close()
	waitDesktop(t, ctx, server, false)
	server.desktop.Store(&sharedDesktop{path: "/new-desktop-fixture"})
	rejected(server.address+desktop.path, "", http.StatusNotFound)

	// The installed desktop app treats the initialize response as its
	// handshake and proceeds with requests without an initialized notification.
	replacement, _, err := websocket.Dial(ctx, server.address+"/new-desktop-fixture", nil)
	if err != nil {
		t.Fatal("replacement desktop could not connect")
	}
	defer replacement.CloseNow()
	readResponse := func(id string) rpc.Message {
		t.Helper()
		for {
			_, frame, err := replacement.Read(ctx)
			if err != nil {
				t.Fatal("replacement desktop could not read an RPC response", err)
			}
			var response rpc.Message
			if err := json.Unmarshal(frame, &response); err != nil {
				t.Fatal(err)
			}
			if string(response.ID) == id {
				if response.Error != nil || len(response.Result) == 0 {
					t.Fatalf("replacement desktop request %s failed: %v", id, response.Error)
				}
				return response
			}
		}
	}
	if err := replacement.Write(ctx, websocket.MessageText, []byte(`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"fixture","version":"1.0.0"}}}`)); err != nil {
		t.Fatal(err)
	}
	readResponse("1")
	if err := replacement.Write(ctx, websocket.MessageText, []byte(`{"id":2,"method":"thread/read","params":{"threadId":"`+created.Thread.ID+`"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readResponse("2").Result, &read); err != nil || read.Thread.ID != created.Thread.ID {
		t.Fatal("replacement desktop lost the shared thread", err)
	}
	waitDesktop(t, ctx, server, true)
	_ = replacement.CloseNow()
	waitDesktop(t, ctx, server, false)
}

func waitDesktop(t *testing.T, ctx context.Context, s *sharedServer, ready bool) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for s.connected() != ready {
		select {
		case <-ctx.Done():
			t.Fatal("desktop readiness did not change", ctx.Err())
		case <-tick.C:
		}
	}
}

func TestSharedCodexRejectsUncertainWriteRetry(t *testing.T) {
	err := sharedWriteError("turn/start", errors.New("closed"))
	var unknown interface{ OutcomeUnknown() bool }
	if !errors.As(err, &unknown) || !unknown.OutcomeUnknown() || strings.Contains(err.Error(), "localhost") {
		t.Fatal("transport failure should require outcome reconciliation", err)
	}
	rpcErr := &rpc.Error{Code: -32602, Message: "invalid params"}
	if sharedWriteError("turn/start", rpcErr) != rpcErr {
		t.Fatal("server rejection changed")
	}
}

func TestSharedRecoveryProbeRequiresDesktopAndMatchingSession(t *testing.T) {
	ctx := context.Background()
	var ready atomic.Bool
	status := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(desktopState{Ready: ready.Load()})
	}))
	defer status.Close()
	shared := &sharedConnection{sharedEndpoint: sharedEndpoint{Control: strings.TrimPrefix(status.URL, "https://")}, http: status.Client()}
	p := &Provider{shared: shared}
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		if m.Method != "thread/read" {
			t.Errorf("unexpected operation in probe: %s", m.Method)
		}
		return json.RawMessage(`{"thread":{"id":"held","status":{"type":"notLoaded"}}}`), nil
	})
	if _, err := p.Probe(ctx, "held", "", 0); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("detached desktop counted as recovered: %v", err)
	}
	ready.Store(true)
	if _, err := p.Probe(ctx, "other", "", 0); err == nil {
		t.Fatal("unrelated thread counted as recovered")
	}
	if _, err := p.Probe(ctx, "held", "", 0); err != nil {
		t.Fatalf("connected desktop and readable session: %v", err)
	}
}

func TestSharedDesktopSurvivesHubRestart(t *testing.T) {
	cli := os.Getenv("REIGNHUB_TEST_SHARED_CODEX_CLI")
	if cli == "" || !system.CodexSharedDesktop {
		t.Skip("provide a compatible desktop CLI")
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CODEX_API_KEY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := config.Config{DataDir: t.TempDir()}
	link, err := startSharedWorker(ctx, cli, cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	// The deliberately detached worker owns the fixture database until its idle exit.
	t.Cleanup(func() {
		shutdown, stop := context.WithTimeout(context.Background(), 75*time.Second)
		defer stop()
		for {
			_, err := link.desktopState(shutdown, http.MethodGet, "")
			if err != nil {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if shutdown.Err() != nil {
			t.Error("idle fixture worker did not exit")
		}
	})
	if err = saveSharedEndpoint(ctx, cfg.DataDir, nil, link.sharedEndpoint); err != nil {
		t.Fatal(err)
	}
	// The endpoint is reachable during startup and after the App has left.
	// A saved, live bridge alone must leave the ordinary IPC path available.
	if link.waitDesktop(ctx) {
		t.Fatal("a bridge with no desktop was selected")
	}
	path := "/" + rand.Text()
	if _, err = link.desktopState(ctx, http.MethodPut, path); err != nil {
		t.Fatal("worker control failed")
	}
	app, err := rpc.Dial(ctx, link.Address+path, nil)
	if err != nil {
		t.Fatal("fixture desktop did not initialize")
	}
	defer app.Close()
	first := New(cfg, "", events.New())
	if err = first.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if first.shared == nil || !first.shared.connected(ctx) {
		t.Fatal("original desktop not recognized")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	var created struct{ Thread struct{ ID string } }
	if err = app.Call(ctx, "thread/start", map[string]any{"cwd": cfg.DataDir, "ephemeral": true}, &created); err != nil || created.Thread.ID == "" {
		t.Fatal("desktop lost the backend while Hub was closed", err)
	}
	// Restart Hub while the desktop itself is reconnecting to the same worker.
	_ = app.Close()
	for link.connected(ctx) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(10 * time.Millisecond)
	}
	rejoined := make(chan *rpc.Client, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		c, _ := rpc.Dial(ctx, link.Address+path, nil)
		rejoined <- c
	}()
	second := New(cfg, "", events.New())
	defer second.Close()
	if err = second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	app = <-rejoined
	if app == nil {
		t.Fatal("fixture desktop failed to reconnect")
	}
	defer app.Close()
	if second.shared == nil || !second.shared.connected(ctx) {
		t.Fatal("fresh Hub did not restore the connected desktop")
	}
	var read struct{ Thread struct{ ID string } }
	if err = second.client.Call(ctx, "thread/read", map[string]any{"threadId": created.Thread.ID}, &read); err != nil || read.Thread.ID != created.Thread.ID {
		t.Fatal("fresh Hub did not reach the previous server", err)
	}
	// A failed explicit launch restores the live desktop generation.
	replacement := "/" + rand.Text()
	if _, err = link.desktopState(ctx, http.MethodPut, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err = link.desktopState(ctx, http.MethodDelete, replacement); err != nil || !link.connected(ctx) {
		t.Fatal("failed launch lost original desktop")
	}
	_ = app.Close()
	for link.connected(ctx) {
		select {
		case <-ctx.Done():
			t.Fatal("desktop disconnect was not observed", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if link.waitDesktop(ctx) {
		t.Fatal("a previously used bridge claimed the next startup")
	}
}

func TestSharedWorkerReportsMissingBackend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cli := filepath.Join(t.TempDir(), "missing-codex.exe")
	if _, err := startSharedWorker(ctx, cli, t.TempDir()); !errors.Is(err, sharedStartupCLI) {
		t.Fatalf("missing executable must report a backend launch failure: %v", err)
	}
}
