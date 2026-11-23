//go:build (darwin && !ios) || windows || (linux && !android)

package hubui

import (
	"context"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	model "github.com/monkeyWie/reignhub/internal/terminal"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

func (s *State) terminalPane(cx *core.Scope, p *terminalPanel) core.Node {
	// Only changing the selected shell or explicitly reconnecting remounts its receiver.
	type attachment struct {
		tab      *terminalTab
		revision int
	}
	active := core.NewMemo(cx, func() attachment {
		s.read()
		if p.active == nil {
			return attachment{}
		}
		return attachment{p.active, p.active.revision}
	})
	msgs := locales.For(cx)
	tabs := base.Tabs{Label: msgs.TerminalTabs(), Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 4}, Children: []core.Node{
		core.Each(func() []*terminalTab { s.read(); return p.tabs }, func(t *terminalTab) int { return t.number },
			func(cx *core.Scope, item func() *terminalTab, _ func() int) core.Node {
				return s.terminalTabButton(cx, p, core.Untracked(cx, item))
			}),
	}}
	add := contextualIconButton(cx, "terminal-add", msgs.NewTerminal(), lucide.Plus, func() {
		if len(p.tabs) < model.MaxTerminals {
			p.addTab()
			s.changed()
		}
	})
	add.Disabled = func() bool { s.read(); return len(p.tabs) >= model.MaxTerminals }
	kill := contextualIconButton(cx, "terminal-terminate", msgs.TerminateTerminal(), lucide.Trash, func() { s.closeTerminal(p, p.active, false) })
	kill.Disabled = func() bool { s.read(); return p.active == nil || p.active.closing }
	hide := contextualIconButton(cx, "terminal-hide", msgs.HideTerminal(), lucide.Triangle, s.toggleTerminal)
	hide.Children = []core.Node{core.Icon{SVG: lucide.Triangle(), Style: core.Style{
		Width: core.Pt(12), Height: core.Pt(12), Transform: core.Transform{Rotate: 180},
	}}}
	return core.View{TestID: "terminal-pane", Style: fill, Children: []core.Node{
		core.View{TestID: "terminal-tab-bar", Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Height: core.Pt(40), Padding: core.Symmetric(4, 8), Gap: 4, FlexShrink: 0},
			Children: []core.Node{
				core.Scroll{Horizontal: true, TestID: "terminal-tab-scroll", Style: core.Style{MinWidth: core.Pt(0), FlexShrink: 1, Height: core.Pt(32)}, Children: []core.Node{tabs}},
				add,
				core.Show{When: func() bool { return s.agentCapability("terminalProfiles") && len(p.tabs) < model.MaxTerminals }, Then: func(cx *core.Scope) core.Node { return s.terminalProfilePicker(cx, p) }},
				spacer(), kill, hide,
			}},
		core.Show{When: func() bool { s.read(); return p.active != nil && p.active.closeError != "" }, Then: func(cx *core.Scope) core.Node {
			return dynamicText(cx, func() string {
				s.read()
				if p.active == nil {
					return ""
				}
				return p.active.closeError
			}, 11, true)
		}},
		core.Dynamic(func(cx *core.Scope) core.Node {
			if tab := active.Get().tab; tab != nil {
				return s.terminalContent(cx, p, tab)
			}
			return nil
		}),
	}}
}

func (s *State) terminalTabButton(cx *core.Scope, p *terminalPanel, tab *terminalTab) core.Node {
	msgs := locales.For(cx)
	n := strconv.Itoa(tab.number)
	fallback := msgs.NumberedTerminal(tab.number)
	title := func() string {
		s.read()
		if tab.title != "" {
			return tab.title
		}
		if tab.profileName != "" {
			return tab.profileName + " · " + n
		}
		return fallback
	}
	selected := core.NewMemo(cx, func() bool { s.read(); return p.active == tab })
	ref := &core.Ref{}
	cx.Effect(func() {
		if selected.Get() {
			cx.Post(func() {
				if p.active == tab {
					ref.Reveal(core.RevealOptions{})
				}
			})
		}
	})
	t := base.Tab{TestID: "terminal-tab-" + n, Selected: selected.Get, Label: fallback,
		Tooltip:   s.terminalTabTooltip(cx, tab),
		Style:     core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Height: core.Pct(100), Gap: 6, Padding: core.Edges{Left: 8, Right: 4}, MinWidth: core.Pt(0), FlexShrink: 1},
		StyleFunc: func() core.Style { return core.Style{FocusVisible: pressStyle(cx).FocusVisible} },
		OnPress: func(core.PressEvent) {
			if p.active != tab {
				p.active = tab
				s.changed()
			}
		},
		Children: []core.Node{glyph(cx, lucide.SquareTerminal, 14), singleLine(cx, title, 12, false)},
	}
	close := contextualIconButton(cx, "terminal-close-"+n, msgs.KillTerminalTab(tab.number), lucide.X, func() { s.closeTerminal(p, tab, false) })
	close.Style.Width, close.Style.Height, close.Style.MinHeight, close.Style.Padding = core.Pt(24), core.Pt(24), core.Pt(24), core.All(6)
	close.Disabled = func() bool { s.read(); return tab.closing }
	return core.View{Ref: ref, Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Height: core.Pt(30), MaxWidth: core.Pt(220), CornerRadius: 7, BorderWidth: 1, Padding: core.Edges{Right: 2}},
		StyleFunc: func() core.Style {
			c := palette(cx)
			if selected.Get() {
				return core.Style{Background: c.muted, BorderColor: c.border}
			}
			return core.Style{BorderColor: core.Transparent, Hover: &core.Style{Background: c.muted}}
		}, Children: []core.Node{t, core.Show{When: func() bool { s.read(); return tab.closing }, Then: func(cx *core.Scope) core.Node {
			return s.loadingRing("terminal-closing-"+n, msgs.TerminateTerminal(), 12)
		}, Else: func(*core.Scope) core.Node { return close }}}}
}

func (tab *terminalTab) setTitle(title string) bool {
	if tab.title == title {
		return false
	}
	tab.title = title
	// Interpret path syntax without resolving it on the controller's OS.
	normalized := strings.ReplaceAll(title, "\\", "/")
	if path.IsAbs(normalized) || (len(normalized) >= 3 && normalized[1:3] == ":/") {
		tab.reportedPath = title
	}
	if shell := terminalShellLabel(path.Base(normalized)); shell != "" {
		tab.reportedShell = shell
	}
	return true
}

func terminalShellLabel(title string) string {
	switch strings.ToLower(title) {
	case "powershell", "pwsh.exe", "pwsh":
		return "PowerShell"
	case "windows powershell", "powershell.exe":
		return "Windows PowerShell"
	case "command prompt", "cmd.exe":
		return "Command Prompt"
	case "wsl", "wsl.exe":
		return "WSL"
	case "bash", "zsh", "sh", "fish", "nu", "ksh", "dash":
		return title
	default:
		return ""
	}
}

func (s *State) terminalTabTooltip(cx *core.Scope, tab *terminalTab) base.Tooltip {
	shell := func() string {
		s.read()
		if tab.profileName != "" {
			return tab.profileName
		}
		if tab.reportedShell != "" {
			return tab.reportedShell
		}
		return locales.For(cx).Terminal()
	}
	location := func() string {
		s.read()
		if tab.reportedPath != "" {
			return tab.reportedPath
		}
		if directory := str(s.selected, "cwd"); directory != "" {
			return directory
		}
		return str(s.selected, "projectDir")
	}
	activity := core.NewMemo(cx, func() string {
		s.read()
		if tab.title == location() || strings.EqualFold(tab.title, shell()) || terminalShellLabel(tab.title) == shell() {
			return ""
		}
		return tab.title
	})
	tip := tooltipCard(cx,
		infoRow(cx, "terminal-info-type", lucide.SquareTerminal, shell, true),
		core.Show{When: func() bool { return location() != "" }, Then: func(cx *core.Scope) core.Node {
			return infoRowText(cx, "terminal-info-path", lucide.Folder, dynamicText(cx, location, 12, false), false)
		}},
		core.Show{When: func() bool { return activity.Get() != "" }, Then: func(cx *core.Scope) core.Node {
			return infoRowText(cx, "terminal-info-activity", lucide.Activity, dynamicText(cx, activity.Get, 12, false), false)
		}},
	)
	tip.PopupStyle.Width = core.Pt(340)
	return tip
}

// Discover choices while the pane is visible. Adding the default shell never
// waits for this read, including on slow or older hosts.
func (s *State) terminalProfilePicker(cx *core.Scope, p *terminalPanel) core.Node {
	profiles := core.NewSignalFunc(cx, []Object(nil), nil)
	api, target, provider, session := s.api, s.target, str(s.selected, "providerId"), str(s.selected, "id")
	ctx, cancel := context.WithTimeout(cx.Context(), 8*time.Second)
	cx.Cleanup(cancel)
	go func() {
		defer cancel()
		v, err := rpc(ctx, api, target, provider, session, "terminal.profiles", nil)
		if err == nil {
			cx.Post(func() { profiles.Set(objects(v)) })
		}
	}()
	return core.Show{When: func() bool { return len(profiles.Get()) > 1 }, Then: func(cx *core.Scope) core.Node {
		open := core.NewSignal(cx, false)
		title := locales.For(cx).ChooseTerminal()
		return base.Popover{TestID: "terminal-choose", Label: title, Open: open.Get,
			OnOpenChange: func(value bool, _ base.Reason) { open.Set(value) },
			Placement:    core.PlaceTop, Align: core.AlignStart, Offset: 5, Margin: 8,
			Style:     core.Style{Width: core.Pt(24), Height: core.Pt(30), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
			StyleFunc: func() core.Style { return contextualIconStyle(cx) }, Trigger: glyph(cx, lucide.ChevronDown, 14),
			Tooltip:        tooltipCard(cx, label(cx, title, 12, false)),
			PopupStyle:     core.Style{MinWidth: core.Pt(160), Padding: core.All(5), Gap: 2, BorderWidth: 1, CornerRadius: 8},
			PopupStyleFunc: func() core.Style { c := palette(cx); return core.Style{Background: c.elevated, BorderColor: c.border} },
			Children: []core.Node{core.Scroll{StyleFunc: func() core.Style {
				return core.Style{MaxHeight: core.Pt(min(300, cx.Env().Window().Height/2))}
			}, Children: []core.Node{core.Each(profiles.Get, func(o Object) string { return str(o, "id") }, func(cx *core.Scope, get func() Object, _ func() int) core.Node {
				profile := get()
				id, name := str(profile, "id"), str(profile, "name")
				return leftButton(button(cx, "terminal-profile-"+id, name, lucide.SquareTerminal, "ghost", func() {
					open.Set(false)
					if len(p.tabs) < model.MaxTerminals {
						p.addTab()
						p.active.profile, p.active.profileName = id, name
						s.changed()
					}
				}))
			})}}}}
	}}
}
