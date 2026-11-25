package hubui

import (
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
	"strings"
)

func (s *State) openSettings(section string) {
	s.settingsReturn = ""
	if section == "general" && s.width.GetUntracked() < 760 {
		section = ""
	}
	s.dialog.Set("settings:" + section)
}
func (s *State) openSettingsDialog(kind string) {
	s.settingsReturn = "settings:" + s.settingsSection
	s.dialog.Set(kind)
}
func (s *State) closeDialog() { next := s.settingsReturn; s.settingsReturn = ""; s.dialog.Set(next) }

type settingsSection struct {
	id, title, detail string
	icon              func() *core.SVG
}

var settingsSections = []settingsSection{
	{"general", "通用", "主题与外观", lucide.SlidersHorizontal},
	{"agents", "Agent", "本机 Agent 与自定义接入", lucide.Bot},
	{"connection", "连接", "服务与设备", lucide.Network},
	{"account", "账号", "登录与设备同步", lucide.User},
	{"about", "关于", "文档与帮助", lucide.Info},
}

func (s *State) preferences(cx *core.Scope, initial string) core.Node {
	if initial == "settings" {
		initial = "general"
	}
	section := core.NewSignal(cx, initial)
	s.settingsSection = initial
	search := core.NewSignal(cx, "")
	narrow := core.NewMemo(cx, func() bool { return s.width.Get() < 760 })
	close := func() { s.settingsReturn = ""; s.dialog.Set("") }
	sidebar := func(cx *core.Scope) core.Node {
		children := []core.Node{row(heading(cx, "设置", "让工作区更适合你"), spacer(), core.Show{When: narrow.Get, Then: func(cx *core.Scope) core.Node {
			return labelledIconButton(cx, "settings-close", "关闭设置", lucide.X, close)
		}}), formInput{TestID: "settings-search", Placeholder: "搜索设置", Label: "搜索设置", Value: search.Get, OnChange: search.Set, Style: core.Style{MinHeight: core.Pt(40)}}, label(cx, "工作区", 11, true)}
		for _, entry := range settingsSections {
			item := entry
			children = append(children, core.Show{When: func() bool {
				q := strings.ToLower(strings.TrimSpace(search.Get()))
				return q == "" || strings.Contains(strings.ToLower(item.title+" "+item.detail+" "+item.id), q)
			}, Then: func(cx *core.Scope) core.Node {
				return core.View{TestID: "settings-nav-" + item.id, Role: core.RoleButton, Label: item.title, OnPress: func(core.PressEvent) { s.settingsSection = item.id; section.Set(item.id) }, Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12, Padding: core.Symmetric(12, 14), CornerRadius: 12}, StyleFunc: func() core.Style {
					c := palette(cx)
					st := pressStyle(cx)
					if section.Get() == item.id {
						st.Background = c.selected
					}
					return st
				}, Children: []core.Node{core.Icon{SVG: item.icon(), Style: core.Style{Width: core.Pt(19), Height: core.Pt(19)}, StyleFunc: func() core.Style { return core.Style{Color: palette(cx).accent} }}, label(cx, item.title, 14, false), spacer(), core.Show{When: narrow.Get, Then: func(cx *core.Scope) core.Node { return glyph(cx, lucide.ChevronRight, 16) }}}}
			}})
		}
		children = append(children, spacer(), label(cx, "ReignHub · Your agents, everywhere.", 11, true))
		return core.View{TestID: "settings-navigation", Style: core.Style{Padding: core.All(24), Gap: 12, MinHeight: core.Pt(0)}, StyleFunc: func() core.Style {
			st := core.Style{Width: core.Pt(238), Background: palette(cx).sidebar}
			if narrow.Get() {
				st.Width = core.Pct(100)
				st.FlexGrow = 1
			}
			return st
		}, Children: children}
	}
	content := func(cx *core.Scope) core.Node {
		return core.View{TestID: "settings-content", Style: fill, Children: []core.Node{
			core.View{Style: core.Style{Padding: core.Edges{Top: 24, Left: 28, Right: 20, Bottom: 12}}, Children: []core.Node{row(core.Show{When: narrow.Get, Then: func(cx *core.Scope) core.Node {
				return labelledIconButton(cx, "settings-back", "返回设置列表", lucide.ArrowLeft, func() { s.settingsSection = ""; section.Set("") })
			}}, dynamicText(cx, func() string {
				for _, item := range settingsSections {
					if item.id == section.Get() {
						return item.title
					}
				}
				return "设置"
			}, 20, false), spacer(), labelledIconButton(cx, "settings-close", "关闭设置", lucide.X, close))}},
			core.Scroll{TestID: "settings-scroll", Style: fill, Children: []core.Node{core.View{Style: core.Style{Padding: core.Edges{Top: 16, Left: 28, Right: 28, Bottom: 32}, Gap: 24}, Children: []core.Node{core.Dynamic(func(cx *core.Scope) core.Node {
				switch section.Get() {
				case "agents":
					if s.api.CanHost() {
						return s.agentSettings(cx)
					}
					return contentSection(cx, "", "", glyph(cx, lucide.Bot, 28), label(cx, "在电脑上管理 Agent", 18, false), label(cx, "请在被控电脑的 ReignHub 中打开「设置 → Agent」，配置本机运行的 Agent。", 14, true))
				case "connection":
					return s.connectionSettings(cx)
				case "account":
					return contentSection(cx, "", "", glyph(cx, lucide.User, 28), label(cx, "账号与设备同步", 18, false), label(cx, "登录账号，将已配对的设备带到你的其他客户端。", 14, true), button(cx, "settings-account", "管理账号", nil, "primary", func() { s.openSettingsDialog("account") }))
				case "about":
					return core.View{Style: core.Style{Gap: 20}, Children: []core.Node{s.brand(cx, 28, false), label(cx, "你的 Agent，随处连接。", 16, false), label(cx, "在桌面、手机和浏览器之间，继续你的工作。", 14, true), button(cx, "settings-docs", "使用文档", lucide.BookOpen, "outline", func() { cx.OpenURL(serviceOrigin()+"/docs", nil) }), button(cx, "settings-source", "开源项目", lucide.GitBranch, "ghost", func() { cx.OpenURL("https://github.com/monkeyWie/reignhub", nil) })}}
				default:
					return s.appearanceSettings(cx)
				}
			})}}}},
		}}
	}
	return base.Dialog{TestID: "dialog-settings", Label: "设置", Open: func() bool { return true }, OnOpenChange: func(open bool, _ base.Reason) {
		if !open {
			close()
		}
	}, BackdropStyle: core.Style{Background: core.RGB(0x000000).Faded(.26), BackdropFilters: []core.Filter{core.Blur(8)}}, PopupStyle: core.Style{CornerRadius: 24, BorderWidth: 1, Overflow: core.Hidden, Shadows: []core.BoxShadow{{Offset: core.Point{Y: 18}, Blur: 70, Color: core.RGB(0x000000).Faded(.25)}}}, PopupStyleFunc: func() core.Style {
		w := cx.Env().Window()
		c := palette(cx)
		return core.Style{Width: core.Pt(min(1050, max(0, w.Width-choose(narrow.Get(), core.Points(16), core.Points(64))))), Height: core.Pt(min(740, max(0, w.Height-choose(narrow.Get(), core.Points(24), core.Points(64))))), Background: c.background, BorderColor: c.border}
	}, Children: []core.Node{core.Dynamic(func(cx *core.Scope) core.Node {
		compact := narrow.Get()
		if compact {
			return core.Dynamic(func(cx *core.Scope) core.Node {
				if section.Get() == "" {
					return sidebar(cx)
				}
				return content(cx)
			})
		}
		return core.View{Style: fill.Merge(core.Style{Direction: core.Row}), Children: []core.Node{sidebar(cx), divider(cx), content(cx)}}
	})}}
}
func (s *State) appearanceSettings(cx *core.Scope) core.Node {
	return core.View{TestID: "appearance-settings", Style: core.Style{Gap: 28}, Children: []core.Node{
		label(cx, "外观", 18, false), ui.Select[string]{TestID: "appearance-select", Label: "主题", Options: []ui.Option[string]{{Value: "system", Label: "跟随系统"}, {Value: "light", Label: "明亮"}, {Value: "dark", Label: "暗黑"}}, Value: func() string { s.read(); return s.appearance }, OnChange: s.setAppearance, Style: core.Style{MaxWidth: core.Pt(300)}},
	}}
}
func (s *State) connectionSettings(cx *core.Scope) core.Node {
	if !s.api.CanHost() {
		connection := contentSection(cx, "", "", label(cx, "连接服务", 18, false), label(cx, serviceOrigin(), 14, true), label(cx, "连接设备时，可以选择官方服务或你自己部署的服务。", 14, true), button(cx, "settings-connect", "连接设备", lucide.Plus, "primary", func() { s.openSettingsDialog("connect") }))
		if !isWeb() && cx.Env().OS() == core.OSAndroid {
			return core.View{Style: core.Style{Gap: 16}, Children: []core.Node{connection,
				contentSection(cx, "", "", label(cx, "后台连接", 18, false), label(cx, "连接期间通过前台服务保持网络。若锁屏后仍断开，可在系统电池设置中允许 ReignHub 后台运行。", 13, true),
					button(cx, "background-settings", "打开系统后台设置", lucide.Settings, "outline", func() {
						cx.CallHost("ras.background-settings", nil, func(_ any, err error) {
							if err != nil {
								s.error = err.Error()
								s.changed()
							}
						})
					})),
			}}
		}
		return connection
	}
	return contentSection(cx, "", "", label(cx, "本机服务", 18, false), dynamicText(cx, func() string { s.read(); return str(s.host, "serviceUrl") }, 14, true), dynamicText(cx, func() string { s.read(); return str(s.host, "name") }, 14, false), label(cx, "优先直连，自动回退到加密中继。", 13, true), button(cx, "settings-service", "配置服务", lucide.Network, "outline", func() { s.openSettingsDialog("service-settings") }), button(cx, "settings-password", "访问密码", lucide.LockKeyhole, "ghost", func() { s.openSettingsDialog("password") }), sectionDivider(cx), s.terminalSettings(cx))
}

func (s *State) agentSettings(cx *core.Scope) core.Node {
	return core.View{TestID: "agent-settings-list", Style: core.Style{Gap: 16}, Children: []core.Node{
		label(cx, "本机 Agent", 22, false),
		button(cx, "add-agent", "添加 ACP Agent", lucide.Plus, "outline", func() { s.openSettingsDialog("agent-config") }),
		core.Each(func() []Object { s.read(); return s.localProviders }, func(p Object) string { return str(p, "id") }, func(cx *core.Scope, p func() Object, _ func() int) core.Node {
			mounted := true
			cx.Cleanup(func() { mounted = false })
			info := row(agentGlyph(cx, str(p(), "id"), 22), agentLabel(cx, p), s.agentControls(cx, "settings-", p, s.openSettingsDialog))
			actions := core.View{Style: core.Style{Direction: core.Row, Gap: 8, JustifyContent: core.JustifyEnd, Wrap: core.Wrap}, Children: []core.Node{
				core.Show{When: func() bool { return str(p(), "id") != "codex-app" }, Then: func(cx *core.Scope) core.Node {
					return s.actionButton(cx, "configure-agent-"+str(p(), "id"), "配置", lucide.SlidersHorizontal, "ghost", func() {
						providerID := str(p(), "id")
						s.call("configure-agent-"+providerID, "host.agents", nil, func(v any) {
							s.agentDefinitions = object(v)
							if mounted {
								s.openSettingsDialog("agent-config:" + providerID)
							}
						})
					})
				}},
			}}
			return core.View{Style: core.Style{Gap: 16}, Children: []core.Node{sectionDivider(cx), contentSection(cx, "", "", info, actions)}}
		}),
	}}
}
