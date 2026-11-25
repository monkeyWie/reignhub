//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/events"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

func TestDirectoryUsesLiveAppOwnerWithoutOpeningConversation(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	p := New(config.Config{}, "", events.New())
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		switch m.Method {
		case "thread/turns/list":
			return json.RawMessage(`{"data":[]}`), nil
		case "thread/list":
			return json.RawMessage(`{"data":[{"id":"external","source":"vscode","originator":"Codex Desktop","status":{"type":"notLoaded"}}]}`), nil
		case "project/list":
			return map[string]any{"data": []any{}}, nil
		default:
			t.Errorf("directory issued non-metadata RPC %s", m.Method)
			return nil, nil
		}
	})
	var fixture *ipcFixture
	fixture = attachIPCFixture(t, p, func(m codexipc.Message) (codexipc.Message, bool) {
		if m.Method == "thread-stream-following-changed" {
			var params struct{ Following bool }
			_ = json.Unmarshal(m.Params, &params)
			if params.Following {
				go fixture.snapshot("external", 1, "fixture text remains host-local")
			}
		}
		return codexipc.Message{}, false
	})
	dir := filepath.Join(fixture.b.home, "thread-writer-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "external.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	page, err := p.Sessions(ctx, "")
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].Status != "running" || page.Sessions[0].ActiveTurnID != "t" {
		t.Fatalf("App execution missing from directory: %+v %v", page, err)
	}
	fixture.send(codexipc.Message{Type: "broadcast", Method: "thread-stream-state-changed", Source: "app-owner", Version: 11, Params: rawJSON(map[string]any{"hostId": "local", "conversationId": "external", "change": map[string]any{
		"type": "snapshot", "revision": 2, "conversationState": map[string]any{"id": "external", "threadRuntimeStatus": map[string]any{"type": "idle"}, "turns": []any{map[string]any{"turnId": "t", "status": "completed"}}},
	}})})
	for {
		page, err = p.Sessions(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if page.Sessions[0].Status == "completed" {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("completion did not reach the directory")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if page.Sessions[0].ActiveTurnID != "" {
		t.Fatal("completed session retains active turn")
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	page, err = p.Sessions(ctx, "")
	if err != nil || page.Sessions[0].Status != "completed" {
		t.Fatal("writer release lost the final completion", err)
	}
	fixture.b.mu.Lock()
	remaining := len(fixture.b.followed)
	fixture.b.mu.Unlock()
	if remaining != 0 {
		t.Fatal("inactive directory-only follower was not released")
	}
}

func TestLockedMissingOwnerWarnsOnlyAffectedSessionAndClearsOnRecovery(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	p := New(config.Config{}, "", events.New())
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		switch m.Method {
		case "thread/list":
			return json.RawMessage(`{"data":[{"id":"held","source":"vscode"},{"id":"unlocked","source":"vscode"}]}`), nil
		case "thread/read":
			return json.RawMessage(`{"thread":{"id":"held","status":{"type":"notLoaded"}}}`), nil
		case "thread/turns/list", "thread/queue/list", "project/list":
			return json.RawMessage(`{"data":[]}`), nil
		default:
			t.Errorf("availability check reached executor: %s", m.Method)
			return nil, nil
		}
	})
	var misses atomic.Int32
	var reachable atomic.Bool
	var f *ipcFixture
	f = attachIPCFixture(t, p, func(m codexipc.Message) (codexipc.Message, bool) {
		switch m.Method {
		case "thread-owner-discovery":
			if !reachable.Load() {
				misses.Add(1)
				return codexipc.Message{ResultType: "error", Error: "no-client-found"}, true
			}
		case "thread-stream-following-changed":
			go f.snapshot("held", 1, "isolated fixture")
		}
		return codexipc.Message{}, false
	})
	dir := filepath.Join(f.b.home, "thread-writer-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.Create(filepath.Join(dir, "held.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	checkRows := func(warn bool) {
		t.Helper()
		page, err := p.Sessions(ctx, "")
		if err != nil || len(page.Sessions) != 2 || page.Sessions[0].CodexOwnerUnreachable != warn || page.Sessions[1].CodexOwnerUnreachable || f.b.needsSharedDesktop() != warn {
			t.Fatalf("directory/restart availability: %+v %v", page, err)
		}
	}
	checkRows(true)
	if misses.Load() != 2 {
		t.Fatalf("expected two read-only discovery attempts, got %d", misses.Load())
	}
	history, err := p.Transcript(ctx, "held", "")
	if err != nil || !history.Session.CodexOwnerUnreachable {
		t.Fatalf("persisted history failed to retain session warning: %+v %v", history.Session, err)
	}
	if _, err := p.Probe(ctx, "held", "", 0); !errors.Is(err, errOwnerUnreachable) {
		t.Fatalf("probe acknowledged an unreachable owner: %v", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	checkRows(false)
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	checkRows(true)
	reachable.Store(true)
	if _, err := p.Probe(ctx, "held", "", 0); err != nil {
		t.Fatalf("recovery probe: %v", err)
	}
	checkRows(false)
}
