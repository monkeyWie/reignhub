package hubui

import (
	"fmt"
	"slices"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/i18n"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

// Reuse the shared menu's focus, keyboard navigation and sheet behavior.
func sessionFilterMenuRow(cx *core.Scope) core.Style {
	return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 10,
		MinHeight: core.Pt(choose(base.OnSheet(cx), core.Points(44), core.Points(34))),
		Padding:   core.Symmetric(6, 10), CornerRadius: 8, MinWidth: core.Pt(0)}
}

func sessionFilterChoice(cx *core.Scope, id string, title func() string, detail func() string, checked func() bool, selectOption func()) base.MenuItem {
	return base.MenuItem{TestID: id, Label: title(), Checked: checked, OnSelect: selectOption, NoCloseOnSelect: true,
		Style: sessionFilterMenuRow(cx), StyleFunc: func() core.Style { return pressStyle(cx) },
		Children: []core.Node{core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Gap: 2}, Children: []core.Node{
			singleLine(cx, title, 13, false),
			core.Show{When: func() bool { return detail != nil && detail() != "" }, Then: func(cx *core.Scope) core.Node {
				return singleLine(cx, detail, 11, true)
			}},
		}}, core.Icon{SVG: lucide.Check(), Style: core.Style{Width: core.Pt(15), Height: core.Pt(15), FlexShrink: 0}, StyleFunc: func() core.Style {
			return core.Style{Color: palette(cx).accent, Opacity: core.Alpha(choose(checked(), float32(1), float32(0)))}
		}}}}
}

func sessionFilterSubmenu(cx *core.Scope, id, title string, icon func() *core.SVG, selection func() int, children []core.Node) core.Node {
	text := singleLine(cx, func() string { return title }, 13, false)
	text.Style.FlexGrow = 1
	back := row(glyph(cx, lucide.ChevronLeft, 14), label(cx, locales.For(cx).SessionFilters(), 13, false)).(core.View)
	back.TestID = "back-" + id
	return base.Submenu{TestID: id, Label: title, Style: sessionFilterMenuRow(cx), StyleFunc: func() core.Style { return pressStyle(cx) },
		Trigger: core.Group{glyph(cx, icon, 16), text,
			dynamicText(cx, func() string {
				if selection != nil && selection() > 0 {
					return fmt.Sprint(selection())
				}
				return ""
			}, 12, true), glyph(cx, lucide.ChevronRight, 14)},
		BackStyle: sessionFilterMenuRow(cx), Back: back,
		Children: children}
}

func (s *State) sessionFilterGroup(cx *core.Scope, dimension, title string, icon func() *core.SVG, selection *map[string]bool) core.Node {
	stamp := core.NewMemo(cx, func() uint64 { s.read(); return s.catalogueVersion })
	options := core.NewMemoFunc(cx, func() []sessionFilterOption {
		stamp.Get()
		i18n.Language(cx)
		return core.Untracked(cx, func() []sessionFilterOption { return s.sessionFilterOptions(dimension) })
	}, slices.Equal[[]sessionFilterOption])
	duplicates := core.NewMemoFunc(cx, func() map[string]int {
		counts := map[string]int{}
		for _, o := range options.Get() {
			counts[o.Title]++
		}
		return counts
	}, nil)
	return sessionFilterSubmenu(cx, "filter-menu-"+dimension, title, icon, func() int { s.read(); return len(*selection) }, []core.Node{
		sessionFilterChoice(cx, "filter-"+dimension+"-all", func() string { return locales.For(cx).FilterAll() }, nil,
			func() bool { s.read(); return len(*selection) == 0 }, func() { *selection = nil; s.filtersChanged() }),
		ui.MenuSeparator{},
		core.Each(options.Get, func(o sessionFilterOption) string { return o.Key }, func(cx *core.Scope, option func() sessionFilterOption, _ func() int) core.Node {
			key := option().Key
			return sessionFilterChoice(cx, sessionFilterID(dimension, key), func() string { return option().Title }, func() string {
				// Paths and device names disambiguate equal project names only.
				if duplicates.Get()[option().Title] > 1 {
					return option().Detail
				}
				return ""
			}, func() bool { s.read(); return (*selection)[key] }, func() {
				if (*selection)[key] {
					delete(*selection, key)
				} else {
					if *selection == nil {
						*selection = map[string]bool{}
					}
					(*selection)[key] = true
				}
				s.filtersChanged()
			})
		}),
	})
}

func sessionFiltersButtonID(section string) string {
	if section == "all" {
		return "session-filters"
	}
	return section + "-filters"
}

func (s *State) sessionFiltersButton(cx *core.Scope, section string, open *core.Signal[bool], unread func() bool) core.Node {
	f := s.sectionFilters(section)
	// Narrow windows use the same drill-in sheet as touch, keeping cascades
	// from covering their parents or running beyond the viewport.
	sheet := core.NewMemo(cx, func() bool { return !cx.Env().Desktop() || cx.Env().Window().Width < 650 })
	return core.Dynamic(func(cx *core.Scope) core.Node {
		onSheet := sheet.Get()
		tipOpen := core.NewSignal(cx, false)
		tip := tooltipCard(cx, label(cx, locales.For(cx).SessionFilters(), 12, false))
		tip.Open = func() bool { return tipOpen.Get() && !open.Get() }
		tip.OnOpenChange = func(value bool, _ base.Reason) { tipOpen.Set(value && !open.GetUntracked()) }
		return base.Menu{TestID: sessionFiltersButtonID(section), Label: locales.For(cx).SessionFilters(), Open: open.Get,
			Presentation: choose(onSheet, base.PresentSheet, base.PresentPopup),
			OnOpenChange: func(value bool, _ base.Reason) {
				open.Set(value)
				if value {
					tipOpen.Set(false)
				}
			},
			Placement: core.PlaceBottom, Align: core.AlignEnd, Offset: 4, Margin: 8,
			Style: core.Style{Width: core.Pt(40), Height: core.Pt(36), CornerRadius: 10, FlexShrink: 0, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
			StyleFunc: func() core.Style {
				s.read()
				st := contextualIconStyle(cx)
				if !open.Get() && (unread() || f.count() == 0) {
					st = st.Merge(contextualAffordance(cx, "directory-section", true))
				}
				if open.Get() || f.count() > 0 {
					st.Color = palette(cx).foreground
				}
				return st
			}, Tooltip: tip,
			Trigger: core.View{Children: []core.Node{
				core.Icon{SVG: lucide.Funnel(), Style: core.Style{Width: core.Pt(16), Height: core.Pt(16)}},
				core.Show{When: func() bool { s.read(); return f.count() > 0 }, Then: func(cx *core.Scope) core.Node {
					return core.View{Style: core.Style{Position: core.Absolute, Top: core.Pt(-4), Right: core.Pt(-5), Width: core.Pt(6), Height: core.Pt(6), CornerRadius: 3}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).accent} }}
				}},
			}},
			BackdropStyle: core.Style{Background: core.RGB(0).Faded(.35)},
			PopupStyleFunc: func() core.Style {
				c := palette(cx)
				st := ui.FinOf(cx).Menu().Over(core.Style{Width: core.Pt(240), Padding: core.All(5), CornerRadius: 12, BorderWidth: 1, Color: c.foreground})
				if onSheet {
					st = core.Style{Background: c.elevated, Color: c.foreground, Padding: core.All(8), PaddingEnv: core.EnvEdges{Bottom: core.EnvSafeArea}, CornerRadius: 16}
				}
				return st
			},
			Children: []core.Node{core.Func(func(cx *core.Scope) core.Node {
				l := locales.For(cx)
				children := []core.Node{
					s.sessionFilterGroup(cx, "device", l.FilterDevices(), lucide.Monitor, &f.Devices),
					s.sessionFilterGroup(cx, "agent", l.FilterAgents(), lucide.Bot, &f.Agents),
					s.sessionFilterGroup(cx, "project", l.FilterProjects(), lucide.Folder, &f.Projects),
				}
				if section == "all" {
					groups := []core.Node{}
					for _, o := range []sessionFilterOption{{Key: groupProject, Title: l.GroupByProject()}, {Key: groupDevice, Title: l.GroupByDevice()}, {Key: groupAgent, Title: l.GroupByAgent()}} {
						choice := sessionFilterChoice(cx, "group-"+o.Key, func() string { return o.Title }, nil, func() bool { s.read(); return s.grouping == o.Key }, func() { s.setGrouping(o.Key) })
						choice.Role, choice.NoCloseOnSelect = core.RoleMenuItemRadio, false
						groups = append(groups, choice)
					}
					children = append(children, ui.MenuSeparator{}, sessionFilterSubmenu(cx, "session-grouping", l.SessionGrouping(), lucide.ListTree, nil, groups))
				}
				children = append(children, core.Show{When: func() bool { s.read(); return f.count() > 0 }, Then: func(cx *core.Scope) core.Node {
					return core.Group{ui.MenuSeparator{}, base.MenuItem{TestID: "reset-session-filters", Label: l.ClearFilters(), NoCloseOnSelect: true,
						Style: sessionFilterMenuRow(cx), StyleFunc: func() core.Style { return pressStyle(cx) },
						OnSelect: func() { s.clearSessionFilters(section) }, Children: []core.Node{glyph(cx, lucide.RotateCcw, 16), label(cx, l.ClearFilters(), 13, false)}}}
				}})
				panel := core.View{TestID: "session-filter-panel", Style: core.Style{MinWidth: core.Pt(0)}, Children: children}
				if onSheet {
					return core.Scroll{TestID: "session-filter-scroll", StyleFunc: func() core.Style { return core.Style{MaxHeight: core.Pt(cx.Env().Window().Height * .7)} }, Children: []core.Node{panel}}
				}
				return panel
			})},
		}
	})
}
