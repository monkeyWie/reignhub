package hubui

import (
	"fmt"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func noticeFixture(t *testing.T, width int, dark bool) (*nsgotest.App, *State) {
	t.Helper()
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = newState(cx, newDemoAPI())
		s.applyColors()
		s.target = "remote"
		return core.View{Style: fill, Children: []core.Node{
			button(cx, "show-notice", "Show notice", nil, "ghost", func() { s.openLink(cx, Message{}, "fixture.zip") }),
			button(cx, "unrelated-update", "Refresh", nil, "ghost", s.changed),
			button(cx, "open-modal", "Open modal", nil, "ghost", func() { s.dialog.Set("connect") }),
			s.noticeOverlay(cx),
			core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) }),
		}}
	}, nsgotest.Size(core.Points(width), 844), nsgotest.Environment(core.Environment{Dark: dark}))
	return app, s
}

func TestNoticeExpiresWithoutBeingExtendedByRefresh(t *testing.T) {
	app, _ := noticeFixture(t, 800, false)
	app.Press("id:show-notice")
	app.Find("id:notice")
	app.Advance(noticeDuration / 2)
	app.Press("id:unrelated-update")
	app.Advance(noticeDuration/2 + time.Millisecond)
	if app.Exists("id:notice") {
		t.Fatal("missing-download notice outlived its timeout after an unrelated refresh")
	}
}

func TestNoticeReplacementGetsFullLifetime(t *testing.T) {
	app, _ := noticeFixture(t, 800, false)
	app.Press("id:show-notice")
	app.Advance(noticeDuration / 2)
	// Repeating the same action must restart the timer even with identical copy.
	app.Press("id:show-notice")
	app.Advance(noticeDuration/2 + time.Millisecond)
	app.Find("id:notice")
	app.Advance(noticeDuration / 2)
	if app.Exists("id:notice") {
		t.Fatal("replacement notice never expired")
	}
	app.Press("id:show-notice")
	app.Press("id:dismiss-notice")
	app.Advance(noticeDuration)
	if app.Exists("id:notice") {
		t.Fatal("dismissed notice reappeared")
	}
}

func TestNoticeAboveModalAndWithinViewport(t *testing.T) {
	for _, width := range []int{390, 800} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("width-%d-dark-%t", width, dark), func(t *testing.T) {
				app, s := noticeFixture(t, width, dark)
				app.Press("id:open-modal")
				app.Type(pairingInput, "01234")
				app.WaitUntil(time.Second, func() bool { return app.Find(pairingInput).Text == "01234" })
				inputID := app.Find(pairingInput).ID
				// An asynchronous result can arrive while a modal is already open.
				app.Post(func() { s.cx.Post(func() { s.openLink(s.cx, Message{}, "fixture.zip") }) })
				app.WaitFor("id:notice", time.Second)
				bounds := globalBounds(app.Find("id:notice"))
				if bounds.Origin.X < 17 || bounds.Origin.X+bounds.Size.Width > core.Points(width-17) || bounds.Size.Height <= 0 {
					t.Fatalf("notice escapes safe viewport: %+v", bounds)
				}
				app.Screenshot(fmt.Sprintf("notice-modal-%d-dark-%t", width, dark))
				// Use hit testing: the modal backdrop must not eat the close action.
				app.PressAt("id:dismiss-notice", core.Point{X: 15, Y: 15})
				if app.Exists("id:notice") {
					t.Fatal("notice close was blocked by modal")
				}
				input := app.Find(pairingInput)
				if input.ID != inputID || input.Text != "01234" {
					t.Fatalf("notice changed modal input: ID %v -> %v, text %q", inputID, input.ID, input.Text)
				}
				app.PressAt("id:dialog-connect-close", core.Point{X: 12, Y: 12})
				if app.Exists("id:dialog-connect") {
					t.Fatal("modal no longer responds after notice dismissal")
				}
			})
		}
	}
}

func TestNoticeTimerStopsWithWorkspace(t *testing.T) {
	var s *State
	var mounted *core.Signal[bool]
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		mounted = core.NewSignal(cx, true)
		return core.Show{When: mounted.Get, Then: func(cx *core.Scope) core.Node {
			s = newState(cx, newDemoAPI())
			s.showNotice("fixture")
			return s.noticeOverlay(cx)
		}}
	})
	app.Find("id:notice")
	app.Post(func() { s.cx.Post(func() { mounted.Set(false) }) })
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:notice") })
	app.Advance(noticeDuration + time.Second)
	if s.notice != "fixture" {
		t.Fatal("timer mutated disposed workspace")
	}
}
