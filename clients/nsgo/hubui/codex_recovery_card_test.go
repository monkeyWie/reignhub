package hubui

import (
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/system"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestCodexRecoveryCardHoverFocusAndRefresh(t *testing.T) {
	if !system.CodexSharedDesktop {
		t.Skip("local desktop restart requires a supported host")
	}
	var s *State
	var changed func(bool)
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = sidebarInteractionFixture(cx)
		s.localProviders = demoProviders()
		s.localProviders[0]["endpoint"] = "ipc-recovery"
		ss := copyObject(s.sessions[0])
		ss["codexOwnerUnreachable"] = true
		s.sessions = []Object{ss}
		w := s.workspace("local")
		w.providers, w.sessions = s.localProviders, s.sessions
		s.workspaces["local"] = w
		changed = func(failed bool) {
			next := copyObject(ss)
			next["codexOwnerUnreachable"] = failed
			s.applyEnvironmentPage("local", "codex-app", []Object{next}, true)
		}
		return core.View{Style: core.Style{Width: core.Pt(360), Height: core.Pct(100)}, Children: []core.Node{s.directory(cx)}}
	}, nsgotest.Size(760, 600), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop, Locale: "zh-CN"}))
	row := sessionRowID("all", s.sessions[0])
	icon, panel, restart := "id:codex-warning-"+row, "id:codex-recovery-"+row, "id:codex-restart-"+row
	iconID := app.Find(icon).ID
	if app.Exists(panel) || app.Exists("id:running-"+row) {
		t.Fatal("closed warning must occupy a single status slot")
	}
	before := globalBounds(app.Find(icon))
	footer := globalBounds(app.Find("id:new-session"))
	column := footer.Origin.X + footer.Size.Width/2
	app.Hover(icon)
	app.Advance(250 * time.Millisecond)
	app.WaitFor(panel, time.Second)
	after, actions := globalBounds(app.Find(icon)), globalBounds(app.Find("id:actions-"+row))
	if before != after || after.Origin.X+after.Size.Width/2 != column || actions.Origin.X+actions.Size.Width > after.Origin.X || app.Find("id:actions-"+row).Style.Opacity != 1 {
		t.Fatal("warning must stay in the footer action column with hover actions on its left", before, after, actions, footer)
	}
	app.Screenshot("codex-session-recovery-card")
	app.Hover(restart)
	app.Advance(450 * time.Millisecond)
	restartID := app.Find(restart).ID
	app.Post(func() { changed(true) })
	app.Settle()
	if app.Find(restart).ID != restartID || app.Find(icon).ID != iconID || app.Exists("id:session-hover-card") {
		t.Fatal("unchanged background directory replaced or obscured the recovery action")
	}
	app.HoverOut()
	app.Advance(450 * time.Millisecond)
	app.WaitUntil(time.Second, func() bool { return !app.Exists(panel) })
	app.Focus(icon)
	app.Advance(250 * time.Millisecond)
	app.WaitFor(panel, time.Second)
	app.Key("tab")
	app.Key("enter")
	if kind := s.dialog.GetUntracked(); !strings.HasPrefix(kind, "restart-session-codex:") {
		t.Fatalf("keyboard did not open the restart confirmation: %q", kind)
	}
	app.Post(func() { changed(false) })
	app.WaitUntil(time.Second, func() bool { return !app.Exists(icon) && !app.Exists(panel) })
	remove := globalBounds(app.Find("id:remove-" + row))
	if remove.Origin.X+remove.Size.Width/2 != column {
		t.Fatal("archive and new session icons must share a column", remove, footer)
	}
}

func TestRemoteCodexRecoveryCardTouchNamesDevice(t *testing.T) {
	var ss Object
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = sidebarInteractionFixture(cx)
		ss = copyObject(s.sessions[1])
		ss["providerId"], ss["codexOwnerUnreachable"] = "codex-app", true
		s.sessions = []Object{ss}
		w := s.workspace("remote")
		w.sessions = s.sessions
		s.workspaces["remote"] = w
		return s.directory(cx)
	}, nsgotest.Size(390, 700), nsgotest.Environment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone, Locale: "zh-CN"}))
	row := sessionRowID("all", ss)
	app.Press("id:codex-warning-" + row)
	app.WaitFor("id:codex-recovery-"+row, time.Second)
	app.Find("text:此会话暂时无法发送消息或同步回复。请在「Linux Studio」的 ReignHub 上重启 Codex App。")
	if app.Exists("id:codex-restart-"+row) || s.selected != nil {
		t.Fatal("opening a remote warning selected the conversation or exposed local restart")
	}
}
