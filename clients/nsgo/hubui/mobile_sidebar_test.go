package hubui

import (
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestCompactSidebarPreservesConversation(t *testing.T) {
	testCompactSidebar(t, false)
}

func TestCompactSidebarPreservesConversationDark(t *testing.T) {
	testCompactSidebar(t, true)
}

func testCompactSidebar(t *testing.T, dark bool) {
	t.Helper()
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = newState(cx, &mobileDemo{newDemoAPI()})
		s.dark = dark
		s.applyColors()
		s.installWorkspaceNavigation()
		s.initialize()
		return s.workspaceView(cx)
	}, nsgotest.Size(390, 844), nsgotest.Environment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone, Dark: dark, Locale: "zh-CN"}))
	app.Find("id:mobile-menu")
	app.Find("id:welcome-connect")
	if app.Exists("id:session-sidebar") {
		t.Fatal("compact launch displayed a directory page")
	}
	openDemoChat(t, app)
	app.Type("id:chat-input", "保留当前会话的草稿")
	input, list := app.Find("id:chat-input").ID, app.Find("id:chat-message-list").ID
	var generation uint64
	app.Post(func() { generation = s.sessionGeneration })
	app.Settle()
	app.PressAt("id:mobile-menu", core.Point{X: 22, Y: 22})
	app.WaitFor("id:session-sidebar", time.Second)
	app.Advance(time.Second)
	if (nsgotest.Target() == "" || nsgotest.Target() == "ios_simulator" || nsgotest.Target() == "android") && (app.Exists("id:close-mobile-sidebar") || app.Exists("id:toggle-sidebar")) {
		t.Fatal("mobile drawer includes a collapse button")
	}
	drawer := globalBounds(app.Find("id:mobile-sidebar"))
	if drawer.Size.Width > 390*.88 || drawer.Size.Width < 280 || drawer.Origin.X != 0 {
		t.Fatalf("drawer did not leave a reachable backdrop: %+v", drawer)
	}
	if app.Find("id:chat-input").ID != input || app.Find("id:chat-message-list").ID != list {
		t.Fatal("opening navigation replaced the conversation")
	}
	app.Screenshot(choose(dark, "compact-sidebar-dark", "compact-sidebar-light"))
	app.PressAt("id:compact-workspace", core.Point{X: 380, Y: 300})
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:mobile-sidebar") })
	showSessionDirectory(t, app)
	app.Press("id:connect-device")
	app.WaitFor("id:dialog-connect", time.Second)
	pressWorkspaceBack(t, app)
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:dialog-connect") })
	app.Find("id:session-sidebar")
	pressWorkspaceBack(t, app)
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:mobile-sidebar") })
	if app.Find("id:chat-input").ID != input || app.Find("id:chat-input").Text != "保留当前会话的草稿" {
		t.Fatal("closing navigation changed the editor or draft")
	}
	app.Post(func() {
		if s.sessionGeneration != generation || s.selected == nil {
			t.Error("drawer navigation changed session lifetime")
		}
	})
	app.Settle()
	showSessionDirectory(t, app)
	app.Press(sessionSelector("demo-device", "pi", "demo-pi"))
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:mobile-sidebar") })
	app.WaitFor("id:chat-input", time.Second)
	showSessionDirectory(t, app)
	app.Press(sessionSelector("demo-device", "codex-app", "demo-chat"))
	app.WaitUntil(time.Second, func() bool {
		return !app.Exists("id:mobile-sidebar") && app.Find("id:chat-input").Text == "保留当前会话的草稿"
	})
	app.Screenshot(choose(dark, "compact-workspace-dark", "compact-workspace-light"))
}
