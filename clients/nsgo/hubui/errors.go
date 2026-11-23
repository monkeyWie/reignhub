package hubui

import (
	"context"
	"errors"
	"strings"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

func operationErrorText(raw string, failure *sessionOperationError) (title, description string) {
	help := sessionAccessHelp(raw, failure)
	if help.Title != "" {
		return help.Title, help.Description
	}
	title, description = "操作未完成", raw
	if strings.Contains(strings.ToLower(raw), "deadline exceeded") || failure != nil && errors.Is(failure, context.DeadlineExceeded) {
		description = "请求未及时返回，请确认操作结果后再决定是否重试。"
	} else if transportError(errors.New(raw)) {
		description = "暂时无法完成操作，请检查设备连接后重试。"
	} else if r := []rune(description); len(r) > 120 {
		description = string(r[:120]) + "…"
	}
	return
}

// Keep the failed operation's context: the user can switch sessions while RPC runs.
type sessionOperationError struct {
	cause                                             error
	target, device, provider, session, source, method string
}

func (e *sessionOperationError) Error() string { return e.cause.Error() }
func (e *sessionOperationError) Unwrap() error { return e.cause }

type accessHelp struct {
	Title, Description string
	Steps              []string
}

func sessionAccessHelp(raw string, failure *sessionOperationError) accessHelp {
	if failure == nil || failure.Error() != raw || failure.method != "send" && failure.method != "steer" || !strings.HasPrefix(failure.provider, "codex-") || !strings.Contains(strings.ToLower(raw), "already has an active writer") {
		return accessHelp{}
	}
	h := accessHelp{Title: "此会话暂时无法发送消息", Description: "该会话由另一个 Codex 进程管理，目前无法连接它。会话历史仍可查看，请确认所在设备上的 Codex 可用后再重试。"}
	where := "被控电脑"
	if failure.device != "" {
		where += "「" + failure.device + "」"
	}
	h.Steps = []string{
		"在" + where + "上确认管理此会话的 Codex 应用、终端或编辑器仍在运行。",
		"在 ReignHub「设置 → Agent」确认「Codex」已启用并显示「已就绪」；如有连接错误，请查看处理指引。",
		"连接恢复后返回原会话重试；请勿仅因这个提示强制结束正在运行的任务。",
	}
	return h
}

func (s *State) errorAtComposer() bool {
	s.read()
	if s.dialog.Get() != "" || s.hostAuthorization.Get() != "" {
		return false
	}
	f := s.failure
	return s.error != "" && f != nil && f.Error() == s.error && s.selected != nil && f.target == s.target && f.provider == str(s.selected, "providerId") && f.session == str(s.selected, "id") && s.conversationVisible()
}

func (s *State) errorOverlay(cx *core.Scope, composer bool) core.Node {
	style := core.Style{Position: core.Absolute, Left: core.Pt(18), Right: core.Pt(18), Bottom: core.Pt(20), AlignItems: core.AlignEnd, ZIndex: 50}
	if composer {
		style.Bottom = core.Pct(100)
		style.Margin.Bottom = 8
		style.AlignItems = core.AlignCenter
	}
	if composer {
		return core.View{TestID: "error-overlay", PointerEvents: core.PointerEventsBoxNone, Style: style, Children: []core.Node{s.errorCard(cx)}}
	}
	style = core.Style{Left: core.Pt(0), Width: core.Pct(100), Bottom: core.Pt(20), Padding: core.Edges{Left: 18, Right: 18}, PaddingEnv: core.EnvEdges{Bottom: core.EnvSafeArea}, AlignItems: core.AlignEnd}
	return core.Overlay{TestID: "error-overlay", Topmost: true, PointerEvents: core.PointerEventsBoxNone, Style: style, Children: []core.Node{s.errorCard(cx)}}
}

func (s *State) errorCard(cx *core.Scope) core.Node {
	message := core.NewMemo(cx, func() string { s.read(); return s.error })
	return core.Dynamic(func(cx *core.Scope) core.Node {
		raw := message.Get()
		help := sessionAccessHelp(raw, s.failure)
		title, description := operationErrorText(raw, s.failure)
		expanded := core.NewSignal(cx, false)
		heading := label(cx, title, 13, false)
		heading.Style.FontWeight = core.FontWeightSemibold
		actions := []core.Node{}
		if help.Title != "" {
			actions = append(actions, button(cx, "session-access-help", "查看接入步骤", nil, "outline", func() { s.dialog.Set("session-access-help") }))
		}
		if description != raw {
			actions = append(actions, button(cx, "session-error-details", "错误详情", nil, "ghost", func() { expanded.Set(!expanded.GetUntracked()) }))
		}
		return core.View{TestID: "error-banner", Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(520), Padding: core.All(14), Gap: 8, CornerRadius: 12, BorderWidth: 1, Shadows: []core.BoxShadow{{Offset: core.Point{Y: 6}, Blur: 24, Color: core.RGBA(0x000000, .16)}}}, StyleFunc: func() core.Style {
			p := palette(cx)
			return core.Style{Background: p.background, BorderColor: core.RGBA(0xf59e0b, .45), Color: p.foreground}
		}, Children: []core.Node{
			row(glyph(cx, lucide.CircleAlert, 18), core.View{Style: fill, Children: []core.Node{heading}}, labelledIconButton(cx, "dismiss-error", "关闭提示", lucide.X, func() { s.error = ""; s.failure = nil; s.changed() })),
			label(cx, description, 12, true),
			core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 6}, Children: actions},
			core.Show{When: expanded.Get, Then: func(cx *core.Scope) core.Node {
				t := label(cx, raw, 11, true)
				t.TestID = "session-error-raw"
				t.Selectable = core.SelectableOn
				return core.Scroll{Style: core.Style{MaxHeight: core.Pt(120)}, Children: []core.Node{t}}
			}},
		}}
	})
}
