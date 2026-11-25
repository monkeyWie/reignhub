package hubui

import (
	"fmt"
	"strings"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

type questionDraft struct {
	Value  string
	Custom bool
}

func (s *State) pruneQuestionDrafts() {
	active := map[string]bool{}
	for _, a := range s.approvals {
		active[s.approvalKey(a)] = true
	}
	for key := range s.questionDrafts {
		if strings.HasPrefix(key, "approval:"+s.key()+":") && !active[key] {
			delete(s.questionDrafts, key)
		}
	}
}

func (s *State) questionCard(cx *core.Scope, a Object) core.Node {
	questions := objects(a["questions"])
	id, key, session := str(a, "id"), s.approvalKey(a), s.key()
	if s.questionDrafts == nil {
		s.questionDrafts = map[string]map[string]questionDraft{}
	}
	if s.questionDrafts[key] == nil {
		s.questionDrafts[key] = map[string]questionDraft{}
	}
	drafts := s.questionDrafts[key]
	values := make([]*core.Signal[questionDraft], len(questions))
	for i, q := range questions {
		values[i] = core.NewSignal(cx, drafts[str(q, "id")])
	}
	index := core.NewSignal(cx, 0)
	busy := func() bool { return s.actionPending(key) }
	disabled := func() bool {
		if s.key() != session || !s.canWriteSession() || busy() {
			return true
		}
		for _, current := range s.approvals {
			if str(current, "id") == id {
				return false
			}
		}
		return true
	}
	submit := func() {
		if disabled() {
			return
		}
		answers := Object{}
		for i, q := range questions {
			value := strings.TrimSpace(values[i].Get().Value)
			if value != "" {
				answers[str(q, "id")] = Object{"answers": []string{value}}
			} else if !flag(a, "allowSkip") {
				index.Set(i)
				return
			}
		}
		decision := "input"
		if len(answers) == 0 && flag(a, "allowSkip") {
			decision = "skip"
		}
		s.approvalAction(a, Object{"decision": decision, "answers": answers})
	}
	content := core.Dynamic(func(cx *core.Scope) core.Node {
		i := index.Get()
		q, value := questions[i], values[i]
		set := func(v questionDraft) {
			if !disabled() {
				drafts[str(q, "id")] = v
				value.Set(v)
			}
		}
		advance := func() {
			if disabled() {
				return
			}
			if i+1 < len(questions) {
				index.Set(i + 1)
			} else {
				submit()
			}
		}
		heading := str(q, "header")
		if heading == "" {
			heading = "提问"
		}
		header := []core.Node{questionLeadingIcon(cx, "question-heading-icon-"+id, lucide.MessageCircleQuestionMark),
			singleLine(cx, func() string { return heading }, 13, true), spacer()}
		if len(questions) > 1 {
			back := questionHeaderButton(cx, "question-back-"+id, "上一题", lucide.ChevronLeft, func() { index.Set(i - 1) })
			back.Disabled = func() bool { return disabled() || i == 0 }
			header = append(header, back, label(cx, fmt.Sprintf("%d / %d", i+1, len(questions)), 12, true))
		}
		if flag(a, "allowSkip") {
			close := questionHeaderButton(cx, "question-dismiss-"+id, "跳过全部问题", lucide.X, func() {
				if !disabled() {
					s.approvalAction(a, Object{"decision": "skip"})
				}
			})
			close.Disabled, close.Busy = disabled, busy
			header = append(header, close)
		}
		children := []core.Node{core.View{TestID: "question-heading-" + id, Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, MinHeight: core.Pt(32), Padding: core.Edges{Left: 8, Right: 2}}, Children: header},
			core.View{Style: core.Style{Padding: core.Edges{Top: 4, Bottom: 8, Left: 8, Right: 8}}, Children: []core.Node{label(cx, str(q, "question"), 14, false)}}}
		options := objects(q["options"])
		optionRows := []core.Node{}
		for j, option := range options {
			text := str(option, "label")
			selected := func() bool { return !value.Get().Custom && value.Get().Value == text }
			body := []core.Node{label(cx, text, 14, false)}
			if description := str(option, "description"); description != "" {
				body = append(body, label(cx, description, 12, true))
			}
			optionRows = append(optionRows, core.View{TestID: fmt.Sprintf("question-option-%s-%d-%d", id, i, j), Role: core.RoleRadio, Label: text, Checked: selected, Disabled: disabled,
				OnPress: func(core.PressEvent) {
					set(questionDraft{Value: text})
					advance()
				},
				Style: core.Style{Group: "question-option", Direction: core.Row, AlignItems: core.AlignStart, Gap: 8, Padding: core.Symmetric(6, 8), MinWidth: core.Pt(0), CornerRadius: 10},
				StyleFunc: func() core.Style {
					c := palette(cx)
					st := pressStyle(cx)
					st.MinHeight = core.Pt(choose(cx.Env().Desktop(), core.Points(36), core.Points(44)))
					st.Hover = &core.Style{Background: c.foreground.Faded(.06)}
					st.Active = &core.Style{Background: c.foreground.Faded(.1)}
					st.Disabled = &core.Style{Opacity: core.Alpha(.5)}
					if selected() {
						st.Background = c.brand.Faded(.12)
					}
					return st
				}, Children: []core.Node{core.View{
					TestID: fmt.Sprintf("question-option-icon-%s-%d-%d", id, i, j), Style: core.Style{Width: core.Pt(20), Height: core.Pt(20), FlexShrink: 0, Margin: core.Edges{Top: 1}, BorderWidth: 1, CornerRadius: 5, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
					StyleFunc: func() core.Style {
						c := palette(cx)
						return core.Style{Color: choose(selected(), c.brand, c.mutedText), BorderColor: choose(selected(), c.brand, c.mutedText.Faded(.35))}
					}, Children: []core.Node{core.Dynamic(func(cx *core.Scope) core.Node {
						if selected() {
							return core.Icon{SVG: lucide.Check(), Style: core.Style{Width: core.Pt(14), Height: core.Pt(14)}}
						}
						return core.Text{Content: fmt.Sprint(j + 1), Style: core.Style{FontSize: 12, LineHeight: 18}}
					})}},
					core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Gap: 2}, Children: body},
					core.Dynamic(func(cx *core.Scope) core.Node {
						if busy() && selected() {
							return core.View{Style: core.Style{Margin: core.Edges{Top: 3}}, Children: []core.Node{s.loadingRing("question-option-loading-"+id, "正在确认回答", 16)}}
						}
						return core.Icon{SVG: lucide.ArrowRight(), Style: core.Style{Width: core.Pt(16), Height: core.Pt(16), FlexShrink: 0, Margin: core.Edges{Top: 3}}, StyleFunc: func() core.Style {
							st := core.Style{Color: palette(cx).mutedText}
							if !selected() {
								st.Opacity = core.Alpha(0)
								st.GroupStyle = &core.GroupStyle{Name: "question-option", Hover: &core.Style{Opacity: core.Alpha(1)}, FocusWithin: &core.Style{Opacity: core.Alpha(1)}}
							}
							return st
						}}
					}),
				}})
		}
		if len(optionRows) > 0 {
			children = append(children, core.View{Style: core.Style{Gap: 2}, Children: optionRows})
		}
		input := editorInput{TestID: fmt.Sprintf("question-input-%s-%d", id, i), Label: "自定义回答", Placeholder: choose(len(options) > 0, "或输入其他回答…", "输入你的回答…"), Disabled: disabled, Masked: flag(q, "isSecret"), Multiline: !flag(q, "isSecret"),
			Value: func() string {
				v := value.Get()
				if v.Custom {
					return v.Value
				}
				return ""
			}, OnChange: func(v string) { set(questionDraft{Value: v, Custom: true}) }, Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), MinHeight: core.Pt(22), MaxHeight: core.Pt(100), FontSize: 14, LineHeight: 22, Padding: core.All(0)}, StyleFunc: func() core.Style { return textInputStyle(cx) }}
		response := []core.Node{questionLeadingIcon(cx, "question-input-icon-"+id, lucide.Pencil), input}
		if flag(a, "allowSkip") {
			skip := button(cx, "question-skip-"+id, "跳过", nil, "ghost", func() {
				set(questionDraft{})
				advance()
			}).(core.View)
			skip.Disabled = disabled
			response = append(response, questionResponseButton(cx, skip))
		}
		customReady := func() bool { return value.Get().Custom && strings.TrimSpace(value.Get().Value) != "" }
		next := busyButton(cx, "question-submit-"+id, choose(i+1 < len(questions), "下一题", "发送"), nil, "primary", func() bool { return busy() && customReady() }, func() {
			if customReady() {
				advance()
			}
		}).(core.View)
		next.Disabled = func() bool { return disabled() || !customReady() }
		response = append(response, questionResponseButton(cx, next))
		children = append(children, core.View{TestID: "question-response-" + id,
			Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, Padding: core.Symmetric(5, 7), Margin: core.Edges{Top: 4}, MinWidth: core.Pt(0), BorderWidth: 1, CornerRadius: 12},
			StyleFunc: func() core.Style {
				c := palette(cx)
				return core.Style{BorderColor: c.mutedText.Faded(.22), FocusWithin: &core.Style{BorderColor: c.focus}}
			}, Children: response})
		return core.View{Style: core.Style{Gap: 4, MinWidth: core.Pt(0)}, Children: children}
	})
	return s.messageInset(cx, core.View{TestID: "question-card-" + id, Label: "Agent 提问", Style: core.Style{Padding: core.All(8), CornerRadius: 16, BorderWidth: 1, MinWidth: core.Pt(0)},
		StyleFunc: func() core.Style {
			return core.Style{Background: palette(cx).muted, BorderColor: palette(cx).mutedText.Faded(.22)}
		}, Children: []core.Node{content}})
}

// Keep action labels centered with the editor while preserving touch targets.
func questionResponseButton(cx *core.Scope, v core.View) core.View {
	v.Style.Padding, v.Style.CornerRadius = core.Symmetric(5, 10), 16
	style := v.StyleFunc
	v.StyleFunc = func() core.Style {
		st := style()
		st.MinHeight = core.Pt(choose(cx.Env().Desktop(), core.Points(30), core.Points(44)))
		return st
	}
	return v
}

func questionHeaderButton(cx *core.Scope, id, title string, icon func() *core.SVG, action func()) core.View {
	v := contextualIconButton(cx, id, title, icon, action)
	style := v.StyleFunc
	v.StyleFunc = func() core.Style {
		st := style()
		st.Disabled = &core.Style{Opacity: core.Alpha(.4)}
		if !cx.Env().Desktop() {
			st.Width, st.Height, st.MinHeight, st.Padding = core.Pt(44), core.Pt(44), core.Pt(44), core.All(14)
		}
		return st
	}
	return v
}

// Share the option badge column; response padding accounts for its one-point border.
func questionLeadingIcon(cx *core.Scope, id string, icon func() *core.SVG) core.Node {
	return core.View{TestID: id, Style: core.Style{Width: core.Pt(20), Height: core.Pt(20), FlexShrink: 0, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter}, Children: []core.Node{
		core.Icon{SVG: icon(), Style: core.Style{Width: core.Pt(16), Height: core.Pt(16), FlexShrink: 0}, StyleFunc: func() core.Style { return core.Style{Color: palette(cx).mutedText} }},
	}}
}
