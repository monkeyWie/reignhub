package hubui

import (
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

type tones struct {
	background, foreground, muted, mutedText, border                core.Color
	canvas, sidebar, elevated, selected, accent, brand, focus, code core.Color
}

func palette(cx *core.Scope) tones {
	p := cx.Theme().Light
	if cx.Dark() {
		p = cx.Theme().Dark
	}
	role := func(name string, fallback core.Color) core.Color {
		if c, ok := p.Colors[name]; ok {
			return c
		}
		return fallback
	}
	return tones{background: p.Background, foreground: p.Text, muted: p.Surface,
		mutedText: p.TextMuted, border: p.Border, canvas: role("canvas", p.Surface),
		sidebar: role("sidebar", p.Surface), elevated: role("elevated", p.Background),
		selected: role("selected", p.SurfacePressed), accent: p.Accent, brand: role("brand", p.Accent), focus: p.Focus, code: role("codeSurface", p.Surface)}
}

var fill = core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), MinHeight: core.Pt(0)}

func row(nodes ...core.Node) core.Node {
	return core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, MinWidth: core.Pt(0)}, Children: nodes}
}
func leftButton(node core.Node) core.Node {
	v := node.(core.View)
	v.Style.JustifyContent = core.JustifyStart
	return v
}
func spacer() core.Node { return core.View{Style: core.Style{FlexGrow: 1}} }
func label(cx *core.Scope, value string, size core.Points, muted bool) core.Text {
	return core.Text{Content: value, Style: core.Style{FontSize: size, LineHeight: size * 1.5}, StyleFunc: func() core.Style {
		c := palette(cx).foreground
		if muted {
			c = palette(cx).mutedText
		}
		return core.Style{Color: c}
	}}
}
func singleLine(cx *core.Scope, value func() string, size core.Points, muted bool) core.Text {
	t := dynamicText(cx, value, size, muted)
	t.MaxLines = 1
	t.Style.FlexShrink = 1
	t.Style.MinWidth = core.Pt(0)
	return t
}
func dynamicText(cx *core.Scope, value func() string, size core.Points, muted bool) core.Text {
	t := label(cx, "", size, muted)
	t.ContentFunc = value
	return t
}
func glyph(cx *core.Scope, icon func() *core.SVG, size core.Points) core.Node {
	return core.Icon{SVG: icon(), Style: core.Style{Width: core.Pt(size), Height: core.Pt(size), FontSize: size, FlexShrink: 0}, StyleFunc: func() core.Style { return core.Style{Color: palette(cx).foreground} }}
}

const buttonHorizontalPadding core.Points = 12

func button(cx *core.Scope, id, title string, icon func() *core.SVG, variant string, onPress func()) core.Node {
	nodes := []core.Node{}
	if icon != nil {
		nodes = append(nodes, core.Icon{SVG: icon(), Style: core.Style{Width: core.Pt(16), Height: core.Pt(16), FlexShrink: 0}})
	}
	if title != "" {
		t := core.Text{Content: title, MaxLines: 1, Style: core.Style{FontSize: 13, LineHeight: 18, FlexShrink: 1, MinWidth: core.Pt(0)}}
		nodes = append(nodes, t)
	}
	return core.View{TestID: id, Role: core.RoleButton, Label: title, OnPress: func(core.PressEvent) {
		if onPress != nil {
			onPress()
		}
	}, Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter, Gap: 8, Padding: core.Symmetric(8, buttonHorizontalPadding), MinHeight: core.Pt(36), CornerRadius: 10, FlexShrink: 0}, StyleFunc: func() core.Style {
		c := palette(cx)
		st := pressStyle(cx).Merge(core.Style{Color: c.foreground})
		if variant == "outline" {
			st.BorderWidth = 1
			st.BorderColor = c.border
			st.Background = c.background
		}
		if variant == "primary" {
			st = filledButtonStyle(cx)
		}
		st.Disabled = &core.Style{Opacity: core.Alpha(.5)}
		return st
	}, Children: nodes}
}

func (s *State) refreshSessionsButton(cx *core.Scope) core.Node {
	busy := core.NewMemo(cx, func() bool { s.read(); return s.manualRefresh })
	v := button(cx, "refresh-sessions", "", nil, "ghost", s.refreshSessions).(core.View)
	v.Label = "刷新会话"
	v.Busy = busy.Get
	v.Disabled = func() bool { s.read(); return busy.Get() || !s.anyConnected() }
	v.Tooltip = tooltipCard(cx, dynamicText(cx, func() string {
		s.read()
		if !s.anyConnected() {
			return "设备离线，重新连接后可刷新会话"
		}
		if busy.Get() {
			return "正在刷新会话"
		}
		return "刷新会话"
	}, 12, false))
	v.Children = []core.Node{core.Dynamic(func(cx *core.Scope) core.Node {
		if busy.Get() {
			return ui.Spinner{TestID: "refresh-sessions-loading", Label: "正在刷新会话", Variant: ui.SpinnerSpokes, Size: 16}
		}
		return glyph(cx, lucide.RefreshCw, 16)
	})}
	return v
}
func divider(cx *core.Scope) core.Node {
	return core.View{Style: core.Style{Width: core.Pt(1), Height: core.Pct(100), FlexShrink: 0}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).border} }}
}

func (s *State) copyButton(cx *core.Scope, id, title, variant string, value func() string, disabled ...func() bool) core.Node {
	copied := core.NewMemo(cx, func() bool {
		s.read()
		return s.copiedValue != "" && s.copiedValue == value()
	})
	return core.Dynamic(func(cx *core.Scope) core.Node {
		text, icon := title, lucide.Copy
		if copied.Get() {
			icon = lucide.Check
			if title != "" {
				text = "已复制"
			}
		}
		v := button(cx, id, text, icon, variant, func() { s.copy(value()) }).(core.View)
		if len(disabled) > 0 {
			v.Disabled = disabled[0]
		}
		if title == "" {
			v.Label = choose(copied.Get(), "已复制", "复制")
			v.Tooltip = tooltipCard(cx, label(cx, v.Label, 12, false))
		}
		return v
	})
}
func heading(cx *core.Scope, title, subtitle string) core.Node {
	t := label(cx, title, 24, false)
	t.Style.FontWeight = core.FontWeightSemibold
	return core.View{Style: core.Style{Gap: 6}, Children: []core.Node{t, label(cx, subtitle, 13, true)}}
}
func card(cx *core.Scope, nodes ...core.Node) core.Node {
	return core.View{Style: core.Style{Padding: core.All(24), Gap: 14, CornerRadius: 16, BorderWidth: 1}, StyleFunc: func() core.Style {
		return core.Style{Background: palette(cx).elevated, BorderColor: palette(cx).border}
	}, Children: nodes}
}

// Sections inside an already bounded surface use spacing and typography;
// another card border would repeat the container's visual boundary.
func contentSection(cx *core.Scope, title, description string, nodes ...core.Node) core.Node {
	children := []core.Node{}
	if title != "" {
		t := label(cx, title, 15, false)
		t.Style.FontWeight = core.FontWeightSemibold
		children = append(children, t)
	}
	if description != "" {
		children = append(children, label(cx, description, 13, true))
	}
	return core.View{Style: core.Style{Gap: 12, MinWidth: core.Pt(0)}, Children: append(children, nodes...)}
}

func sectionDivider(cx *core.Scope) core.Node {
	return core.View{Style: core.Style{Height: core.Pt(1), Width: core.Pct(100), FlexShrink: 0}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).border} }}
}

func Root(cx *core.Scope) core.Node {
	api := platformAPI(cx)
	if demoRequested() {
		api = newDemoAPI()
	}
	return RootWithAPI(cx, api, true)
}
func RootWithAPI(cx *core.Scope, api API, start bool) core.Node {
	s := newState(cx, api)
	s.loadAppearance()
	installScrollbars(cx)
	s.installDesktopTray(cx)
	s.installWorkspaceNavigation()
	s.observeNativeBackground()
	s.observeScreenAwake()
	if start {
		s.initialize()
	} else {
		s.ready = true
	}
	return s.workspaceView(cx)
}

func (s *State) workspaceView(cx *core.Scope) core.Node {
	page := core.NewMemo(cx, func() bool { return s.width.Get() < compactBreakpoint })
	return s.windowFrame(cx, core.View{TestID: "agenthub", Style: fill.Merge(core.Style{PaddingEnv: core.EnvEdges{Top: core.EnvSafeArea, Bottom: core.EnvSafeArea}}), StyleFunc: func() core.Style {
		return core.Style{Background: palette(cx).canvas, Color: palette(cx).foreground, FontSize: 13}
	}, OnLayout: func(b core.Bounds) { s.width.Set(b.Size.Width) }, Children: []core.Node{
		core.Dynamic(func(cx *core.Scope) core.Node {
			compact := page.Get()
			// Only crossing the layout breakpoint replaces the workspace. Child
			// construction also reads environment/theme data; keep those updates
			// in its reactive properties instead of resetting every pane.
			return core.Untracked(cx, func() core.Node {
				if compact {
					return s.compactWorkspace(cx)
				}
				selection := core.NewMemo(cx, func() string {
					s.read()
					if s.selected == nil {
						return ""
					}
					return s.key()
				})
				return core.View{Style: fill, Children: []core.Node{s.workspaceToolbar(cx), core.View{Style: fill.Merge(core.Style{Direction: core.Row}), Children: []core.Node{
					core.View{TestID: "sidebar-container", StyleFunc: func() core.Style {
						return core.Style{Display: choose(s.sidebarHidden.Get(), core.DisplayNone, core.DisplayFlex), Width: core.Pt(s.sidebarWidth()), MinHeight: core.Pt(0), FlexShrink: 0}
					}, Children: []core.Node{s.directory(cx)}},
					s.resize(cx),
					core.Dynamic(func(cx *core.Scope) core.Node {
						selection.Get()
						return core.Untracked(cx, func() core.Node { return s.conversation(cx, false) })
					}),
				}}}}
			})
		}),
		core.Show{When: func() bool { s.read(); return s.error != "" && !s.errorAtComposer() }, Then: func(cx *core.Scope) core.Node {
			return s.errorOverlay(cx, false)
		}},
		s.noticeOverlay(cx),
		core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) }),
		s.hostAuthorizationLayer(),
	}})
}

func (s *State) resize(cx *core.Scope) core.Node {
	w := s.sessionWidth
	var initial core.Points
	return core.View{TestID: "resize-sessions", Style: core.Style{Width: core.Pt(5), FlexShrink: 0, AlignItems: core.AlignCenter}, StyleFunc: func() core.Style {
		return core.Style{Display: choose(s.sidebarHidden.Get(), core.DisplayNone, core.DisplayFlex), Background: palette(cx).background}
	}, Class: "cursor-col-resize", OnPan: func(e core.PanEvent) {
		if e.Phase == core.GestureBegan {
			initial = w.Get()
		}
		lo, hi := core.Points(260), core.Points(400)
		w.Set(min(hi, max(lo, initial+e.Translation.X)))
	}, Children: []core.Node{core.View{Style: core.Style{Width: core.Pt(1), Height: core.Pct(100)}, StyleFunc: func() core.Style { return core.Style{Hover: &core.Style{Background: palette(cx).border}} }}}}
}
func choose[T any](condition bool, a, b T) T {
	if condition {
		return a
	}
	return b
}
func isMobile(cx *core.Scope) bool {
	return cx.Env().OS() == core.OS("ios") || cx.Env().OS() == core.OS("android")
}

func (s *State) welcome(cx *core.Scope) core.Node {
	return core.View{Style: fill.Merge(core.Style{AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter, Padding: core.All(32), Gap: 20}), StyleFunc: func() core.Style { return s.workspaceSurface(cx) }, Children: []core.Node{glyph(cx, lucide.Network, 52), label(cx, "你的 Agent，随处连接", 26, false), label(cx, "输入设备 ID，连接电脑上的 Codex 与其它 Agent。", 13, true), button(cx, "welcome-connect", "连接你的电脑", nil, "primary", func() { s.dialog.Set("connect") })}}
}
