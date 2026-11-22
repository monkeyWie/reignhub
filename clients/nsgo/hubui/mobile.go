package hubui

import (
	"strings"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

// Compact screens keep one content pane. The directory is an overlay, so
// opening navigation does not replace the editor, transcript or subscription.
func (s *State) compactWorkspace(cx *core.Scope) core.Node {
	selection := core.NewMemo(cx, func() string {
		s.read()
		if s.selected == nil {
			return ""
		}
		return s.key()
	})
	cx.Cleanup(func() { s.mobileSidebarOpen.Set(false) })
	return core.View{TestID: "compact-workspace", Style: fill, Children: []core.Node{
		core.Dynamic(func(cx *core.Scope) core.Node {
			selection.Get()
			return core.Untracked(cx, func() core.Node {
				if s.selected != nil {
					return s.conversation(cx, true)
				}
				return core.View{Style: fill, Children: []core.Node{
					core.View{TestID: "compact-home-header", Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12, Padding: core.Symmetric(8, 12), FlexShrink: 0}, Children: []core.Node{s.mobileMenuButton(cx), s.brand(cx, 16, false)}},
					s.home(cx),
				}}
			})
		}),
		base.Sheet{TestID: "mobile-sidebar", Label: locales.For(cx).ShowSidebar(), Side: core.PlaceLeft,
			Open: s.mobileSidebarOpen.Get,
			OnOpenChange: func(open bool, _ base.Reason) {
				if !open {
					s.closeMobileSidebar()
				}
			},
			BackdropStyle:  core.Style{Background: core.RGB(0x000000).Faded(.35)},
			PopupStyle:     core.Style{Width: core.Pt(320), MaxWidth: core.Pct(88), Height: core.Pct(100)},
			PopupStyleFunc: func() core.Style { return core.Style{Background: palette(cx).sidebar} },
			Children:       []core.Node{core.View{Style: fill.Merge(core.Style{PaddingEnv: core.EnvEdges{Top: core.EnvSafeArea, Bottom: core.EnvSafeArea, Left: core.EnvSafeArea}}), Children: []core.Node{s.directory(cx)}}},
		},
	}}
}

func (s *State) mobileMenuButton(cx *core.Scope) core.Node {
	v := labelledIconButton(cx, "mobile-menu", locales.For(cx).ShowSidebar(), lucide.Menu, s.openMobileSidebar).(core.View)
	v.Expanded = s.mobileSidebarOpen.Get
	v.Style.Width, v.Style.Height, v.Style.MinHeight = core.Pt(44), core.Pt(44), core.Pt(44)
	return v
}

func (s *State) openMobileSidebar() {
	s.clearRootBack()
	s.cx.DismissKeyboard()
	s.mobileSidebarOpen.Set(true)
}

func (s *State) closeMobileSidebar() {
	s.mobileSidebarOpen.Set(false)
	s.clearRootBack()
	if s.selected != nil {
		s.markSessionRead(s.selected)
		s.changed()
	}
}

func (s *State) conversationVisible() bool {
	return s.selected != nil && (!s.narrowLayout() || !s.mobileSidebarOpen.Get())
}

func (s *State) showHome() {
	s.cancelSessionEntry()
	s.restoreSession = nil
	s.persistDraft()
	s.rememberDraft()
	s.rememberSession()
	if s.watchCancel != nil {
		s.watchCancel()
	}
	s.sessionGeneration++
	s.selected, s.transcript = nil, nil
	s.messageRows, s.approvals, s.uploads = nil, nil, nil
	s.transcriptSyncing, s.transcriptLoading = false, false
	s.draft.Set("")
	s.closeMobileSidebar()
	s.changed()
}
func deviceName(d Object) string {
	if n := strings.TrimSpace(str(d, "name")); n != "" && n != "RAS" {
		return n
	}
	if code := str(d, "connectionId"); code != "" {
		return "设备 " + code
	}
	return "未命名设备"
}
func deviceStatus(d Object) string {
	if !flag(d, "connected") {
		if online, known := d["online"].(bool); known {
			if online {
				return "在线 · 点击连接"
			}
			return "离线"
		}
		return "在线状态暂不可用"
	}
	path := str(d, "path")
	if path == "" {
		return "已连接 · 正在协商连接路径"
	}
	return connectivity.PathLabel(path)
}
