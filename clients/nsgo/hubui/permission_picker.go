package hubui

import (
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
	"github.com/nsgo-dev/nsgo/ui/chat"
)

func permissionPicker(cx *core.Scope, id, title, selected string, options []chat.Option, fullIDs map[string]bool, fullSelected bool, change func(string)) core.Node {
	open := core.NewSignal(cx, false)
	warning := func() core.Color { return cx.Palette().Warning }
	textColor := func() core.Color { return palette(cx).foreground }
	muted := func() core.Color { return palette(cx).mutedText }
	triggerInk := muted
	if fullSelected {
		triggerInk = warning
	}
	rows := []core.Node{label(cx, options[0].Group, 12, true)}
	for _, opt := range options {
		ink, secondary, check := textColor, muted, textColor
		if fullIDs[opt.ID] {
			ink, secondary, check = warning, warning, warning
		}
		icon := lucide.Shield
		switch {
		case fullIDs[opt.ID]:
			icon = lucide.ShieldAlert
		case opt.ID == "read-only":
			icon = lucide.Eye
		case opt.ID == "ask":
			icon = lucide.Hand
		case opt.ID == "auto":
			icon = lucide.ShieldCheck
		}
		lines := []core.Node{ui.Text(opt.Label, 13, ui.Medium, ink)}
		if opt.Description != "" {
			lines = append(lines, ui.Text(opt.Description, 12, ui.Regular, secondary))
		}
		children := []core.Node{
			ui.Icon(icon, 16, secondary),
			core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0)}, Children: lines},
		}
		if opt.ID == selected {
			children = append(children, ui.Icon(lucide.Check, 14, check))
		}
		rows = append(rows, base.Button{Label: opt.Label, Selected: func() bool { return opt.ID == selected },
			Style:     core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, Padding: core.Symmetric(6, 8), CornerRadius: 6},
			StyleFunc: func() core.Style { return pressStyle(cx) },
			OnPress:   func(core.PressEvent) { open.Set(false); change(opt.ID) }, Children: children})
	}
	return base.Popover{TestID: "composer-setting-" + id, Label: title, Open: open.Get, OnOpenChange: func(v bool, _ base.Reason) { open.Set(v) },
		Placement: core.PlaceTop, Align: core.AlignStart, Offset: 4, Margin: 8,
		Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Padding: core.Symmetric(4, 8), Gap: 4, CornerRadius: 8},
		StyleFunc: func() core.Style {
			st := pressStyle(cx)
			if open.Get() {
				st.Background = palette(cx).muted
			}
			return st
		},
		Trigger: core.Group{ui.Text(title, 13, ui.Regular, triggerInk), ui.Icon(lucide.ChevronDown, 12, triggerInk)},
		PopupStyleFunc: func() core.Style {
			return ui.FinOf(cx).Menu().Over(core.Style{Width: core.Pt(min(320, cx.Env().Window().Width-16)), CornerRadius: ui.RadiusCard, BorderWidth: 1})
		},
		Children: []core.Node{core.Scroll{Style: core.Style{MaxHeight: core.Pt(348), FlexShrink: 1}, Children: []core.Node{core.View{Style: core.Style{Padding: core.All(6), Gap: 2}, Children: rows}}}}}
}
