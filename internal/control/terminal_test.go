package control

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/terminal"
)

type terminalProvider struct {
	provider.Provider
	dir string
}

func (p terminalProvider) Transcript(_ context.Context, session, _ string) (provider.Transcript, error) {
	return provider.Transcript{Session: provider.Session{ID: session, CWD: p.dir}}, nil
}
func (p terminalProvider) Status(context.Context) provider.Status {
	return provider.Status{State: "ready"}
}

func TestTerminalDispatchAuthorizationAndTrustedDirectory(t *testing.T) {
	var gotDir string
	m := terminal.New(context.Background(), func(_ context.Context, dir string, _ terminal.Size, _ string) (terminal.Engine, error) {
		gotDir = dir
		return nil, errors.New("fixture stops before spawning")
	})
	defer m.Close()
	dir := t.TempDir()
	d := Dispatcher{Terminals: m, Providers: map[string]provider.Provider{"test": terminalProvider{dir: dir}}, Execute: func(Request, func() Response) Response {
		t.Fatal("terminal contents entered durable receipts")
		return Response{}
	}}
	params, _ := json.Marshal(terminal.Params{ID: strings.Repeat("aa", 32), Size: terminal.Size{Cols: 80, Rows: 24}})
	r := Request{ID: "request", Method: "terminal.open", Provider: "test", Session: "fixture", Params: params}
	ctx := context.Background()
	if out := d.HandleRemote(ctx, r); out.Code != "TERMINAL_DENIED" || gotDir != "" {
		t.Fatal("remote identity/permission was not enforced", out)
	}
	d.RemoteTerminalAllowed = func() bool { return true }
	if out := d.HandleRemote(ctx, r); out.Code != "TERMINAL_DENIED" {
		t.Fatal("missing authenticated identity accepted", out)
	}
	if out := d.HandleRemote(WithRemoteIdentity(ctx, "paired-controller"), r); out.Error != "fixture stops before spawning" || gotDir != dir {
		t.Fatal("host cwd not resolved", out, gotDir)
	}
	gotDir = ""
	d.RemoteTerminalAllowed = func() bool { return false }
	if out := d.HandleRequest(ctx, r); out.Error != "fixture stops before spawning" || gotDir != dir {
		t.Fatal("local depends on remote permission", out)
	}
	paired := WithRemoteIdentity(ctx, "paired-controller")
	list := Request{Method: "terminal.profiles", Provider: "test", Session: "fixture"}
	if out := d.HandleRemote(paired, list); out.Code != "TERMINAL_DENIED" {
		t.Fatal("peer listed shells without terminal permission", out)
	}
	d.RemoteTerminalAllowed = func() bool { return true }
	if out := d.HandleRemote(paired, list); out.Error != "" || out.NotModified || out.Revision != "" {
		t.Fatal("live discovery failed", out)
	}
	unknown := r
	unknown.Method = "terminal.openProfile"
	unknown.Params, _ = json.Marshal(terminal.Params{ID: strings.Repeat("ab", 32), Profile: filepath.Join(dir, "unlisted.exe"), Size: terminal.Size{Cols: 80, Rows: 24}})
	gotDir = ""
	if out := d.HandleRemote(paired, unknown); out.Error == "" || gotDir != "" {
		t.Fatal("unknown executable reached factory", out)
	}
	d.Providers["test"] = terminalProvider{dir: "relative/path"}
	gotDir = ""
	if out := d.HandleRequest(ctx, r); out.Error == "" || gotDir != "" {
		t.Fatal("invalid cwd reached factory", out)
	}
	for _, method := range []string{"terminal.profiles", "terminal.openProfile", "terminal.open", "terminal.attach", "terminal.read", "terminal.input", "terminal.resize", "terminal.close", "terminal.closeStatus"} {
		if ReadOnly(method) {
			t.Fatal("terminal traffic must not be cached", method)
		}
		if CancellableRead(method) != (method == "terminal.read" || method == "terminal.profiles" || method == "terminal.closeStatus") {
			t.Fatal("wrong cancellation policy", method)
		}
	}
	if out := d.HandleRemote(ctx, Request{Method: "host.terminal.enabled"}); out.Error == "" {
		t.Fatal("peer enabled its own permission")
	}
}
