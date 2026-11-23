package hubui

import (
	"context"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestControllerPlatformAndDisplay(t *testing.T) {
	for _, tc := range []struct {
		info             Object
		icon, identifier string
	}{
		{Object{"name": "Levi 的 Mac", "platform": "darwin", "kind": "native", "fingerprint": "0123456789abcdef"}, "apple", "设备标识 · 0123-4567"},
		{Object{"name": "Microsoft Edge 157", "platform": "windows", "kind": "browser", "fingerprint": "0123456789abcdef"}, "edge", ""},
		{Object{"name": "iQOO Z9 Turbo", "platform": "android", "kind": "native"}, "android", ""},
		{Object{"name": "iPad (A16)", "platform": "ios", "kind": "native"}, "apple", ""},
		{Object{"name": "WORKSTATION", "platform": "windows", "kind": "native"}, "windows", ""},
		{Object{"name": "Chrome", "kind": "native", "platform": "windows"}, "windows", ""},
		{Object{"name": "Chrome 157", "platform": "darwin", "kind": "browser"}, "chrome", ""},
		{Object{"name": "Google Chrome 157", "kind": "browser"}, "chrome", ""},
		{Object{"name": "Edge · Android"}, "edge", ""},
		{Object{"name": "Edge · Mac"}, "edge", ""},
		{Object{"name": "Chrome"}, "chrome", ""},
		{Object{"name": "Firefox 157", "kind": "browser", "platform": "android"}, "firefox", ""},
		{Object{"name": "Safari 26", "kind": "browser", "platform": "ios"}, "safari", ""},
		{Object{"name": "Opera 120", "kind": "browser"}, "opera", ""},
		{Object{"name": "Brave 1", "kind": "browser"}, "brave", ""},
		{Object{"name": "Vivaldi 7", "kind": "browser"}, "vivaldi", ""},
		{Object{"name": "Chromium 157", "kind": "browser"}, "chromium", ""},
		{Object{"name": "Unknown browser", "kind": "browser", "platform": "darwin"}, "browser", ""},
		{Object{"name": "ChromeBook"}, "device", ""},
		{Object{"name": "unknown Android name"}, "device", ""},
	} {
		if got := controllerIconKey(tc.info); got != tc.icon {
			t.Errorf("icon %q want %q", got, tc.icon)
		}
		if got := controllerIdentifier(tc.info); got != tc.identifier {
			t.Errorf("identifier %q want %q", got, tc.identifier)
		}
	}
	for _, tc := range []struct{ name, model, want string }{
		{"iPad", "iPad15,7", "iPad (A16)"},
		{"Levi 的 iPad", "iPad15,7", "Levi 的 iPad"},
		{"iPhone", "iPhone17,3", "iPhone 16"},
		{"iPad", "iPad99,99", "iPad"},
	} {
		if got := appleDeviceName(tc.name, tc.model); got != tc.want {
			t.Errorf("Apple name %q want %q", got, tc.want)
		}
	}
}
func TestControllerIdentityRows(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(choose(dark, "dark", "light"), func(t *testing.T) {
			app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
				cx.SetTheme(core.DefaultTheme())
				profiles := []Object{
					{"name": "Levi 的 MacBook Pro", "platform": "darwin", "kind": "native", "fingerprint": "abcdef0123456789"},
					{"name": "Windows PC", "platform": "windows", "kind": "native"},
					{"name": "Linux PC", "platform": "linux", "kind": "native"},
					{"name": "iPad (A16)", "platform": "ios", "kind": "native"},
					{"name": "iQOO Z9 Turbo", "platform": "android", "kind": "native"},
					{"name": "Microsoft Edge 157", "platform": "windows", "kind": "browser"},
					{"name": "Chrome"},
					{"name": "Firefox 157", "kind": "browser", "platform": "android"},
					{"name": "Safari 26", "kind": "browser", "platform": "ios"},
					{"name": "Opera 120", "kind": "browser"},
					{"name": "Brave 1", "kind": "browser"},
					{"name": "Vivaldi 7", "kind": "browser"},
				}
				rows := []core.Node{}
				for _, profile := range profiles {
					rows = append(rows, controllerIdentity(cx, func() Object { return profile }))
				}
				return core.View{Style: core.Style{Padding: core.All(24), Gap: 16}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).background} }, Children: rows}
			}, nsgotest.Size(390, 620), nsgotest.Environment(core.Environment{Dark: dark}))
			app.WaitFor("text:Microsoft Edge 157", time.Second)
			app.Find("text:设备标识 · abcd-ef01")
			app.Find("text:iPad (A16)")
			app.Find("text:iQOO Z9 Turbo")
			for _, key := range []string{"windows", "linux", "android", "edge", "chrome", "firefox", "safari", "opera", "brave", "vivaldi"} {
				app.Find("id:controller-platform-" + key)
			}
			app.Screenshot("controller-identities-" + choose(dark, "dark", "light"))
		})
	}
}

func TestAuthorizedControllersStayStableDuringRefresh(t *testing.T) {
	var s *State
	phone := Object{"client": "z-phone", "name": "Phone", "kind": "native", "platform": "android", "authorizedAt": int64(1795000000000)}
	laptop := Object{"client": "a-laptop", "name": "Laptop", "kind": "native", "platform": "windows"}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = newState(cx, newDemoAPI())
		s.host = Object{"grants": []Object{phone, laptop}}
		s.dialog.Set("local-access")
		return core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) })
	}, nsgotest.Size(920, 720), nsgotest.Environment(core.Environment{Idiom: core.IdiomDesktop}))

	const revoke = "id:revoke-z-phone"
	const other = "id:revoke-a-laptop"
	const icon = "id:controller-platform-android"
	resting := app.Find(revoke).Style.Background
	app.Hover(revoke)
	button := app.Find(revoke)
	buttonID, iconID := button.ID, app.Find(icon).ID
	background := button.Style.Background
	if background == resting {
		t.Fatal("revoke button did not show hover feedback")
	}

	renamed := copyObject(phone)
	renamed["name"] = "Renamed phone"
	for _, grants := range [][]Object{
		{copyObject(laptop), copyObject(phone)},
		{copyObject(laptop), renamed},
		{renamed, copyObject(laptop)},
	} {
		app.Post(func() {
			s.host = Object{"grants": grants}
			s.changed()
		})
		app.Settle()
		button := app.Find(revoke)
		if globalBounds(button).Origin.Y >= globalBounds(app.Find(other)).Origin.Y {
			t.Fatal("refresh reordered authorized controllers")
		}
		if button.ID != buttonID || button.Style.Background != background || app.Find(icon).ID != iconID {
			t.Fatal("refresh rebuilt the row or reset its hover background")
		}
	}
	app.Find("text:Renamed phone")
	authorized := copyObject(laptop)
	authorized["authorizedAt"] = float64(1795000060000)
	app.Post(func() {
		s.host = Object{"grants": []Object{renamed, authorized}}
		s.changed()
	})
	app.Settle()
	if globalBounds(app.Find(other)).Origin.Y >= globalBounds(app.Find(revoke)).Origin.Y || app.Find(revoke).ID != buttonID {
		t.Fatal("new approval did not move its existing row to the top")
	}
	app.Post(func() {
		s.host = Object{"grants": []Object{renamed}}
		s.changed()
	})
	app.Settle()
	if app.Exists(other) || app.Find(revoke).ID != buttonID {
		t.Fatal("authorization removal did not preserve the surviving row")
	}
}

type profileCaptureAPI struct {
	*demoAPI
	profile device.ClientInfo
}

func (a *profileCaptureAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	a.profile = device.ClientInfoFrom(ctx)
	return nil, nil
}
func TestProfileSnapshotReachesAPI(t *testing.T) {
	base := &profileCaptureAPI{demoAPI: newDemoAPI()}
	api := &identifiedAPI{API: base}
	info := device.ClientInfo{Name: "vivo V2618A", Platform: "android", Kind: "native"}
	api.setInfo(info)
	_, _ = api.Call(context.Background(), "client.reconnect", nil, "", nil)
	if base.profile != info {
		t.Fatalf("missing profile: %#v", base.profile)
	}
}

func TestSidebarIdentifiesLocalAndRemotePlatforms(t *testing.T) {
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = workspaceLayoutFixture(cx)
		s.setAppearance("dark")
		s.devices = []Object{{"id": "local", "local": true, "connected": true}, {"id": "pc", "name": "Desktop", "connected": true}}
		return s.workspaceView(cx)
	}, nsgotest.Size(980, 700), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	app.Find("id:host-platform-local")
	remote := app.Find("id:host-platform-pc").ID
	app.Post(func() {
		if s.hostIcon(s.cx, "local") != brandLogos["apple"] || s.hostIcon(s.cx, "pc") != lucide.Monitor() {
			t.Error("wrong icon for local Mac or unidentified host")
		}
		s.workspaces["pc"] = workspaceCache{providers: []Object{{"installPath": `C:\Program Files\Fixture\agent.exe`}}}
		if s.hostIcon(s.cx, "pc") != brandLogos["windows"] {
			t.Error("legacy Windows installation path did not identify host")
		}
		s.workspaces["pc"] = workspaceCache{}
		s.devices[1] = Object{"id": "pc", "name": "Desktop", "platform": "windows", "connected": false}
		s.changed()
	})
	app.Settle()
	app.Post(func() {
		if s.hostIcon(s.cx, "pc") != brandLogos["windows"] {
			t.Error("offline Windows device lost its platform")
		}
	})
	app.Settle()
	if remote != app.Find("id:host-platform-pc").ID {
		t.Error("metadata refresh replaced the device icon view")
	}
	app.Screenshot("sidebar-host-platforms")
}
