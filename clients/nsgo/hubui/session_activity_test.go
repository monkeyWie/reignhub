package hubui

import (
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestSessionActivityIndicatorsAndCompletion(t *testing.T) {
	var s *State
	var running Object
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = workspaceLayoutFixture(cx)
		s.sessions[1]["status"] = "idle"
		running = copyObject(s.sessions[0])
		s.observeSessionActivity("local", []Object{running})
		return s.workspaceView(cx)
	}, nsgotest.Size(1100, 900), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop, Locale: "zh-CN"}))
	row := sessionRowID("all", running)
	spinner := app.Find("id:running-" + row).ID
	app.Focus("id:chat-input")
	app.Compose("id:chat-input", "wei")
	app.Screenshot("composer-composing-light")
	app.Commit("id:chat-input", "未")
	app.Type("id:chat-input", "发送草稿")
	editor := app.Find("id:chat-input").ID
	checkNeutralInteraction := func(c colorScheme) {
		t.Helper()
		foundInk := false
		var checkDrawing func(*nsgotest.View)
		checkDrawing = func(v *nsgotest.View) {
			for _, op := range v.Ops {
				if op.Color == core.RGB(c.brand) {
					t.Fatal("ordinary loading uses the brand attention color")
				}
				foundInk = foundInk || op.Color == core.RGB(c.secondary)
			}
			for _, child := range v.Children {
				checkDrawing(child)
			}
		}
		checkDrawing(app.Find("id:running-" + row))
		input := app.Find("id:chat-input")
		if input.Input.AccentColor != core.RGB(c.text) || input.Input.AutoCapitalize != core.AutoCapitalizeNone {
			t.Fatal("composer did not configure input-method color and capitalization")
		}
		if !foundInk || input.Input.CaretColor != core.RGB(c.text) {
			t.Fatal("spinner or caret did not follow the neutral theme")
		}
		if app.Find("id:running-"+row).ID != spinner || input.ID != editor || input.Text != "未发送草稿" {
			t.Fatalf("appearance change restarted the spinner or replaced the draft editor: spinner %v → %v, editor %v → %v, draft %q", spinner, app.Find("id:running-"+row).ID, editor, input.ID, input.Text)
		}
	}
	checkNeutralInteraction(lightColors)
	sidebarMenuAction(app, "theme-toggle")
	checkNeutralInteraction(darkColors)
	app.Screenshot("session-running-indicator-dark")
	sidebarMenuAction(app, "theme-toggle")
	checkNeutralInteraction(lightColors)
	app.Post(func() { s.cx.Post(func() { s.notice = "Fixture update"; s.changed() }) })
	app.Settle()
	if app.Find("id:running-"+row).ID != spinner {
		t.Fatal("unrelated update restarted the activity spinner")
	}
	app.Screenshot("session-running-indicator-light")
	if nsgotest.Target() != "macos" {
		app.Hover("id:" + row)
		app.WaitUntil(time.Second, func() bool { return app.Find("id:actions-"+row).Style.Opacity > .5 })
		app.Hover("id:pin-" + row)
		app.Advance(500 * time.Millisecond)
		app.WaitFor("text:置顶会话", time.Second)
		if app.Find("id:pin-"+row).Style.Background != 0 || app.Find("id:status-"+row).Style.Opacity != 0 {
			t.Fatal("running indicator must be hidden while the row actions are visible")
		}
		app.Screenshot("session-contextual-icon-tooltip")
		app.Press("id:pin-" + row)
		if s.pinned[sessionIdentity(running)] == 0 || str(s.selected, "id") != "design" {
			t.Fatal("inline pin was not clickable or opened the conversation")
		}
		row = sessionRowID("pinned", running)
		app.HoverOut()
	}
	// Another Agent with the same native ID cannot settle Codex execution.
	app.Post(func() {
		s.cx.Post(func() {
			alias := copyObject(running)
			alias["providerId"], alias["status"] = "pi", "idle"
			s.observeSessionActivity("local", []Object{alias})
			unknown := copyObject(running)
			unknown["status"] = "unknown"
			s.applyEnvironmentPage("local", "codex-app", []Object{unknown}, true)
		})
	})
	app.Settle()
	if app.Exists("id:unread-" + row) {
		t.Fatal("unknown or another provider's idle state created a completion dot")
	}
	app.Post(func() {
		s.cx.Post(func() {
			completed := copyObject(running)
			completed["status"] = "completed"
			s.applyEnvironmentPage("local", "codex-app", []Object{completed}, true)
		})
	})
	app.WaitFor("id:unread-"+row, time.Second)
	app.WaitFor("id:"+row, time.Second)
	if app.Exists("id:running-" + row) {
		t.Fatal("finished session still spins")
	}
	if app.Find("id:unread-"+row).Style.Background != core.RGB(lightColors.brand) {
		t.Fatal("unread dot does not use the light brand attention color")
	}
	app.Hover("id:" + row)
	if app.Find("id:status-"+row).Style.Opacity != 0 || app.Find("id:actions-"+row).Style.Opacity != 1 {
		t.Fatal("hover must replace the unread dot with the row actions")
	}
	app.HoverOut()
	if app.Find("id:status-"+row).Style.Opacity == 0 {
		t.Fatal("unread indicator did not return after hover")
	}
	app.Screenshot("session-completion-dot-light")
	sidebarMenuAction(app, "theme-toggle")
	if app.Find("id:unread-"+row).Style.Background != core.RGB(darkColors.brand) {
		t.Fatal("unread dot does not follow the dark brand attention color")
	}
	app.Screenshot("session-completion-dot-dark")
	app.Post(func() {
		s.cx.Post(func() {
			restored := newState(s.cx, newDemoAPI())
			if !restored.unreadSessions[activityIdentity(running)] {
				t.Error("completion acknowledgement state did not survive restart")
			}
		})
	})
	app.Settle()
	app.Press("id:" + row)
	if app.Exists("id:unread-" + row) {
		t.Fatal("opening the completed conversation did not clear its dot")
	}
}

func TestCompletionIgnoresFailureOfflineAndInitialHistory(t *testing.T) {
	nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := workspaceLayoutFixture(cx)
		ss := copyObject(s.sessions[0])
		for _, terminal := range []string{"idle", "completed", "failed", "interrupted", "unknown", "notLoaded"} {
			ss["status"] = terminal
			s.observeSessionActivity("local", []Object{ss})
		}
		if len(s.unreadSessions) != 0 {
			t.Fatal("initial history was marked unread")
		}
		for _, terminal := range []string{"failed", "interrupted", "systemError"} {
			ss["status"] = "running"
			s.observeSessionActivity("local", []Object{ss})
			ss["status"] = terminal
			s.observeSessionActivity("local", []Object{ss})
		}
		ss["status"] = "running"
		s.observeSessionActivity("local", []Object{ss})
		s.devices[0]["connected"] = false
		ss["status"] = "completed"
		s.observeSessionActivity("local", []Object{ss})
		if len(s.unreadSessions) != 0 {
			t.Fatal("failure or offline cache was marked completed")
		}
		return core.View{}
	})
}
