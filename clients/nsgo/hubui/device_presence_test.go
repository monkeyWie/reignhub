package hubui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

type directoryFixture struct {
	*pairingFixture
	reads  atomic.Int32
	online atomic.Bool
	broken atomic.Bool
}

func (f *directoryFixture) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	switch method {
	case "client.list":
		return []Object{{"id": "demo-device", "name": "工作电脑", "connectionId": "123456", "service": DefaultService, "connected": false}}, nil
	case "client.presence":
		f.reads.Add(1)
		if f.broken.Load() {
			return nil, errors.New("service unavailable")
		}
		return []Object{{"id": "demo-device", "online": f.online.Load(), "checkedAt": time.Now().Unix()}}, nil
	default:
		return f.pairingFixture.Call(ctx, method, p, target, r)
	}
}

func TestDeviceListShowsServerPresenceWithoutConnecting(t *testing.T) {
	f := &directoryFixture{pairingFixture: &pairingFixture{demoAPI: newDemoAPI(), steps: make(chan connectivity.Progress, 2), finish: make(chan error, 1)}}
	f.online.Store(true)
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return RootWithAPI(cx, f, true)
	}, nsgotest.Size(390, 844))
	showSessionDirectory(t, app)
	app.WaitFor("text:在线", 2*time.Second)
	if f.calls.Load() != 0 || f.reads.Load() != 1 {
		t.Fatal("presence started a connection or duplicated its batch")
	}
	app.WaitFor("id:session-sidebar", time.Second)
	app.WaitFor(sessionSelector("demo-device", "codex-app", "demo-chat"), time.Second)
	if app.Exists("id:dialog-connecting") {
		t.Fatal("saved device navigation blocked on connection")
	}
	app.Press("id:device-demo-device")
	app.Press("id:reconnect-device")
	app.Press("id:sidebar-connection-status-demo-device")
	app.WaitFor("text:请在被控电脑的 ReignHub 中允许此次连接", time.Second)
	f.finish <- errors.New("被控设备离线")
	app.WaitFor("text:被控电脑不在线", time.Second)
	app.Press("id:dialog-connection-status-close")
	if !app.Exists("id:session-sidebar") || f.calls.Load() != 1 {
		t.Fatal("failed connection discarded cached page or retried")
	}
	app.Screenshot("device-list-server-presence")
}

func TestPresenceTextDoesNotConfuseOfflineWithDisconnected(t *testing.T) {
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := newState(cx, newDemoAPI())
		for _, test := range []struct {
			online  any
			checked int64
			want    string
		}{
			{true, time.Now().Unix(), "在线"},
			{false, time.Now().Unix(), "离线"},
			{nil, time.Now().Unix(), ""},
			{true, time.Now().Add(-2 * time.Minute).Unix(), ""},
		} {
			s.devicePresence = map[string]Object{"host": {"online": test.online, "checkedAt": test.checked}}
			if got := s.deviceListStatus(Object{"id": "host"}); got != test.want {
				t.Errorf("presence=%v: %q want %q", test.online, got, test.want)
			}
		}
		return core.View{}
	})
	_ = app
}

type cachedPresenceAPI struct {
	*cachedWorkspaceAPI
	serverOnline   atomic.Bool
	presenceBroken atomic.Bool
}

func (f *cachedPresenceAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.presence" {
		if f.presenceBroken.Load() {
			return nil, errors.New("presence unavailable")
		}
		return []Object{{"id": "demo-device", "online": f.serverOnline.Load(), "checkedAt": time.Now().Unix()}}, nil
	}
	return f.cachedWorkspaceAPI.Call(ctx, method, p, target, r)
}

func TestOfflineDeviceOpensCacheAndConnectsOnceWhenPresenceReturns(t *testing.T) {
	f := &cachedPresenceAPI{cachedWorkspaceAPI: &cachedWorkspaceAPI{demoAPI: newDemoAPI(), connectResult: make(chan error, 1), liveRelease: make(chan struct{})}}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = newState(cx, f)
		s.width.Set(390)
		s.initialize()
		return s.compactWorkspace(cx)
	}, nsgotest.Size(390, 844))
	showSessionDirectory(t, app)
	app.WaitFor("text:离线", time.Second)
	app.WaitFor(sessionSelector("demo-device", "codex-app", "demo-chat"), time.Second)
	app.Press(sessionSelector("demo-device", "codex-app", "demo-chat"))
	app.WaitFor("text:本地保存的完整消息", time.Second)
	app.Find("role:button 设备离线")
	if f.connects.Load() != 0 || app.Exists("id:connection-retrying") {
		t.Fatal("offline device initiated a connection instead of waiting")
	}
	app.Type("id:chat-input", "离线草稿")
	if app.Find("id:chat-input").Props.Disabled || !app.Find("id:send-message").Props.Disabled {
		t.Fatal("offline state prevented editing or allowed sending")
	}
	f.presenceBroken.Store(true)
	app.Post(func() { s.cx.Post(func() { s.directoryChecked = time.Time{}; s.refreshDevicePresence() }) })
	app.WaitUntil(time.Second, func() bool { return !s.directoryChecking })
	app.Find("role:button 设备离线")
	f.presenceBroken.Store(false)
	f.serverOnline.Store(true)
	app.Post(func() { s.cx.Post(func() { s.directoryChecked = time.Time{}; s.refreshDevicePresence() }) })
	app.WaitUntil(time.Second, func() bool { return f.connects.Load() == 1 })
	app.WaitFor("id:connection-retrying", time.Second)
	f.connectResult <- errors.New("network reset")
	app.WaitUntil(time.Second, func() bool { return !s.connection("demo-device").reconnecting })
	app.Post(func() { s.cx.Post(func() { delete(s.retrying, "demo-device"); s.autoReconnect() }) })
	app.Settle()
	if f.connects.Load() != 1 {
		t.Fatal("failed first connection entered automatic retry loop")
	}
	app.Find("text:本地保存的完整消息")
	if app.Find("id:chat-input").Text != "离线草稿" {
		t.Fatal("presence transition lost the draft")
	}
}

func TestPresencePausesEstablishedRecoveryAndResumesOnOnline(t *testing.T) {
	f := &cachedPresenceAPI{cachedWorkspaceAPI: &cachedWorkspaceAPI{demoAPI: newDemoAPI(), connectResult: make(chan error, 1), liveRelease: make(chan struct{})}}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = newState(cx, f)
		s.devices = []Object{{"id": "demo-device", "autoReconnect": true}}
		s.connection("demo-device").established = true
		s.autoReconnect()
		return core.View{}
	})
	app.WaitUntil(time.Second, func() bool { return f.connects.Load() == 1 })
	app.Post(func() {
		s.cx.Post(func() {
			s.applyDevicePresence([]Object{{"id": "demo-device", "online": false, "checkedAt": time.Now().Unix()}})
			s.autoReconnect()
		})
	})
	app.WaitUntil(time.Second, func() bool { return !s.connection("demo-device").reconnecting })
	if s.connectionTitle("demo-device") != "设备离线" || f.connects.Load() != 1 {
		t.Fatal("offline recovery did not pause")
	}
	app.Post(func() {
		s.cx.Post(func() {
			s.applyDevicePresence([]Object{{"id": "demo-device", "online": true, "checkedAt": time.Now().Unix()}})
		})
	})
	app.WaitUntil(time.Second, func() bool { return f.connects.Load() == 2 })
	f.connectResult <- nil
	app.WaitUntil(time.Second, func() bool { return s.connectionReady("demo-device") })
	if s.connectionTitle("demo-device") != "连接正常" || s.deviceListStatus(s.devices[0]) != "在线" {
		t.Fatal("successful reconnect did not update presence and readiness")
	}
}

type foregroundPresenceFixture struct {
	*demoAPI
	calls   atomic.Int32
	release chan struct{}
}

func (f *foregroundPresenceFixture) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.presence" {
		if f.calls.Add(1) == 1 {
			// Model a queued response arriving after foreground starts a new read.
			<-f.release
			return []Object{{"id": "demo-device", "online": false, "checkedAt": time.Now().Unix()}}, nil
		}
		return []Object{{"id": "demo-device", "online": true, "checkedAt": time.Now().Unix()}}, nil
	}
	return f.demoAPI.Call(ctx, method, p, target, r)
}

func TestForegroundPresenceDiscardsLateBackgroundResult(t *testing.T) {
	f := &foregroundPresenceFixture{demoAPI: newDemoAPI(), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	})
	app, s := conversationFixture(t, f, 390, Object{}, false)
	app.Post(func() { s.cx.Post(func() { s.keepAlive = true; s.refreshDevicePresence() }) })
	app.WaitUntil(time.Second, func() bool { return f.calls.Load() == 1 })
	app.Post(func() { s.cx.Post(func() { s.setBackground(true); s.setBackground(false); s.refreshDevicePresence() }) })
	app.WaitUntil(time.Second, func() bool { return f.calls.Load() == 2 })
	close(f.release)
	// Let the old API call post its response after the fresh foreground read.
	time.Sleep(50 * time.Millisecond)
	app.Settle()
	check := make(chan bool, 1)
	app.Post(func() {
		s.cx.Post(func() {
			online, known := s.deviceOnline(s.devices[0])
			check <- known && online && !s.directoryChecking
		})
	})
	app.WaitUntil(time.Second, func() bool {
		select {
		case ok := <-check:
			if !ok {
				t.Fatal("foreground presence remained blocked by background request")
			}
			return true
		default:
			return false
		}
	})
}

func TestPairedDeviceMenuTouch(t *testing.T)   { checkPairedDeviceMenu(t, true) }
func TestPairedDeviceMenuDesktop(t *testing.T) { checkPairedDeviceMenu(t, false) }

func checkPairedDeviceMenu(t *testing.T, phone bool) {
	t.Helper()
	var s *State
	width := choose(phone, core.Points(390), core.Points(1000))
	env := core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop, Dark: true}
	if phone {
		env = core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone}
	}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = workspaceLayoutFixture(cx)
		s.applyColors()
		s.devices = []Object{{"id": "fixture-host", "name": "Studio MacBook Pro", "platform": "darwin", "connected": true, "path": "Relay · QUIC"}}
		return s.workspaceView(cx)
	}, nsgotest.Size(width, 844), nsgotest.Environment(env))
	showSessionDirectory(t, app)
	app.Press("id:device-fixture-host")
	app.WaitFor("id:disconnect-device", time.Second)
	if app.Exists("id:reconnect-device") || !strings.Contains(app.Find("id:device-summary-status").Text, "Relay · UDP") {
		t.Fatal("connected device did not present its active path and disconnect action")
	}
	app.Screenshot(choose(phone, "device-actions-touch", "device-actions-desktop"))
	app.Post(func() { s.devices[0]["connected"] = false; s.changed() })
	app.WaitFor("id:reconnect-device", time.Second)
	if app.Exists("id:disconnect-device") || strings.Contains(app.Find("id:device-summary-status").Text, "UDP") {
		t.Fatal("disconnected device retained the old action or route")
	}
	app.Press("id:device-details")
	app.WaitFor("id:dialog-connection-status", time.Second)
	app.Press("id:dialog-connection-status-close")
	app.Press("id:device-fixture-host")
	app.Press("id:forget-device")
	app.WaitFor("id:confirm-forget", time.Second)
	app.Press("id:dialog-cancel")
	if len(s.devices) != 1 {
		t.Fatal("cancelling removal forgot the device")
	}
}
