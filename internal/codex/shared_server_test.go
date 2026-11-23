package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/rpc"
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
