package hubui

import (
	"context"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"testing"
	"time"
)

type mobileDemo struct{ *demoAPI }

func (*mobileDemo) CanHost() bool { return false }
func (d *mobileDemo) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.presence" {
		return []Object{{"id": "third", "online": false, "checkedAt": time.Now().Unix()}}, nil
	}
	if method == "client.list" {
		return []Object{
			{"id": "demo-device", "name": "MacBook Pro", "connectionId": "123456", "path": "P2P · WebRTC", "connected": true},
			{"id": "second", "name": "开发工作站", "connectionId": "654321", "path": "Relay · WebSocket", "connected": true},
			{"id": "third", "name": "离线电脑", "connectionId": "111222", "connected": false},
		}, nil
	}
	return d.demoAPI.Call(ctx, method, p, target, r)
}
func TestMobileMultiDeviceNavigation(t *testing.T) {
	testMobileDeviceAndScanner(t, core.OS("android"), false)
}
func TestMobileScannerEvents(t *testing.T) { testMobileDeviceAndScanner(t, core.OS("android"), true) }
func testMobileDeviceAndScanner(t *testing.T, os core.OS, scan bool) {
	api := &mobileDemo{newDemoAPI()}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, api, true) }, nsgotest.Size(390, 844), nsgotest.Environment(core.Environment{OS: os, Idiom: core.IdiomPhone, Locale: "zh-CN"}))
	showSessionDirectory(t, app)
	app.WaitFor("id:device-second", time.Second)
	app.Find("id:device-demo-device")
	app.Find("id:device-second")
	if app.Exists("text:Relay · WebSocket") || app.Exists("text:P2P · WebRTC") {
		t.Fatal("device list exposed connection paths instead of presence")
	}
	app.WaitFor(`text:"离线"`, time.Second)
	if !scan {
		app.Screenshot("mobile-devices")
	}
	app.WaitFor(sessionSelector("second", "codex-app", "demo-chat"), time.Second)
	app.Press(sessionSelector("second", "codex-app", "demo-chat"))
	app.WaitFor("id:chat-input", time.Second)
	showSessionDirectory(t, app)
	pressWorkspaceBack(t, app)
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:mobile-sidebar") })
	showSessionDirectory(t, app)
	app.WaitFor(sessionSelector("demo-device", "codex-app", "demo-chat"), time.Second)
	if !scan {
		return
	}
	app.Press("id:scan-qr")
	app.Find("id:live-qr-camera")
	app.Emit("id:live-qr-camera", "code", "not-a-ras-qr")
	app.Find("text:不是有效的 ReignHub 设备二维码，请扫描电脑上的设备二维码。")
	code := pairingCode(Object{"service": DefaultService, "connectionId": "654321", "public": "fixture-public"})
	app.Emit("id:live-qr-camera", "code", code)
	app.Find("id:dialog-connect")
	if app.Find(pairingInput).Text != "654321" {
		t.Fatal("scan did not prefill target ID")
	}
	if app.Exists("id:live-qr-camera") {
		t.Fatal("camera must close after a valid scan")
	}
	app.Press("id:dialog-cancel")
	app.Press("id:scan-qr")
	pressWorkspaceBack(t, app)
	if app.Exists("id:dialog-scan") {
		t.Fatal("system back did not dismiss camera")
	}
	app.Find("id:session-sidebar")
}
func TestBrowserDeviceNames(t *testing.T) {
	for _, tc := range []struct{ ua, want string }{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X) Chrome/142.0.0 Safari/537.36", "Chrome 142"},
		{"Mozilla/5.0 (Windows NT 10.0) Chrome/142 Safari/537.36 Edg/142", "Microsoft Edge 142"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS) Version/18.0 Mobile Safari/604.1", "Safari 18"},
		{"Mozilla/5.0 (Linux; Android 15) Chrome/142.0.0 Safari/537.36", "Chrome 142"},
		{"Mozilla/5.0 (iPhone) CriOS/142.0 Mobile Safari/604.1", "Chrome 142"},
		{"Mozilla/5.0 (Linux) Firefox/142.0", "Firefox 142"},
		{"Mozilla/5.0 Chrome/157 Safari/537.36 Edg/157.0.123", "Microsoft Edge 157"},
		{"Mozilla/5.0 Chrome/142 SamsungBrowser/29.0", "Samsung Internet 29"},
		{"Mozilla/5.0 Chrome/142 OPR/123.0", "Opera 123"},
		{"Mozilla/5.0 (iPhone) FxiOS/145.1 Mobile Safari/604.1", "Firefox 145"},
		{"Mozilla/5.0 Safari/604.1", "Safari"},
		{"unknown", "浏览器"},
	} {
		if got := browserDeviceName(tc.ua); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
	if got := deviceName(Object{"name": "RAS", "connectionId": "123456"}); got != "设备 123456" {
		t.Fatal(got)
	}
}
