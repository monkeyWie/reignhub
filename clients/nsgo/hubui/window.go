package hubui

import (
	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/desktop"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

const windowBarHeight core.Points = 40

func customWindow(cx *core.Scope) bool {
	return !isWeb() && desktop.IsAvailable(cx)
}

func (s *State) windowFrame(cx *core.Scope, content core.Node) core.Node {
	if !customWindow(cx) {
		return content
	}

	// Paint the title bar ourselves while the platform keeps its own controls:
	// traffic lights on macOS and caption buttons on Windows. GTK removes
	// its controls with the decorations, so Linux gets GNOME-style controls.
	desktop.SetTitleBar(cx, desktop.TitleBarHidden, windowBarHeight)
	desktop.SetWindowControls(cx, desktop.WindowControls{})
	desktop.SetMenuBar(cx, desktop.MenuBarAutoHide)
	bar := core.View{
		TestID:  "window-toolbar",
		Desktop: core.DesktopViewProps{WindowDrag: core.WindowDragOn},
		// Windows searches native siblings from front to back for caption hits.
		// Animated content can extend its hit-search area beyond its own bounds;
		// keep the toolbar above that content without changing its flow layout.
		Style: core.Style{ZIndex: 1},
		StyleFunc: func() core.Style {
			insets := desktop.TitleBar(cx)
			return core.Style{
				Direction: core.Row, AlignItems: core.AlignCenter, FlexShrink: 0,
				Height:     core.Pt(max(windowBarHeight, insets.Top)),
				Padding:    core.Edges{Left: insets.Left, Right: insets.Right},
				Background: palette(cx).background,
			}
		},
		Children: []core.Node{s.sidebarTitlebarSurface(cx), core.View{
			TestID:  "window-drag-region",
			Style:   core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Height: core.Pct(100), Direction: core.Row, AlignItems: core.AlignCenter},
			Desktop: core.DesktopViewProps{WindowDrag: core.WindowDragOn},
			Children: []core.Node{core.Show{When: func() bool { return !s.narrowLayout() }, Then: func(cx *core.Scope) core.Node {
				return core.Group{s.sidebarToolbarSlot(cx, func() core.Points { return desktop.TitleBar(cx).Left }), s.contentToolbarSlot(cx)}
			}}},
		}},
	}
	if cx.Env().OS() == core.OSLinux {
		bar.Children = append(bar.Children, linuxWindowControls(cx)...)
	}
	return core.View{TestID: "window-frame", Style: fill, Children: []core.Node{
		core.Show{When: func() bool { return !desktop.FullScreen(cx) }, Then: func(*core.Scope) core.Node { return bar }},
		content,
	}}
}

func (s *State) sidebarWidth() core.Points {
	return max(260, min(s.sessionWidth.Get(), s.width.Get()*0.46))
}

func (s *State) sidebarCollapsed() bool { return !s.mobileClient && s.sidebarHidden.Get() }

func (s *State) sidebarToggle(cx *core.Scope) core.Node {
	if s.mobileClient {
		return nil
	}
	title := func() string {
		if s.sidebarCollapsed() {
			return locales.For(cx).ShowSidebar()
		}
		return locales.For(cx).HideSidebar()
	}
	v := labelledIconButton(cx, "toggle-sidebar", "", lucide.PanelLeft, func() {
		s.sidebarHidden.Set(!s.sidebarHidden.GetUntracked())
		s.saveSidebarPreferences()
	}).(core.View)
	v.LabelFunc = title
	v.Expanded = func() bool { return !s.sidebarCollapsed() }
	v.Tooltip = tooltipCard(cx, dynamicText(cx, title, 12, false))
	v.Desktop.WindowDrag = core.WindowDragOff
	v.Style.Width, v.Style.Height, v.Style.MinHeight, v.Style.Padding = core.Pt(40), core.Pt(30), core.Pt(30), core.All(7)
	if !cx.Env().Desktop() {
		v.Style.Width, v.Style.Height, v.Style.MinHeight = core.Pt(44), core.Pt(44), core.Pt(44)
	}
	v.Children = []core.Node{core.Icon{SVGFunc: func() *core.SVG {
		if s.sidebarCollapsed() {
			return lucide.PanelLeftOpen()
		}
		return lucide.PanelLeftClose()
	}, Style: core.Style{Width: core.Pt(16), Height: core.Pt(16)}}}
	return v
}

// The same control stays reachable after collapse, including in Web/full screen.
// Native title-bar insets reserve space for platform-owned window controls.
func (s *State) sidebarToolbarSlot(cx *core.Scope, leading func() core.Points) core.Node {
	return core.View{TestID: "sidebar-toolbar-slot", Desktop: core.DesktopViewProps{WindowDrag: core.WindowDragOn}, Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, JustifyContent: core.JustifyEnd, Height: core.Pct(100), Padding: core.Edges{Right: 12}, FlexShrink: 0}, StyleFunc: func() core.Style {
		width := s.sidebarWidth() - leading()
		if s.sidebarCollapsed() {
			width = 44
		}
		return core.Style{Width: core.Pt(max(56, width))}
	}, Children: []core.Node{
		core.Show{When: func() bool { return s.inlineSidebarHeading(cx) && !s.sidebarCollapsed() }, Then: func(cx *core.Scope) core.Node {
			return core.Group{
				core.View{Desktop: core.DesktopViewProps{WindowDrag: core.WindowDragOff}, Style: core.Style{Margin: core.Edges{Left: 12}}, Children: []core.Node{s.brand(cx, 16, true)}},
				spacer(), s.addDeviceButton(cx),
			}
		}},
		s.sidebarToggle(cx),
	}}
}

// Only a native macOS window reserves a separate row for traffic lights.
// Browsers (including on macOS) and full screen keep one sidebar header.
func (s *State) inlineSidebarHeading(cx *core.Scope) bool {
	trafficLights := customWindow(cx) && cx.Env().OS() == core.OSMacOS && !desktop.FullScreen(cx)
	return !s.narrowLayout() && !trafficLights
}

// Continue the sidebar surface behind the native controls without affecting
// their platform-owned inset or the toolbar's layout measurements.
func (s *State) sidebarTitlebarSurface(cx *core.Scope) core.Node {
	return core.View{TestID: "sidebar-titlebar-surface", PointerEvents: core.PointerEventsNone, Style: core.Style{Position: core.Absolute, Top: core.Pt(0), Bottom: core.Pt(0), Left: core.Pt(0)}, StyleFunc: func() core.Style {
		return core.Style{Display: choose(s.narrowLayout() || s.sidebarCollapsed(), core.DisplayNone, core.DisplayFlex), Width: core.Pt(s.sidebarWidth()), Background: palette(cx).sidebar}
	}}
}

func (s *State) workspaceToolbar(cx *core.Scope) core.Node {
	return core.Show{When: func() bool { return !customWindow(cx) || desktop.FullScreen(cx) }, Then: func(cx *core.Scope) core.Node {
		return core.View{TestID: "workspace-toolbar", Style: core.Style{Height: core.Pt(choose(cx.Env().Desktop(), windowBarHeight, core.Points(44))), FlexShrink: 0, Direction: core.Row}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).background} }, Children: []core.Node{s.sidebarTitlebarSurface(cx), s.sidebarToolbarSlot(cx, func() core.Points { return 0 }), s.contentToolbarSlot(cx)}}
	}}
}

// Linux's hidden decorations contain no buttons in NSGo. Keep the familiar
// circular header buttons without restoring a separate system title bar.
func linuxWindowControls(cx *core.Scope) []core.Node {
	control := func(id, title string, icon func() *core.SVG, action func()) core.Node {
		return core.View{
			TestID: id, Role: core.RoleButton, Label: title,
			Tooltip: tooltipCard(cx, label(cx, title, 12, false)),
			Desktop: core.DesktopViewProps{WindowDrag: core.WindowDragOff},
			OnPress: func(core.PressEvent) { action() },
			Style:   core.Style{Width: core.Pt(28), Height: core.Pt(28), Margin: core.Edges{Right: 6}, CornerRadius: 14, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter, FlexShrink: 0},
			StyleFunc: func() core.Style {
				c := palette(cx)
				return core.Style{Color: c.foreground, Background: c.selected, Hover: &core.Style{Background: c.border}, Active: &core.Style{Opacity: core.Alpha(.65)}, FocusVisible: &core.Style{OutlineWidth: 2, OutlineColor: c.focus}}
			},
			Children: []core.Node{core.Icon{SVG: icon(), Style: core.Style{Width: core.Pt(14), Height: core.Pt(14)}}},
		}
	}
	return []core.Node{
		control("window-minimize", "最小化", lucide.Minus, func() { desktop.MinimizeWindow(cx) }),
		core.Dynamic(func(cx *core.Scope) core.Node {
			maximized := desktop.Maximized(cx)
			return control("window-maximize", choose(maximized, "还原窗口", "最大化"), choose(maximized, lucide.Copy, lucide.Square), func() { desktop.ToggleMaximize(cx) })
		}),
		control("window-close", "关闭窗口", lucide.X, func() { desktop.CloseWindow(cx) }),
	}
}
