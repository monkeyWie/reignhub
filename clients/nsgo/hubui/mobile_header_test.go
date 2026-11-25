package hubui

import (
	"strings"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
)

func TestMobileHeaderOverlaysMessages(t *testing.T)     { testMobileHeader(t, false) }
func TestMobileHeaderOverlaysMessagesDark(t *testing.T) { testMobileHeader(t, true) }

func testMobileHeader(t *testing.T, dark bool) {
	t.Helper()
	text := strings.Repeat("这是一段较长的回复。滚动时，消息应当从顶部悬浮导航下面经过，逐渐淡出，而不是被挤到下面。\n\n", 25)
	transcript := Object{"turns": []Object{{"id": "turn", "status": "completed", "items": []Object{{"id": "answer", "role": "assistant", "text": text}}}}}
	app, s := conversationFixture(t, newDemoAPI(), 390, nil, true)
	initialFade := globalBounds(app.Find("id:conversation-header-fade"))
	app.Post(func() {
		s.cx.Post(func() {
			s.selected["title"] = "带有很长标题的多行会话不会把顶部按钮挤出屏幕"
			s.selected["cwd"] = "/Users/example/projects/long-project-name"
			if dark {
				s.dark = true
				s.cx.SetAppearance(core.AppearanceDark)
			}
			s.setTranscript(transcript)
			s.changed()
		})
	})
	app.Settle()
	conversation := globalBounds(app.Find("id:conversation"))
	timeline := globalBounds(app.Find("id:timeline"))
	header := globalBounds(app.Find("id:conversation-header"))
	if timeline.Origin.Y != conversation.Origin.Y || header.Origin.Y != timeline.Origin.Y {
		t.Fatalf("header displaced the list: conversation=%v timeline=%v header=%v", conversation, timeline, header)
	}
	for _, id := range []string{"mobile-menu", "conversation-title-pill", "connection-status-demo-device", "attachment-cache"} {
		b := globalBounds(app.Find("id:" + id))
		if b.Origin.X < conversation.Origin.X || b.Origin.X+b.Size.Width > conversation.Origin.X+conversation.Size.Width+.5 {
			t.Fatalf("%s exceeds phone width: %v", id, b)
		}
	}
	fade := app.Find("id:conversation-header-fade")
	fadeBounds := globalBounds(fade)
	title := globalBounds(app.Find("id:conversation-title-pill"))
	if initialFade != fadeBounds || fadeBounds.Size.Height == 0 || fadeBounds.Origin.Y+fadeBounds.Size.Height != title.Origin.Y+title.Size.Height {
		t.Fatalf("loading history must keep the fade within the controls: initial=%v loaded=%v title=%v", initialFade, fadeBounds, title)
	}
	if fade.Props.PointerEvents != core.PointerEventsNone {
		t.Fatal("fade intercepts message interactions")
	}
	if app.Find("id:conversation-header").Props.PointerEvents != core.PointerEventsBoxNone {
		t.Fatal("empty toolbar area intercepts scrolling")
	}
	list := app.Find("id:chat-message-list")
	distance := core.Points(140)
	if list.Offset.Y > 140 {
		distance = -140
	}
	app.Scroll("id:chat-message-list", distance)
	if globalBounds(app.Find("id:conversation-header")) != header {
		t.Fatal("toolbar moved with messages")
	}
	app.Screenshot(map[bool]string{false: "mobile-floating-header", true: "mobile-floating-header-dark"}[dark])
	app.Press("id:attachment-cache")
	app.WaitUntil(time.Second, func() bool { return s.dialog.GetUntracked() == "clear-cache" })
}
