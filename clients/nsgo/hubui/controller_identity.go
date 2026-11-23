package hubui

import (
	"strings"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

func browserIconKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, browser := range []struct{ name, key string }{
		{"microsoft edge", "edge"}, {"edge", "edge"},
		{"google chrome", "chrome"}, {"chrome", "chrome"},
		{"chromium", "chromium"}, {"firefox", "firefox"},
		{"safari", "safari"}, {"opera", "opera"},
		{"brave", "brave"}, {"vivaldi", "vivaldi"},
	} {
		if name == browser.name || strings.HasPrefix(name, browser.name+" ") || strings.HasPrefix(name, browser.name+"/") {
			return browser.key
		}
	}
	return "browser"
}
func controllerIconKey(info Object) string {
	kind := str(info, "kind")
	browser := browserIconKey(str(info, "name"))
	// Browser identity takes precedence over its underlying operating system.
	// Recognize older name-only grants, but never infer an OS from a hostname.
	if kind == "browser" || kind == "" && browser != "browser" {
		return browser
	}
	return platformIconKey(str(info, "platform"))
}

func platformIconKey(platform string) string {
	switch strings.ToLower(platform) {
	case "darwin", "macos", "ios":
		return "apple"
	case "windows", "android", "linux":
		return strings.ToLower(platform)
	}
	return "device"
}

// A saved host can be offline or running a version that predates platform
// metadata. Old catalogues can still identify a Windows host by its native
// executable path. Device names are user-editable.
func (s *State) hostIcon(cx *core.Scope, target string) *core.SVG {
	s.read()
	platform := str(s.networkDevice(target), "platform")
	if platform == "" && target == "local" {
		platform = string(cx.Env().OS())
	}
	if platform == "" {
		for _, provider := range s.workspaces[target].providers {
			path := str(provider, "installPath")
			if len(path) >= 3 && strings.ContainsAny(path[:1], "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz") && path[1:3] == `:\` {
				platform = "windows"
				break
			}
		}
	}
	if svg := brandLogos[platformIconKey(platform)]; svg != nil {
		return svg
	}
	return lucide.Monitor()
}

func (s *State) hostGlyph(cx *core.Scope, target string, size core.Points) core.Icon {
	return core.Icon{TestID: "host-platform-" + target, SVGFunc: func() *core.SVG { return s.hostIcon(cx, target) },
		Style:     core.Style{Width: core.Pt(size), Height: core.Pt(size), FlexShrink: 0},
		StyleFunc: func() core.Style { return core.Style{Color: palette(cx).foreground} }}
}

func identityGlyph(cx *core.Scope, key string, size core.Points) core.Node {
	if svg := brandLogos[key]; svg != nil {
		return core.Icon{SVG: svg, TestID: "controller-platform-" + key, Style: core.Style{Width: core.Pt(size), Height: core.Pt(size), FlexShrink: 0}, StyleFunc: func() core.Style { return core.Style{Color: palette(cx).foreground} }}
	}
	if key == "browser" {
		return glyph(cx, lucide.Globe, size)
	}
	return glyph(cx, lucide.Monitor, size)
}
func controllerIdentifier(info Object) string {
	if str(info, "kind") != "native" {
		return ""
	}
	switch str(info, "platform") {
	case "darwin", "windows", "linux":
		fingerprint := str(info, "fingerprint")
		if len(fingerprint) >= 8 {
			return "设备标识 · " + fingerprint[:4] + "-" + fingerprint[4:8]
		}
	}
	return ""
}
func controllerIdentity(cx *core.Scope, get func() Object, details ...core.Node) core.Node {
	icon := core.NewMemo(cx, func() string { return controllerIconKey(get()) })
	return row(core.Dynamic(func(cx *core.Scope) core.Node {
		return identityGlyph(cx, icon.Get(), 22)
	}), core.View{Style: fill, Children: append([]core.Node{
		singleLine(cx, func() string { return str(get(), "name") }, 13, false),
		core.Show{When: func() bool { return controllerIdentifier(get()) != "" }, Then: func(cx *core.Scope) core.Node {
			return dynamicText(cx, func() string { return controllerIdentifier(get()) }, 11, true)
		}},
	}, details...)})
}
