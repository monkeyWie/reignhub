package hubui

import (
	"context"
	"errors"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// Action state belongs to the operation, not the page or a global UI lock.
// Writes continue if their initiating control unmounts; they are never replayed.
func (s *State) actionPending(key string) bool {
	s.read()
	return s.actions[key]
}

func (s *State) runAction(key string, work func(context.Context) (any, error), done func(any), current ...func() bool) {
	s.runScopedAction(s.ctx, key, true, work, done, current...)
}

func (s *State) runScopedAction(owner context.Context, key string, showError bool, work func(context.Context) (any, error), done func(any), current ...func() bool) {
	s.runActionResult(owner, key, showError, work, func(v any, err error) {
		if owner.Err() != nil || len(current) > 0 && !current[0]() {
			return
		}
		if err != nil && showError {
			s.error, s.failure = err.Error(), nil
			_ = errors.As(err, &s.failure)
		} else if err == nil && done != nil {
			done(v)
		}
	})
}

func (s *State) runActionResult(owner context.Context, key string, clearError bool, work func(context.Context) (any, error), finish func(any, error)) {
	if owner.Err() != nil || s.actionPending(key) {
		return
	}
	if s.actions == nil {
		s.actions = map[string]bool{}
	}
	s.actions[key] = true
	if clearError {
		s.error, s.failure = "", nil
	}
	s.changed()
	go func() {
		ctx, cancel := context.WithTimeout(owner, 30*time.Second)
		defer cancel()
		v, err := work(ctx)
		s.cx.Post(func() {
			delete(s.actions, key)
			if s.ctx.Err() != nil {
				return
			}
			finish(v, err)
			s.changed()
		})
	}()
}

func (s *State) actionButton(cx *core.Scope, id, title string, icon func() *core.SVG, variant string, onPress func()) core.Node {
	return busyButton(cx, id, title, icon, variant, func() bool { return s.actionPending(id) }, onPress)
}

func busyButton(cx *core.Scope, id, title string, icon func() *core.SVG, variant string, pending func() bool, onPress func()) core.Node {
	busy := core.NewMemo(cx, pending)
	v := button(cx, id, title, icon, variant, onPress).(core.View)
	v.Busy, v.Disabled = busy.Get, busy.Get
	// Keep the real content's bounds while the spinner replaces it. An empty
	// leading icon slot would offset text-only labels from the button's center.
	v.Children = []core.Node{core.View{
		TestID:    id + "-content",
		Style:     core.Style{Direction: core.Row, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter, Gap: 8, MinWidth: core.Pt(16), MinHeight: core.Pt(18), FlexShrink: 1},
		StyleFunc: func() core.Style { return core.Style{Opacity: core.Alpha(choose(busy.Get(), float32(0), float32(1)))} },
		Children:  v.Children,
	}, core.View{
		PointerEvents: core.PointerEventsNone,
		Style:         core.Style{Position: core.Absolute, Left: core.Pt(0), Right: core.Pt(0), Top: core.Pt(0), Bottom: core.Pt(0), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
		Children: []core.Node{core.Show{When: busy.Get, Then: func(cx *core.Scope) core.Node {
			return actionSpinner(cx, id+"-loading", title, variant == "primary", 16)
		}}},
	}}
	return v
}

func actionSpinner(cx *core.Scope, id, title string, inverse bool, size core.Points) core.Node {
	if inverse {
		theme := *cx.Theme()
		theme.Light.TextMuted, theme.Dark.TextMuted = theme.Light.AccentText, theme.Dark.AccentText
		cx.SetTheme(&theme)
	}
	return ui.Spinner{TestID: id, Label: title, Variant: ui.SpinnerSpokes, Size: size}
}

func pressStyle(cx *core.Scope) core.Style {
	c := palette(cx)
	return core.Style{Hover: &core.Style{Background: c.muted}, Active: &core.Style{Background: c.selected, Opacity: core.Alpha(.8)}, FocusVisible: &core.Style{OutlineWidth: 2, OutlineColor: c.focus}}
}

func filledButtonStyle(cx *core.Scope) core.Style {
	c := palette(cx)
	return core.Style{
		Background: c.foreground, Color: c.background,
		Hover:        &core.Style{Background: ui.Mix(c.foreground, c.background, .08)},
		Active:       &core.Style{Background: ui.Mix(c.foreground, c.background, .16)},
		FocusVisible: &core.Style{OutlineWidth: 2, OutlineColor: c.focus},
	}
}

func (s *State) approvalKey(a Object) string { return "approval:" + s.key() + ":" + str(a, "id") }

func (s *State) approvalAction(a, decision Object) {
	if !s.canWriteSession() {
		return
	}
	key, target, p, id := s.key(), s.target, str(s.selected, "providerId"), str(s.selected, "id")
	s.runAction(s.approvalKey(a), func(ctx context.Context) (any, error) {
		return rpc(ctx, s.api, target, p, id, "decide", Object{"approval": a["id"], "decision": decision})
	}, func(any) {
		kept := []Object{}
		for _, existing := range s.approvals {
			if str(existing, "id") != str(a, "id") {
				kept = append(kept, existing)
			}
		}
		s.approvals = kept
	}, func() bool { return s.key() == key })
}

func labelledIconButton(cx *core.Scope, id, title string, icon func() *core.SVG, onPress func()) core.Node {
	v := button(cx, id, "", icon, "ghost", onPress).(core.View)
	v.Label, v.Tooltip = title, tooltipCard(cx, label(cx, title, 12, false))
	return v
}

// Secondary controls stay in the layout, avoiding title jumps. Keyboard focus
// reveals the same controls; touch uses the row's shared context menu instead.
func contextualAffordance(cx *core.Scope, group string, touchVisible bool) core.Style {
	if !cx.Env().Desktop() {
		if touchVisible {
			return core.Style{}
		}
		return core.Style{Display: core.DisplayNone}
	}
	// Auto is the zero/unset style value; On explicitly overrides hidden None.
	shown := &core.Style{Opacity: core.Alpha(1), PointerEvents: core.PointerEventsOn}
	return core.Style{Opacity: core.Alpha(0), PointerEvents: core.PointerEventsNone, GroupStyle: &core.GroupStyle{Name: group, Hover: shown, FocusWithin: shown}}
}
func contextualIconButton(cx *core.Scope, id, title string, icon func() *core.SVG, action func()) core.View {
	return contextualIconButtonWithLabel(cx, id, label(cx, title, 12, false), icon, action)
}

func contextualIconButtonWithLabel(cx *core.Scope, id string, title core.Text, icon func() *core.SVG, action func()) core.View {
	v := button(cx, id, "", icon, "ghost", action).(core.View)
	v.Label, v.LabelFunc = title.Content, title.ContentFunc
	tip := tooltipCard(cx, title)
	tip.Placement, tip.Offset = core.PlaceTop, 6
	tip.PopupStyle.Padding, tip.PopupStyle.CornerRadius = core.Symmetric(5, 8), 6
	v.Tooltip = tip
	v.Style.Width, v.Style.Height, v.Style.MinHeight, v.Style.Padding = core.Pt(30), core.Pt(30), core.Pt(30), core.All(7)
	v.StyleFunc = func() core.Style { return contextualIconStyle(cx) }
	return v
}

func contextualIconStyle(cx *core.Scope) core.Style {
	c := palette(cx)
	return core.Style{Color: c.mutedText, Hover: &core.Style{Color: c.foreground}, Active: &core.Style{Color: c.foreground, Opacity: core.Alpha(.65)}, FocusVisible: &core.Style{Color: c.foreground, OutlineWidth: 2, OutlineColor: c.focus}}
}
