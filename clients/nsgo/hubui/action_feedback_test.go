package hubui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestActionButtonContentStaysCentered(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, width := range []core.Points{390, 800} {
			t.Run(fmt.Sprintf("width-%g-dark-%t", width, dark), func(t *testing.T) {
				app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
					(&State{cx: cx}).applyColors()
					pending := core.NewSignal(cx, false)
					return core.View{Style: core.Style{Padding: core.All(24), Gap: 20, AlignItems: core.AlignCenter}, Children: []core.Node{
						row(busyButton(cx, "deny", "拒绝", nil, "outline", pending.Get, func() { pending.Set(true) }), busyButton(cx, "allow", "允许连接", nil, "primary", pending.Get, func() { pending.Set(true) })),
						busyButton(cx, "save", "保存", lucide.Save, "outline", pending.Get, nil),
						busyButton(cx, "icon", "", lucide.RefreshCw, "ghost", pending.Get, nil),
					}}
				}, nsgotest.Size(width, 300), nsgotest.Environment(core.Environment{Dark: dark}))
				centered := func(container, content core.Bounds) {
					t.Helper()
					if math.Abs(float64(container.Origin.X+container.Size.Width/2-content.Origin.X-content.Size.Width/2)) > 1 || math.Abs(float64(container.Origin.Y+container.Size.Height/2-content.Origin.Y-content.Size.Height/2)) > 1 {
						t.Fatalf("content is not centered: button=%+v content=%+v", container, content)
					}
				}
				bounds := map[string]core.Bounds{}
				for _, id := range []string{"deny", "allow", "save", "icon"} {
					bounds[id] = globalBounds(app.Find("id:" + id))
					centered(bounds[id], globalBounds(app.Find("id:"+id+"-content")))
				}
				// Text-only labels must be centered themselves, with no empty icon slot.
				centered(bounds["deny"], globalBounds(app.Find("text:拒绝")))
				centered(bounds["allow"], globalBounds(app.Find("text:允许连接")))
				app.Hover("id:allow")
				app.Screenshot(fmt.Sprintf("buttons-hover-%g-dark-%t", width, dark))
				app.HoverOut()
				app.Press("id:allow")
				for _, id := range []string{"deny", "allow", "save", "icon"} {
					app.WaitFor("id:"+id+"-loading", time.Second)
					if globalBounds(app.Find("id:"+id)) != bounds[id] {
						t.Fatal("loading changed button bounds")
					}
					centered(bounds[id], globalBounds(app.Find("id:"+id+"-loading")))
				}
				app.Screenshot(fmt.Sprintf("buttons-loading-centered-%g-dark-%t", width, dark))
			})
		}
	}
}

func TestHostAuthorizationButtonLabelsCentered(t *testing.T) {
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := newState(cx, newDemoAPI())
		s.applyColors()
		s.host = Object{"pending": []Object{{"id": "fixture", "name": "Chrome 157", "kind": "browser", "platform": "darwin", "remember": true, "fingerprint": "0123456789abcdef"}}}
		return s.dialogView(cx, "authorize:fixture")
	}, nsgotest.Size(800, 600))
	for _, entry := range []struct{ id, text string }{{"deny-host-request", "拒绝"}, {"approve-host-request", "允许连接"}} {
		button := globalBounds(app.Find("id:" + entry.id))
		text := globalBounds(app.Find("text:" + entry.text))
		if math.Abs(float64(button.Origin.X+button.Size.Width/2-text.Origin.X-text.Size.Width/2)) > 1 {
			t.Fatalf("authorization label %q is not centered", entry.text)
		}
	}
	app.Screenshot("authorization-buttons-centered")
}

func TestHostAuthorizationCloseNarrowLight(t *testing.T) { testHostAuthorizationClose(t, 390, false) }
func TestHostAuthorizationCloseNarrowDark(t *testing.T)  { testHostAuthorizationClose(t, 390, true) }
func TestHostAuthorizationCloseWideLight(t *testing.T)   { testHostAuthorizationClose(t, 800, false) }
func TestHostAuthorizationCloseWideDark(t *testing.T)    { testHostAuthorizationClose(t, 800, true) }

func testHostAuthorizationClose(t *testing.T, width core.Points, dark bool) {
	t.Helper()
	f := &feedbackAPI{demoAPI: newDemoAPI(), requests: make(chan feedbackRequest, 8)}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = newState(cx, f)
		s.applyColors()
		s.host = Object{"pending": []Object{{"id": "fixture", "name": "Microsoft Edge 157", "kind": "browser", "remember": true}}}
		s.dialog.Set("authorize:fixture")
		return core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) })
	}, nsgotest.Size(width, 600), nsgotest.Environment(core.Environment{Dark: dark}))
	x := app.Find("id:dialog-authorize-close")
	xb := globalBounds(x)
	header := globalBounds(app.Find("id:dialog-authorize-close-header"))
	deny := globalBounds(app.Find("id:deny-host-request"))
	approve := globalBounds(app.Find("id:approve-host-request"))
	if x.Props.Label == "" || math.Abs(float64(xb.Origin.X+xb.Size.Width-header.Origin.X-header.Size.Width)) > 1 || xb.Origin.Y >= deny.Origin.Y || xb.Origin.Y >= approve.Origin.Y {
		t.Fatalf("close must be accessible at the top right, separate from the two footer choices: x=%+v header=%+v footer=%+v", xb, header, deny)
	}
	if xb.Origin.X < 0 || xb.Origin.X+xb.Size.Width > width {
		t.Fatal("close falls outside the viewport")
	}
	app.Screenshot(fmt.Sprintf("authorization-close-%g-dark-%t", width, dark))
	app.Press("id:approve-host-request")
	request := awaitFeedback(t, f)
	app.WaitFor("id:approve-host-request-loading", time.Second)
	app.PressAt("id:dialog-authorize-close", core.Point{X: 18, Y: 18})
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:dialog-authorize") })
	if len(f.requests) != 0 {
		t.Fatal("close submitted another authorization decision")
	}
	request.result <- nil
}

type feedbackRequest struct {
	method string
	params Object
	result chan error
}

func hostAuthorizationFixture(t *testing.T, expires int64) (*nsgotest.App, *State, *feedbackAPI) {
	t.Helper()
	f := &feedbackAPI{demoAPI: newDemoAPI(), requests: make(chan feedbackRequest, 8)}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = newState(cx, f)
		s.applyColors()
		s.host = Object{"pending": []Object{{"id": "fixture", "name": "Microsoft Edge 157", "kind": "browser", "remember": true, "expires": expires}}}
		s.dialog.Set("authorize:fixture")
		return core.View{Style: fill, Children: []core.Node{
			core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) }),
			core.Show{When: func() bool { s.read(); return s.error != "" }, Then: func(cx *core.Scope) core.Node { return s.errorOverlay(cx, false) }},
		}}
	}, nsgotest.Size(800, 600))
	return app, s, f
}

func TestHostAuthorizationExpiryAndWithdrawal(t *testing.T) {
	for _, reason := range []string{"server-expired", "withdrawn", "deadline"} {
		t.Run(reason, func(t *testing.T) {
			expires := time.Now().Add(time.Hour).Unix()
			if reason == "deadline" {
				expires = time.Now().Add(time.Second).Unix()
			}
			app, s, f := hostAuthorizationFixture(t, expires)
			if reason == "server-expired" {
				app.Press("id:approve-host-request")
				request := awaitFeedback(t, f)
				request.result <- errors.New("授权请求已过期")
			} else if reason == "withdrawn" {
				app.Post(func() { s.cx.Post(func() { s.host = Object{}; s.changed() }) })
			} else {
				app.Advance(time.Second)
			}
			app.WaitFor("text:连接请求已失效", 2*time.Second)
			if app.Exists("id:approve-host-request") || app.Exists("id:deny-host-request") {
				t.Fatal("expired authorization retained invalid choices")
			}
			app.Press("id:dialog-authorize-close")
			app.WaitUntil(time.Second, func() bool { return !app.Exists("id:dialog-authorize") })
		})
	}
}

func TestHostAuthorizationErrorIsAboveModalAndDismissible(t *testing.T) {
	app, _, f := hostAuthorizationFixture(t, time.Now().Add(time.Hour).Unix())
	app.Press("id:approve-host-request")
	request := awaitFeedback(t, f)
	request.result <- errors.New("Fixture authorization failed")
	app.WaitFor("id:error-banner", time.Second)
	bounds := globalBounds(app.Find("id:error-banner"))
	if bounds.Origin.X < 0 || math.Abs(float64(bounds.Origin.X+bounds.Size.Width-782)) > 1 {
		t.Fatalf("global notification must stay at the viewport's right inset: %+v", bounds)
	}
	app.Screenshot("authorization-error-above-modal")
	// Dispatch through hit testing, not directly to the handler: the backdrop
	// must not intercept the notification's close button.
	app.PressAt("id:dismiss-error", core.Point{X: 12, Y: 12})
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:error-banner") })
	app.Find("id:approve-host-request")
	app.PressAt("id:dialog-authorize-close", core.Point{X: 12, Y: 12})
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:dialog-authorize") })
	if len(f.requests) != 0 {
		t.Fatal("dismissing authorization submitted a decision")
	}
}

type feedbackAPI struct {
	*demoAPI
	requests chan feedbackRequest
}

func (f *feedbackAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "host.account.send" || method == "host.account.verify" || method == "host.configure" || method == "host.respond" || method == "client.call" && str(r, "method") == "decide" {
		request := feedbackRequest{method: method, params: copyObject(p), result: make(chan error, 1)}
		select {
		case f.requests <- request:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case err := <-request.result:
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.demoAPI.Call(ctx, method, p, target, r)
}

func feedbackFixture(t *testing.T) (*nsgotest.App, *State, *feedbackAPI) {
	t.Helper()
	f := &feedbackAPI{demoAPI: newDemoAPI(), requests: make(chan feedbackRequest, 8)}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s = newState(cx, f)
		s.devices = []Object{{"id": "demo-device", "connected": true}}
		s.target, s.selected = "demo-device", Object{"id": "demo-chat", "providerId": "codex-app"}
		s.providers = demoProviders()
		s.host = Object{"name": "Fixture computer", "serviceUrl": "https://example.com"}
		return core.View{Style: fill, Children: []core.Node{
			button(cx, "edit-fixture", "Edit", nil, "outline", func() { s.dialog.Set("service-settings") }),
			s.actionButton(cx, "other-action", "Other action", nil, "outline", func() { s.call("other-action", "host.password", nil, nil) }),
			s.conversation(cx, false),
			core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) }),
		}}
	}, nsgotest.Size(800, 900))
	return app, s, f
}

func awaitFeedback(t *testing.T, f *feedbackAPI) feedbackRequest {
	t.Helper()
	select {
	case r := <-f.requests:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("action never reached API")
		return feedbackRequest{}
	}
}

func TestSaveFeedbackRetainsDraftAndIsolatesOtherActions(t *testing.T) {
	app, s, f := feedbackFixture(t)
	app.Press("id:edit-fixture")
	app.Clear("id:device-name")
	app.Type("id:device-name", "Unsaved name")
	before := globalBounds(app.Find("id:dialog-save"))
	app.Press("id:dialog-save")
	request := awaitFeedback(t, f)
	app.WaitFor("id:dialog-save-loading", time.Second)
	if !app.Find("id:dialog-save").Props.Busy || !app.Find("id:dialog-save").Props.Disabled || !app.Find("id:device-name").Input.Disabled {
		t.Fatal("save did not acknowledge activation and prevent conflicting input")
	}
	if globalBounds(app.Find("id:dialog-save")).Size != before.Size {
		t.Fatal("busy state changed button size")
	}
	spinner := app.Find("id:dialog-save-loading").ID
	for range 3 {
		app.PressAt("id:dialog-save", core.Point{X: 8, Y: 8})
		app.Post(func() { s.cx.Post(s.changed) })
		app.Settle()
		if app.Find("id:dialog-save-loading").ID != spinner {
			t.Fatal("unrelated update restarted spinner")
		}
	}
	if len(f.requests) != 0 {
		t.Fatal("duplicate save")
	}
	app.Screenshot("save-pending")
	request.result <- errors.New("Fixture save failed")
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:dialog-save-loading") })
	app.WaitFor("text:Fixture save failed", time.Second)
	if app.Find("id:device-name").Text != "Unsaved name" {
		t.Fatal("failed save lost input")
	}
	app.Press("id:dialog-save")
	request = awaitFeedback(t, f)
	if str(request.params, "name") != "Unsaved name" {
		t.Fatal("retry did not retain form data")
	}
	app.Press("id:dialog-cancel")
	app.Type("id:chat-input", "Independent draft")
	if app.Find("id:send-message").Props.Disabled {
		t.Fatal("settings save blocked message composition")
	}
	app.Press("id:other-action")
	app.WaitUntil(time.Second, func() bool { return !app.Find("id:other-action").Props.Disabled })
	app.Press("id:edit-fixture")
	app.WaitFor("id:dialog-save-loading", time.Second)
	request.result <- nil
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:dialog-save-loading") })
	app.WaitFor("id:dialog-service-settings", time.Second)
	// Completion from a dismissed form must not close a newer form instance.
	if app.Find("id:device-name").Text != "Fixture computer" {
		t.Fatal("stale save rewrote a newer form")
	}
}

func TestApprovalFeedbackPreventsConflictsWithoutBlockingOtherApprovals(t *testing.T) {
	app, s, f := feedbackFixture(t)
	app.Post(func() {
		s.cx.Post(func() {
			s.approvals = []Object{{"id": "first", "params": Object{"title": "First request"}}, {"id": "second", "params": Object{"title": "Second request"}}}
			s.changed()
		})
	})
	app.WaitFor("id:approve-first", time.Second)
	app.Press("id:approve-first")
	first := awaitFeedback(t, f)
	app.WaitFor("id:approval-loading-first", time.Second)
	if !app.Find("id:deny-first").Props.Disabled || app.Find("id:approve-second").Props.Disabled {
		t.Fatal("approval pending state is not scoped to its request")
	}
	app.PressAt("id:deny-first", core.Point{X: 8, Y: 8})
	if len(f.requests) != 0 {
		t.Fatal("conflicting approval was submitted")
	}
	first.result <- errors.New("Fixture approval failed")
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:approval-loading-first") })
	app.WaitFor("id:approve-first", time.Second)
	app.Press("id:approve-first")
	retry := awaitFeedback(t, f)
	retry.result <- nil
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:approve-first") })
	app.WaitFor("id:approve-second", time.Second)
}

func TestTouchRowsShowPressFeedbackAndIconNames(t *testing.T) {
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, newDemoAPI(), true) }, nsgotest.Size(390, 844), nsgotest.Environment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone}))
	showSessionDirectory(t, app)
	app.WaitFor("id:device-demo-device", time.Second)
	for _, id := range []string{"device-demo-device", sessionRowID("all", Object{"deviceId": "demo-device", "providerId": "codex-app", "id": "demo-chat"})} {
		app.WaitFor("id:"+id, time.Second)
		style := app.Find("id:" + id).Style
		app.PressDown("id:" + id)
		if reflect.DeepEqual(style, app.Find("id:"+id).Style) {
			t.Fatalf("%s has no visible touch feedback", id)
		}
		app.PressUp("id:" + id)
		app.Press("id:" + id)
		if id == "device-demo-device" {
			app.Press("id:dialog-device-close")
		}
	}
	for _, id := range []string{"mobile-menu", "attachment-cache", "attach-files"} {
		if app.Find("id:"+id).Props.Label == "" {
			t.Fatalf("%s has no accessible name", id)
		}
	}
	app.Screenshot("touch-action-feedback")
}
