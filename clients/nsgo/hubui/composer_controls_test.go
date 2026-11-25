package hubui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

type composerSettingsAPI struct {
	*demoAPI
	mu                          sync.Mutex
	effort                      string
	permission, permissionLabel string
	writes                      chan Object
	result                      chan error
}

func (a *composerSettingsAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.call" {
		switch str(r, "method") {
		case "composer.settings":
			a.mu.Lock()
			defer a.mu.Unlock()
			controls := []Object{{"id": "permission", "label": "权限", "selected": a.permission, "selectedLabel": a.permissionLabel, "choices": []Object{{"id": "ask", "label": "请求批准"}, {"id": "full", "label": "完全访问"}}}}
			if str(r, "model") == "test-model" {
				controls = append(controls, Object{"id": "effort", "label": "推理级别", "selected": a.effort, "choices": []Object{{"id": "low", "label": "低"}, {"id": "medium", "label": "中"}, {"id": "high", "label": "高"}, {"id": "max", "label": "最高"}}})
			}
			return controls, nil
		case "composer.configure":
			params := object(r["params"])
			a.writes <- params
			select {
			case err := <-a.result:
				if err == nil && str(params, "control") == "effort" {
					a.mu.Lock()
					a.effort = str(params, "value")
					a.mu.Unlock()
				}
				return nil, err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return a.demoAPI.Call(ctx, method, p, target, r)
}

func TestComposerSettingsApplyAndFollowModel(t *testing.T) {
	testComposerControls(t, false)
}

func TestComposerSettingsKeyboard(t *testing.T) {
	testComposerControls(t, true)
}

func testComposerControls(t *testing.T, keyboard bool) {
	for _, width := range []core.Points{390, 1100} {
		t.Run(choose(width < 500, "phone", "desktop"), func(t *testing.T) {
			a := &composerSettingsAPI{demoAPI: newDemoAPI(), permission: "ask", effort: choose(keyboard, "high", "low"), writes: make(chan Object, 1), result: make(chan error, 1)}
			app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
				cx.SetTheme(core.DefaultTheme())
				s := newState(cx, a)
				s.applyColors()
				if keyboard {
					cx.SetAppearance(core.AppearanceDark)
				}
				s.width.Set(width)
				s.devices = []Object{{"id": "demo-device", "connected": true}}
				s.target = "demo-device"
				s.selected = Object{"id": "demo-chat", "providerId": "codex-app"}
				s.model = "test-model"
				s.providers = []Object{{"id": "codex-app", "capabilities": Object{"composerSettings": true}}}
				s.models = []Object{{"id": "test-model", "name": "First model", "imageInput": true}, {"id": "model-b", "name": "Second model"}}
				return s.windowFrame(cx, core.View{Style: core.Style{Padding: core.Edges{Top: 84}}, Children: []core.Node{s.composer(cx)}})
			}, nsgotest.PendingAlerts(), nsgotest.Size(width, 380), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop, Locale: "zh-CN"}))
			app.WaitFor("text:First model "+choose(keyboard, "高", "低"), 3*time.Second)
			if keyboard {
				app.Screenshot("composer-model-caption")
			}
			app.Type("id:chat-input", "保留草稿")

			editor := app.Find("id:chat-input").ID
			activate := func(selector string) {
				t.Helper()
				if keyboard {
					app.Focus(selector)
					app.Key("enter")
				} else {
					app.Press(selector)
				}
			}
			activate("id:model-selector")
			app.WaitFor("id:effort-thumb", 2*time.Second)
			app.Find("text:视觉")
			app.Type("id:model-search", "SECOND")
			if app.Exists("role:dialog >> role:button First model") || !app.Exists("role:dialog >> role:button Second model") {
				t.Fatal("model search did not filter the list")
			}
			app.Type("id:model-search", "unlisted")
			app.Find("text:没有匹配的模型")
			app.Key("escape")
			app.WaitUntil(2*time.Second, func() bool { return !app.Exists("id:model-search") })
			activate("id:model-selector")
			app.WaitFor("id:effort-thumb", 2*time.Second)
			heading := app.Find("id:effort-heading").Frame
			rail := app.Find("id:composer-setting-effort").Frame
			if rail.Origin.Y < heading.Origin.Y+heading.Size.Height || rail.Size.Width < 200 {
				t.Fatal("effort slider needs its own full row", heading, rail)
			}
			if keyboard {
				dot := "id:effort-stop-medium"
				app.Screenshot("composer-effort-rest")
				app.Hover(dot)
				app.Advance(220 * time.Millisecond)
				// The headless driver exposes animated values. Native targets capture the rendered frames.
				v := app.Find(dot).Animated
				if v.Set != 0 && v.Scale <= 1.4 {
					t.Fatal("hover did not enlarge the stop", v.Scale)
				}
				app.Screenshot("composer-effort-hover")
				app.Advance(450 * time.Millisecond)
				if v.Set != 0 && app.Find(dot).Animated.Scale == v.Scale {
					t.Fatal("the hovered stop stayed at a fixed scale")
				}
				app.HoverOut()
				app.Advance(200 * time.Millisecond)
				if v := app.Find(dot).Animated; (v.Set != 0 && v.Scale != 1) || len(a.writes) != 0 {
					t.Fatal("hover changed the effort or left an enlarged stop", v.Scale)
				}
				app.Focus("id:effort-thumb")
				app.Key("end")
			} else {
				track := app.Find("id:composer-setting-effort").Frame.Size.Width
				thumb := app.Find("id:effort-thumb").Frame.Size.Width
				app.Drag("id:effort-thumb", core.Point{X: track - thumb})
			}
			app.WaitFor("id:composer-setting-saving", time.Second)
			app.Find("id:model-selector >> text:选择模型")
			for _, level := range []string{"low", "medium", "high", "max"} {
				app.Find("id:effort-stop-" + level)
			}
			change := <-a.writes
			if str(change, "model") != "test-model" || str(change, "control") != "effort" || str(change, "value") != "max" || !app.Find("id:send-message").Props.Disabled {
				t.Fatal("incorrect in-flight change", change)
			}
			a.result <- nil
			app.WaitUntil(time.Second, func() bool { return !app.Exists("id:model-effort-saving") && app.Exists("text:最高") })
			app.Screenshot("composer-model-picker-" + choose(keyboard, "keyboard", "drag") + "-" + choose(width < 500, "narrow", "wide"))
			if keyboard {
				if app.Exists("text:⌃ T") {
					app.Key("ctrl+t")
				} else {
					app.MouseDown("id:effort-stop-low", core.MouseButtonLeft)
					app.MouseUp("id:effort-stop-low", core.MouseButtonLeft)
				}
				app.WaitFor("id:model-effort-saving", time.Second)
				if change := <-a.writes; str(change, "value") != "low" {
					t.Fatal("the picker requested the wrong level", change)
				}
				a.result <- errors.New("Agent declined the change")
				app.WaitUntil(time.Second, func() bool { return !app.Exists("id:model-effort-saving") && app.Exists("text:最高") })
			}
			app.Key("escape")
			app.WaitUntil(time.Second, func() bool { return !app.Exists("id:model-search") })
			app.Find("text:First model 最高")
			if keyboard {
				app.Press("id:dismiss-error")
				app.Press("id:chat-input")
			}
			activate("id:composer-setting-permission")
			app.WaitFor("role:button 完全访问", 2*time.Second)
			activate("role:button 完全访问")
			app.WaitUntil(time.Second, func() bool { return len(app.Alerts()) == 1 })
			if len(a.writes) != 0 || app.Alerts()[0].Actions[0].Role != core.AlertCancel {
				t.Fatal("selecting full access wrote before confirmation or omitted Cancel")
			}
			app.Answer(0)
			if len(a.writes) != 0 || app.Exists("id:composer-setting-saving") {
				t.Fatal("cancelling changed the session permission")
			}
			app.Press("id:chat-input")
			activate("id:composer-setting-permission")
			activate("role:button 完全访问")
			app.WaitUntil(time.Second, func() bool { return len(app.Alerts()) == 1 })
			app.Answer(1)
			app.WaitFor("id:composer-setting-saving", time.Second)
			<-a.writes
			a.result <- errors.New("Agent 拒绝了所选权限")
			app.WaitUntil(time.Second, func() bool { return !app.Exists("id:composer-setting-saving") })
			app.Find("text:请求批准")
			// Resume keyboard navigation in the fixture window after the simulated system alert.
			app.Press("id:chat-input")
			activate("id:model-selector")
			app.WaitFor("role:button Second model", 2*time.Second)
			activate("role:button Second model")
			app.WaitUntil(time.Second, func() bool {
				return app.Exists("id:composer-setting-permission") && !app.Exists("id:composer-setting-effort")
			})
			app.Key("escape")
			app.WaitFor("role:button Second model", time.Second)
			if app.Exists("text:Second model 最高") {
				t.Fatal("model inherited the previous reasoning level")
			}
			if v := app.Find("id:chat-input"); v.Text != "保留草稿" || v.ID != editor {
				t.Fatal("settings replaced draft or editor")
			}
		})
	}
}

func TestComposerPermissionFollowsOwnerSnapshot(t *testing.T) {
	a := &composerSettingsAPI{demoAPI: newDemoAPI()}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s = newState(cx, a)
		s.target = "demo-device"
		s.devices = []Object{{"id": s.target, "connected": true}}
		s.selected = Object{"id": "demo-chat", "providerId": "codex-app"}
		s.providers = []Object{{"id": "codex-app", "capabilities": Object{"composerSettings": true}}}
		s.stream = &sessionStream{s: s, ctx: s.ctx, key: s.key(), epoch: "owner", baseline: true}
		s.transcript = Object{"turns": []Object{{"id": "in-progress", "status": "inProgress"}}}
		s.watchComposerControls(cx)
		return s.composerSettingsRow(cx)
	}, nsgotest.Environment(core.Environment{Locale: "zh-CN"}))
	app.WaitFor("text:权限未同步", time.Second)
	a.mu.Lock()
	a.permissionLabel = "团队沙箱"
	a.mu.Unlock()
	app.Post(func() {
		s.stream.batch(Object{"epoch": "owner", "cursor": 1, "events": []Object{{"kind": "composerSettings"}}})
	})
	app.WaitFor("id:composer-setting-permission >> text:团队沙箱", time.Second)
	app.Press("id:composer-setting-permission")
	app.WaitFor("role:button 请求批准", time.Second)
	if app.Exists("role:dialog >> role:button 团队沙箱") {
		t.Fatal("a display-only permission was offered as a writable choice")
	}
}

func TestFullAccessConsentBelongsToTheSelectedSession(t *testing.T) {
	a := &composerSettingsAPI{demoAPI: newDemoAPI(), writes: make(chan Object, 2), result: make(chan error, 1)}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s = newState(cx, a)
		s.target, s.model = "fixture", "model"
		s.selected = Object{"id": "first", "providerId": "example-acp"}
		s.devices = []Object{{"id": "fixture", "name": "Test Mac", "connected": true}}
		s.providers = []Object{{"id": "example-acp", "name": "Test Agent", "capabilities": Object{"composerSettings": true}}}
		s.composerControlsModel = s.model
		s.composerControls = []Object{{"id": "acp:trust", "label": "Mode", "selected": "ask", "choices": []Object{
			{"id": "ask", "label": "Ask"}, {"id": "bypassPermissions", "label": "Full access", "fullAccess": true},
		}}}
		return s.composerSettingsRow(cx)
	}, nsgotest.PendingAlerts())
	selectFull := func() {
		app.Press("id:composer-setting-acp:trust")
		app.WaitFor("role:button Full access", time.Second)
		app.Press("role:button Full access")
		app.WaitUntil(time.Second, func() bool { return len(app.Alerts()) == 1 })
		msg := app.Alerts()[0].Message
		if !strings.Contains(msg, "Test Agent") || !strings.Contains(msg, "Test Mac") {
			t.Fatal("confirmation omitted its agent or device", msg)
		}
	}
	selectFull()
	app.Answer(-1)
	selectFull()
	app.Post(func() {
		s.selected = Object{"id": "second", "providerId": "example-acp"}
		s.sessionGeneration++
		s.changed()
	})
	app.Settle()
	app.Answer(1)
	if len(a.writes) > 0 {
		t.Fatal("dismissed or stale confirmation changed permissions")
	}
	selectFull()
	app.Answer(1)
	app.WaitUntil(time.Second, func() bool { return len(a.writes) == 1 })
	change := <-a.writes
	if str(change, "control") != "acp:trust" || str(change, "value") != "bypassPermissions" {
		t.Fatal("confirmed the wrong permission", change)
	}
	a.result <- nil
}

type delayedPermissionsAPI struct {
	*composerSettingsAPI
	requests chan chan string
}

func (a *delayedPermissionsAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method != "client.call" || str(r, "method") != "composer.settings" {
		return a.composerSettingsAPI.Call(ctx, method, p, target, r)
	}
	response := make(chan string, 1)
	select {
	case a.requests <- response:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case value := <-response:
		return []Object{{"id": "permission", "selectedLabel": value}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSessionEntryDisplaysCachedPermissionBeforeOwnerReplies(t *testing.T) {
	api := &delayedPermissionsAPI{&composerSettingsAPI{demoAPI: newDemoAPI()}, make(chan chan string, 12)}
	var s *State
	selectChat := func(id string) {
		s.selectSession(Object{"deviceId": "demo-device", "id": id, "providerId": "codex-app"})
	}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s = newState(cx, api)
		s.target = "demo-device"
		s.providers = []Object{{"id": "codex-app", "capabilities": Object{"composerSettings": true}}}
		s.devices = []Object{{"id": s.target, "connected": true}}
		selectChat("first")
		gen := core.NewMemo(cx, func() uint64 { s.read(); return s.sessionGeneration })
		return core.Dynamic(func(cx *core.Scope) core.Node {
			gen.Get()
			s.watchComposerControls(cx)
			return s.composerSettingsRow(cx)
		})
	})
	// A watch can invalidate the first request while the fixture joins.
	replyUntil := func(value string) {
		t.Helper()
		app.WaitUntil(2*time.Second, func() bool {
			select {
			case reply := <-api.requests:
				reply <- value
			default:
			}
			return app.Exists("text:" + value)
		})
	}
	replyUntil("权限甲")
	app.Post(func() { selectChat("second") })
	replyUntil("权限乙")
	app.Post(func() { selectChat("first") })
	app.WaitFor("text:权限甲", time.Second)
	if app.Exists("text:权限乙") {
		t.Fatal("another session's permission leaked into the composer")
	}
	replyUntil("权限甲更新")
}
