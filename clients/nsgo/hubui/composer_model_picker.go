package hubui

import (
	"slices"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

type modelOption struct {
	id, name string
	vision   bool
}

func effortControl(control Object) bool {
	return str(control, "id") == "effort" || str(control, "kind") == "effort"
}

func (s *State) modelEffortControl() Object {
	if s.composerControlsModel == s.model && s.agentCapability("composerSettings") {
		for _, control := range s.composerControls {
			if effortControl(control) {
				return control
			}
		}
	}
	return nil
}

func (s *State) modelOptions() []modelOption {
	var options []modelOption
	for _, m := range s.models {
		options = append(options, modelOption{str(m, "id"), str(m, "name"), flag(m, "imageInput")})
	}
	// Session-specific native choices can change independently of discovery.
	if c := s.composerControl("model"); c != nil && s.composerControlsModel == s.model {
		discovered := options
		options = nil
		for _, v := range objects(c["choices"]) {
			id := str(v, "id")
			index := slices.IndexFunc(discovered, func(m modelOption) bool { return m.id == id })
			options = append(options, modelOption{id, str(v, "label"), index >= 0 && discovered[index].vision})
		}
	}
	if s.model != "" && !slices.ContainsFunc(options, func(m modelOption) bool { return m.id == s.model }) {
		options = append([]modelOption{{id: s.model, name: s.model}}, options...)
	}
	return options
}

func (s *State) composerModelPicker(cx *core.Scope, running func() bool) core.Node {
	options := core.NewMemoFunc(cx, func() []modelOption { s.read(); return s.modelOptions() }, slices.Equal)
	type caption struct{ model, effort string }
	current := core.NewMemo(cx, func() caption {
		s.read()
		l := locales.For(cx)
		name := s.model
		for _, m := range options.Get() {
			if m.id == s.model && m.name != "" {
				name = m.name
				break
			}
		}
		if name == "" {
			name = l.PickerChooseModel()
		}
		control := s.modelEffortControl()
		selected := str(control, "selected")
		for _, choice := range objects(control["choices"]) {
			if selected != "" && str(choice, "id") == selected {
				return caption{name, effortLabel(l, selected, str(choice, "label"))}
			}
		}
		return caption{model: name}
	})
	modelText := func(cx *core.Scope, parts func() caption, size core.Points) core.Text {
		t := singleLine(cx, nil, size, false)
		t.EllipsizeMode = core.EllipsizeMiddle
		t.SpansFunc = func() []core.Span {
			c := parts()
			spans := []core.Span{{Content: c.model}}
			if c.effort != "" {
				spans = append(spans, core.Span{Content: " " + c.effort, Style: core.Style{Color: palette(cx).mutedText}})
			}
			return spans
		}
		return t
	}
	return core.Show{When: running, Then: func(cx *core.Scope) core.Node { return modelText(cx, current.Get, 12) }, Else: func(cx *core.Scope) core.Node {
		l := locales.For(cx)
		open := core.NewSignal(cx, false)
		query := core.NewSignal(cx, "")
		closePicker := func() { open.Set(false); query.Set("") }
		saving := func() bool { s.read(); return s.composerWrites[s.key()] }
		trigger := func() caption {
			if open.Get() {
				return caption{model: l.PickerChooseModel()}
			}
			return current.Get()
		}
		return base.Popover{TestID: "model-selector", LabelFunc: func() string { c := trigger(); return strings.TrimSpace(c.model + " " + c.effort) }, Open: open.Get, OnOpenChange: func(v bool, _ base.Reason) { open.Set(v); query.Set("") },
			Placement: core.PlaceTop, Align: core.AlignEnd, Offset: 5, Margin: 8,
			Style: core.Style{MaxWidth: core.Pt(220), MinWidth: core.Pt(0), FlexShrink: 1, MinHeight: core.Pt(30), Padding: core.Symmetric(2, 10), CornerRadius: 8, JustifyContent: core.JustifyCenter},
			StyleFunc: func() core.Style {
				st := pressStyle(cx)
				if open.Get() {
					st.Background = palette(cx).muted
				}
				return st
			},
			Trigger:    row(modelText(cx, trigger, 13), glyph(cx, lucide.ChevronDown, 12)),
			PopupStyle: core.Style{BorderWidth: 1, CornerRadius: 10},
			PopupStyleFunc: func() core.Style {
				c := palette(cx)
				return core.Style{Width: core.Pt(min(310, cx.Env().Window().Width-16)), Background: c.elevated, BorderColor: c.border, Shadows: []core.BoxShadow{ui.Shadow(0, 5, 16, 0, ui.Black(.16))}}
			},
			Children: []core.Node{
				core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, Padding: core.Symmetric(6, 13)}, Children: []core.Node{
					label(cx, l.PickerModel(), 12, true), glyph(cx, lucide.Search, 14), editorInput{TestID: "model-search", OnKeyPress: func(key string) {
						if strings.EqualFold(key, "escape") {
							closePicker()
						}
					}, Label: l.PickerSearchModels(), Placeholder: l.PickerSearchModels(), Value: query.Get, OnChange: query.Set, AutoFocus: !isMobile(cx), Style: core.Style{MinWidth: core.Pt(0), FlexGrow: 1, Height: core.Pt(30), FontSize: 13}, StyleFunc: func() core.Style { return textInputStyle(cx) }},
				}},
				core.Scroll{StyleFunc: func() core.Style {
					room := cx.Env().Window().Height - cx.Env().KeyboardHeight()
					return core.Style{MaxHeight: core.Pt(min(238, max(48, room-210))), FlexShrink: 1}
				}, Children: []core.Node{core.Dynamic(func(cx *core.Scope) core.Node {
					needle := strings.ToLower(strings.TrimSpace(query.Get()))
					var rows []core.Node
					for _, m := range options.Get() {
						if !strings.Contains(strings.ToLower(m.name+" "+m.id), needle) {
							continue
						}
						name := m.name
						if name == "" {
							name = m.id
						}
						selected := func() bool { s.read(); return s.model == m.id }
						content := []core.Node{singleLine(cx, func() string { return name }, 14, false)}
						if m.vision {
							content = append(content, modelBadge(cx, l.PickerVision()))
						}
						content = append(content, spacer(), core.Show{When: selected, Then: func(cx *core.Scope) core.Node { return glyph(cx, lucide.Check, 15) }})
						rows = append(rows, core.View{Role: core.RoleButton, Label: name, Selected: selected, Disabled: saving,
							Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 6, MinHeight: choose(isMobile(cx), core.Pt(44), core.Pt(34)), Padding: core.Symmetric(2, 9), CornerRadius: 6}, StyleFunc: func() core.Style { return pressStyle(cx) },
							OnPress: func(core.PressEvent) {
								if !s.agentCapability("composerSettings") {
									open.Set(false)
									query.Set("")
								}
								if saving() || s.model == m.id {
									return
								}
								if s.composerControl("model") != nil {
									s.applyComposerChoice("model", m.id)
								} else {
									s.model = m.id
									s.persistDraft()
									s.changed()
								}
							}, Children: content})
					}
					if len(rows) == 0 {
						rows = append(rows, label(cx, choose(needle == "", l.PickerNoModels(), l.PickerNoMatches()), 13, true))
					}
					return core.View{Style: core.Style{Padding: core.Edges{Left: 5, Right: 5, Bottom: 5}}, Children: rows}
				})}},
				core.Dynamic(func(cx *core.Scope) core.Node {
					cx.OnKeyWith("escape", core.KeyOptions{When: core.KeyAlways}, closePicker)
					return s.modelEffortPanel(cx)
				}),
			}}
	}}
}

func modelBadge(cx *core.Scope, title string) core.Node {
	return core.View{Style: core.Style{CornerRadius: 10, BorderWidth: 1, Padding: core.Symmetric(0, 5), FlexShrink: 0}, StyleFunc: func() core.Style { c := palette(cx); return core.Style{BorderColor: c.border, Background: c.muted} }, Children: []core.Node{label(cx, title, 10, true)}}
}

type effortLevel struct{ id, name string }

func effortLabel(l locales.Messages, id, name string) string {
	if name != "" && !strings.EqualFold(name, id) {
		return name
	}
	switch id {
	case "none", "off":
		return l.EffortOff()
	case "minimal":
		return l.EffortMinimal()
	case "low":
		return l.EffortLow()
	case "medium":
		return l.EffortMedium()
	case "high":
		return l.EffortHigh()
	case "xhigh":
		return l.EffortExtraHigh()
	case "max":
		return l.EffortMax()
	}
	if name != "" {
		return name
	}
	return id
}

func (s *State) modelEffortPanel(cx *core.Scope) core.Node {
	type settings struct {
		model, control string
		levels         []effortLevel
	}
	l := locales.For(cx)
	available := core.NewMemoFunc(cx, func() settings {
		s.read()
		c := s.modelEffortControl()
		if c == nil || !s.canWriteSession() {
			return settings{}
		}
		v := settings{model: s.model, control: str(c, "id")}
		for _, opt := range objects(c["choices"]) {
			id := str(opt, "id")
			v.levels = append(v.levels, effortLevel{id, effortLabel(l, id, str(opt, "label"))})
		}
		return v
	}, func(a, b settings) bool {
		return a.model == b.model && a.control == b.control && slices.Equal(a.levels, b.levels)
	})
	return core.Dynamic(func(cx *core.Scope) core.Node {
		set := available.Get()
		if len(set.levels) == 0 {
			return nil
		}
		preview := core.NewSignal(cx, -1)
		busy := core.NewMemo(cx, func() bool { s.read(); return s.composerWrites[s.key()] })
		cx.Effect(func() {
			if !busy.Get() {
				preview.Set(-1)
			}
		})
		selected := func() int {
			if i := preview.Get(); i >= 0 {
				return i
			}
			s.read()
			id := str(s.composerControl(set.control), "selected")
			return slices.IndexFunc(set.levels, func(level effortLevel) bool { return level.id == id })
		}
		name := func() string {
			if i := selected(); i >= 0 {
				return set.levels[i].name
			}
			return l.EffortUnspecified()
		}
		commit := func(i int) {
			if !busy.GetUntracked() && set.model == s.model && s.pendingSend() == "" {
				s.applyComposerChoice(set.control, set.levels[i].id)
			}
			if !s.composerWrites[s.key()] {
				preview.Set(-1)
			}
		}
		grade := singleLine(cx, name, 15, false)
		grade.Style.FontWeight = ui.Semibold
		grade.StyleFunc = func() core.Style { return core.Style{Color: palette(cx).brand} }
		head := core.View{TestID: "effort-heading", Style: core.Style{
			Direction: core.Row, AlignItems: core.AlignCenter, Gap: 6, MinHeight: core.Pt(24),
		}, Children: []core.Node{label(cx, l.ThinkingEffort(), 12, true), grade,
			core.Show{When: busy.Get, Then: func(cx *core.Scope) core.Node {
				return actionSpinner(cx, "model-effort-saving", l.ThinkingEffort(), false, 14)
			}}}}
		if len(set.levels) > 1 && cx.Env().Desktop() {
			cx.OnKeyWith("ctrl+t", core.KeyOptions{When: core.KeyAlways}, func() { commit((selected() + 1) % len(set.levels)) })
			head.Children = append(head.Children, spacer(), modelBadge(cx, "⌃ T"))
		}
		panel := core.View{Style: core.Style{Padding: core.Symmetric(8, 13), Gap: 3}, Children: []core.Node{head}}
		if len(set.levels) > 1 {
			// The indicator measures travel between stop centers; its caps extend under the thumb.
			const capRadius = 12
			marks := []core.Node{core.View{
				PointerEvents: core.PointerEventsNone,
				Style:         core.Style{Position: core.Absolute, Top: core.Pt(0), Bottom: core.Pt(0), Left: core.Pt(-capRadius), Right: core.Pt(-capRadius), CornerRadius: capRadius, BorderWidth: 1},
				StyleFunc: func() core.Style {
					ink := palette(cx).foreground
					return core.Style{Background: ink.Faded(.10), BorderColor: ink.Faded(.08)}
				},
			}}
			for i, level := range set.levels {
				fraction := float32(i) / float32(len(set.levels)-1)
				marks = append(marks, effortSliderStop(cx, level.id, fraction, func() bool { return i <= selected() }))
			}
			panel.Children = append(panel.Children, base.Slider{
				TestID: "composer-setting-effort", ThumbTestID: "effort-thumb", Label: l.ThinkingEffort(), Marks: marks,
				ValueText: func(v float64) string { return set.levels[int(v)].name },
				Value:     func() base.SliderValue { return base.SingleValue(float64(max(0, selected()))) },
				Max:       func() float64 { return float64(len(set.levels) - 1) }, Step: func() float64 { return 1 },
				OnChange:       func(v base.SliderValue) { preview.Set(int(v.End)) },
				OnChangeEnd:    func(v base.SliderValue) { commit(int(v.End)) },
				Disabled:       func() bool { return busy.Get() || s.pendingSend() != "" || !s.draftReady() },
				Style:          core.Style{AlignSelf: core.AlignStretch, MinWidth: core.Pt(0)},
				TrackStyle:     core.Style{Height: core.Pt(44), JustifyContent: core.JustifyCenter},
				IndicatorStyle: core.Style{Height: core.Pt(2 * capRadius), Margin: core.Symmetric(0, 14)},
				RangeStyle:     core.Style{Margin: core.Symmetric(0, -capRadius), CornerRadius: capRadius},
				RangeStyleFunc: func() core.Style { return core.Style{Background: palette(cx).brand.Faded(.86)} },
				ThumbStyle:     core.Style{Width: core.Pt(28), Height: core.Pt(28), CornerRadius: 14, ZIndex: 2, Background: ui.White(1), Shadows: []core.BoxShadow{ui.Shadow(0, 1, 3, 0, ui.Black(.15))}},
				ThumbStyleFunc: func() core.Style { return core.Style{FocusVisible: ui.FocusRing(cx, 2)} },
			})
		}
		return core.Group{core.View{Style: core.Style{Height: core.Pt(1), FlexShrink: 0}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).border} }}, panel}
	})
}

// Keep the hover target fixed while the dot scales. The parent slider still owns presses and drags.
func effortSliderStop(cx *core.Scope, id string, fraction float32, filled func() bool) core.Node {
	scale := core.NewAnimatedValue(cx, 1)
	hovered := core.NewSignal(cx, false)
	wasHovered := false
	cx.Effect(func() {
		over := hovered.Get()
		if !over && !wasHovered {
			return
		}
		wasHovered = over
		target := choose(over, 2.0, 1.0)
		if cx.Env().ReduceMotion() {
			scale.Set(target)
			return
		}
		move := core.Timing(scale, target, core.TimingConfig{Duration: 150 * time.Millisecond, Easing: core.BezierEasing(core.EaseOut)})
		if over {
			breathe := core.ValueKeyframes(scale, core.KeyframeTiming{Duration: 1250 * time.Millisecond, Easing: core.BezierEasing(core.EaseInOut), Iterations: core.Forever}, core.Stops(2, 1.6, 2)...)
			move = core.Sequence(move, breathe)
		}
		cx.StartAnimation(core.AtFrameRate(move, 30), nil)
	})
	return core.View{
		OnMouseEnter: func(core.MouseEvent) { hovered.Set(true) }, OnMouseLeave: func(core.MouseEvent) { hovered.Set(false) },
		Style: core.Style{Position: core.Absolute, Top: core.Pct(50), Width: core.Pt(24), Height: core.Pt(24), ZIndex: 1,
			Transform: core.Transform{TranslateXPercent: -50, TranslateYPercent: -50}, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
		StyleFunc: func() core.Style {
			x := fraction
			if cx.Env().RTL() {
				x = 1 - x
			}
			return core.Style{Left: core.Pct(100 * x)}
		},
		Children: []core.Node{core.View{TestID: "effort-stop-" + id, PointerEvents: core.PointerEventsNone,
			Style: core.Style{Width: core.Pt(4), Height: core.Pt(4), CornerRadius: 2, Animated: &core.AnimatedStyle{Scale: scale}},
			StyleFunc: func() core.Style {
				color := palette(cx).foreground.Faded(.30)
				if filled() {
					color = ui.White(.56)
				}
				return core.Style{Background: color}
			},
		}},
	}
}
