//go:build darwin || windows || (linux && !android) || (js && wasm && !nsgominiprogram && !nsgoharmony)

package hubui

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	model "github.com/monkeyWie/reignhub/internal/terminal"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

type remoteTerminalFixture struct {
	API
	opens atomic.Int32
	input chan string
}

func (f *remoteTerminalFixture) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	name := str(r, "method")
	if method != "client.call" || !strings.HasPrefix(name, "terminal.") {
		return f.API.Call(ctx, method, p, target, r)
	}
	if target != "fixture-host" || str(r, "provider") != "pi" || str(r, "session") != "fixture-chat" {
		return nil, errors.New("unexpected terminal destination")
	}
	params := object(r["params"])
	switch name {
	case "terminal.open":
		f.opens.Add(1)
		return model.Snapshot{ID: str(params, "id"), Attachment: "fixture", Size: model.Size{Cols: 80, Rows: 24},
			Sequence: 1, Data: []byte("\x1b]2;paired-shell\x07ready\r\n")}, nil
	case "terminal.read":
		<-ctx.Done()
		return nil, ctx.Err()
	case "terminal.input":
		b, err := base64.StdEncoding.DecodeString(str(params, "data"))
		if err == nil {
			select {
			case f.input <- string(b):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, err
	case "terminal.resize", "terminal.detach":
		return nil, nil
	default:
		return nil, errors.New("unexpected terminal call: " + name)
	}
}

func TestTerminalViewerUsesPairedHost(t *testing.T) {
	api := &remoteTerminalFixture{API: newDemoAPI(), input: make(chan string, 4)}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := newState(cx, api)
		s.width.Set(390)
		s.target, s.selected = "fixture-host", Object{"providerId": "pi", "id": "fixture-chat"}
		s.providers = []Object{{"id": "pi", "capabilities": Object{"terminal": true}}}
		return s.conversation(cx, true)
	}, nsgotest.Size(390, 844), nsgotest.Environment(core.Environment{OS: core.OSIOS, Idiom: core.IdiomPhone}))
	app.Press("id:terminal-toggle")
	app.WaitFor("text:paired-shell", 5*time.Second)
	app.Press("id:terminal-toggle")
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:conversation-terminal") })
	app.Press("id:terminal-toggle")
	app.WaitFor("id:conversation-terminal", 3*time.Second)
	app.Key("q")
	app.WaitUntil(2*time.Second, func() bool { return len(api.input) > 0 })
	if got := <-api.input; got != "q" || api.opens.Load() != 1 {
		t.Fatalf("received %q after %d shell opens", got, api.opens.Load())
	}
}
