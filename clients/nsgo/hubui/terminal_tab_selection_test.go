//go:build darwin || windows || (linux && !android) || (js && wasm && !nsgominiprogram && !nsgoharmony)

package hubui

import (
	"testing"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestTerminalTabFillFollowsSelection(t *testing.T) {
	for _, appearance := range []string{"light", "dark"} {
		t.Run(appearance, func(t *testing.T) {
			panel := &terminalPanel{}
			panel.addTab()
			panel.addTab()
			app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
				s := newState(cx, newDemoAPI())
				s.applyColors()
				s.setAppearance(appearance)
				return base.Tabs{Style: core.Style{Direction: core.Row}, Children: []core.Node{
					s.terminalTabButton(cx, panel, panel.tabs[0]),
					s.terminalTabButton(cx, panel, panel.tabs[1]),
				}}
			}, nsgotest.Size(600, 300), nsgotest.Environment(core.Environment{Idiom: core.IdiomDesktop, OS: core.OSWindows}))
			const first, second = "id:terminal-tab-1", "id:terminal-tab-2"
			check := func(active int) {
				t.Helper()
				for i, selector := range []string{first, second} {
					tab := app.Find(selector)
					_, _, _, alpha := tab.Parent.Style.Background.Components()
					filled := alpha > 0
					if want := i+1 == active; tab.Props.Selected != want || filled != want {
						t.Fatalf("tab %d selected=%v filled=%v, active tab %d", i+1, tab.Props.Selected, filled, active)
					}
				}
			}
			check(2)
			app.Hover(first)
			check(2)
			app.Press(first)
			check(1)
			app.Hover("id:terminal-close-2")
			check(1)
			app.Focus(first)
			app.Key("right")
			check(2)
			app.HoverOut()
			check(2)
		})
	}
}
