package hubui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func queueViewFixture() Object {
	return Object{"state": "ready", "messages": []Object{
		{"id": "next", "text": "然后更新文档，并同步类型。\n保留这一段完整说明。", "status": "queued", "attachments": []Object{{"name": "界面示意.png", "kind": "image"}}},
		{"id": "same-text", "text": "然后更新文档，并同步类型。\n保留这一段完整说明。", "status": "queued"},
	}}
}

func TestMessageQueueLayoutPreviewAndEditorStability(t *testing.T) {
	for _, width := range []core.Points{390, 1100} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%g-dark-%t", width, dark), func(t *testing.T) {
				var s *State
				app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
					s = newState(cx, newDemoAPI())
					s.applyColors()
					s.width.Set(width)
					s.devices = []Object{{"id": "demo-device", "connected": true}}
					s.target = "demo-device"
					s.selected = Object{"id": "demo-chat", "providerId": "codex-app", "title": "文档更新"}
					s.setTranscript(Object{"queue": queueViewFixture(), "turns": []Object{{"id": "active", "status": "inProgress", "items": []Object{{"id": "answer", "role": "assistant", "phase": "commentary", "text": "明白，我会整理文档并更新相关说明。"}}}}})
					s.draft.Set("保留当前草稿")
					return s.conversation(cx, false)
				}, nsgotest.Size(width, 844), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Dark: dark}))
				editor := app.Find("id:chat-input").ID
				queue := globalBounds(app.Find("id:message-queue"))
				composer := globalBounds(app.Find("id:composer-card"))
				if queue.Origin.X < 0 || queue.Origin.X+queue.Size.Width > width || queue.Origin.Y+queue.Size.Height > composer.Origin.Y+1 {
					t.Fatalf("queue layout: %+v composer %+v", queue, composer)
				}
				app.Find("id:queued-message-next")
				app.Find("id:queued-message-same-text")
				app.Screenshot(fmt.Sprintf("message-queue-%g-dark-%t", width, dark))
				app.Press("id:queued-message-next")
				app.WaitFor("id:queued-message-preview-next", time.Second)
				app.Find("text:界面示意.png")
				popup := app.Find("id:queued-message-preview-next").ID
				for i := 0; i < 3; i++ {
					app.Post(func() {
						s.cx.Post(func() {
							s.transcript, _ = applyEvents(s.transcript, []Object{{"kind": "queue", "queue": queueViewFixture()}})
							s.changed()
						})
					})
					app.Settle()
					if app.Find("id:queued-message-preview-next").ID != popup {
						t.Fatal("queue refresh replaced open preview")
					}
				}
				app.Screenshot(fmt.Sprintf("message-queue-preview-%g-dark-%t", width, dark))
				app.Press("id:queued-message-close-next")
				app.Post(func() {
					s.cx.Post(func() {
						s.transcript, _ = applyEvents(s.transcript, []Object{{"kind": "queue", "queue": Object{"state": "unknown"}}})
						s.changed()
					})
				})
				app.WaitFor("text:待发送 · 等待同步", time.Second)
				app.Find("id:queued-message-next")
				app.Post(func() {
					s.cx.Post(func() {
						s.transcript, _ = applyEvents(s.transcript, []Object{{"kind": "queue", "queue": Object{"state": "ready", "messages": []Object{}}}})
						s.changed()
					})
				})
				app.WaitUntil(time.Second, func() bool { return !app.Exists("id:message-queue") })
				if v := app.Find("id:chat-input"); v.ID != editor || v.Text != "保留当前草稿" {
					t.Fatal("queue consumed draft or rebuilt editor")
				}
			})
		}
	}
}

func TestMessageQueueLongListAndCachedState(t *testing.T) {
	q := queueViewFixture()
	rows := objects(q["messages"])
	for i := 0; i < 18; i++ {
		rows = append(rows, Object{"id": fmt.Sprint(i), "text": strings.Repeat("长消息🙂", 100), "status": "queued"})
	}
	q["messages"] = rows
	app, s := conversationFixture(t, newDemoAPI(), 390, Object{"queue": q}, false)
	app.Find("text:待发送 · 等待同步")
	if b := globalBounds(app.Find("id:message-queue-scroll")); b.Size.Height > 145 {
		t.Fatal("queue pushed composer out of viewport", b)
	}
	if len(objects(object(s.snapshotProjection()["queue"])["messages"])) != 20 {
		t.Fatal("queue lost from cache")
	}
	app.Post(func() { s.cx.Post(func() { s.transcript = Object{}; s.changed() }) })
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:message-queue") })
}

func TestMessageQueueStreamReplaysOverStaleSnapshot(t *testing.T) {
	f := &independentStreamAPI{demoAPI: newDemoAPI(), snapshots: make(chan snapshotRequest, 4), events: make(chan Object, 8), approvalsStarted: make(chan struct{})}
	cached := Object{"queue": queueViewFixture(), "turns": []Object{{"id": "reply", "status": "inProgress", "items": []Object{}}}}
	app, s := conversationFixture(t, f, 390, cached, true)
	app.Post(func() { s.cx.Post(s.startSessionWatch) })
	app.WaitUntil(time.Second, func() bool { return len(f.snapshots) > 0 })
	request := <-f.snapshots
	request.reply <- Object{"streamEpoch": "live", "streamCursor": 0, "queue": queueViewFixture(), "turns": cached["turns"]}
	app.WaitUntil(time.Second, func() bool { app.Settle(); return s.stream.baseline })
	f.events <- Object{"epoch": "live", "cursor": 0, "events": []Object{{"kind": "snapshot"}}}
	app.WaitUntil(time.Second, func() bool { return len(f.snapshots) > 0 })
	request = <-f.snapshots
	f.events <- Object{"epoch": "live", "cursor": 1, "events": []Object{{"id": 1, "kind": "queue", "queue": Object{"state": "ready", "messages": []Object{}}}}}
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:message-queue") })
	request.reply <- Object{"streamEpoch": "live", "streamCursor": 0, "queue": queueViewFixture(), "turns": cached["turns"]}
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:transcript-syncing") })
	if app.Exists("id:message-queue") {
		t.Fatal("stale history resurrected consumed queue")
	}
	if len(s.messageRows) != 0 {
		t.Fatal("queued messages leaked into transcript", s.messageRows)
	}
}

func TestMessageQueuePartialSourceFailureRetainsOtherMessages(t *testing.T) {
	old := Object{"state": "ready", "messages": []Object{{"id": "server", "text": "server input"}, {"id": "local", "text": "old"}}}
	next := Object{"state": "unknown", "messages": []Object{{"id": "local", "text": "new"}}}
	merged := mergeMessageQueue(old, next)
	rows := objects(merged["messages"])
	if str(merged, "state") != "unknown" || len(rows) != 2 || str(rows[0], "text") != "new" || str(rows[1], "id") != "server" {
		t.Fatal("partial read erased known queue", merged)
	}
	if len(objects(mergeMessageQueue(merged, Object{"state": "ready", "messages": []Object{}})["messages"])) != 0 {
		t.Fatal("ready empty queue did not clear stale rows")
	}
}

func TestCachedMessageQueueClearsOnCompletedSnapshot(t *testing.T) {
	cached := Object{"queue": queueViewFixture()}
	page := Object{"turns": []Object{{"id": "t"}}}
	if len(objects(object(previewTranscript(cached, page)["queue"])["messages"])) != 2 {
		t.Fatal("a history fragment discarded the cached queue")
	}
	if _, present := mergeLatest(cached, page)["queue"]; present {
		t.Fatal("a complete snapshot with no queue resurrected cached messages")
	}
}
