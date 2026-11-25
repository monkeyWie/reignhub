package hubui

import (
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestDesktopMessageSelectionCopy(t *testing.T) {
	if target := nsgotest.Target(); target != "" && target != "macos" && target != "windows" && target != "linux" {
		t.Skip("native desktop selection")
	}
	const line = "选择这段文字"
	for _, role := range []string{"user", "assistant"} {
		t.Run(role, func(t *testing.T) {
			app := startSelectionFixture(t,
				Object{"id": "neighbor", "role": choose(role == "user", "assistant", "user"), "text": "相邻消息"},
				Object{"id": "fixture", "role": role, "text": "第一段\n\n" + line + "\n\n第三段"},
			)
			const bodyID = "id:message-body-turn|fixture"
			body := app.Find(bodyID)
			selected := app.Find("text:" + line)
			for v := body.Parent; v != nil; v = v.Parent {
				if v.Props.SelectionArea {
					t.Fatal("a selectable ancestor includes content outside this message")
				}
			}
			if !needsMessageSelectionMenu {
				contextEvent := app.Find("id:context-event-probe").Events
				for v := selected; v != nil; v = v.Parent {
					if v.Events&contextEvent != 0 {
						t.Fatal("a message ancestor intercepts the system text menu")
					}
				}
			}
			if nsgotest.Target() == "" {
				app.Focus("id:copy-message-turn|fixture")
				app.SelectTextRange(bodyID, "text:"+line, 2, "text:"+line, 4)
				app.Key("mod+c")
				if app.Clipboard != "这段" {
					t.Fatalf("shortcut copied %q", app.Clipboard)
				}
				if needsMessageSelectionMenu {
					app.RightClick("text:相邻消息")
					if len(app.PopupMenu) != 0 {
						t.Fatal("opened a menu for the neighboring message")
					}
					app.RightClick("text:" + line)
					// The selection snapshot survives a focus change while the menu is open.
					app.SelectText(bodyID, core.Bounds{})
					app.ChoosePopup("Copy")
					if app.Clipboard != "这段" {
						t.Fatalf("menu copied %q", app.Clipboard)
					}
				}
				app.Type("id:chat-input", "draft")
				app.Key("mod+a")
				app.Key("mod+c")
				if app.Clipboard != "draft" {
					t.Fatalf("composer copied %q", app.Clipboard)
				}
				return
			}
			if needsMessageSelectionMenu {
				app.DragAt("text:"+line, core.Point{X: 1, Y: selected.Bounds().Size.Height / 2}, core.Point{X: 30}, core.Point{X: 100})
				app.RightClick("text:" + line)
				if items := app.PopupMenu; len(items) != 2 || items[0].Label != "Copy" || items[1].Label != "Select all" || items[1].Role != core.MenuSelectAll {
					t.Fatalf("unexpected selection menu: %+v", items)
				}
				app.ChoosePopup("Copy")
				if app.Clipboard != line {
					t.Fatalf("menu copied %q", app.Clipboard)
				}
				// The native test driver can activate named application callbacks.
				// For a system role, its keyboard equivalent reaches the same editor.
				app.Key("mod+a")
				app.RightClick("text:" + line)
				app.ChoosePopup("Copy")
				if app.Clipboard != "第一段\n"+line+"\n第三段" {
					t.Fatalf("Select All and Copy returned %q", app.Clipboard)
				}
			}
		})
	}
}

func startSelectionFixture(t *testing.T, messages ...Object) *nsgotest.App {
	t.Helper()
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := workspaceLayoutFixture(cx)
		s.setTranscript(outgoingTranscript(messages...))
		return core.Group{
			s.workspaceView(cx),
			core.View{TestID: "context-event-probe", Style: core.Style{Position: core.Absolute, Top: core.Pt(0), Left: core.Pt(0), Width: core.Pt(1), Height: core.Pt(1)}, OnContextMenu: func(core.ContextMenuEvent) {}},
		}
	}, nsgotest.Size(1100, 820), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop, Locale: "en"}))
	app.WaitUntil(time.Second, func() bool { return app.Find("id:chat-input").Focused })
	return app
}
