package hubui

import (
	"fmt"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestTouchConnectionButtonsHitTesting(t *testing.T) {
	testTouchConnectionButtons(t, 844, 0)
}

func TestTouchConnectionButtonsWithShortViewport(t *testing.T) {
	if target := nsgotest.Target(); target != "" && target != "web" {
		t.Skip("synthetic short viewport; native phone coverage uses its real keyboard")
	}
	testTouchConnectionButtons(t, 380, 0)
}

func TestTouchConnectionButtonsWithKeyboard(t *testing.T) {
	if target := nsgotest.Target(); target != "" && target != "web" {
		t.Skip("synthetic keyboard; native phone coverage uses its real keyboard")
	}
	testTouchConnectionButtons(t, 568, 240)
}

func testTouchConnectionButtons(t *testing.T, height, keyboard core.Points) {
	t.Helper()
	f := &pairingFixture{demoAPI: newDemoAPI(), steps: make(chan connectivity.Progress, 2), finish: make(chan error, 1)}
	var scope *core.Scope
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		scope = cx
		return RootWithAPI(cx, f, true)
	}, nsgotest.Size(390, height), nsgotest.Environment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone}))
	showSessionDirectory(t, app)
	app.WaitFor("id:connect-device", time.Second)
	checkButtons := func() {
		t.Helper()
		var bottom, top core.Points
		app.Post(func() {
			bottom = scope.Env().Window().Height - scope.Env().KeyboardHeight()
			top = scope.Env().SafeArea().Top
		})
		app.Settle()
		for _, id := range []string{"dialog-cancel", "connect-submit", "dialog-connect-close"} {
			b := globalBounds(app.Find("id:" + id))
			if b.Origin.Y < top || b.Origin.Y+b.Size.Height > bottom+1 || b.Size.Height < 44 {
				t.Fatalf("%s is outside the usable viewport [%v,%v] or too small: %+v", id, top, bottom, b)
			}
		}
	}
	open := func() {
		t.Helper()
		app.Press("id:connect-device")
		app.Type(pairingInput, "123456")
		if keyboard > 0 {
			app.SetEnvironment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone, KeyboardHeight: keyboard, SafeArea: core.Edges{Top: 24, Bottom: 16}})
		}
		app.Advance(time.Second)
		checkButtons()
	}
	open()
	assertCodeFillsRow(t, app, "connection-code", "connect-submit")
	app.Screenshot(fmt.Sprintf("touch-connect-%g-keyboard-%g", height, keyboard))
	app.PressAt("id:dialog-cancel", core.Point{X: 20, Y: 20})
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:dialog-connect") })
	if f.calls.Load() != 0 {
		t.Fatal("cancel started a connection")
	}
	open()
	app.PressAt("id:connect-submit", core.Point{X: 20, Y: 20})
	app.WaitFor("id:connection-loading", time.Second)
	app.Advance(time.Second)
	app.PressAt("id:connect-cancel", core.Point{X: 20, Y: 20})
	app.WaitUntil(time.Second, func() bool { return f.canceled.Load() == 1 })
	if f.calls.Load() != 1 {
		t.Fatal("connection was duplicated")
	}
}
