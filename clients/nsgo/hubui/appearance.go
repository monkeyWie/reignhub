package hubui

import (
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type colorScheme struct {
	canvas, sidebar, page, elevated, muted, selected, border, text, secondary, brand, brandText, code uint32
}

// Zinc surfaces with green links, selected controls and attention markers.
var lightColors = colorScheme{
	canvas: 0xfafafa, sidebar: 0xfafafa, page: 0xffffff, elevated: 0xffffff,
	muted: 0xf4f4f5, selected: 0xf4f4f5, border: 0xe4e4e7,
	text: 0x09090b, secondary: 0x71717a, brand: 0x79c476, brandText: 0x102610, code: 0xf4f4f5,
}
var darkColors = colorScheme{
	canvas: 0x09090b, sidebar: 0x18181b, page: 0x09090b, elevated: 0x18181b,
	muted: 0x27272a, selected: 0x27272a, border: 0x27272a,
	text: 0xfafafa, secondary: 0xa1a1aa, brand: 0x79c476, brandText: 0x09090b, code: 0x18181b,
}

func schemePalette(c colorScheme, p core.Palette) core.Palette {
	p.Background, p.Surface = core.RGB(c.page), core.RGB(c.muted)
	p.SurfaceHover, p.SurfacePressed = core.RGB(c.selected).Faded(.7), core.RGB(c.selected)
	p.Text, p.TextMuted, p.Border = core.RGB(c.text), core.RGB(c.secondary), core.RGB(c.border)
	p.Accent = core.RGB(c.brand)
	p.AccentHover, p.AccentPressed, p.AccentText = p.Accent.Faded(.85), p.Accent, core.RGB(c.brandText)
	p.Focus, p.Selection, p.Scrollbar = p.TextMuted, p.Accent.Faded(.22), p.TextMuted.Faded(.35)
	// Keep NSGo component roles alongside the workspace surface roles.
	roles := map[string]core.Color{
		"brand": core.RGB(c.brand), "brandText": core.RGB(c.brandText),
		"canvas": core.RGB(c.canvas), "sidebar": core.RGB(c.sidebar), "elevated": core.RGB(c.elevated), "selected": core.RGB(c.selected), "codeSurface": core.RGB(c.code),
		"field": core.RGB(c.elevated), "fieldBorder": p.Border, "inset": core.RGB(c.code), "raised": core.RGB(c.elevated), "line": p.Border, "faint": p.TextMuted,
		"foreground": p.Text, "card": core.RGB(c.elevated), "card-foreground": p.Text,
		"popover": core.RGB(c.elevated), "popover-foreground": p.Text,
		"primary": p.Text, "primary-foreground": p.Background,
		"secondary": p.Surface, "secondary-foreground": p.Text,
		"muted": p.Surface, "muted-foreground": p.TextMuted, "accent-foreground": p.Text,
		"input": p.Border, "ring": p.Focus,
		"sidebar-foreground": p.Text, "sidebar-primary": p.Accent, "sidebar-primary-foreground": p.AccentText,
		"sidebar-accent": core.RGB(c.selected), "sidebar-accent-foreground": p.Text,
		"sidebar-border": p.Border, "sidebar-ring": p.Focus,
		"code": p.Text, "accentFill": p.Accent, "light": p.Text, "onLight": p.Background, "track": p.Border, "knob": p.Background, "info": p.Accent,
	}
	for name, value := range roles {
		p.Colors[name] = value
	}

	return p
}

// NSGo uses the editor's palette accent for its field caret and for macOS
// input-method marks. Scope the override so links and selections keep their colors.
func useEditorTheme(cx *core.Scope) {
	theme := *cx.Theme()
	theme.Light.Accent = theme.Light.Text
	theme.Dark.Accent = theme.Dark.Text
	cx.SetTheme(&theme)
}

type formInput ui.Input

func (in formInput) Render(cx *core.Scope) core.Node {
	useEditorTheme(cx)
	in.AutoCapitalize = core.AutoCapitalizeNone
	return ui.Input(in)
}

type editorInput core.Input

func (in editorInput) Render(cx *core.Scope) core.Node {
	useEditorTheme(cx)
	in.AutoCapitalize = core.AutoCapitalizeNone
	return core.Input(in)
}

func textInputStyle(cx *core.Scope) core.Style {
	p := cx.Palette()
	return core.Style{Color: p.Text, PlaceholderColor: p.TextMuted, CaretColor: p.Text, SelectionColor: p.Selection}
}

func (s *State) applyColors() {
	t := core.DefaultTheme()
	t.Light = schemePalette(lightColors, t.Light)
	t.Dark = schemePalette(darkColors, t.Dark)
	t.Radius = 10
	s.cx.SetTheme(t)
}
func (s *State) loadAppearance() {
	s.appearance = "system"
	if v, ok := s.cx.Storage().GetItem("appearance"); ok {
		switch v {
		case "true", "dark":
			s.appearance = "dark"
		case "false", "light":
			s.appearance = "light"
		}
	}
	s.applyColors()
	s.setAppearance(s.appearance)
}
func (s *State) setAppearance(value string) {
	s.appearance = value
	switch value {
	case "dark":
		s.cx.SetAppearance(core.AppearanceDark)
	case "light":
		s.cx.SetAppearance(core.AppearanceLight)
	default:
		s.appearance = "system"
		s.cx.SetAppearance(core.AppearanceSystem)
	}
	s.dark = s.cx.Dark()
	_ = s.cx.Storage().SetItem("appearance", s.appearance)
	s.changed()
}
