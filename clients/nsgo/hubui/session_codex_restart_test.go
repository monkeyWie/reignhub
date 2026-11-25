package hubui

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/system"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

type sessionRestartAPI struct {
	*demoAPI
	activations atomic.Int32
	probes      atomic.Int32
	request     chan Object
	probeResult chan error
}

func (f *sessionRestartAPI) Call(ctx context.Context, method string, p Object, target string, req Object) (any, error) {
	switch {
	case method == "host.activate":
		f.activations.Add(1)
		f.request <- req
		return nil, nil
	case method == "client.call" && target == "local" && str(req, "method") == "watch" && str(req, "session") == "build" && flag(req, "probe"):
		f.probes.Add(1)
		select {
		case err := <-f.probeResult:
			return Object{"epoch": "fixture", "reset": true}, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case method == "client.list" || method == "client.call" && str(req, "method") == "providers":
		// Keep background scans independent of the explicitly requested probe.
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.demoAPI.Call(ctx, method, p, target, req)
}

func TestSessionRestartConfirmsOnceAndRetriesOnlyFailedProbe(t *testing.T) {
	if !system.CodexSharedDesktop {
		t.Skip("local shared Codex desktop launch is unavailable on this platform")
	}
	f := &sessionRestartAPI{demoAPI: newDemoAPI(), request: make(chan Object, 2), probeResult: make(chan error, 2)}
	var s *State
	var ss Object
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = sidebarInteractionFixture(cx)
		s.api = f
		s.localProviders = []Object{{"id": "codex-app", "instances": []Object{{"pid": 42}}}}
		ss = copyObject(s.sessions[0])
		ss["codexOwnerUnreachable"] = true
		s.workspaces["local"] = workspaceCache{sessions: []Object{ss}}
		return core.View{Children: []core.Node{
			button(cx, "test-restart", "Restart", nil, "outline", func() { s.restartSessionCodex(ss) }),
			core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) }),
		}}
	}, nsgotest.Size(800, 600))
	app.Press("id:test-restart")
	app.Press("id:dialog-cancel")
	if f.activations.Load() != 0 {
		t.Fatal("opening or cancelling confirmation restarted Codex")
	}
	app.Press("id:test-restart")
	app.Press("id:confirm-activate")
	app.WaitUntil(time.Second, func() bool { return f.probes.Load() == 1 })
	if req := <-f.request; !flag(req, "confirmRestart") || number(objects(req["expected"])[0], "pid") != 42 {
		t.Fatalf("missing confirmed instance: %v", req)
	}
	app.Press("id:test-restart")
	if f.activations.Load() != 1 || app.Exists("id:confirm-activate") {
		t.Fatal("duplicate restart while waiting for session verification")
	}
	f.probeResult <- errors.New("fixture unavailable")
	app.WaitUntil(time.Second, func() bool {
		busy, failure := s.sessionCodexRecoveryFeedback(ss)
		return !busy && failure == "fixture unavailable"
	})
	app.Press("id:test-restart")
	app.WaitUntil(time.Second, func() bool { return f.probes.Load() == 2 })
	f.probeResult <- nil
	app.WaitUntil(time.Second, func() bool { return !s.codexRecoveryIssue(s.workspace("local").sessions[0]) })
	if f.activations.Load() != 1 {
		t.Fatal("retrying the read restarted Codex twice")
	}
}
