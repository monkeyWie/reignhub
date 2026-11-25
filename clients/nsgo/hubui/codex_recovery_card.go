package hubui

import (
	"time"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

func codexWarningIcon(cx *core.Scope, id string) core.View {
	v := core.View{TestID: "codex-warning-" + id, Style: core.Style{Width: core.Pt(30), Height: core.Pt(30), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
		Children: []core.Node{core.Icon{SVG: lucide.CircleAlert(), Style: core.Style{Width: core.Pt(16), Height: core.Pt(16)}, StyleFunc: func() core.Style { return core.Style{Color: cx.Palette().Warning} }}}}
	v.Role, v.Label, v.Ref = core.RoleButton, locales.For(cx).CodexSessionUnavailable(), new(core.Ref)
	v.StyleFunc = func() core.Style { return contextualIconStyle(cx) }
	if !cx.Env().Desktop() {
		v.Style.Width, v.Style.Height = core.Pt(40), core.Pt(40)
	}
	return v
}

func (s *State) codexRecoveryCard(cx *core.Scope, id string, item func() directoryRow, open *core.Signal[bool], suppressed func() bool, trigger core.View) core.Node {
	l := locales.For(cx)
	actionRef := new(core.Ref)
	trigger.Expanded, trigger.ShowsExpanded = open.Get, true
	if cx.Env().Desktop() {
		trigger.OnPress = func(core.PressEvent) { open.Set(true) }
		// The floating panel lives outside the sidebar's tab sequence.
		trigger.OnKeyDown = func(e core.KeyEvent) bool {
			if e.Key != "tab" || e.Modifiers != (core.Modifiers{}) || !open.GetUntracked() || !s.canRestartSessionCodex(item().Session) {
				return false
			}
			if pending, _ := s.sessionCodexRecoveryFeedback(item().Session); pending {
				return false
			}
			actionRef.Focus()
			return true
		}
	}
	card := tooltipCard(cx)
	panel := core.View{TestID: "codex-recovery-" + id, Style: core.Style{Gap: 8, MinWidth: core.Pt(0)}, Children: []core.Node{
		dynamicText(cx, func() string {
			s.read()
			ss := item().Session
			pending, failure := s.sessionCodexRecoveryFeedback(ss)
			switch {
			case pending:
				return l.CodexSessionReconnecting()
			case failure != "":
				return failure
			case s.canRestartSessionCodex(ss):
				return l.CodexSessionRecovery()
			case str(ss, "deviceId") != "" && str(ss, "deviceId") != control.LocalTarget:
				return l.CodexSessionRecoveryRemote(s.environmentName(str(ss, "deviceId")))
			default:
				return l.CodexSessionRecoveryUnavailable()
			}
		}, 13, false),
		core.Show{When: func() bool {
			s.read()
			return s.canRestartSessionCodex(item().Session)
		}, Then: func(cx *core.Scope) core.Node {
			failed := core.NewMemo(cx, func() bool {
				s.read()
				_, failure := s.sessionCodexRecoveryFeedback(item().Session)
				return failure != ""
			})
			return core.Dynamic(func(cx *core.Scope) core.Node {
				title := choose(failed.Get(), l.CodexRecoveryRetry(), l.RestartCodexApp())
				btn := button(cx, "codex-restart-"+id, title, lucide.RotateCw, "outline", func() {
					open.Set(false)
					s.restartSessionCodex(item().Session)
				}).(core.View)
				btn.Ref = actionRef
				btn.OnKeyDown = func(e core.KeyEvent) bool {
					if e.Key == "tab" && e.Modifiers == (core.Modifiers{Shift: true}) {
						trigger.Ref.Focus()
						return true
					}
					return false
				}
				btn.Disabled = func() bool { s.read(); pending, _ := s.sessionCodexRecoveryFeedback(item().Session); return pending }
				return btn
			})
		}},
	}}
	return base.HoverCard{Open: func() bool { return open.Get() && !suppressed() }, OnOpenChange: func(value bool, _ base.Reason) { open.Set(value && !suppressed()) },
		OpenDelay: 200 * time.Millisecond, Placement: core.PlaceRight, Offset: 4, Margin: 8,
		PopupStyle: card.PopupStyle, PopupStyleFunc: func() core.Style {
			st := card.PopupStyleFunc()
			st.Width = core.Pt(min(280, max(120, cx.Env().Window().Width-24)))
			return st
		},
		Trigger: trigger, Children: []core.Node{panel}}
}
