package hubui

import (
	"encoding/base64"
	"fmt"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/nsgo-dev/nsgo/base"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/i18n"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

func (s *State) manageDevice(id string) {
	s.deviceMenuTarget = id
	if id == control.LocalTarget {
		s.dialog.Set("local")
		return
	}
	s.dialog.Set("device")
}
func (s *State) sidebarDevices(cx *core.Scope) core.Node {
	return core.View{Style: core.Style{Padding: core.Symmetric(10, 12), Gap: 4}, Children: []core.Node{
		core.Show{When: func() bool { return !s.inlineSidebarHeading(cx) }, Then: func(cx *core.Scope) core.Node {
			return row(s.brand(cx, 18, true), spacer(), s.addDeviceButton(cx), core.Show{When: s.narrowLayout, Then: func(cx *core.Scope) core.Node {
				v := labelledIconButton(cx, "close-mobile-sidebar", locales.For(cx).HideSidebar(), lucide.PanelLeftClose, s.closeMobileSidebar).(core.View)
				v.Style.Width, v.Style.Height, v.Style.MinHeight = core.Pt(44), core.Pt(44), core.Pt(44)
				return v
			}})
		}},
		core.Scroll{TestID: "sidebar-devices", Style: core.Style{MaxHeight: core.Pt(148), MinHeight: core.Pt(0)}, Children: []core.Node{
			core.Each(func() []Object { s.read(); return s.devices }, func(d Object) string { return str(d, "id") }, func(cx *core.Scope, d func() Object, _ func() int) core.Node {
				id := str(d(), "id")
				return core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 4}, Children: []core.Node{
					core.View{TestID: "device-" + id, Role: core.RoleButton, LabelFunc: func() string {
						if flag(d(), "local") {
							return locales.For(s.cx).LocalDevice()
						}
						return deviceName(d())
					}, OnPress: func(core.PressEvent) { s.manageDevice(id) }, Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, Padding: core.Symmetric(8, 8), CornerRadius: 8}, StyleFunc: func() core.Style { return pressStyle(cx) }, Children: []core.Node{
						s.hostGlyph(cx, id, 15),
						singleLine(cx, func() string {
							if flag(d(), "local") {
								return locales.For(s.cx).LocalDevice()
							}
							return deviceName(d())
						}, 12, false), spacer(),
						dynamicText(cx, func() string {
							s.read()
							if flag(d(), "local") {
								return ""
							}
							if s.connectionRecovering(id) {
								return "连接中"
							}
							if flag(d(), "connected") {
								return ""
							}
							return s.deviceListStatus(d())
						}, 11, true),
					}},
					core.Show{When: func() bool { return !flag(d(), "local") }, Then: func(cx *core.Scope) core.Node {
						return s.connectionIndicator(cx, func() string { return id }, "sidebar-")
					}},
				}}
			}),
		}},
	}}
}

func (s *State) addDeviceButton(cx *core.Scope) core.Node {
	v := sidebarIconButton(cx, "connect-device", locales.For(cx).AddDevice(), lucide.Plus, func() { s.scanned = nil; s.dialog.Set("connect") }).(core.View)
	v.Desktop.WindowDrag = core.WindowDragOff
	return v
}
func (s *State) directory(cx *core.Scope) core.Node {
	stamp := core.NewMemo(cx, func() uint64 { s.read(); return s.catalogueVersion })
	rows := core.NewMemoFunc(cx, func() []directoryRow { stamp.Get(); i18n.Language(cx); return core.Untracked(cx, s.directoryRows) }, nil)
	brake := &listTouchBrake{enabled: !isWeb() && cx.Env().OS() == core.OSAndroid}
	return core.View{TestID: "session-sidebar", Style: fill, StyleFunc: func() core.Style { c := palette(cx); return core.Style{Background: c.sidebar, Color: c.foreground} }, Children: []core.Node{
		s.sidebarDevices(cx),
		s.emptyCatalogue(cx),
		brake.wrap(core.List{TestID: "session-list", Style: fill, Count: func() int { return len(rows.Get()) }, Key: func(i int) any { return rows.Get()[i].ID }, EstimatedSize: 62, Row: func(cx *core.Scope, r core.ListRow) core.Node {
			item := func() directoryRow {
				v := rows.Get()
				i := r.Index()
				if i >= 0 && i < len(v) {
					return v[i]
				}
				return directoryRow{}
			}
			key := core.NewMemo(cx, func() string { return item().ID })
			return core.Dynamic(func(cx *core.Scope) core.Node {
				key.Get()
				data := core.Untracked(cx, item)
				if data.Kind == "filters" {
					return s.sessionFilterFeedback(cx, data.Section)
				}
				if data.Kind == "empty" {
					return core.View{TestID: data.ID, Style: core.Style{Padding: core.Symmetric(8, 14)}, Children: []core.Node{label(cx, data.Title, 12, true)}}
				}
				if data.Header {
					return s.treeHeader(cx, item, brake)
				}
				return s.sessionRow(cx, item, brake)
			})
		}}),
		core.View{TestID: "sidebar-footer", Style: core.Style{Padding: core.Symmetric(8, 12)}, Children: []core.Node{row(
			s.sidebarMenu(cx), spacer(),
			core.Show{When: func() bool { return isMobile(cx) }, Then: func(cx *core.Scope) core.Node {
				return sidebarIconButton(cx, "scan-qr", "扫描二维码", lucide.ScanLine, func() { s.scan(cx) })
			}},
			sidebarIconButton(cx, "new-session", locales.For(cx).NewSession(), lucide.SquarePen, func() { s.openSessionCreation(nil) }),
		)}},
	}}
}
func (s *State) treeHeader(cx *core.Scope, item func() directoryRow, brake *listTouchBrake) core.Node {
	// Construction reads must not subscribe the keyed row boundary to metadata.
	data := core.Untracked(cx, item)
	key := data.Group
	if data.Kind == "section" {
		key = data.ID
	}
	unread := func() bool { s.read(); return s.collapsed[key] && item().Unread }
	headerLabel := func() string {
		title := item().Title
		if unread() {
			title += " · " + locales.For(cx).SessionCompletedUnread()
		}
		return title
	}
	unreadIndicator := func() core.Node {
		return core.Show{When: unread, Then: func(cx *core.Scope) core.Node {
			return unreadDot(cx, data.ID, locales.For(cx).SessionCompletedUnread())
		}}
	}
	if data.Kind == "section" {
		filtersOpen := core.NewSignal(cx, false)
		trigger := core.View{TestID: data.ID, Role: core.RoleButton, LabelFunc: headerLabel, Expanded: func() bool { s.read(); return !s.collapsed[key] },
			OnPress: func(core.PressEvent) {
				brake.action(func() { s.collapsed[key] = !s.collapsed[key]; s.saveSidebarPreferences() })()
			},
			Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Gap: 6, Padding: core.Symmetric(9, 6), CornerRadius: 7, MinHeight: core.Pt(38)},
			StyleFunc: func() core.Style {
				return core.Style{Active: &core.Style{Opacity: core.Alpha(.65)}, FocusVisible: &core.Style{OutlineWidth: 2, OutlineColor: palette(cx).focus}}
			}, Children: []core.Node{
				singleLine(cx, func() string { return item().Title }, 13, true),
				core.Icon{TestID: data.ID + "-disclosure", SVGFunc: func() *core.SVG {
					s.read()
					if s.collapsed[key] {
						return lucide.ChevronRight()
					}
					return lucide.ChevronDown()
				},
					Style: core.Style{Width: core.Pt(13), Height: core.Pt(13)}, StyleFunc: func() core.Style {
						s.read()
						if s.collapsed[key] {
							return core.Style{Opacity: core.Alpha(1)}
						}
						return contextualAffordance(cx, "directory-section", true)
					}},
				spacer(), core.Show{When: func() bool { return !cx.Env().Desktop() }, Then: func(cx *core.Scope) core.Node {
					return unreadIndicator()
				}},
			}}
		children := []core.Node{trigger, s.sessionFiltersButton(cx, data.Section, filtersOpen, unread),
			core.Show{When: func() bool { return cx.Env().Desktop() && !filtersOpen.Get() && unread() }, Then: func(cx *core.Scope) core.Node {
				hidden := &core.Style{Opacity: core.Alpha(0)}
				return core.View{TestID: data.ID + "-status", PointerEvents: core.PointerEventsNone,
					Style: core.Style{Position: core.Absolute, Right: core.Pt(0), Top: core.Pt(0), Bottom: core.Pt(0), Width: core.Pt(32),
						AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter,
						GroupStyle: &core.GroupStyle{Name: "directory-section", Hover: hidden, FocusWithin: hidden}},
					Children: []core.Node{unreadDot(cx, data.ID, locales.For(cx).SessionCompletedUnread())}}
			}},
		}
		return core.View{Style: core.Style{Group: "directory-section", Direction: core.Row, AlignItems: core.AlignCenter, Margin: core.Edges{Top: 8, Left: 8, Right: 12}}, Children: children}
	}
	menuOpen, tipOpen := core.NewSignal(cx, false), core.NewSignal(cx, false)
	actionsHover := core.NewSignal(cx, false)
	tip := tooltipCard(cx, s.groupInfo(cx, item, true))
	tip.Placement = core.PlaceRight
	tip.Open = func() bool {
		s.read()
		return tipOpen.Get() && !menuOpen.Get() && !actionsHover.Get() && s.pinDrag == nil
	}
	tip.OnOpenChange = func(open bool, _ base.Reason) {
		tipOpen.Set(open && !menuOpen.GetUntracked() && !actionsHover.GetUntracked())
	}
	trigger := core.View{TestID: data.ID, Role: core.RoleButton,
		Expanded:  func() bool { s.read(); return !s.collapsed[key] },
		LabelFunc: func() string { return headerLabel() + " · " + locales.For(cx).FilterMatches(fmt.Sprint(item().Count)) },
		OnPress: func(core.PressEvent) {
			brake.action(func() { s.collapsed[key] = !s.collapsed[key]; s.saveSidebarPreferences() })()
		},
		Style:     core.Style{Direction: core.Row, AlignItems: core.AlignCenter, FlexGrow: 1, FlexShrink: 1, Gap: 7, Padding: core.Edges{Top: 7, Bottom: 7, Left: 5, Right: 36}, MinHeight: core.Pt(34), MinWidth: core.Pt(0), CornerRadius: 7},
		StyleFunc: func() core.Style { return pressStyle(cx) }, Tooltip: tip, Children: []core.Node{
			core.Icon{TestID: data.ID + "-icon", SVGFunc: func() *core.SVG {
				s.read()
				switch data.Kind {
				case "device":
					return s.hostIcon(cx, data.Target)
				case "agent":
					return agentIcon(data.Provider)
				default:
					if s.collapsed[key] {
						return lucide.FolderClosed()
					}
					return lucide.FolderOpen()
				}
			}, Style: core.Style{Width: core.Pt(14), Height: core.Pt(14), FlexShrink: 0}, StyleFunc: func() core.Style {
				return core.Style{Color: palette(cx).foreground}
			}},
			singleLine(cx, func() string { return item().Title }, 12, true),
		}}
	status := core.View{PointerEvents: core.PointerEventsNone, Style: core.Style{Position: core.Absolute, Right: core.Pt(2), Top: core.Pt(0), Bottom: core.Pt(0), Width: core.Pt(30), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter}, StyleFunc: func() core.Style {
		if data.Kind == "project" && cx.Env().Desktop() {
			hidden := &core.Style{Opacity: core.Alpha(0), PointerEvents: core.PointerEventsNone}
			return core.Style{GroupStyle: &core.GroupStyle{Name: "directory-group", Hover: hidden, FocusWithin: hidden}}
		}
		return core.Style{}
	}, Children: []core.Node{unreadIndicator()}}
	var row core.Node = trigger
	menu := []core.Node{s.groupInfo(cx, item, false)}
	if data.Kind == "project" {
		children := []core.Node{trigger, status}
		create := func() { project := item(); s.openSessionCreation(&project) }
		menu = append(menu, ui.MenuItem{TestID: "project-create-action", Text: locales.For(cx).NewSession(), Icon: lucide.SquarePen, OnSelect: create})
		children = append(children, core.Show{When: func() bool { return cx.Env().Desktop() }, Then: func(cx *core.Scope) core.Node {
			return core.View{TestID: "actions-" + data.ID, Style: core.Style{Position: core.Absolute, Top: core.Pt(0), Bottom: core.Pt(0), Right: core.Pt(2), JustifyContent: core.JustifyCenter},
				OnMouseEnter: func(core.MouseEvent) { actionsHover.Set(true); tipOpen.Set(false) }, OnMouseLeave: func(core.MouseEvent) { actionsHover.Set(false) },
				StyleFunc: func() core.Style { return contextualAffordance(cx, "directory-group", false) },
				Children:  []core.Node{contextualIconButton(cx, "create-"+data.ID, locales.For(cx).NewSession(), lucide.SquarePen, create)}}
		}})
		row = core.View{Style: core.Style{Group: "directory-group", Direction: core.Row, AlignItems: core.AlignCenter, CornerRadius: 7, MinWidth: core.Pt(0)}, StyleFunc: func() core.Style {
			return core.Style{Hover: &core.Style{Background: palette(cx).muted}}
		}, Children: children}
	} else {
		trigger.Children = append(trigger.Children, status)
		row = trigger
	}
	return core.View{Style: core.Style{Padding: core.Edges{Top: 2, Left: core.Points(8 + data.Depth*14), Right: 8}}, Children: []core.Node{
		ui.ContextMenu{TestID: "menu-" + data.ID, Open: menuOpen.Get, OnOpenChange: func(open bool, _ base.Reason) {
			menuOpen.Set(open)
			if open {
				tipOpen.Set(false)
			}
		}, Trigger: row, Children: menu},
	}}

}
func (s *State) sessionRow(cx *core.Scope, item func() directoryRow, brake *listTouchBrake) core.Node {
	// Construction reads must not subscribe the keyed row boundary to metadata.
	data := core.Untracked(cx, item)
	ss := data.Session
	id := sessionRowID(data.Section, ss)
	ref := &core.Ref{}
	var pan func(core.PanEvent)
	if data.Section == "pinned" {
		pan = s.pinnedPan(cx, ss, ref)
	}
	pin := core.NewMemo(cx, func() bool { s.read(); return s.pinned[sessionIdentity(item().Session)] != 0 })
	selected := func() bool {
		s.read()
		return s.target == str(item().Session, "deviceId") && str(s.selected, "providerId") == str(item().Session, "providerId") && str(s.selected, "id") == str(item().Session, "id")
	}
	menuOpen := core.NewSignal(cx, false)
	actionsHover := core.NewSignal(cx, false)
	tipOpen := core.NewSignal(cx, false)
	tip := s.sessionTooltip(cx, item)
	tip.Open = func() bool {
		s.read()
		return tipOpen.Get() && !menuOpen.Get() && !actionsHover.Get() && s.pinDrag == nil
	}
	tip.OnOpenChange = func(open bool, _ base.Reason) { tipOpen.Set(open && !menuOpen.GetUntracked()) }
	return ui.ContextMenu{TestID: "menu-" + id, Open: menuOpen.Get, OnOpenChange: func(open bool, _ base.Reason) {
		menuOpen.Set(open)
		if open {
			tipOpen.Set(false)
		}
	}, Children: []core.Node{s.pinOrderMenu(cx, item), core.Dynamic(func(cx *core.Scope) core.Node {
		pinned := pin.Get()
		return ui.MenuItem{TestID: "session-pin-action", Text: choose(pinned, locales.For(cx).UnpinSession(), locales.For(cx).PinSession()), Icon: choose(pinned, lucide.PinOff, lucide.Pin), OnSelect: func() { s.togglePinned(item().Session) }}
	}), ui.MenuItem{TestID: "session-remove-action", Text: locales.For(cx).RemoveSession(), Icon: lucide.Archive, Variant: ui.MenuItemDanger, OnSelect: func() { s.requestSessionRemoval(item().Session) }}}, Trigger: core.View{Style: core.Style{Group: "session-row", Direction: core.Row, AlignItems: core.AlignCenter, Margin: core.Symmetric(2, 8), CornerRadius: 9, MinWidth: core.Pt(0)}, StyleFunc: func() core.Style {
		st := core.Style{Padding: core.Edges{Left: core.Points(item().Depth * 14)}, Hover: &core.Style{Background: palette(cx).muted}, FocusWithin: &core.Style{Background: palette(cx).muted}}
		if selected() {
			st.Background = palette(cx).selected
		}
		return st
	}, Children: []core.Node{
		core.View{TestID: id, Ref: ref, OnPan: pan, PanActivation: core.PanActivation{ActiveOffsetY: 7, FailOffsetX: 50}, Role: core.RoleButton, LabelFunc: func() string {
			v := item()
			return v.Title + " · " + s.environmentName(str(v.Session, "deviceId")) + " · " + s.providerName(str(v.Session, "deviceId"), str(v.Session, "providerId")) + " · " + s.sessionStatusLabel(v.Session)
		}, OnPress: func(core.PressEvent) { brake.action(func() { s.selectSession(item().Session) })() }, Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Padding: core.Edges{Top: 9, Bottom: 9, Left: 7, Right: 70}, Gap: 5, CornerRadius: 9}, StyleFunc: func() core.Style {
			st := pressStyle(cx)
			if data.Section == "pinned" && cx.Env().Desktop() {
				s.read()
				if s.pinDrag != nil && s.pinDrag.source == sessionIdentity(ss) {
					st.Opacity = core.Alpha(.55)
				}
			}
			return st
		}, Tooltip: tip, Children: []core.Node{
			singleLine(cx, func() string {
				v := item().Title
				if v == "" {
					return locales.For(s.cx).UnnamedSession()
				}
				return v
			}, 13, false),
			row(glyph(cx, sessionIcon(ss), 12), singleLine(cx, func() string {
				s.read()
				v := item().Session
				return s.environmentName(str(v, "deviceId"))
			}, 11, true)),
		}},
		s.sessionStatusIndicator(cx, id, item),
		s.pinnedDropMarker(cx, sessionIdentity(ss)),
		core.Show{When: func() bool { return cx.Env().Desktop() }, Then: func(cx *core.Scope) core.Node {
			return core.View{TestID: "actions-" + id, Style: core.Style{Position: core.Absolute, Top: core.Pt(4), Right: core.Pt(4), Direction: core.Row, AlignItems: core.AlignCenter, CornerRadius: 7},
				OnMouseEnter: func(core.MouseEvent) { actionsHover.Set(true) }, OnMouseLeave: func(core.MouseEvent) { actionsHover.Set(false) },
				StyleFunc: func() core.Style {
					return contextualAffordance(cx, "session-row", false)
				},
				Children: []core.Node{
					core.Dynamic(func(cx *core.Scope) core.Node {
						pinned := pin.Get()
						return contextualIconButton(cx, "pin-"+id, choose(pinned, locales.For(cx).UnpinSession(), locales.For(cx).PinSession()), choose(pinned, lucide.PinOff, lucide.Pin), func() { s.togglePinned(item().Session) })
					}),
					contextualIconButton(cx, "remove-"+id, locales.For(cx).RemoveSession(), lucide.Archive, func() { s.requestSessionRemoval(item().Session) }),
				}}
		}},
	}}}

}

func sessionRowID(section string, session Object) string {
	return "session-" + section + "-" + base64.RawURLEncoding.EncodeToString([]byte(sessionIdentity(session)))
}
