package hubui

import (
	"fmt"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestComposerShiftReturnInsertsNewlineWithoutSending(t *testing.T) {
	if nsgotest.Target() == "" {
		t.Skip("modified native editor keys require a platform/browser target")
	}
	f := &delayedSendAPI{demoAPI: newDemoAPI(), release: make(chan struct{})}
	close(f.release)
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s := newState(cx, f)
		s.devices = []Object{{"id": "demo-device", "connected": true}}
		s.target = "demo-device"
		s.providers = demoProviders()
		s.selected = Object{"id": "demo-chat", "providerId": "codex-app"}
		return s.conversation(cx, true)
	}, nsgotest.Size(1000, 844), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	editor := app.Find("id:chat-input").ID
	app.Type("id:chat-input", "第一行 🙂")
	app.Key("shift+enter")
	app.Type("id:chat-input", "第二行")
	if v := app.Find("id:chat-input"); v.Text != "第一行 🙂\n第二行" || v.ID != editor || f.sends.Load() != 0 {
		t.Fatalf("Shift+Return submitted or lost newline: %q, sends=%d", v.Text, f.sends.Load())
	}
	// Use actual native selection keys: the headless Select helper only emits
	// a selection notification and does not move a platform editor's caret.
	app.Key("mod+a")
	app.Key("shift+enter")
	if v := app.Find("id:chat-input"); v.Text != "\n" || f.sends.Load() != 0 {
		t.Fatalf("newline did not replace the selected range: %q", v.Text)
	}
	app.Type("id:chat-input", "单独回车发送")
	app.Key("enter")
	app.WaitUntil(time.Second, func() bool { return f.sends.Load() == 1 && app.Find("id:chat-input").Text == "" })
}

func TestComposerReturnClearsWithoutReplacingEditor(t *testing.T) {
	f := &delayedSendAPI{demoAPI: newDemoAPI(), release: make(chan struct{})}
	close(f.release)
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s := newState(cx, f)
		s.devices = []Object{{"id": "demo-device", "connected": true}}
		s.target = "demo-device"
		s.providers = demoProviders()
		s.selected = Object{"id": "demo-chat", "providerId": "codex-app"}
		return s.conversation(cx, true)
	}, nsgotest.Size(1000, 844), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	editor := app.Find("id:chat-input").ID
	for i := 0; i < 12; i++ {
		app.Type("id:chat-input", fmt.Sprintf("检查回车发送 %d 🙂", i))
		app.Submit("id:chat-input")
		app.WaitUntil(time.Second, func() bool { return f.sends.Load() == int32(i+1) && !app.Exists("id:send-loading") })
		if v := app.Find("id:chat-input"); v.Text != "" || v.ID != editor {
			t.Fatalf("Return %d left text %q or replaced editor", i, v.Text)
		}
	}
	app.Type("id:chat-input", "下一条草稿")
	app.Settle()
	if app.Find("id:chat-input").Text != "下一条草稿" {
		t.Fatal("new draft lost")
	}
}

func TestUnreachableOwnerPreservesDraftUntilExplicitSendAfterRecovery(t *testing.T) {
	f := &retryMessageAPI{demoAPI: newDemoAPI(), requests: make(chan retryRequest, 4)}
	row := Object{"id": "demo-chat", "providerId": "codex-app"}
	unreachable := copyObject(row)
	unreachable["codexOwnerUnreachable"] = true
	turns := []Object{{"id": "active", "status": "inProgress"}}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s = newState(cx, f)
		s.selected, s.target = row, "demo-device"
		s.devices = []Object{{"id": s.target, "connected": true}}
		s.providers = []Object{{"id": "codex-app", "capabilities": Object{"steer": true, "interrupt": true}}}
		s.applyCataloguePage("codex-app", []Object{unreachable}, true)
		// Stale history must neither clear nor later revive the directory warning.
		s.setTranscript(Object{"session": row, "turns": turns})
		return s.conversation(cx, true)
	}, nsgotest.Size(1000, 844), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	editorID := app.Find("id:chat-input").ID
	const draft = "再检查一下这个问题"
	app.Type("id:chat-input", draft)
	app.Submit("id:chat-input")
	if !app.Find("id:send-message").Props.Disabled || !app.Find("id:interrupt-generation").Props.Disabled {
		t.Fatal("unreachable owner still offered a write")
	}
	if editor := app.Find("id:chat-input"); editor.Props.Disabled || editor.Text != draft || editor.ID != editorID {
		t.Fatalf("ownership failure left editor %d (original %d), disabled=%v, text=%q", editor.ID, editorID, editor.Props.Disabled, editor.Text)
	}
	app.Post(func() {
		s.applyCataloguePage("codex-app", []Object{row}, true)
		s.setTranscript(Object{"session": unreachable, "turns": turns})
	})
	app.WaitUntil(time.Second, func() bool { return !app.Find("id:send-message").Props.Disabled })
	if len(f.requests) != 0 || app.Find("id:chat-input").Text != draft || app.Find("id:interrupt-generation").Props.Disabled {
		t.Fatal("recovery consumed the draft or kept the session blocked")
	}
	app.Press("id:send-message")
	app.WaitUntil(time.Second, func() bool { return len(f.requests) == 1 })
	request := <-f.requests
	if str(request.params, "method") != "steer" || str(request.params, "text") != draft {
		t.Fatalf("recovery submitted the wrong operation: %v", request.params)
	}
	request.result <- nil
}

// Replay an editor's late change notification after submission. Delivery and
// navigation may complete independently of the native text callback.
func TestSubmittedDraftEchoDoesNotRestoreOrEraseNewInput(t *testing.T) {
	f := &delayedSendAPI{demoAPI: newDemoAPI(), release: make(chan struct{})}
	app, s := conversationFixture(t, f, 390, Object{}, true)
	const sent = "  同样的内容 🙂  "
	app.Type("id:chat-input", sent)
	app.Press("id:send-message")
	app.WaitFor("id:send-loading", time.Second)
	app.Post(func() { s.cx.Post(func() { s.changeDraft(sent) }) })
	app.Settle()
	if app.Find("id:chat-input").Text != "" {
		t.Fatal("late submitted-value echo restored consumed draft")
	}
	app.Type("id:chat-input", sent)
	close(f.release)
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:send-loading") })
	if app.Find("id:chat-input").Text != sent {
		t.Fatal("new identical draft was consumed by old completion")
	}
	app.Press("id:send-message")
	app.WaitUntil(time.Second, func() bool { return f.sends.Load() == 2 && !app.Exists("id:send-loading") })
	if app.Find("id:chat-input").Text != "" {
		t.Fatal("second explicit send was not consumed")
	}
	// A delayed submit must not restore input or send again while disconnected.
	app.Post(func() {
		s.cx.Post(func() {
			s.devices[0]["connected"] = false
			s.draft.Set("离线草稿")
			s.submitDraft(sent, "send")
		})
	})
	app.Settle()
	if app.Find("id:chat-input").Text != "离线草稿" || f.sends.Load() != 2 {
		t.Fatal("offline submit replaced/replayed draft")
	}
}
