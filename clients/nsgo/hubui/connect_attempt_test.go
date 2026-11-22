package hubui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

const pairingInput = "id:connection-code >> label:/.+/"

type pairingFixture struct {
	*demoAPI
	steps           chan connectivity.Progress
	finish          chan error
	calls, canceled atomic.Int32
}

func (*pairingFixture) CanHost() bool { return false }
func (f *pairingFixture) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method != "client.connect" {
		return f.demoAPI.Call(ctx, method, p, target, r)
	}
	f.calls.Add(1)
	connectivity.Report(ctx, "approval", "请在被控电脑的 ReignHub 中允许此次连接", 125*time.Second)
	for {
		select {
		case <-ctx.Done():
			f.canceled.Add(1)
			return nil, ctx.Err()
		case step := <-f.steps:
			connectivity.Report(ctx, step.Stage, step.Message, step.Timeout)
		case err := <-f.finish:
			if err != nil {
				return nil, err
			}
			return []Object{{"id": "demo-device", "service": str(p, "service"), "connectionId": str(p, "code"), "name": "MacBook Pro", "connected": true, "path": "P2P · WebRTC"}}, nil
		}
	}
}
func startPairing(t *testing.T) (*nsgotest.App, *pairingFixture) {
	t.Helper()
	f := &pairingFixture{demoAPI: newDemoAPI(), steps: make(chan connectivity.Progress, 2), finish: make(chan error, 1)}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, f, true) }, nsgotest.Size(390, 844), nsgotest.Environment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone}))
	showSessionDirectory(t, app)
	app.WaitFor("id:connect-device", time.Second)
	app.Press("id:connect-device")
	app.Type(pairingInput, "123456")
	app.Press("id:connect-submit")
	app.WaitFor("id:connection-loading", time.Second)
	app.WaitFor("text:请在被控电脑的 ReignHub 中允许此次连接", time.Second)
	return app, f
}
func TestPairingProgressStaysVisibleAndCanBeCanceled(t *testing.T) {
	app, f := startPairing(t)
	spinner := app.Find("id:connection-loading").ID
	f.steps <- connectivity.Progress{Stage: "direct", Message: "正在尝试 P2P 直连", Timeout: 25 * time.Second}
	app.WaitFor("text:正在尝试 P2P 直连", time.Second)
	// Elapsed updates must not remount the native dialog or restart its spinner.
	app.WaitUntil(3*time.Second, func() bool { return !strings.HasPrefix(app.Find("id:connect-elapsed").Text, "已用 0 秒") })
	if app.Find("id:connection-loading").ID != spinner {
		t.Fatal("progress remounted the dialog")
	}
	app.Screenshot("phone-pairing-progress")
	app.Press("id:connect-cancel")
	app.WaitUntil(time.Second, func() bool { return f.canceled.Load() == 1 })
	if app.Exists("id:dialog-connecting") || app.Exists("id:error-banner") || f.calls.Load() != 1 {
		t.Fatal("cancel retained progress, showed an error, or duplicated the request")
	}
}
func TestPairingFailureSupportsRetryWithoutLosingDevice(t *testing.T) {
	app, f := startPairing(t)
	f.finish <- errors.New("等待主机授权超时")
	app.WaitFor("text:等待授权未完成", time.Second)
	if app.Exists("id:connection-loading") || app.Exists("id:error-banner") {
		t.Fatal("failure is still loading or duplicated as a toast")
	}
	app.Screenshot("phone-pairing-failed")
	app.Press("id:connect-retry")
	app.WaitUntil(time.Second, func() bool { return f.calls.Load() == 2 })
	f.finish <- nil
	app.WaitFor("id:session-sidebar", 3*time.Second)
	if app.Exists("id:dialog-connecting") {
		t.Fatal("success did not dismiss progress")
	}
}
func TestPairingFailureEditKeepsServiceAndDeviceID(t *testing.T) {
	app, f := startPairing(t)
	f.finish <- errors.New("被控设备离线")
	app.WaitFor("text:被控电脑不在线", time.Second)
	app.Press("id:connect-edit")
	app.WaitFor(pairingInput, time.Second)
	if app.Find(pairingInput).Text != "123456" || app.Find("id:connection-service").Text != DefaultService {
		t.Fatal("editing discarded the target")
	}
}

type retryProgressFixture struct {
	*demoAPI
	steps  chan connectivity.Progress
	finish chan error
	calls  atomic.Int32
}

func (*retryProgressFixture) CanHost() bool { return false }
func (f *retryProgressFixture) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method != "client.reconnect" {
		return f.demoAPI.Call(ctx, method, p, target, r)
	}
	f.calls.Add(1)
	connectivity.Report(ctx, "signal", "正在建立信令连接", 12*time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case step := <-f.steps:
			if observe := connectivity.Observer(ctx); observe != nil {
				observe(step)
			}
		case err := <-f.finish:
			if err != nil {
				return nil, err
			}
			return f.demoAPI.Call(ctx, method, p, target, r)
		}
	}
}
func TestRetryKeepsStatusPanelAndShowsActualTransportSteps(t *testing.T) {
	f := &retryProgressFixture{demoAPI: newDemoAPI(), steps: make(chan connectivity.Progress, 8), finish: make(chan error, 1)}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, f, true) }, nsgotest.Size(390, 844), nsgotest.Environment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone}))
	openDemoChat(t, app)
	app.Type("id:chat-input", "重试时保留草稿")
	app.Press("id:connection-status-demo-device")
	app.Press("id:connection-retry")
	app.WaitFor("text:正在建立信令连接", time.Second)
	if app.Exists("id:dialog-connecting") || !app.Exists("id:dialog-connection-status") {
		t.Fatal("retry opened a separate progress panel")
	}
	spinner := app.Find("id:connection-loading").ID
	f.steps <- connectivity.Progress{ID: "stun:cf", Stage: "udp", Message: "公共 STUN 节点", Detail: "stun:stun.cloudflare.com:3478 · 并行探测", State: "info"}
	f.steps <- connectivity.Progress{ID: "stun:self", Stage: "udp", Message: "自建 STUN 节点", Detail: "stun:probe.example.com:26331 · 并行探测", State: "info"}
	f.steps <- connectivity.Progress{ID: "tcp", Stage: "tcp", Message: "探测 TCP 公网映射", Detail: "probe.example.com:26331", State: "running"}
	f.steps <- connectivity.Progress{ID: "path", Stage: "relay", Message: "正在使用加密中继", Detail: "wss://reignhub.app/p2p/relay", State: "success"}
	app.WaitFor("text:wss://reignhub.app/p2p/relay", time.Second)
	app.Find("text:stun:stun.cloudflare.com:3478 · 并行探测")
	app.Find("text:stun:probe.example.com:26331 · 并行探测")
	app.Press("id:copy-connection-log")
	app.WaitFor("role:button 已复制", time.Second)
	if !strings.Contains(app.Clipboard, "stun:probe.example.com:26331") || !strings.Contains(app.Clipboard, "ReignHub connection diagnostics") {
		t.Fatalf("incomplete connection report: %s", app.Clipboard)
	}
	if app.Find("id:connection-loading").ID != spinner {
		t.Fatal("progress remounted the status panel")
	}
	app.Screenshot("phone-retry-connection-details")
	f.finish <- errors.New("网络验证失败")
	app.WaitFor("id:connection-retry", time.Second)
	app.Press("id:connection-retry")
	app.WaitUntil(time.Second, func() bool { return f.calls.Load() == 2 })
	app.WaitFor("text:上一轮连接失败", time.Second)
	app.WaitFor("text:网络验证失败", time.Second)
	if app.Exists("id:connection-error-raw") {
		t.Fatal("new attempt retained the previous connection failure")
	}
	f.steps <- connectivity.Progress{ID: "path", Stage: "direct", Message: "已切换为 TCP 直连", Detail: "192.168.1.2:5000 → 192.168.1.3:5000", State: "success"}
	app.WaitFor("text:已切换为 TCP 直连", time.Second)
	f.finish <- nil
	app.WaitFor("text:传输通道已连接；同步状态以实际响应为准。", time.Second)
	if app.Exists("id:connection-error-raw") || app.Exists("id:error-banner") {
		t.Fatal("successful retry retained stale failure")
	}
	app.Press("id:dialog-connection-status-close")
	if app.Find("id:chat-input").Text != "重试时保留草稿" {
		t.Fatal("retry discarded draft")
	}
}
func TestConnectionTimelineIsBoundedAndUpdatesNodeResults(t *testing.T) {
	a := &connectAttempt{started: time.Now()}
	a.record(connectivity.Progress{ID: "stun:node", Message: "探测节点", State: "running"})
	at := a.steps[0].at
	a.record(connectivity.Progress{ID: "stun:node", Message: "已获得公网映射", State: "success"})
	if len(a.steps) != 1 || a.steps[0].at != at || a.steps[0].State != "success" {
		t.Fatal("node result was duplicated")
	}
	for i := 0; i < 200; i++ {
		a.record(connectivity.Progress{ID: fmt.Sprint(i), Message: "候选地址", State: "info"})
	}
	if len(a.steps) != 80 {
		t.Fatal("unbounded connection history")
	}
}

func TestCanceledRetryStopsTimerAndAllowsAnotherRetry(t *testing.T) {
	f := &retryProgressFixture{demoAPI: newDemoAPI(), steps: make(chan connectivity.Progress, 8), finish: make(chan error, 1)}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, f, true) }, nsgotest.Size(390, 844))
	openDemoChat(t, app)
	app.Press("id:connection-status-demo-device")
	app.Press("id:connection-retry")
	app.WaitFor("text:正在建立信令连接", time.Second)
	app.Press("id:connect-cancel")
	app.WaitFor("role:button 未连接", time.Second)
	app.Press("id:connection-status-demo-device")
	elapsed := app.Find("id:connect-elapsed").Text
	app.WaitUntil(2*time.Second, func() bool { return f.calls.Load() == 1 })
	if app.Exists("id:connection-loading") || app.Exists("id:connection-retrying") {
		t.Fatal("canceled task still claims to be connecting")
	}
	app.Press("id:copy-connection-log")
	if !strings.Contains(app.Clipboard, "用户取消连接") || !strings.Contains(app.Clipboard, "active=false complete=false") {
		t.Fatal("cancel was not recorded as a terminal attempt")
	}
	if app.Find("id:connect-elapsed").Text != elapsed {
		t.Fatal("terminal attempt timer kept running")
	}
	app.Press("id:connection-retry")
	app.WaitUntil(time.Second, func() bool { return f.calls.Load() == 2 })
	app.WaitFor("id:connection-loading", time.Second)
	f.finish <- nil
	app.WaitFor("text:传输通道已连接；同步状态以实际响应为准。", time.Second)
}

func TestInactiveAttemptElapsedDoesNotAccumulateWallTime(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	for _, a := range []*connectAttempt{
		{started: start, finished: start.Add(3 * time.Second)},
		{started: start, events: []connectionStep{{at: start.Add(3 * time.Second)}}},
	} {
		if a.elapsed() != 3*time.Second {
			t.Fatalf("inactive attempt accumulated wall time: %s", a.elapsed())
		}
	}
}

func TestManualRetryReplacesStuckAttemptAndIgnoresOldCompletion(t *testing.T) {
	f := &retryProgressFixture{demoAPI: newDemoAPI(), steps: make(chan connectivity.Progress, 8), finish: make(chan error, 1)}
	app, s := conversationFixture(t, f, 390, Object{}, false)
	old := make(chan *connectAttempt, 1)
	app.Post(func() {
		s.cx.Post(func() {
			s.reconnectDevice("demo-device")
			old <- s.connection("demo-device").attempt
		})
	})
	app.WaitUntil(time.Second, func() bool { return f.calls.Load() == 1 })
	previous := <-old
	app.Post(func() { s.cx.Post(func() { s.reconnectDevice("demo-device") }) })
	app.WaitUntil(time.Second, func() bool { return f.calls.Load() == 2 })
	check := make(chan bool, 1)
	app.Post(func() {
		s.cx.Post(func() {
			h := s.connection("demo-device")
			check <- !previous.active && !previous.finished.IsZero() && h.attempt != previous && h.attempt.active && h.reconnecting
		})
	})
	app.WaitUntil(time.Second, func() bool {
		select {
		case ok := <-check:
			if !ok {
				t.Fatal("replacement corrupted by canceled attempt")
			}
			return true
		default:
			return false
		}
	})
	f.finish <- nil
	app.WaitFor("role:button 连接正常", time.Second)
	// Reproduce the exact stale flags from the diagnostic report. Retry must
	// start a fresh task rather than silently returning on reconnecting=true.
	app.Post(func() {
		s.cx.Post(func() {
			s.connection("demo-device").reconnecting = true
			s.connection("demo-device").attempt = &connectAttempt{started: time.Now().Add(-time.Hour)}
			s.reconnectDevice("demo-device")
		})
	})
	app.WaitUntil(time.Second, func() bool { return f.calls.Load() == 3 })
	f.finish <- nil
	app.WaitFor("role:button 连接正常", time.Second)
}
