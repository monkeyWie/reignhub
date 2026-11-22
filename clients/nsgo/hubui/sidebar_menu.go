package hubui

import (
	"context"
	"strings"
	"time"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

// All trailing sidebar actions share the title-bar button's center line.
func sidebarIconButton(cx *core.Scope, id, title string, icon func() *core.SVG, action func()) core.Node {
	v := labelledIconButton(cx, id, title, icon, action).(core.View)
	v.Style.Width, v.Style.Height, v.Style.MinHeight, v.Style.Padding = core.Pt(40), core.Pt(36), core.Pt(36), core.All(8)
	if !cx.Env().Desktop() {
		v.Style.Height, v.Style.MinHeight = core.Pt(44), core.Pt(44)
	}
	return v
}

func (s *State) accountName() string {
	s.read()
	if !flag(s.accountStatus, "linked") {
		return ""
	}
	if name := strings.TrimSpace(str(object(s.accountStatus["user"]), "name")); name != "" {
		return name
	}
	return "已登录"
}

func (s *State) refreshAccountStatus() {
	if s.accountStatusLoading || s.ctx.Err() != nil {
		return
	}
	s.accountStatusLoading = true
	method, origin := "client.account.status", serviceOrigin()
	if s.api.CanHost() {
		method = "host.account.status"
		if configured := str(s.host, "serviceUrl"); configured != "" {
			origin = configured
		}
	}
	params := Object{"service": origin, "name": s.clientName}
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		defer cancel()
		v, err := s.api.Call(ctx, method, params, "", nil)
		var directory []Object
		var listErr error
		if err == nil && flag(object(v), "linked") {
			var list any
			list, listErr = s.api.Call(ctx, strings.TrimSuffix(method, "status")+"devices", params, "", nil)
			directory = objects(object(list)["devices"])
		}
		s.cx.Post(func() {
			s.accountStatusLoading = false
			if s.ctx.Err() == nil && err == nil {
				s.accountStatus = object(v)
				if listErr == nil {
					s.accountDevices = directory
				}
				s.changed()
			}
		})
	}()
}

func (s *State) sidebarMenu(cx *core.Scope) core.Node {
	open := core.NewSignal(cx, false)
	action := func(id, text string, icon func() *core.SVG, f func()) core.Node {
		v := button(cx, id, text, icon, "ghost", func() { open.Set(false); f() }).(core.View)
		v.Style.JustifyContent = core.JustifyStart
		return v
	}
	refresh := s.refreshSessionsButton(cx).(core.View)
	refresh.Style.JustifyContent = core.JustifyStart
	refresh.Children = append(refresh.Children, label(cx, "刷新会话", 13, false))
	return base.Popover{TestID: "sidebar-menu", Label: "设置与账号", Open: open.Get,
		OnOpenChange: func(value bool, _ base.Reason) {
			open.Set(value)
			if value {
				s.refreshAccountStatus()
			}
		},
		Placement: core.PlaceTop, Align: core.AlignStart, Offset: 6, Margin: 8,
		Style:     core.Style{MinHeight: core.Pt(40), MinWidth: core.Pt(40), MaxWidth: core.Pct(75), Padding: core.All(10), CornerRadius: 8, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
		StyleFunc: func() core.Style { return pressStyle(cx) },
		Tooltip:   tooltipCard(cx, label(cx, "设置与账号", 12, false)),
		Trigger: core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, MinWidth: core.Pt(0)}, Children: []core.Node{
			glyph(cx, lucide.Settings, 16), core.Show{When: func() bool { return s.accountName() != "" }, Then: func(cx *core.Scope) core.Node {
				return singleLine(cx, s.accountName, 13, false)
			}},
		}},
		PopupStyle: core.Style{Padding: core.All(6), Gap: 2, CornerRadius: 12, BorderWidth: 1},
		PopupStyleFunc: func() core.Style {
			c := palette(cx)
			return core.Style{Width: core.Pt(min(250, cx.Env().Window().Width-24)), Background: c.elevated, Color: c.foreground, BorderColor: c.border, Shadows: []core.BoxShadow{{Offset: core.Point{Y: 4}, Blur: 16, Color: core.RGB(0).Faded(.16)}}}
		},
		Children: []core.Node{
			action("account-menu", "账号与设备同步", lucide.User, func() { s.dialog.Set("account") }),
			action("server-settings", "设置", lucide.Settings, func() { s.openSettings("general") }),
			action("theme-toggle", "切换主题", lucide.SunMoon, s.toggleTheme),
			refresh,
			action("sidebar-help", "帮助", lucide.Info, func() { s.openSettings("about") }),
		},
	}
}
