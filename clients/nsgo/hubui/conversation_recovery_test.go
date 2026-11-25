package hubui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func conversationFixture(t *testing.T, api API, width core.Points, transcript Object, connected bool) (*nsgotest.App, *State) {
	t.Helper()
	var state *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		state = newState(cx, api)
		state.width.Set(width)
		state.devices = []Object{{"id": "demo-device", "connected": connected}}
		state.target = "demo-device"
		state.selected = Object{"id": "demo-chat", "providerId": "codex-app", "title": "会话"}
		state.model = "test-model"
		if transcript != nil {
			state.setTranscript(transcript)
		}
		view := state.conversation(cx, true).(core.View)
		view.StyleFunc = func() core.Style {
			return core.Style{Background: palette(cx).background, Color: palette(cx).foreground}
		}
		return view
	}, nsgotest.Size(width, 844), nsgotest.Environment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone}))
	return app, state
}

func TestMobileMultilineMessage320(t *testing.T) { testMobileMultilineMessage(t, 320) }
func TestMobileMultilineMessage390(t *testing.T) { testMobileMultilineMessage(t, 390) }
func TestMobileMultilineMessage720(t *testing.T) { testMobileMultilineMessage(t, 720) }

func TestReadingPositionWhileReplyGrows(t *testing.T) {
	f := &delayedSendAPI{demoAPI: newDemoAPI(), release: make(chan struct{})}
	reply := strings.Repeat("A line of the reply.\n\n", 50)
	transcript := func() Object {
		return outgoingTranscript(Object{"id": "answer", "role": "assistant", "text": reply})
	}
	app, s := conversationFixture(t, f, 390, transcript(), true)
	const list = "id:chat-message-list"
	assertBottom := func() {
		t.Helper()
		app.WaitUntil(time.Second, func() bool {
			v := app.Find(list)
			return v.Content.Height > v.Frame.Size.Height && v.Content.Height-v.Frame.Size.Height-v.Offset.Y <= 1 && !app.Exists("id:jump-to-latest")
		})
	}
	grow := func() {
		t.Helper()
		before := app.Find(list).Content.Height
		app.Post(func() {
			reply += strings.Repeat("More streamed text.\n\n", 4)
			s.setTranscript(transcript())
		})
		app.WaitUntil(time.Second, func() bool { return app.Find(list).Content.Height > before })
		app.Settle()
	}
	assertBottom()
	app.Scroll(list, -12)
	location := func() core.Points { return globalBounds(app.Find("id:message-turn|answer")).Origin.Y }
	before := location()
	grow()
	if after := location(); after != before {
		t.Fatalf("streaming moved the paragraph being read by %g points", after-before)
	}
	app.WaitFor("id:jump-to-latest", time.Second)

	app.Post(func() {
		page := transcript()
		older := Object{"id": "older", "status": "completed", "items": []Object{
			{"id": "user", "role": "user", "text": "Earlier instruction"},
			{"id": "reply", "role": "assistant", "text": strings.Repeat("Earlier reply.\n\n", 30)},
		}}
		page["turns"] = append([]Object{older}, objects(page["turns"])...)
		s.setTranscript(page)
	})
	app.Settle()
	if after := location(); after != before {
		t.Fatalf("loading earlier history moved the paragraph being read by %g points", after-before)
	}
	app.Find("id:jump-to-latest")

	app.Scroll(list, app.Find(list).Content.Height)
	// Native trackpad steps round to whole points. Finish the gesture if it
	// stopped just short of the bottom.
	if v := app.Find(list); v.Content.Height-v.Frame.Size.Height-v.Offset.Y > 1 {
		app.Scroll(list, v.Content.Height)
	}
	assertBottom()
	grow()
	assertBottom()
	app.Scroll(list, -80)
	before = location()
	app.Type("id:chat-input", "My follow-up")
	if after := location(); after != before {
		t.Fatalf("typing moved the reading position by %g points", after-before)
	}
	app.Press("id:send-message")
	app.WaitFor("id:send-loading", time.Second)
	assertBottom()
	grow()
	assertBottom()
}

func TestSendingLongMessageAndJumpToLatestReachTheBottom(t *testing.T) {
	f := &delayedSendAPI{demoAPI: newDemoAPI(), release: make(chan struct{})}
	app, s := conversationFixture(t, f, 390, Object{}, true)
	// Seed the multiline draft directly: desktop test keyboards treat Return
	// as submit, unlike the phone keyboard used by this scenario.
	app.Post(func() {
		s.cx.Post(func() { s.draft.Set(strings.Repeat("这是一条超过可视区域的长消息。\n", 35)) })
	})
	app.Settle()
	app.Press("id:send-message")
	app.WaitFor("id:send-loading", time.Second)
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:jump-to-latest") })
	// Simulate the reduced viewport while a phone keyboard is open. The
	// bottom must remain visible while composing the next multiline draft.
	if nsgotest.Target() == "" {
		app.Resize(390, 520)
	}
	app.Post(func() { s.cx.Post(func() { s.draft.Set("下一条草稿\n第二行\n第三行") }) })
	app.Settle()
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:jump-to-latest") })
	app.Post(func() { s.cx.Post(func() { s.timeline.list.ScrollTo(core.Point{}, false) }) })
	app.Settle()
	app.WaitFor("id:jump-to-latest", time.Second)
	jump := app.Find("id:jump-to-latest")
	timeline := globalBounds(app.Find("id:timeline"))
	bounds := globalBounds(jump)
	if delta := bounds.Origin.X + bounds.Size.Width/2 - timeline.Origin.X - timeline.Size.Width/2; delta < -1 || delta > 1 {
		t.Fatal("jump-to-latest is not centered above the composer")
	}
	if bounds.Size.Width != bounds.Size.Height || jump.Props.Label == "" {
		t.Fatal("jump-to-latest lost its circular shape or accessible name")
	}
	app.Screenshot("jump-to-latest-icon")
	app.Press("id:jump-to-latest")
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:jump-to-latest") })
	app.Screenshot("long-message-follows-bottom")
}

func TestKeyboardRoundTripKeepsLatestMessageVisible(t *testing.T) {
	transcript := Object{"turns": []Object{{"id": "keyboard", "status": "completed", "items": []Object{{"id": "answer", "role": "assistant", "text": strings.Repeat("回复中的一段文字。\n\n", 80) + "最后一行"}}}}}
	app, s := conversationFixture(t, newDemoAPI(), 390, transcript, true)
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:jump-to-latest") })
	app.Focus("id:chat-input")
	for _, height := range []core.Points{120, 300, 180, 0, 300, 0} {
		app.SetEnvironment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone, KeyboardHeight: height})
		app.Post(func() { s.cx.Post(func() { s.draft.Set("第一行\n第二行\n第三行\n第四行") }) })
		app.Settle()
		app.WaitUntil(time.Second, func() bool { return !app.Exists("id:jump-to-latest") })
		app.WaitUntil(time.Second, func() bool {
			list := app.Find("id:chat-message-list")
			return list.Content.Height-list.Frame.Size.Height-list.Offset.Y < 1
		})
	}
	app.Screenshot("keyboard-closed-at-latest")
	// Opening the keyboard while reading history must not pull us to latest.
	app.Scroll("id:chat-message-list", -200)
	app.WaitFor("id:jump-to-latest", time.Second)
	for _, height := range []core.Points{300, 0} {
		app.SetEnvironment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone, KeyboardHeight: height})
		app.WaitFor("id:jump-to-latest", time.Second)
	}
	app.Press("id:jump-to-latest")
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:jump-to-latest") })
}

func testMobileMultilineMessage(t *testing.T, width core.Points) {
	t.Helper()
	text := "发现的问题\n\n1. 网站页脚没有公示任何有效的客服联系方式（如联系邮箱、联系电话或在线客服），用户在其他页面也难以直接找到官方客服渠道。\n\n修改建议\n\n1. 请在网站全站页脚公示至少一种有效的联系方式，例如联系邮箱、联系电话或在线客服入口，方便用户随时联系。\n\n" + "https://example.com/" + strings.Repeat("long-path-", 30) + "\n\n```text\n" + strings.Repeat("long-code-", 30) + "\n```"
	transcript := Object{"turns": []Object{{"id": "layout", "status": "completed", "items": []Object{{"id": "user", "role": "user", "text": text}}}}}
	app, state := conversationFixture(t, newDemoAPI(), width, transcript, true)
	// Phone simulators use their device's real viewport rather than Size.
	viewport := globalBounds(app.Find("id:conversation"))
	for _, id := range []string{"message-layout|user", "message-body-layout|user", "composer-card"} {
		b := globalBounds(app.Find("id:" + id))
		if b.Origin.X < viewport.Origin.X || b.Origin.X+b.Size.Width > viewport.Origin.X+viewport.Size.Width+.5 {
			t.Fatalf("%s exceeds viewport %v: %v", id, viewport, b)
		}
	}
	app.Screenshot("mobile-multiline-message")
	// A short follow-up must shrink back from the preceding wrapped message.
	app.Post(func() {
		state.cx.Post(func() {
			state.setTranscript(Object{"turns": []Object{{"id": "layout", "status": "completed", "items": []Object{{"id": "user", "role": "user", "text": "收到"}}}}})
			state.changed()
		})
	})
	app.WaitFor("text:收到", time.Second)
	bubble := globalBounds(app.Find("id:message-body-layout|user"))
	message := globalBounds(app.Find("id:message-layout|user"))
	if bubble.Size.Width < 40 || bubble.Size.Width > 120 || bubble.Origin.X+bubble.Size.Width < message.Origin.X+message.Size.Width-.5 {
		t.Fatalf("short bubble must hug its text and remain right aligned: bubble=%v message=%v", bubble, message)
	}
	app.Screenshot("short-message-bubble")
}

type delayedSendAPI struct {
	*demoAPI
	release chan struct{}
	sends   atomic.Int32
	fail    atomic.Bool
}

func (f *delayedSendAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.call" && str(r, "method") == "send" {
		f.sends.Add(1)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.release:
		}
		if f.fail.Load() {
			return nil, errors.New("发送失败")
		}
		return nil, nil
	}
	return f.demoAPI.Call(ctx, method, p, target, r)
}

func TestOfflineEditingAndSendLoadingPreserveNewDraft(t *testing.T) {
	f := &delayedSendAPI{demoAPI: newDemoAPI(), release: make(chan struct{})}
	app, s := conversationFixture(t, f, 390, Object{}, false)
	app.Type("id:chat-input", "断线时也能写\n下一行")
	if app.Find("id:chat-input").Props.Disabled || !app.Find("id:send-message").Props.Disabled {
		t.Fatal("offline must disable sending only")
	}
	app.Post(func() { s.cx.Post(func() { s.send("send") }) })
	app.Settle()
	if f.sends.Load() != 0 {
		t.Fatal("offline sent a command")
	}
	app.Post(func() { s.cx.Post(func() { s.devices[0]["connected"] = true; s.changed() }) })
	app.WaitUntil(time.Second, func() bool { return !app.Find("id:send-message").Props.Disabled })
	app.Press("id:send-message")
	app.WaitFor("id:send-loading", time.Second)
	app.Find("text:断线时也能写")
	if app.Find("id:chat-input").Text != "" {
		t.Fatal("submitted message did not immediately move into the timeline")
	}
	if !app.Find("id:send-message").Props.Disabled || app.Find("id:chat-input").Props.Disabled {
		t.Fatal("pending send must stay visible, block duplicates and allow editing")
	}
	app.Post(func() { s.cx.Post(func() { s.send("send") }) })
	app.Screenshot("send-loading")
	app.Clear("id:chat-input")
	app.Type("id:chat-input", "下一条草稿")
	close(f.release)
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:send-loading") })
	if f.sends.Load() != 1 || app.Find("id:chat-input").Text != "下一条草稿" {
		t.Fatal("duplicate command or new draft was lost")
	}
	app.Screenshot("send-loading-draft-preserved")
}

func TestSendFailureStopsLoadingAndKeepsMessage(t *testing.T) {
	f := &delayedSendAPI{demoAPI: newDemoAPI(), release: make(chan struct{})}
	f.fail.Store(true)
	app, _ := conversationFixture(t, f, 390, Object{}, true)
	app.Type("id:chat-input", "这条消息不能丢")
	app.Press("id:send-message")
	app.WaitFor("id:send-loading", time.Second)
	close(f.release)
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:send-loading") })
	if app.Find("id:chat-input").Text != "" {
		t.Fatal("submitted draft was not moved to the timeline")
	}
	app.Find("text:这条消息不能丢")
	app.Find("id:error-banner")
	app.Press("id:dismiss-error")
	// Dismissing the operation error must retain the failed message status.
	app.Find("text:发送失败")
}

type replayAPI struct {
	*demoAPI
	offline, recovered, wrongCursor atomic.Bool
}

const initialLiveText = "已收到的回复🙂"

func (f *replayAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.reconnect" {
		f.offline.Store(false)
		f.recovered.Store(true)
	}
	if method == "client.call" && str(r, "method") == "watch" {
		if f.offline.Load() {
			return nil, errors.New("设备离线")
		}
		// Match the real event log: cancellation is not an acknowledgement.
		// A replacement watch must be able to replay the same cursor.
		if f.recovered.Load() && number(r, "after") < 12 {
			if str(r, "epoch") != "replay" || number(r, "after") != 11 {
				f.wrongCursor.Store(true)
			}
			return Object{"epoch": "replay", "cursor": 12, "events": []Object{{"kind": "text", "turnId": "live", "itemId": "answer", "offset": len(utf16.Encode([]rune(initialLiveText))), "delta": "，重连后继续输出"}}}, nil
		}
		if str(r, "epoch") == "" {
			return Object{"epoch": "replay", "cursor": 10, "reset": true}, nil
		}
		if number(r, "after") == 10 {
			return Object{"epoch": "replay", "cursor": 11, "events": []Object{{"kind": "text", "turnId": "live", "itemId": "answer", "offset": 0, "delta": initialLiveText}}}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return Object{"epoch": "replay", "cursor": number(r, "after")}, nil
		}
	}
	return f.demoAPI.Call(ctx, method, p, target, r)
}

func TestReconnectPreservesLiveReplyAndReplaysFromCursor(t *testing.T) {
	f := &replayAPI{demoAPI: newDemoAPI()}
	app, s := conversationFixture(t, f, 390, Object{}, true)
	app.Post(func() { s.cx.Post(s.startSessionWatch) })
	waitAnimatedText(app, initialLiveText, time.Second)
	app.WaitUntil(time.Second, func() bool { app.Settle(); return s.streamCursor == 11 })
	f.offline.Store(true)
	app.Post(func() { s.cx.Post(func() { s.devices[0]["connected"] = false; s.changed() }) })
	app.WaitFor("role:button 未连接", time.Second)
	// Return to the foreground while offline: transcript reads succeed from an
	// older cache, but must not erase the visible streamed reply.
	app.Post(func() { s.cx.Post(s.startSessionWatch) })
	app.WaitFor("text:"+initialLiveText, time.Second)
	app.Post(func() { s.cx.Post(func() { s.reconnectDevice("demo-device") }) })
	waitAnimatedText(app, initialLiveText+"，重连后继续输出", 3*time.Second)
	if f.wrongCursor.Load() {
		t.Fatal("reconnect discarded the replay cursor")
	}
	app.Screenshot("reconnected-live-reply")
}

// Counting serialization catches UI-thread work even with a fast fake disk.
type marshalCounter struct{ calls atomic.Int32 }

func (c *marshalCounter) MarshalJSON() ([]byte, error) {
	c.calls.Add(1)
	return []byte(`null`), nil
}

func TestSendFirstFrameDoesNotSerializeHistoryOrRehashUploads(t *testing.T) {
	f := &delayedSendAPI{demoAPI: newDemoAPI(), release: make(chan struct{})}
	app, s := conversationFixture(t, f, 390, Object{}, true)
	counter := &marshalCounter{}
	app.Post(func() {
		s.cx.Post(func() {
			s.transcript["serializationProbe"] = counter
			s.uploads = []*Upload{{ID: "image", Name: "image.png", MIME: "image/png", Data: make([]byte, 1<<20), SHA256: "computed-during-upload", Status: "ready"}}
			s.changed()
		})
	})
	app.Type("id:chat-input", "立即反馈")
	var synchronousWrites atomic.Int32
	app.Post(func() {
		s.cx.Post(func() {
			before := counter.calls.Load()
			s.send("send")
			synchronousWrites.Store(counter.calls.Load() - before)
		})
	})
	app.WaitFor("id:send-loading", time.Second)
	app.Find("id:send-loading")
	app.Find("text:立即反馈")
	if synchronousWrites.Load() != 0 {
		t.Fatal("send serialized full history before its first pending frame")
	}
	if str(s.outgoing[s.key()][0].Attachments[0], "sha256") != "computed-during-upload" {
		t.Fatal("send recalculated the already uploaded attachment digest")
	}
	if !app.Find("id:send-message").Props.Disabled {
		t.Fatal("first pending frame allowed a duplicate send")
	}
	close(f.release)
}

func TestUserBubblePreservesTypedLineBreaks(t *testing.T) {
	const message = "生成链接\nhttps://example.com/first.zip\nhttps://example.com/second.xla"
	app, _ := conversationFixture(t, newDemoAPI(), 720, outgoingTranscript(Object{"id": "prompt", "role": "user", "text": strings.ReplaceAll(message, "\n", "\r\n")}), true)
	app.WaitFor("text:"+message, time.Second)
	app.Screenshot("user-message-line-breaks")
}
