package hubui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func sidebarInteractionFixture(cx *core.Scope) *State {
	s := newState(cx, newDemoAPI())
	s.loadAppearance()
	s.setAppearance("light")
	s.devices = []Object{{"id": "local", "local": true, "connected": true}, {"id": "remote", "name": "Linux Studio", "connected": true}}
	s.workspaces = map[string]workspaceCache{"local": {providers: demoProviders()}, "remote": {providers: demoProviders()}}
	s.sessions = []Object{
		{"id": "build", "deviceId": "local", "providerId": "codex-app", "title": "重构会话目录与设备连接", "model": "gpt-6.1-sol", "cwd": "/work/" + strings.Repeat("nested-project/", 8) + "reign", "projectKey": "shared", "createdAt": 300, "recencyAt": 800000, "updatedAt": 800, "status": "running"},
		{"id": "review", "deviceId": "remote", "providerId": "claude-code", "title": "审查多设备同步与断线恢复", "cwd": "/srv/reign", "projectKey": "shared", "createdAt": 200, "recencyAt": 900000, "updatedAt": 900, "status": "waiting"},
		{"id": "design", "deviceId": "local", "providerId": "pi", "title": "完成侧边栏交互设计", "cwd": "/work/design", "createdAt": 100, "status": "idle"},
	}
	s.pinned[sessionIdentity(s.sessions[2])] = 1
	return s
}

func TestSessionHoverContextMenuAndFilters(t *testing.T) {
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = sidebarInteractionFixture(cx)
		return core.View{Style: fill, Children: []core.Node{core.View{Style: core.Style{Height: core.Pct(100)}, StyleFunc: func() core.Style { return core.Style{Width: core.Pt(min(390, cx.Env().Window().Width))} }, Children: []core.Node{s.directory(cx)}}}}
	}, nsgotest.Size(800, 1000), nsgotest.Environment(core.Environment{Locale: "zh-CN", Idiom: core.IdiomDesktop, OS: core.OSMacOS}))
	local := "id:" + sessionRowID("all", s.sessions[0])
	remote := "id:" + sessionRowID("all", s.sessions[1])
	localRow := app.Find(local)
	if localRow.Children[1].Children[1].Text != s.environmentName("local") {
		t.Fatal("session row repeats Agent name beside its icon")
	}
	if !strings.Contains(localRow.Props.Label, "Codex") {
		t.Fatal("accessible label lost Agent identity")
	}

	if nsgotest.Target() != "macos" {
		app.Hover(local)
		app.Advance(450 * time.Millisecond)
		app.WaitFor("id:session-hover-card", time.Second)
	}
	app.RightClick(local)
	app.WaitFor("id:session-pin-action", time.Second)
	if app.Exists("id:session-hover-card") {
		t.Fatal("hover overlaps context menu")
	}
	app.Screenshot("sidebar-context-menu")
	app.Press("id:session-pin-action")
	app.WaitFor("id:"+sessionRowID("pinned", s.sessions[0]), time.Second)
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:session-pin-action") })
	if s.selected != nil {
		t.Fatal("context action opened a conversation")
	}
	app.RightClick("id:" + sessionRowID("pinned", s.sessions[0]))
	app.WaitFor("role:menuitem 取消置顶", time.Second)
	app.Press("id:session-pin-action")
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:session-pin-action") })
	if s.pinned[sessionIdentity(s.sessions[0])] != 0 {
		t.Fatal("unpin did not remove the pin")
	}
	if app.Exists("id:session-grouping") {
		t.Fatal("grouping is still permanently visible")
	}
	openSectionFilters(app, "all")
	app.WaitFor("id:session-filter-panel", time.Second)
	app.Screenshot("sidebar-filters-light")
	openFilterDimension(app, "device")
	app.Screenshot("sidebar-filter-devices-light")
	app.Press("id:" + sessionFilterID("device", "remote"))
	leaveFilterDimension(app, "device")
	if app.Exists(local) || !app.Exists(remote) {
		t.Fatal("device filter did not update sections")
	}
	openFilterDimension(app, "agent")
	app.Press("id:" + sessionFilterID("agent", "codex-app"))
	leaveFilterDimension(app, "agent")
	app.WaitFor("id:all-filter-empty", time.Second)
	if app.Exists(remote) {
		t.Fatal("filters did not intersect")
	}
	app.Press("id:reset-session-filters")
	if !app.Exists(local) || !app.Exists(remote) {
		t.Fatal("reset did not restore sessions")
	}
	openFilterDimension(app, "project")
	app.Press("id:" + sessionFilterID("project", sessionProjectKey(s.sessions[0])))
	if len(s.filteredSessions()) != 2 {
		t.Fatal("shared repository filter did not span devices")
	}
	closeSectionFilters(app)
	app.Press("id:clear-all-filters")
	app.Post(func() { s.cx.Post(func() { s.setAppearance("dark") }) })
	app.Settle()

	for _, size := range []core.Size{{Width: 320, Height: 640}, {Width: 390, Height: 844}} {
		app.Resize(size.Width, size.Height)
		openSectionFilters(app, "all")
		app.WaitFor("id:session-filter-panel", time.Second)
		bounds := globalBounds(app.Find("id:session-filter-panel"))
		if bounds.Origin.X < 0 || bounds.Origin.X+bounds.Size.Width > size.Width+.5 || bounds.Origin.Y < 0 || bounds.Origin.Y+bounds.Size.Height > size.Height+.5 {
			t.Fatal("filter panel overflows", size, bounds)
		}
		app.Screenshot(fmt.Sprintf("sidebar-filters-%d-dark", int(size.Width)))
		openFilterDimension(app, "project")
		app.Screenshot(fmt.Sprintf("sidebar-filter-projects-%d-dark", int(size.Width)))
		closeSectionFilters(app)
	}
}

func TestSessionContextMenuLongPress(t *testing.T) {
	if nsgotest.Target() == "web" || nsgotest.Target() == "macos" {
		t.Skip("desktop automation sends a held mouse press, not a finger; touch is covered by the headless phone fixture")
	}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { s = sidebarInteractionFixture(cx); return s.directory(cx) }, nsgotest.Size(390, 844), nsgotest.Environment(core.Environment{Locale: "zh-CN", Idiom: core.IdiomPhone, OS: core.OSAndroid}))
	local := "id:" + sessionRowID("all", s.sessions[0])
	app.LongPress(local)
	app.WaitFor("id:session-pin-action", time.Second)
	app.Press("id:session-pin-action")
	if s.pinned[sessionIdentity(s.sessions[0])] == 0 || s.selected != nil {
		t.Fatal("long press failed to pin without opening conversation")
	}
}

func TestSessionFilterIdentityAndMembership(t *testing.T) {
	nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := sidebarInteractionFixture(cx)
		// Identical leaf names and paths are separate without a shared host key.
		a, b := copyObject(s.sessions[0]), copyObject(s.sessions[1])
		delete(a, "projectKey")
		delete(b, "projectKey")
		a["cwd"], b["cwd"] = "/work/reign", "/work/reign"
		s.sessions = append(s.sessions, a, b)
		a["id"], b["id"] = "local-project", "remote-project"
		s.filters.Projects = map[string]bool{sessionProjectKey(a): true}
		if got := s.filteredSessions(); len(got) != 1 || str(got[0], "id") != "local-project" {
			t.Fatal("project name/path collision", got)
		}
		s.filters.Projects[sessionProjectKey(b)] = true
		if len(s.filteredSessions()) != 2 {
			t.Fatal("multi-select is not a union")
		}
		s.filters.Devices = map[string]bool{"remote": true}
		if len(s.filteredSessions()) != 1 {
			t.Fatal("dimensions do not intersect")
		}
		s.filters = sessionFilters{Agents: map[string]bool{"pi": true}}
		counts := map[string]int{}
		for _, row := range s.directoryRows() {
			if row.Kind == "section" {
				counts[row.Section] = row.Count
			}
		}
		if counts["all"] != 0 || counts["pinned"] != 1 || counts["recent"] != 4 {
			t.Fatal("project filter affected pins or recent sessions", counts)
		}
		return core.View{}
	})
}

func TestPinnedFiltersKeepOrderAndWorkspace(t *testing.T) {
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = workspaceLayoutFixture(cx)
		s.pinned[sessionIdentity(s.sessions[1])] = 2
		return s.workspaceView(cx)
	}, nsgotest.Size(1100, 900), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop, Locale: "zh-CN"}))
	order := s.pinnedOrder()
	selected := sessionIdentity(s.selected)
	app.Type("id:chat-input", "keep this draft")
	editor := app.Find("id:chat-input").ID
	local := "id:" + sessionRowID("pinned", s.sessions[2])
	remote := "id:" + sessionRowID("pinned", s.sessions[1])
	openSectionFilters(app, "pinned")
	if app.Exists("id:session-grouping") {
		t.Fatal("pinned filter exposes grouping")
	}
	panel := app.Find("id:session-filter-panel").ID
	openFilterDimension(app, "device")
	choice := "id:" + sessionFilterID("device", "remote")
	checkbox := app.Find(choice).ID
	app.Press(choice)
	app.WaitUntil(time.Second, func() bool { return !app.Exists(local) && app.Exists(remote) })
	if !app.Find(choice).Props.Checked || app.Find(choice).ID != checkbox || app.Find("id:session-filter-panel").ID != panel {
		t.Fatal("selection closed/replaced its menu or lost its checked state")
	}
	// Multiple selections are a union and can be toggled without dismissal.
	app.Press("id:" + sessionFilterID("device", "local"))
	app.WaitFor(local, time.Second)
	app.Press("id:" + sessionFilterID("device", "local"))
	leaveFilterDimension(app, "device")
	openFilterDimension(app, "agent")
	app.Press("id:" + sessionFilterID("agent", "pi"))
	app.WaitFor("id:pinned-filter-empty", time.Second)
	app.Find("id:section:pinned")
	app.Find("id:pinned-filters")
	for _, section := range []string{"all", "recent"} {
		app.Find("id:" + sessionRowID(section, s.sessions[0]))
		for _, ss := range s.sessions[1:] {
			if app.Exists("id:" + sessionRowID(section, ss)) {
				t.Fatal("filtered pin leaked into another section")
			}
		}
	}
	leaveFilterDimension(app, "agent")
	app.Screenshot("pinned-filter-empty-menu")
	app.Press("id:reset-session-filters")
	app.WaitFor(local, time.Second)
	app.WaitFor(remote, time.Second)
	if !slices.Equal(s.pinnedOrder(), order) || sessionIdentity(s.selected) != selected || app.Find("id:chat-input").ID != editor || app.Find("id:chat-input").Text != "keep this draft" {
		t.Fatal("filtering changed pin order, selection or draft")
	}
	closeSectionFilters(app)
}

func TestCollapsedPinnedFiltersKeepSectionClosed(t *testing.T) {
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := sidebarInteractionFixture(cx)
		s.collapsed["section:pinned"] = true
		return s.directory(cx)
	}, nsgotest.Size(800, 700), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	openSectionFilters(app, "pinned")
	if app.Find("id:section:pinned").Props.Expanded {
		t.Fatal("opening the filter expanded the section")
	}
}

func TestPinnedFilterSheetReturnsAndClears(t *testing.T) {
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := sidebarInteractionFixture(cx)
		return s.directory(cx)
	}, nsgotest.Size(390, 720), nsgotest.Environment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone}))
	openSectionFilters(app, "pinned")
	openFilterDimension(app, "device")
	if app.Exists("id:filter-menu-agent") {
		t.Fatal("sheet displays more than one menu level")
	}
	app.Press("id:" + sessionFilterID("device", "remote"))
	app.WaitFor("id:pinned-filter-empty", time.Second)
	app.Press("id:back-filter-menu-device")
	app.WaitFor("id:filter-menu-device", time.Second)
	if app.Exists("id:filter-device-all") {
		t.Fatal("Back left child choices visible")
	}
	app.Press("id:reset-session-filters")
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:pinned-filter-empty") })
	app.Screenshot("pinned-filter-sheet")
	closeSectionFilters(app)
}

func TestFilterMenuKeyboardSelection(t *testing.T) {
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := sidebarInteractionFixture(cx)
		s.filters.Devices = map[string]bool{"local": true}
		return core.View{Style: fill.Merge(core.Style{Padding: core.All(24)}), Children: []core.Node{s.sessionFiltersButton(cx, "all", core.NewSignal(cx, false), func() bool { return false })}}
	}, nsgotest.Size(800, 600), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	app.Press("id:session-filters")
	app.WaitFor("id:filter-menu-device", time.Second)
	app.Focus("id:filter-menu-device")
	app.Key("right")
	app.WaitFor("id:filter-device-all", time.Second)
	choice := "id:" + sessionFilterID("device", "remote")
	// Navigate with real keys: directly focusing the choice would hide a
	// framework regression that updates only its reported menu focus.
	app.Key("home")
	app.Key("down")
	app.Key("space")
	app.WaitUntil(time.Second, func() bool { return app.Exists(choice) && app.Find(choice).Props.Checked })
	leaveFilterDimension(app, "device")
	app.Key("down")
	app.Key("enter")
	app.WaitFor("id:filter-agent-all", time.Second)
	closeSectionFilters(app)
}
