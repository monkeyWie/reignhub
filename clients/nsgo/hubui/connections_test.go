package hubui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

type connectionFixture struct {
	*demoAPI
	broken, offline, blockWatch, autoReconnect              atomic.Bool
	watchStarts, watchCancels, snapshots, reconnects, sends atomic.Int32
	version                                                 atomic.Int32
	firstWatchFailure                                       <-chan struct{}
}

func (*connectionFixture) CanHost() bool { return false }
func (f *connectionFixture) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.reconnect" {
		f.reconnects.Add(1)
		f.offline.Store(false)
	}
	if method == "client.call" {
		switch str(r, "method") {
		case "send", "steer":
			f.sends.Add(1)
		case "watch":
			if flag(r, "probe") && !f.broken.Load() && !f.offline.Load() {
				epoch := choose(f.version.Load() > 0, "resumed", "initial")
				return Object{"epoch": epoch, "cursor": 0, "reset": str(r, "epoch") != epoch}, nil
			}
			if f.watchStarts.Add(1) == 1 && f.firstWatchFailure != nil {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-f.firstWatchFailure:
				}
			}
			if f.blockWatch.Load() {
				<-ctx.Done()
				f.watchCancels.Add(1)
				return nil, ctx.Err()
			}
		case "transcript":
			f.snapshots.Add(1)
		}
		if f.broken.Load() {
			return nil, context.DeadlineExceeded
		}
		if f.offline.Load() {
			return nil, errors.New("设备离线")
		}
		if str(r, "method") == "transcript" && f.version.Load() > 0 {
			return Object{"session": Object{"id": "demo-chat", "providerId": "codex-app"}, "turns": []Object{{"id": "resumed", "status": "completed", "items": []Object{{"id": "latest", "role": "assistant", "text": "后台期间的最新消息"}}}}}, nil
		}
	}
	v, err := f.demoAPI.Call(ctx, method, p, target, r)
	if method == "client.list" || method == "client.reconnect" {
		for _, d := range objects(v) {
			d["connected"] = !f.offline.Load()
			d["autoReconnect"] = f.autoReconnect.Load()
		}
	}
	return v, err
}

func TestAutomaticConnectionErrorsUseStatusInsteadOfOverlay(t *testing.T) {
	failed := make(chan struct{})
	f := &connectionFixture{demoAPI: newDemoAPI(), firstWatchFailure: failed}
	// Keep automatic retry disabled so the failure remains inspectable.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, f, true) }, nsgotest.Size(390, 844))
	openDemoChat(t, app)
	app.Type("id:chat-input", "保留草稿")
	f.broken.Store(true)
	close(failed)
	app.Find("role:button 连接正常")
	if app.Exists("id:error-banner") {
		t.Fatal("automatic read failure showed an operation error")
	}
	app.Press("id:connection-status-demo-device")
	app.WaitFor("id:connection-error-raw", time.Second)
	app.Find("role:button 连接正常")
	if raw := app.Find("id:connection-error-raw").Text; strings.Count(raw, "context deadline exceeded") != 1 {
		t.Fatalf("duplicate details: %q", raw)
	}
	app.Screenshot("phone-connection-status")
	f.broken.Store(false)
	app.Press("id:connection-retry")
	app.WaitFor("text:传输通道已连接；同步状态以实际响应为准。", 4*time.Second)
	if app.Exists("id:connection-error-raw") || app.Exists("id:error-banner") {
		t.Fatal("recovered connection retained an error")
	}
	app.Press("id:dialog-connection-status-close")
	if app.Find("id:chat-input").Text != "保留草稿" || f.sends.Load() != 0 {
		t.Fatalf("recovery changed or sent the draft: text=%q sends=%d", app.Find("id:chat-input").Text, f.sends.Load())
	}
}

func TestMobileBackgroundRetainsWatchAndForegroundRecoversDisconnect(t *testing.T) {
	if nsgotest.Target() != "" {
		t.Skip("native test host cannot pin lifecycle; lifecycle injection runs headlessly")
	}
	f := &connectionFixture{demoAPI: newDemoAPI()}
	f.blockWatch.Store(true)
	f.autoReconnect.Store(true)
	env := core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, f, true) }, nsgotest.Size(390, 844), nsgotest.Environment(env))
	openDemoChat(t, app)
	app.WaitUntil(time.Second, func() bool { return f.watchStarts.Load() > 0 })
	app.Type("id:chat-input", "后台前的草稿")
	input := app.Find("id:chat-input").ID
	env.Lifecycle = core.LifecycleBackground
	app.SetEnvironment(env)
	if f.watchCancels.Load() != 0 {
		t.Fatal("background visibility cancelled a healthy watch")
	}
	before := f.snapshots.Load()
	f.version.Store(1)
	f.offline.Store(true)
	env.Lifecycle = core.LifecycleActive
	app.SetEnvironment(env)
	app.WaitFor("id:message-resumed|latest", 4*time.Second)
	app.WaitUntil(time.Second, func() bool { return f.snapshots.Load() > before && f.reconnects.Load() == 1 })
	if app.Exists("id:error-banner") || app.Find("id:chat-input").Text != "后台前的草稿" || app.Find("id:chat-input").ID != input || f.sends.Load() != 0 {
		t.Fatal("resume showed a popup, rebuilt the editor or replayed a command")
	}
	app.Screenshot("phone-resumed-conversation")
}

func TestBackgroundDisconnectRecoversWithoutForeground(t *testing.T) {
	f := &connectionFixture{demoAPI: newDemoAPI()}
	f.blockWatch.Store(true)
	f.autoReconnect.Store(true)
	app, s := conversationFixture(t, f, 390, Object{}, true)
	app.Type("id:chat-input", "保留后台恢复草稿")
	input := app.Find("id:chat-input").ID
	app.Post(func() { s.cx.Post(s.startSessionWatch) })
	app.WaitUntil(time.Second, func() bool { return f.watchStarts.Load() > 0 })
	app.Post(func() {
		s.cx.Post(func() {
			s.connection("demo-device").established = true
			s.setBackground(true)
			f.offline.Store(true)
			f.version.Store(1)
			s.poll()
		})
	})
	app.WaitFor("id:message-resumed|latest", 4*time.Second)
	if !s.backgrounded || f.reconnects.Load() != 1 || f.sends.Load() != 0 || app.Find("id:chat-input").ID != input || app.Find("id:chat-input").Text != "保留后台恢复草稿" {
		t.Fatal("background recovery waited for foreground, replayed commands or changed the draft")
	}
}

func TestOperationErrorDetailsDoNotRepeatSummary(t *testing.T) {
	for _, raw := range []string{"文件类型不支持", "请求超时或取消；操作结果可能尚未返回，请刷新后再决定是否重试: context deadline exceeded"} {
		t.Run(raw, func(t *testing.T) {
			app := nsgotest.Start(t, func(cx *core.Scope) core.Node { s := newState(cx, newDemoAPI()); s.error = raw; return s.errorCard(cx) }, nsgotest.Size(390, 400))
			if raw == "文件类型不支持" {
				if app.Exists("id:session-error-details") {
					t.Fatal("identical error has redundant details")
				}
			} else {
				app.Find("text:请求未及时返回，请确认操作结果后再决定是否重试。")
				app.Press("id:session-error-details")
				if app.Find("id:session-error-raw").Text != raw {
					t.Fatal("raw details lost")
				}
			}
		})
	}
}

func TestNeverConnectedDeviceDoesNotStartAutomaticRetries(t *testing.T) {
	f := &connectionFixture{demoAPI: newDemoAPI()}
	app, s := conversationFixture(t, f, 390, Object{}, false)
	checked := make(chan bool, 1)
	app.Post(func() {
		s.cx.Post(func() {
			s.devices = []Object{{"id": "never-connected", "connected": false, "autoReconnect": true}}
			s.connection("never-connected").attempt = &connectAttempt{complete: true}
			s.autoReconnect()
			checked <- s.connection("never-connected").reconnecting
		})
	})
	app.WaitUntil(time.Second, func() bool {
		select {
		case retrying := <-checked:
			if retrying || f.reconnects.Load() != 0 {
				t.Fatal("first failed connection retried automatically")
			}
			return true
		default:
			return false
		}
	})
}

func TestReadTimeoutsDoNotReconnectAHealthyDevice(t *testing.T) {
	f := &connectionFixture{demoAPI: newDemoAPI()}
	app, s := conversationFixture(t, f, 390, Object{}, true)
	checked := make(chan bool, 1)
	app.Post(func() {
		s.cx.Post(func() {
			s.devices[0]["autoReconnect"] = true
			s.connection("demo-device").established = true
			for _, source := range []string{"models", "catalogue", "transcript", "watch"} {
				s.setConnectionIssue("demo-device", source, context.DeadlineExceeded)
				s.autoReconnect()
			}
			checked <- s.connection("demo-device").reconnecting
		})
	})
	app.WaitUntil(time.Second, func() bool {
		select {
		case retrying := <-checked:
			if retrying || f.reconnects.Load() != 0 {
				t.Fatal("read timeout rebuilt a healthy connection")
			}
			return true
		default:
			return false
		}
	})
	app.Post(func() { s.cx.Post(func() { s.devices[0]["connected"] = false; s.autoReconnect() }) })
	app.WaitUntil(time.Second, func() bool { return f.reconnects.Load() == 1 })
}

type slowCatalogueFixture struct {
	*connectionFixture
	started chan bool
}

func (f *slowCatalogueFixture) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.sync" {
		_, deadline := ctx.Deadline()
		select {
		case f.started <- deadline:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.connectionFixture.Call(ctx, method, p, target, r)
}
func TestSlowCatalogueHasNoDeadlineAndDoesNotBlockPresence(t *testing.T) {
	f := &slowCatalogueFixture{connectionFixture: &connectionFixture{demoAPI: newDemoAPI()}, started: make(chan bool, 1)}
	f.autoReconnect.Store(true)
	app, s := conversationFixture(t, f, 390, Object{}, true)
	app.Post(func() { s.cx.Post(func() { s.ready = true; s.connection("demo-device").established = true; s.poll() }) })
	app.WaitUntil(time.Second, func() bool {
		select {
		case deadline := <-f.started:
			if deadline {
				t.Fatal("catalogue has fixed deadline")
			}
			return true
		default:
			return false
		}
	})
	f.offline.Store(true)
	// The catalogue remains blocked. A second local presence read must still
	// detect the lost transport and start recovery.
	app.Post(func() { s.cx.Post(s.poll) })
	app.WaitUntil(time.Second, func() bool { return f.reconnects.Load() == 1 })
	app.Post(func() { s.cx.Post(func() { s.setBackground(true) }) })
}
func TestReconnectStatusShowsAnimatedIndicator(t *testing.T) {
	f := &connectionFixture{demoAPI: newDemoAPI()}
	app, s := conversationFixture(t, f, 390, Object{}, true)
	app.Post(func() {
		s.cx.Post(func() {
			s.connection("demo-device").established = true
			s.connection("demo-device").reconnecting = true
			s.connection("demo-device").attempt = &connectAttempt{active: true}
			s.changed()
		})
	})
	app.WaitFor("id:connection-retrying", time.Second)
	app.Find("role:button 连接中")
	app.Screenshot("phone-connection-retrying")
}

type stalePresenceAPI struct {
	*connectionFixture
	started, release chan struct{}
}

func (f *stalePresenceAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.list" {
		close(f.started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.release:
		}
		return []Object{{"id": "demo-device", "connected": false}}, nil
	}
	return f.connectionFixture.Call(ctx, method, p, target, r)
}

func TestVerifiedConnectionImmediatelyUpdatesIconAndIgnoresStalePresence(t *testing.T) {
	f := &stalePresenceAPI{connectionFixture: &connectionFixture{demoAPI: newDemoAPI()}, started: make(chan struct{}), release: make(chan struct{})}
	app, s := conversationFixture(t, f, 390, Object{}, false)
	app.Post(func() {
		s.cx.Post(func() {
			s.setConnectionIssue("demo-device", "watch", errors.New("relay 已断开"))
			s.setConnectionIssue("", "catalogue", errors.New("local provider unavailable"))
			s.pollPresence()
		})
	})
	app.WaitUntil(time.Second, func() bool {
		select {
		case <-f.started:
			return true
		default:
			return false
		}
	})
	app.Post(func() {
		s.cx.Post(func() {
			s.acceptConnection("demo-device", []Object{{"id": "demo-device", "connected": true, "path": "P2P · WebRTC"}})
			s.changed()
		})
	})
	app.WaitFor("role:button 连接正常", time.Second)
	close(f.release)
	app.WaitUntil(time.Second, func() bool { return !s.presencePolling })
	app.Find("role:button 连接正常")
}

func TestKeepAliveForegroundStillChecksConnectionAndResyncs(t *testing.T) {
	f := &connectionFixture{demoAPI: newDemoAPI()}
	f.autoReconnect.Store(true)
	app, s := conversationFixture(t, f, 390, Object{}, true)
	app.Type("id:chat-input", "前后台切换保留草稿")
	app.Post(func() {
		s.cx.Post(func() {
			s.keepAlive = true
			s.connection("demo-device").established = true
			s.devices[0]["autoReconnect"] = true
			s.setBackground(true)
			// Simulate an OS-aborted transport despite keepAlive being enabled.
			f.offline.Store(true)
			s.setBackground(false)
		})
	})
	app.WaitUntil(time.Second, func() bool { return f.reconnects.Load() == 1 })
	app.WaitFor("role:button 连接正常", time.Second)
	if app.Find("id:chat-input").Text != "前后台切换保留草稿" || f.sends.Load() != 0 {
		t.Fatal("foreground recovery changed/replayed draft")
	}
}
