package hubui

import (
	"testing"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestDesktopWindowFrame(t *testing.T) {
	if nsgotest.Target() != "" {
		t.Skip("window action recording uses the simulated platform")
	}
	for _, os := range []core.OS{core.OSMacOS, core.OSWindows, core.OSLinux} {
		t.Run(string(os), func(t *testing.T) {
			app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, newDemoAPI(), false) }, nsgotest.Size(1000, 800), nsgotest.Environment(core.Environment{OS: os, Idiom: core.IdiomDesktop}))
			hidden, controls := false, false
			for _, command := range app.Window {
				if command.Action == nsgotest.WindowSetTitleBar && command.TitleBar == core.TitleBarHidden {
					hidden = true
				}
				if command.Action == nsgotest.WindowSetControls && command.Controls == (core.WindowControls{}) {
					controls = true
				}
			}
			if !hidden || !controls {
				t.Fatal("title bar must be hidden while platform window controls stay visible")
			}
			if app.Find("id:window-drag-region").Props.WindowDrag != core.WindowDragOn {
				t.Fatal("missing drag region")
			}
			if os != core.OSMacOS {
				bar := globalBounds(app.Find("id:window-toolbar"))
				for _, id := range []string{"brand-home", "connect-device", "toggle-sidebar"} {
					bounds := globalBounds(app.Find("id:" + id))
					if bounds.Origin.Y < bar.Origin.Y || bounds.Origin.Y+bounds.Size.Height > bar.Origin.Y+bar.Size.Height {
						t.Fatalf("%s occupies a second header row: %v, toolbar %v", id, bounds, bar)
					}
				}
				app.Press("id:connect-device")
				app.Find("id:dialog-connect")
				app.Press("id:dialog-connect-close")
				app.Press("id:toggle-sidebar")
				app.Press("id:toggle-sidebar")
				app.Find("id:brand-home")
			} else if globalBounds(app.Find("id:brand-home")).Origin.Y < windowBarHeight {
				t.Fatal("macOS sidebar heading overlaps the traffic-light row")
			}
			for _, id := range []string{"window-minimize", "window-maximize", "window-close"} {
				if app.Exists("id:"+id) != (os == core.OSLinux) {
					t.Fatalf("%s: only Linux needs app-drawn controls", id)
				}
			}
			if os == core.OSLinux {
				for _, id := range []string{"window-minimize", "window-maximize", "window-close"} {
					if app.Find("id:"+id).Props.WindowDrag != core.WindowDragOff {
						t.Fatal("window control must not start dragging")
					}
					app.Press("id:" + id)
				}
				commands := app.Window[len(app.Window)-3:]
				for i, action := range []nsgotest.WindowAction{nsgotest.WindowMinimize, nsgotest.WindowToggleMaximize, nsgotest.WindowClose} {
					if commands[i].Action != action {
						t.Fatalf("Linux control action %d: %v", i, commands[i])
					}
				}
			}
			// Native caption buttons can be taller or sit on either side.
			insets := core.Edges{Top: 48, Left: 80, Right: 138}
			app.SetEnvironment(core.Environment{OS: os, Idiom: core.IdiomDesktop, TitleBar: insets})
			bar := globalBounds(app.Find("id:window-toolbar"))
			drag := globalBounds(app.Find("id:window-drag-region"))
			if bar.Size.Height != 48 || drag.Origin.X != 80 || drag.Origin.X+drag.Size.Width > 1000-138 {
				t.Fatalf("native caption controls overlap the drag region: bar=%v drag=%v", bar, drag)
			}
			app.Resize(390, 844)
			toolbar := globalBounds(app.Find("id:window-toolbar"))
			content := globalBounds(app.Find("id:agenthub"))
			if content.Origin.Y != toolbar.Size.Height || toolbar.Size.Width != 390 {
				t.Fatalf("narrow window overlaps content: bar=%v content=%v", toolbar, content)
			}
			app.SetEnvironment(core.Environment{OS: os, Idiom: core.IdiomDesktop, FullScreen: true})
			if app.Exists("id:window-toolbar") {
				t.Fatal("window controls remain in full screen")
			}
			if globalBounds(app.Find("id:agenthub")).Origin.Y != 0 {
				t.Fatal("full screen retains title bar gap")
			}
			app.Resize(1000, 800)
			app.Press("id:toggle-sidebar")
			if !app.Exists("id:workspace-toolbar") {
				t.Fatal("full-screen sidebar restore control is missing")
			}
			app.SetEnvironment(core.Environment{OS: os, Idiom: core.IdiomDesktop, TitleBar: insets})
			app.Press("id:toggle-sidebar")
			if globalBounds(app.Find("id:sidebar-container")).Size.Width != 300 {
				t.Fatal("could not restore sidebar after leaving full screen")
			}
		})
	}
}
func TestNoDesktopFrameOnMobileOrWeb(t *testing.T) {
	if nsgotest.Target() != "" {
		t.Skip("simulated platform environments")
	}
	for _, os := range []core.OS{core.OSAndroid, core.OS("ios"), core.OS("web")} {
		t.Run(string(os), func(t *testing.T) {
			app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, newDemoAPI(), false) }, nsgotest.Size(390, 844), nsgotest.Environment(core.Environment{OS: os}))
			if app.Exists("id:window-toolbar") || len(app.Window) > 0 {
				t.Fatal("desktop frame shown on mobile or Web")
			}
		})
	}
}
func TestWindowFrameAppearance(t *testing.T) {
	app := startDemo(t, 1320, 880)
	openDemoChat(t, app)
	app.Screenshot("window-frame-light")
	sidebarMenuAction(app, "theme-toggle")
	app.Screenshot("window-frame-dark")
	if nsgotest.Target() == "web" && app.Exists("id:window-toolbar") {
		t.Fatal("browser must not show desktop controls")
	}
}
