//go:build (darwin && !ios) || windows || (linux && !android)

package hubui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/provider"
	model "github.com/monkeyWie/reignhub/internal/terminal"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestTerminalDividerTracksDragDuringWorkspaceUpdates(t *testing.T) {
	var heights []core.Points
	var initial core.Points
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := workspaceLayoutFixture(cx)
		s.selectedTerminal().visible = true
		return s.withTerminal(cx, core.View{Style: fill, OnLayout: func(b core.Bounds) {
			heights = append(heights, b.Size.Height)
			if initial > 0 && b.Size.Height < initial-5 {
				// Model a session update arriving before the next pointer move.
				cx.Post(s.changed)
			}
		}})
	}, nsgotest.Size(1100, 900), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	app.Post(func() {
		initial = heights[len(heights)-1]
		heights = nil
	})
	app.Settle()
	app.Drag("id:conversation-split-handle-0", core.Point{Y: -160})
	prev := initial
	for _, h := range heights {
		if h > prev+1 {
			t.Fatalf("divider moved back from %g to %g during a one-way drag", prev, h)
		}
		prev = h
	}
	if len(heights) < 2 || math.Abs(float64(prev-(initial-160))) > 1 {
		t.Fatalf("divider did not track the full drag from %g: %v", initial, heights)
	}
}

func TestTerminalDividerPreservesMessagePosition(t *testing.T) {
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = workspaceLayoutFixture(cx)
		s.selectedTerminal().visible = true
		turns := []Object{}
		for i := range 24 {
			turns = append(turns, Object{"id": fmt.Sprint(i), "status": "completed", "items": []Object{
				{"id": "user", "role": "user", "text": fmt.Sprintf("Synthetic instruction %d", i)},
				{"id": "reply", "role": "assistant", "text": strings.Repeat("A reply with **formatting** and variable height.\n\n", 3+i%5)},
			}})
		}
		s.setTranscript(Object{"turns": turns})
		return s.conversation(cx, false)
	}, nsgotest.Size(1100, 900), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	distance := func() core.Points {
		v := app.Find("id:chat-message-list")
		return v.Content.Height - v.Frame.Size.Height - v.Offset.Y
	}
	app.WaitUntil(time.Second, func() bool { return math.Abs(float64(distance())) <= 1 })
	list, editor := app.Find("id:chat-message-list").ID, app.Find("id:chat-input").ID
	beforeHeight := app.Find("id:terminal-pane").Frame.Size.Height
	if nsgotest.Target() == "" {
		// Stop between moves, keeping the handle held while the workspace refreshes.
		handle := app.Find("id:conversation-split-handle-0")
		at := core.Point{X: handle.Frame.Size.Width / 2, Y: handle.Frame.Size.Height / 2}
		finger := app.Touch("id:conversation-split-handle-0", at)
		for _, dy := range []core.Points{-120, 40, 0, -80} {
			finger.Move(core.Point{X: at.X, Y: at.Y + dy})
			if gap := distance(); math.Abs(float64(gap)) > 1 {
				t.Fatalf("during drag at %g the latest message was %g points from the bottom", dy, gap)
			}
			app.Post(s.changed)
			app.Advance(80 * time.Millisecond)
			h := app.Find("id:terminal-pane").Frame.Size.Height
			if math.Abs(float64(h-(beforeHeight-dy))) > 1 || math.Abs(float64(distance())) > 1 {
				t.Fatalf("while held at %g: terminal height %g, want %g; latest is %g from bottom", dy, h, beforeHeight-dy, distance())
			}
		}
		finger.Lift()
		beforeHeight = app.Find("id:terminal-pane").Frame.Size.Height
	}
	app.Drag("id:conversation-split-handle-0", core.Point{Y: -160})
	if after := app.Find("id:terminal-pane").Frame.Size.Height; after < beforeHeight+100 {
		t.Fatalf("divider did not resize the terminal: before=%g after=%g", beforeHeight, after)
	}
	app.WaitUntil(time.Second, func() bool { return math.Abs(float64(distance())) <= 1 && !app.Exists("id:jump-to-latest") })
	app.Drag("id:conversation-split-handle-0", core.Point{Y: 180}, core.Point{Y: -80}, core.Point{Y: 160})
	app.WaitUntil(time.Second, func() bool { return math.Abs(float64(distance())) <= 1 && !app.Exists("id:jump-to-latest") })
	app.Scroll("id:chat-message-list", -300)
	app.WaitFor("id:jump-to-latest", time.Second)
	// The divider moves the bottom of the viewport while the top stays put.
	var readingRow string
	app.Post(func() {
		p, _ := s.timeline.list.ListPosition()
		readingRow = "id:message-" + s.messageRows[p.Index].ID
	})
	app.Settle()
	before := globalBounds(app.Find(readingRow)).Origin.Y
	if nsgotest.Target() == "" {
		handle := app.Find("id:conversation-split-handle-0")
		at := core.Point{X: handle.Frame.Size.Width / 2, Y: handle.Frame.Size.Height / 2}
		finger := app.Touch("id:conversation-split-handle-0", at)
		for _, dy := range []core.Points{-60, 40, 0} {
			finger.Move(core.Point{X: at.X, Y: at.Y + dy})
			app.Post(s.changed)
			app.Advance(80 * time.Millisecond)
			if moved := globalBounds(app.Find(readingRow)).Origin.Y - before; math.Abs(float64(moved)) > 1 {
				t.Fatalf("while holding the divider at %g the reading row moved by %g", dy, moved)
			}
		}
		finger.Lift()
	}
	app.Drag("id:conversation-split-handle-0", core.Point{Y: -100}, core.Point{Y: 80}, core.Point{})
	app.WaitUntil(time.Second, func() bool { return math.Abs(float64(globalBounds(app.Find(readingRow)).Origin.Y-before)) <= 1 })
	if app.Find("id:chat-message-list").ID != list || app.Find("id:chat-input").ID != editor {
		t.Fatal("divider remounted the message list or composer")
	}
	app.Press("id:jump-to-latest")
	app.WaitUntil(time.Second, func() bool { return math.Abs(float64(distance())) <= 1 })
	app.Post(func() {
		turns := objects(s.transcript["turns"])
		next := copyObject(turns[len(turns)-1])
		items := append([]Object(nil), objects(next["items"])...)
		items[1] = copyObject(items[1])
		items[1]["text"] = str(items[1], "text") + strings.Repeat("New streamed content.\n\n", 20)
		next["items"] = items
		s.setTranscript(Object{"turns": append(append([]Object(nil), turns[:len(turns)-1]...), next)})
	})
	app.WaitUntil(time.Second, func() bool { return math.Abs(float64(distance())) <= 1 && !app.Exists("id:jump-to-latest") })
	app.Screenshot("terminal-divider-messages-stable")
}

type terminalScrollFixture struct{ *terminalUITestEngine }

func (terminalScrollFixture) Resize(model.Size) {}

func TestTerminalTabRestoresScrollPosition(t *testing.T) {
	var history strings.Builder
	for i := range 75 {
		fmt.Fprintf(&history, "fixture line %d\r\n", i)
	}
	manager := model.New(context.Background(), func(context.Context, string, model.Size, string) (model.Engine, error) {
		return terminalScrollFixture{&terminalUITestEngine{dirty: true, input: history.String(), done: make(chan struct{})}}, nil
	})
	t.Cleanup(func() { _ = manager.Close() })
	dispatcher := &control.Dispatcher{Terminals: manager, Providers: map[string]provider.Provider{"pi": terminalUITestProvider{dir: t.TempDir()}}}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := workspaceLayoutFixture(cx)
		s.providers = []Object{{"id": "pi", "capabilities": Object{"terminal": true}}}
		s.api = &terminalUITestAPI{s.api, dispatcher}
		return s.workspaceView(cx)
	}, nsgotest.Size(1100, 900), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	const scroll = "id:conversation-terminal-scroll"
	app.Press("id:terminal-toggle")
	app.WaitFor(scroll, 3*time.Second)
	app.WaitUntil(2*time.Second, func() bool { return app.Find(scroll).Offset.Y > 500 })
	app.Scroll(scroll, -240)
	app.WaitUntil(2*time.Second, func() bool {
		view := app.Find(scroll)
		return view.Offset.Y > 0 && view.Content.Height-view.Frame.Size.Height-view.Offset.Y > 100
	})
	before := app.Find(scroll).Offset.Y
	app.Press("id:terminal-add")
	app.WaitUntil(3*time.Second, func() bool { return app.Exists(scroll) && app.Find(scroll).Offset.Y > before+100 })
	app.Press("id:terminal-tab-1")
	app.WaitUntil(2*time.Second, func() bool {
		if !app.Exists(scroll) {
			return false
		}
		offset := app.Find(scroll).Offset.Y
		// NSGo restores the emulator row, which can round to one cell.
		return math.Abs(float64(offset-before)) <= 16
	})
}
