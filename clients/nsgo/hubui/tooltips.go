package hubui

import (
	"fmt"
	"slices"
	"time"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

// A bare Tooltip node has no surface in NSGo. Keep hints on the same bounded,
// themed surface, including icon hints and project paths.
func tooltipCard(cx *core.Scope, children ...core.Node) base.Tooltip {
	return base.Tooltip{
		Delay: 350 * time.Millisecond, Placement: core.PlaceTop, Offset: 8, Margin: 8,
		PopupStyle: core.Style{Padding: core.All(12), Gap: 7, CornerRadius: 10, BorderWidth: 1},
		PopupStyleFunc: func() core.Style {
			c := palette(cx)
			return core.Style{MaxWidth: core.Pt(min(340, max(120, cx.Env().Window().Width-24))), Background: c.elevated, Color: c.foreground, BorderColor: c.border,
				Shadows: []core.BoxShadow{{Offset: core.Point{Y: 4}, Blur: 16, Color: core.RGB(0).Faded(.16)}}}
		}, Children: children,
	}
}

// Every information row uses the same icon column and text baseline, including
// wrapped paths. The entire panel remains one surface.
func infoRow(cx *core.Scope, id string, icon func() *core.SVG, value func() string, title bool) core.Node {
	text := dynamicText(cx, value, 12, false)
	text.MaxLines = 2
	return infoRowText(cx, id, icon, text, title)
}

func infoRowText(cx *core.Scope, id string, icon func() *core.SVG, text core.Text, title bool) core.Node {
	if title {
		text.Style.FontSize, text.Style.LineHeight, text.Style.FontWeight = 13, 20, core.FontWeightSemibold
	}
	text.Style.FlexGrow, text.Style.FlexShrink, text.Style.MinWidth = 1, 1, core.Pt(0)
	return core.View{TestID: id, Style: core.Style{Direction: core.Row, AlignItems: core.AlignStart, Gap: 9, MinWidth: core.Pt(0)}, Children: []core.Node{
		core.View{Style: core.Style{Width: core.Pt(16), Height: core.Pt(20), FlexShrink: 0, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter}, Children: []core.Node{
			core.Icon{SVGFunc: icon, Style: core.Style{Width: core.Pt(15), Height: core.Pt(15)}, StyleFunc: func() core.Style {
				return core.Style{Color: choose(title, palette(cx).foreground, palette(cx).mutedText)}
			}},
		}}, text,
	}}
}

type groupInfoLine struct {
	Key, Text  string
	Repository bool
}

func (s *State) groupInfoLines(item directoryRow) []groupInfoLine {
	lines := []groupInfoLine{}
	for _, repo := range item.Repositories {
		lines = append(lines, groupInfoLine{Key: "repo:" + repo, Text: repo, Repository: true})
	}
	multipleDevices := false
	for _, dir := range item.Directories {
		if dir.Target != item.Directories[0].Target {
			multipleDevices = true
			break
		}
	}
	for _, dir := range item.Directories {
		text := dir.Path
		if multipleDevices {
			text += " · " + s.environmentName(dir.Target)
		}
		lines = append(lines, groupInfoLine{Key: dir.Target + ":" + dir.Path, Text: text})
	}
	return lines
}

func (s *State) groupInfo(cx *core.Scope, item func() directoryRow, compact bool) core.Node {
	lines := core.NewMemoFunc(cx, func() []groupInfoLine { s.read(); return s.groupInfoLines(item()) }, slices.Equal)
	previewLimit := func() int { return min(7, max(1, int((cx.Env().Window().Height-200)/46))) }
	return core.View{TestID: "group-info-card", Style: core.Style{Gap: 10, MinWidth: core.Pt(0)}, StyleFunc: func() core.Style {
		return core.Style{Width: core.Pt(min(296, max(120, cx.Env().Window().Width-64)))}
	}, Children: []core.Node{
		infoRow(cx, "group-info-title", func() *core.SVG {
			switch item().Kind {
			case "device":
				return s.hostIcon(cx, item().Target)
			case "agent":
				return agentIcon(item().Provider)
			default:
				return lucide.Folder()
			}
		}, func() string { return item().Title }, true),
		infoRow(cx, "group-info-count", lucide.MessageCircle, func() string {
			v := item()
			text := locales.For(cx).FilterMatches(fmt.Sprint(v.Count))
			if v.Active > 0 {
				text += " · " + locales.For(cx).ActiveSessionCount(fmt.Sprint(v.Active))
			}
			return text
		}, false),
		core.Show{When: func() bool { return len(lines.Get()) > 0 }, Then: func(cx *core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 10}, Children: []core.Node{
				core.View{Style: core.Style{Height: core.Pt(1), FlexShrink: 0}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).border} }},
				core.Scroll{StyleFunc: func() core.Style {
					return core.Style{MaxHeight: core.Pt(min(320, max(80, cx.Env().Window().Height-160)))}
				}, Children: []core.Node{
					core.View{Style: core.Style{Gap: 10, MinWidth: core.Pt(0)}, Children: []core.Node{
						core.Each(func() []groupInfoLine {
							values := lines.Get()
							if limit := previewLimit(); compact && len(values) > limit {
								return values[:limit]
							}
							return values
						}, func(v groupInfoLine) string { return v.Key }, func(cx *core.Scope, value func() groupInfoLine, _ func() int) core.Node {
							return infoRow(cx, "group-info-location", func() *core.SVG {
								if value().Repository {
									return lucide.FolderGit2()
								}
								return lucide.Folder()
							}, func() string { return value().Text }, false)
						}),
					}},
				}},
				core.Show{When: func() bool { return compact && len(lines.Get()) > previewLimit() }, Then: func(cx *core.Scope) core.Node {
					return infoRow(cx, "group-info-more", lucide.Ellipsis, func() string {
						return locales.For(cx).MoreProjectDetails(fmt.Sprint(len(lines.Get()) - previewLimit()))
					}, false)
				}},
			}}
		}},
	}}
}

func (s *State) sessionTooltip(cx *core.Scope, item func() directoryRow) base.Tooltip {
	optional := func(id string, icon func() *core.SVG, value func() string) core.Node {
		return core.Show{When: func() bool { return value() != "" }, Then: func(cx *core.Scope) core.Node { return infoRow(cx, id, icon, value, false) }}
	}
	tip := tooltipCard(cx, core.View{TestID: "session-hover-card", Style: core.Style{Gap: 10, MinWidth: core.Pt(0)}, Children: []core.Node{
		infoRow(cx, "session-info-title", lucide.MessageCircle, func() string { return item().Title }, true),
		infoRow(cx, "session-info-device", func() *core.SVG {
			return s.hostIcon(cx, str(item().Session, "deviceId"))
		}, func() string { s.read(); return s.environmentName(str(item().Session, "deviceId")) }, false),
		infoRow(cx, "session-info-agent", func() *core.SVG { return sessionIcon(item().Session)() }, func() string {
			s.read()
			v := item().Session
			return s.providerName(str(v, "deviceId"), str(v, "providerId"))
		}, false),
		optional("session-info-model", lucide.Cpu, func() string { return str(item().Session, "model") }),
		optional("session-info-path", lucide.Folder, func() string { return item().Path }),
		optional("session-info-branch", lucide.GitBranch, func() string {
			git := object(item().Session["gitInfo"])
			if branch := str(git, "branch"); branch != "" {
				return branch
			}
			if commit := str(git, "commit"); commit != "" {
				return locales.For(cx).DetachedGitHead(commit[:min(8, len(commit))])
			}
			return ""
		}),
	}})
	tip.PopupStyle.Width = core.Pt(320)
	tip.Placement = core.PlaceRight // Rich sidebar details open toward the content.
	return tip
}
