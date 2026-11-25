package hubui

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestBrowserScreenAwakeReleasesLateRequestsAndRetriesOnInteraction(t *testing.T) {
	testBrowserScreenAwake(t)
}

func TestAndroidConversationKeepsScreenOnOnlyWhileVisibleAndActive(t *testing.T) {
	if isWeb() || nsgotest.Target() != "" {
		t.Skip("requires native host lifecycle injection")
	}
	host := core.NewHost()
	var awake atomic.Bool
	var requests atomic.Int32
	host.Handle("ras.screen-awake", func(call *core.HostCall) {
		awake.Store(flag(object(call.Data), "enabled"))
		requests.Add(1)
		call.Resolve(nil)
	})
	// A protected background connection must not keep the display awake.
	host.Handle("ras.background", func(call *core.HostCall) { call.Resolve(Object{"running": true}) })
	env := core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, newDemoAPI(), true) }, nsgotest.Size(390, 844), nsgotest.Environment(env), nsgotest.Host(host))
	showSessionDirectory(t, app)
	app.WaitFor("id:device-demo-device", time.Second)
	if awake.Load() {
		t.Fatal("device page kept screen awake")
	}
	openDemoChat(t, app)
	app.WaitUntil(time.Second, awake.Load)
	before := requests.Load()
	app.Type("id:chat-input", "draft")
	app.Settle()
	if requests.Load() != before {
		t.Fatal("ordinary chat updates replaced the display hold")
	}
	for _, lifecycle := range []core.Lifecycle{core.LifecycleInactive, core.LifecycleBackground, core.LifecycleActive} {
		env.Lifecycle = lifecycle
		app.SetEnvironment(env)
		app.WaitUntil(time.Second, func() bool { return awake.Load() == (lifecycle == core.LifecycleActive) })
	}
	app.Press("id:mobile-menu")
	app.WaitUntil(time.Second, func() bool { return !awake.Load() })
	app.Press(sessionSelector("demo-device", "codex-app", "demo-chat"))
	app.WaitUntil(time.Second, awake.Load)
	app.Back()
	app.Settle()
	if !awake.Load() {
		t.Fatal("root Back hid the conversation")
	}
	// A wider phone/tablet layout keeps its persistent sidebar.
	app.Resize(720, 844)
	app.WaitFor("id:session-sidebar", time.Second)
	if app.Exists("id:toggle-sidebar") {
		t.Fatal("wide mobile sidebar includes a collapse button")
	}
	app.Press(sessionSelector("demo-device", "codex-app", "demo-chat"))
	app.WaitUntil(time.Second, awake.Load)
	app.Back()
	app.WaitUntil(time.Second, func() bool { return !awake.Load() })
}

func TestIOSConversationDisplayHoldReleasesOnBackAndBackground(t *testing.T) {
	if isWeb() || nsgotest.Target() != "" {
		t.Skip("requires injected lifecycle and keep-awake recorder")
	}
	env := core.Environment{OS: core.OSIOS, Idiom: core.IdiomPhone}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, newDemoAPI(), true) }, nsgotest.Size(390, 844), nsgotest.Environment(env))
	openDemoChat(t, app)
	app.WaitUntil(time.Second, func() bool { return len(app.Awake) == 1 && app.Awake[0].Display })
	env.Lifecycle = core.LifecycleBackground
	app.SetEnvironment(env)
	app.WaitUntil(time.Second, func() bool { return len(app.Awake) == 0 })
	env.Lifecycle = core.LifecycleActive
	app.SetEnvironment(env)
	app.WaitUntil(time.Second, func() bool { return len(app.Awake) == 1 })
	showSessionDirectory(t, app)
	app.WaitUntil(time.Second, func() bool { return len(app.Awake) == 0 })
	app.Back()
	app.WaitUntil(time.Second, func() bool { return len(app.Awake) == 1 })
}

func TestDesktopConversationDoesNotPreventSleep(t *testing.T) {
	if isWeb() || nsgotest.Target() != "" {
		t.Skip("requires injected environment")
	}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, newDemoAPI(), true) }, nsgotest.Size(1100, 844), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	openDemoChat(t, app)
	if len(app.AwakeHolds) != 0 {
		t.Fatal("desktop chat acquired a display hold")
	}
}
