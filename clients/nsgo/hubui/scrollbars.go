package hubui

import (
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
)

var messageOverflow = core.NewKey("message horizontal overflow", false)

type workspaceScrollbars struct{ base.ScrollbarTheme }

func (w *workspaceScrollbars) Scrollbar(cx *core.Scope, ref *core.Ref, horizontal bool) core.Node {
	theme := &w.ScrollbarTheme
	if horizontal && messageOverflow.Use(cx) {
		// The reader must be able to discover clipped columns before the first swipe.
		// NSGo hides even an always-on thumb when the content fits the viewport.
		message := *theme
		message.Mode = base.ScrollbarAlways
		return message.Scrollbar(cx, ref, true)
	}
	return theme.Scrollbar(cx, ref, horizontal)
}

func installScrollbars(cx *core.Scope) {
	thumb := func() core.Style {
		return core.Style{Background: palette(cx).mutedText.Faded(.45)}
	}
	cx.SetScrollbars(&workspaceScrollbars{base.ScrollbarTheme{
		Mode:           base.ScrollbarScrolling,
		Motion:         base.ScrollbarMotion{Idle: 700, Enter: 100, Exit: 180},
		MinThumbLength: 32,
		Vertical: &base.ScrollbarLook{
			Label:      "垂直滚动条",
			Style:      core.Style{Position: core.Absolute, Top: core.Pt(0), Bottom: core.Pt(0), Right: core.Pt(0), Width: core.Pt(8), Padding: core.All(2)},
			ThumbStyle: core.Style{CornerRadius: 2}, ThumbStyleFunc: thumb,
		},
		Horizontal: &base.ScrollbarLook{
			Label:      "水平滚动条",
			Style:      core.Style{Position: core.Absolute, Left: core.Pt(0), Right: core.Pt(0), Bottom: core.Pt(0), Height: core.Pt(8), Padding: core.All(2)},
			ThumbStyle: core.Style{CornerRadius: 2}, ThumbStyleFunc: thumb,
		},
	}})
}
