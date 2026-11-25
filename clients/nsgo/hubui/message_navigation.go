package hubui

import (
	"fmt"
	"github.com/monkeyWie/reignhub/pkg/persistence"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
)

type messageNavigationTarget struct {
	id     string
	row    int
	stored persistence.Landmark
}

func messageNavigationTargets(rows []Message, height core.Points) []messageNavigationTarget {
	indices := []int{}
	for i := range rows {
		if rows[i].Role == "user" {
			indices = append(indices, i)
		}
	}
	n := min(len(indices), 24, max(1, int((height-48)/12)))
	out := make([]messageNavigationTarget, n)
	for i := range n {
		index := 0
		if n > 1 {
			index = i * (len(indices) - 1) / (n - 1)
		}
		r := indices[index]
		out[i] = messageNavigationTarget{id: rows[r].ID, row: r}
	}
	return out
}

type messageNavigationPreview struct{ instruction, response string }

func navigationPreview(rows []Message, target messageNavigationTarget) messageNavigationPreview {
	if target.row < 0 || target.row >= len(rows) || rows[target.row].ID != target.id {
		return messageNavigationPreview{}
	}
	user := rows[target.row]
	preview := messageNavigationPreview{instruction: navigationSnippet(messagePreviewText(user), 180)}
	if preview.instruction == "" {
		preview.instruction = choose(len(user.Attachments) > 0, "附件消息", "用户消息")
	}
	for i := target.row + 1; i < len(rows); i++ {
		m := rows[i]
		if m.Role == "user" || user.Turn != "" && m.Turn != "" && m.Turn != user.Turn {
			break
		}
		if m.Role == "assistant" && strings.TrimSpace(m.Text) != "" {
			preview.response = navigationSnippet(messagePreviewText(m), 480)
			break
		}
	}
	return preview
}

// Bound preview layout work even for very large historical responses.
func navigationSnippet(text string, limit int) string {
	var out strings.Builder
	space, count := false, 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			space = out.Len() > 0
			continue
		}
		if count >= limit {
			out.WriteRune('…')
			break
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
		count++
	}
	return out.String()
}

func (s *State) messageNavigation(cx *core.Scope, rows *core.Memo[[]Message], viewport *core.Signal[core.Size], navigate func(int)) core.Node {
	targets := core.NewMemoFunc(cx, func() []messageNavigationTarget {
		if s.snapshots != nil && s.snapshots.storage != nil {
			s.read()
		}
		view := rows.Get()
		height := viewport.Get().Height
		if !flag(s.transcript, "_localOlder") || len(s.landmarks) == 0 {
			return messageNavigationTargets(view, height)
		}
		indices := map[string]int{}
		for i, v := range view {
			indices[v.ID] = i
		}
		out := []messageNavigationTarget{}
		for _, i := range persistence.Samples(len(s.landmarks), min(24, max(1, int((height-48)/12)))) {
			a := s.landmarks[i]
			row, ok := indices[a.ID]
			if !ok {
				row = -1
			}
			out = append(out, messageNavigationTarget{id: a.ID, row: row, stored: a})
		}
		return out
	}, slices.Equal[[]messageNavigationTarget])
	hover := core.NewSignal(cx, float32(-100))
	previewTarget := core.NewSignal(cx, "")
	return core.View{TestID: "message-navigation", PointerEvents: core.PointerEventsBoxNone, Style: core.Style{Position: core.Absolute, Top: core.Pt(0), Bottom: core.Pt(0), Left: core.Pt(4), Width: core.Pt(44), JustifyContent: core.JustifyCenter}, Children: []core.Node{
		core.View{Style: core.Style{MaxHeight: core.Pt(288)}, OnMouseLeave: func(core.MouseEvent) { hover.Set(-100) }, Children: []core.Node{
			core.Each(targets.Get, func(t messageNavigationTarget) string { return t.id }, func(cx *core.Scope, target func() messageNavigationTarget, index func() int) core.Node {
				id := fmt.Sprintf("message-tick-%s", target().id)
				move := func(e core.MouseEvent) { hover.Set(float32(index()) + float32(e.Position.Y/12) - .5) }
				return core.View{TestID: id, Role: core.RoleButton, Label: "跳转到消息", Tooltip: base.Tooltip{
					Open: func() bool { return previewTarget.Get() == target().id },
					OnOpenChange: func(open bool, _ base.Reason) {
						if open {
							previewTarget.Set(target().id)
						} else if previewTarget.GetUntracked() == target().id {
							previewTarget.Set("")
						}
					},
					Delay: 60 * time.Millisecond, Placement: core.PlaceRight, Offset: 12,
					PopupStyle: core.Style{Padding: core.All(16), Gap: 8, CornerRadius: 14, BorderWidth: 1},
					PopupStyleFunc: func() core.Style {
						c := palette(cx)
						return core.Style{Width: core.Pt(min(340, max(120, viewport.Get().Width-80))), Background: c.muted, Color: c.foreground, BorderColor: c.border, Shadows: []core.BoxShadow{{Offset: core.Point{Y: 6}, Blur: 18, Color: core.RGB(0).Faded(.15)}}}
					},
					Children: []core.Node{core.Dynamic(func(cx *core.Scope) core.Node {
						preview := core.NewMemo(cx, func() messageNavigationPreview {
							t := target()
							if t.row < 0 {
								return messageNavigationPreview{instruction: navigationSnippet(t.stored.Preview, 180)}
							}
							return navigationPreview(rows.Get(), t)
						})
						return core.View{TestID: "message-navigation-preview", Style: core.Style{Gap: 8}, Children: []core.Node{
							core.Text{TestID: "navigation-instruction", ContentFunc: func() string { return preview.Get().instruction }, MaxLines: 1, Style: core.Style{FontSize: 13, FontWeight: core.FontWeightMedium}},
							core.Show{When: func() bool { return preview.Get().response != "" }, Then: func(cx *core.Scope) core.Node {
								return core.Text{TestID: "navigation-response", ContentFunc: func() string { return preview.Get().response }, MaxLines: 3, Style: core.Style{FontSize: 12, LineHeight: 19}, StyleFunc: func() core.Style { return core.Style{Color: palette(cx).mutedText} }}
							}},
						}}
					})},
				}, OnMouseEnter: move, OnMouseMove: move, OnPress: func(core.PressEvent) {
					t := target()
					if t.row >= 0 {
						navigate(t.row)
					} else {
						s.openLandmark(t.stored, navigate)
					}
				},
					Style: core.Style{Height: core.Pt(12), Width: core.Pt(44), JustifyContent: core.JustifyCenter, Padding: core.Edges{Left: 8}},
					StyleFunc: func() core.Style {
						return core.Style{FocusVisible: pressStyle(cx).FocusVisible, Active: &core.Style{Opacity: core.Alpha(.6)}}
					},
					Children: []core.Node{core.View{TestID: id + "-bar", Style: core.Style{Width: core.Pt(9), Height: core.Pt(2), CornerRadius: 1, TransformOrigin: core.ObjectPosition{X: core.PositionAxis{Offset: core.Pct(0)}, Y: core.PositionAxis{Offset: core.Pct(50)}}}, StyleFunc: func() core.Style {
						distance := float64(float32(index()) - hover.Get())
						wave := float32(math.Exp(-distance * distance / 3))
						st := core.Style{Background: palette(cx).foreground, Opacity: core.Alpha(.3 + .7*wave), Transform: core.Transform{ScaleX: 1 + 2*wave}}
						if !cx.Env().ReduceMotion() {
							st.Transition = core.Transition{Duration: 140 * time.Millisecond, Easing: core.EaseOut, Properties: core.TransitionTransform | core.TransitionOpacity}
						}
						return st
					}}},
				}
			}),
		}},
	}}
}
