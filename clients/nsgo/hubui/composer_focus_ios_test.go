package hubui

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestMobileComposerKeyboardOnTap(t *testing.T) {
	var height atomic.Int32
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s := newState(cx, newDemoAPI())
		s.selected = Object{"id": "fixture", "providerId": "codex-app"}
		s.target = "demo-device"
		cx.Effect(func() { height.Store(int32(cx.Env().KeyboardHeight())) })
		return core.View{Style: fill, Children: []core.Node{spacer(), s.composer(cx)}}
	})
	app.WaitUntil(3*time.Second, func() bool { return app.Find("id:chat-input").Focused })
	app.Settle()
	if h := height.Load(); h != 0 {
		t.Fatalf("entry opened the software keyboard (%d pt)", h)
	}
	app.Press("id:chat-input")
	app.WaitUntil(3*time.Second, func() bool { return height.Load() > 100 })
}
