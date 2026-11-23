package hubui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestCollapsedGroupsCarryUnreadToVisibleParent(t *testing.T) {
	var s *State
	var groups []directoryRow
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = workspaceLayoutFixture(cx)
		s.grouping = groupAgent
		s.sessions[0]["status"] = "idle"
		s.unreadSessions[activityIdentity(s.sessions[0])] = true
		for _, r := range s.directoryRows() {
			if r.Header && r.Target == "local" && r.Kind != "section" {
				groups = append(groups, r)
			}
		}
		return s.workspaceView(cx)
	}, nsgotest.Size(1100, 1100), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop, Locale: "zh-CN"}))
	if len(groups) != 3 {
		t.Fatalf("expected device, Agent, project: %v", groups)
	}
	for _, group := range groups {
		if app.Exists("id:unread-" + group.ID) {
			t.Fatal("expanded parent duplicates the child's dot")
		}
	}
	for i := len(groups) - 1; i >= 0; i-- {
		app.Press("id:" + groups[i].ID)
		app.WaitFor("id:unread-"+groups[i].ID, time.Second)
		if i+1 < len(groups) && app.Exists("id:unread-"+groups[i+1].ID) {
			t.Fatal("hidden descendant retains a visible dot")
		}
	}
	app.Press("id:section:all")
	app.WaitFor("id:unread-section:all", time.Second)
	app.Press("id:section:recent")
	app.WaitFor("id:unread-section:recent", time.Second)
	app.Screenshot("collapsed-sections-unread")
	app.Press("id:section:all")
	for _, group := range groups {
		app.WaitFor("id:unread-"+group.ID, time.Second)
		app.Press("id:" + group.ID)
	}
	row := sessionRowID("all", s.sessions[0])
	app.WaitFor("id:unread-"+row, time.Second)
	app.Press("id:" + row)
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:unread-section:recent") && !app.Exists("id:unread-"+row) })
	app.Post(func() {
		s.cx.Post(func() {
			s.unreadSessions[activityIdentity(s.sessions[2])] = true
			s.collapsed["section:pinned"] = true
			s.saveSidebarPreferences()
		})
	})
	app.WaitFor("id:unread-section:pinned", time.Second)
	if app.Exists("id:unread-section:all") || app.Exists("id:unread-section:recent") {
		t.Fatal("pin leaked unread into another section")
	}
}

func TestCollapsedSectionDotSharesTrailingActions(t *testing.T) {
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := newState(cx, newDemoAPI())
		s.collapsed["section:all"] = true
		section := directoryRow{ID: "section:all", Kind: "section", Section: "all", Title: "Projects", Unread: true}
		return core.View{Style: core.Style{Width: core.Pt(300), Padding: core.All(40)}, Children: []core.Node{
			s.treeHeader(cx, func() directoryRow { return section }, &listTouchBrake{}),
			button(cx, "outside-section", "Outside section", nil, "ghost", func() {}),
		}}
	}, nsgotest.Size(900, 700), nsgotest.Environment(core.Environment{OS: core.OSWindows, Idiom: core.IdiomDesktop}))

	const status = "id:section:all-status"
	const filter = "id:session-filters"
	dot := globalBounds(app.Find("id:unread-section:all"))
	button := globalBounds(app.Find(filter))
	if dot.Origin.X <= button.Origin.X || dot.Origin.X+dot.Size.Width >= button.Origin.X+button.Size.Width {
		t.Fatal("unread dot is outside the trailing action slot")
	}
	if app.Find(status).Style.Opacity == 0 || app.Find(filter).Style.Opacity != 0 {
		t.Fatal("section should show the unread dot at rest")
	}
	// The desktop test surfaces exercise native focus. The headless run
	// drives the pointer without relying on the OS cursor's position.
	nativeFocus := nsgotest.Target() == "windows" || nsgotest.Target() == "macos"
	if nativeFocus {
		app.Focus("id:section:all")
	} else {
		app.HoverAt("id:section:all", core.Point{X: 22, Y: 16})
	}
	app.WaitUntil(time.Second, func() bool {
		return app.Find(status).Style.Opacity == 0 && app.Find(filter).Style.Opacity != 0
	})
	if nativeFocus {
		app.Focus("id:outside-section")
	} else {
		app.HoverOut()
	}
	app.WaitUntil(time.Second, func() bool {
		return app.Find(status).Style.Opacity != 0 && app.Find(filter).Style.Opacity == 0
	})
}

func TestCollapsedUnreadRespectsFiltersAndGrouping(t *testing.T) {
	nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := sidebarInteractionFixture(cx)
		s.sessions[0]["status"] = "idle"
		s.unreadSessions[activityIdentity(s.sessions[0])] = true
		for _, grouping := range []string{groupProject, groupDevice, groupAgent} {
			s.grouping = grouping
			s.filters = sessionFilters{Devices: map[string]bool{"remote": true}}
			for _, row := range s.directoryRows() {
				if row.Unread && row.Section != "recent" {
					t.Fatal("filtered session contributed unread", grouping, row)
				}
			}
			s.filters = sessionFilters{}
			found := false
			for _, row := range s.directoryRows() {
				found = found || row.Kind == "project" && row.Unread
			}
			if !found {
				t.Fatal("project lost unread", grouping)
			}
		}
		return core.View{}
	})
}

type projectCreationAPI struct {
	*staticWorkspaceAPI
	writes chan questionSubmission
}

func (f *projectCreationAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method != "client.call" || str(r, "method") != "create" {
		return f.staticWorkspaceAPI.Call(ctx, method, p, target, r)
	}
	w := questionSubmission{target: target, request: r, result: make(chan error, 1)}
	select {
	case f.writes <- w:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case err := <-w.result:
		return Object{"id": "created", "providerId": str(r, "provider"), "cwd": str(object(r["options"]), "cwd"), "title": "Created fixture"}, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestProjectCreateUsesDevicePathsAndRecovers(t *testing.T) {
	var s *State
	var project directoryRow
	f := &projectCreationAPI{staticWorkspaceAPI: &staticWorkspaceAPI{newDemoAPI()}, writes: make(chan questionSubmission, 4)}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = workspaceLayoutFixture(cx)
		s.api = f
		s.sessions[0]["projectDir"] = "/local/reign"
		s.sessions[1]["projectDir"] = `C:\work\reign`
		s.sessions[1]["providerId"] = "pi"
		for _, r := range s.directoryRows() {
			if r.Kind == "project" {
				project = r
				break
			}
		}
		return s.workspaceView(cx)
	}, nsgotest.Size(1100, 900), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop, Locale: "zh-CN"}))
	app.Type("id:chat-input", "保留当前草稿")
	if nsgotest.Target() == "macos" {
		app.Focus("id:" + project.ID)
	} else {
		app.Hover("id:" + project.ID)
	}
	app.WaitUntil(time.Second, func() bool { return app.Find("id:actions-"+project.ID).Style.Opacity > .5 })
	if nsgotest.Target() == "macos" {
		app.Focus("id:create-" + project.ID)
	} else {
		app.Hover("id:create-" + project.ID)
	}
	app.Advance(500 * time.Millisecond)
	app.WaitFor("text:新建会话", time.Second)
	if app.Find("id:create-"+project.ID).Style.Background != 0 || app.Exists("id:group-info-card") {
		t.Fatal("project action has a filled tile or overlapping information panel")
	}
	app.Screenshot("project-create-hover")
	if nsgotest.Target() != "macos" {
		app.Press("id:create-" + project.ID)
	} else {
		app.RightClick("id:" + project.ID)
		app.Press("id:project-create-action")
	}
	app.WaitFor("id:new-session-cwd", time.Second)
	if s.collapsed[project.Group] || app.Find("id:new-session-cwd").Text != "/local/reign" || str(s.selected, "id") != "design" {
		t.Fatal("project action collapsed/navigated or lost project root")
	}
	app.Press("id:new-session-device")
	app.Press("role:option Linux Studio")
	app.WaitUntil(time.Second, func() bool { return app.Find("id:new-session-cwd").Text == `C:\work\reign` })
	app.Screenshot("project-create-remote-directory")
	app.Press("id:create-session-confirm")
	app.WaitUntil(time.Second, func() bool { return len(f.writes) > 0 })
	w := <-f.writes
	if w.target != "remote" || str(w.request, "provider") != "pi" || str(object(w.request["options"]), "cwd") != `C:\work\reign` {
		t.Fatal("creation used another device's Agent/path", w.target, w.request)
	}
	app.WaitFor("id:create-session-confirm-loading", time.Second)
	app.PressAt("id:create-session-confirm", core.Point{X: 8, Y: 8})
	if len(f.writes) > 0 {
		t.Fatal("duplicate create")
	}
	w.result <- errors.New("Fixture rejected before creation")
	app.WaitUntil(time.Second, func() bool { return !app.Find("id:create-session-confirm").Props.Disabled })
	if app.Find("id:new-session-cwd").Text != `C:\work\reign` || app.Find("id:chat-input").Text != "保留当前草稿" {
		t.Fatal("failed creation lost form or existing conversation draft")
	}
	app.Press("id:create-session-confirm")
	app.WaitUntil(time.Second, func() bool { return len(f.writes) > 0 })
	(<-f.writes).result <- nil
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:new-session-cwd") })
	app.Press("id:new-session")
	app.WaitFor("id:new-session-cwd", time.Second)
	if app.Find("id:new-session-cwd").Text != "" {
		t.Fatal("global creation retained old project context")
	}
}

func TestProjectCreateTouchAndOfflineScope(t *testing.T) {
	if nsgotest.Target() != "" {
		t.Skip("headless fixture supplies finger input")
	}
	var s *State
	var project directoryRow
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = sidebarInteractionFixture(cx)
		s.grouping = groupAgent
		for _, r := range s.directoryRows() {
			if r.Kind == "project" && r.Target == "remote" {
				project = r
				break
			}
		}
		s.devices[1]["connected"] = false
		return core.View{Style: fill, Children: []core.Node{s.directory(cx), core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) })}}
	}, nsgotest.Size(390, 844), nsgotest.Environment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone, Locale: "zh-CN"}))
	if app.Exists("id:create-" + project.ID) {
		t.Fatal("touch shows hover-only action")
	}
	app.LongPress("id:" + project.ID)
	app.WaitFor("id:project-create-action", time.Second)
	app.Press("id:project-create-action")
	app.WaitFor("id:new-session-cwd", time.Second)
	if s.collapsed[project.Group] || app.Exists("id:new-session-provider") || app.Find("id:new-session-cwd").Text != "" {
		t.Fatal("offline project fell back to an unrelated device/path")
	}
}
