package hubui

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
)

func TestUserMessageSelectionSurvivesBackgroundUpdates(t *testing.T) {
	const text = "长按复制这条消息，后台刷新不应打断选择。"
	transcript := outgoingTranscript(Object{"id": "user", "role": "user", "text": text})
	app, s := conversationFixture(t, newDemoAPI(), 390, transcript, true)
	before := app.Find("text:" + text).ID
	app.LongPress("text:" + text)
	for i := 0; i < 8; i++ {
		app.Post(func() { s.cx.Post(s.changed) })
		app.Advance(100 * time.Millisecond)
		if app.Find("text:"+text).ID != before {
			t.Fatal("background update replaced selectable user text and dismissed its menu")
		}
	}
	// Updating another row replaces the list projection too, but must retain
	// this user's selection surface when the message itself is unchanged.
	app.Post(func() {
		s.cx.Post(func() {
			s.setTranscript(outgoingTranscript(Object{"id": "user", "role": "user", "text": text}, Object{"id": "answer", "role": "assistant", "text": "另一条消息的更新"}))
			s.changed()
		})
	})
	app.Settle()
	if app.Find("text:"+text).ID != before {
		t.Fatal("updating another message replaced the selected user text")
	}
}

func TestUncertainMessageCannotRetryAndMergesPersistedEcho(t *testing.T) {
	f := &retryMessageAPI{demoAPI: newDemoAPI(), requests: make(chan retryRequest, 4)}
	app, s := conversationFixture(t, f, 390, Object{}, true)
	app.Type("id:chat-input", "possibly persisted before renderer failed")
	app.Press("id:send-message")
	app.WaitUntil(time.Second, func() bool { return len(f.requests) == 1 })
	request := <-f.requests
	id := s.outgoing[s.key()][0].ID
	bubble := app.Find("id:message-" + id)
	body := app.Find("text:possibly persisted before renderer failed").ID
	height := bubble.Frame.Size.Height
	request.result <- &control.ResponseError{Code: control.WriteOutcomeUnknown, Message: "App renderer failed after submission"}
	app.WaitFor("text:发送结果待确认", time.Second)
	if app.Exists("id:retry-message-" + id) {
		t.Fatal("uncertain execution offered an unsafe replay")
	}
	app.Post(func() {
		s.cx.Post(func() {
			s.retryOutgoing(id)
			s.setTranscript(outgoingTranscript(Object{"id": "remote", "role": "user", "text": "possibly persisted before renderer failed"}))
			s.changed()
		})
	})
	app.WaitUntil(time.Second, func() bool { return len(s.messageRows) == 1 && s.messageRows[0].ID == "turn|remote" })
	if after := app.Find("id:message-" + id); after.ID != bubble.ID || after.Frame.Size.Height != height || body != app.Find("text:possibly persisted before renderer failed").ID {
		t.Fatal("confirmation replaced the submitted bubble or changed its height")
	}
	if len(f.requests) != 0 || len(s.outgoing[s.key()]) != 0 || app.Exists("id:delivery-"+id) {
		t.Fatal("uncertain message duplicated or retained after echo")
	}
	app.Screenshot("uncertain-message-confirmed-by-echo")
}

type retryRequest struct {
	params Object
	result chan error
}

type retryMessageAPI struct {
	*demoAPI
	requests chan retryRequest
}

func (f *retryMessageAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.call" && (str(r, "method") == "send" || str(r, "method") == "steer") {
		request := retryRequest{params: copyObject(r), result: make(chan error, 1)}
		select {
		case f.requests <- request:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case err := <-request.result:
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if method == "client.call" && str(r, "method") == "transcript" {
		return nil, errors.New("snapshot temporarily unavailable")
	}
	return f.demoAPI.Call(ctx, method, p, target, r)
}

func TestFailedMessageRetryKeepsOriginalPayloadAndNewDraft(t *testing.T) {
	f := &retryMessageAPI{demoAPI: newDemoAPI(), requests: make(chan retryRequest, 4)}
	app, s := conversationFixture(t, f, 390, Object{}, true)
	app.Post(func() {
		s.cx.Post(func() {
			s.uploads = []*Upload{{ID: "original-attachment", Name: "original.txt", MIME: "text/plain", Data: []byte("file"), SHA256: "original-digest", Status: "ready"}}
			s.changed()
		})
	})
	app.Type("id:chat-input", "这条发送失败后重试")
	app.Press("id:send-message")
	nextRequest := func() retryRequest {
		t.Helper()
		select {
		case request := <-f.requests:
			return request
		case <-time.After(time.Second):
			t.Fatal("message request not sent")
			return retryRequest{}
		}
	}
	original := nextRequest()
	original.result <- errors.New("网络中断")
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:send-loading") })
	id := s.outgoing[s.key()][0].ID
	retry := "id:retry-message-" + id
	copy := "id:copy-message-" + id
	app.Find(retry)
	if globalBounds(app.Find(retry)).Origin.X >= globalBounds(app.Find(copy)).Origin.X {
		t.Fatal("retry action is not to the left of copy")
	}
	app.Screenshot("failed-message-retry")
	app.Post(func() {
		s.cx.Post(func() {
			s.devices[0]["connected"] = false
			s.changed()
			s.retryOutgoing(id)
		})
	})
	app.Settle()
	if !app.Find(retry).Props.Disabled || len(f.requests) != 0 {
		t.Fatal("offline retry sent a request")
	}
	app.Post(func() { s.cx.Post(func() { s.devices[0]["connected"] = true; s.changed() }) })
	app.Type("id:chat-input", "下一条草稿")
	app.Post(func() {
		s.cx.Post(func() {
			s.model = "next-draft-model"
			s.uploads = []*Upload{{ID: "new-attachment", Name: "next.txt", Status: "uploading"}}
			s.reconcileOutgoing(time.Time{})
			s.rebuildMessages()
			s.changed()
		})
	})
	app.WaitFor("text:next-draft-model", time.Second)
	app.Press(retry)
	app.WaitFor("id:send-loading", time.Second)
	app.Find("id:delivery-" + id)
	retried := nextRequest()
	if str(retried.params, "text") != str(original.params, "text") || str(retried.params, "model") != "test-model" || !reflect.DeepEqual(retried.params["attachments"], original.params["attachments"]) {
		t.Fatalf("retry changed original payload: %#v", retried.params)
	}
	app.Post(func() { s.cx.Post(func() { s.retryOutgoing(id); s.retryOutgoing(id) }) })
	app.Settle()
	if len(f.requests) != 0 || len(s.outgoing[s.key()]) != 1 || s.outgoing[s.key()][0].ID != id {
		t.Fatal("retry sent duplicate commands or duplicated the local message")
	}
	if app.Find("id:chat-input").Text != "下一条草稿" || len(s.uploads) != 1 || s.uploads[0].ID != "new-attachment" {
		t.Fatal("retry consumed the new draft or attachments")
	}
	retried.result <- nil
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:send-loading") })
	app.Find(copy)
	if app.Exists(retry) {
		t.Fatal("successful retry still offers another retry")
	}
}

func TestMessageFooterCopyPosition(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		t.Run(role, func(t *testing.T) {
			app, _ := conversationFixture(t, newDemoAPI(), 390, outgoingTranscript(Object{"id": "fixture", "role": role, "text": "Fixture"}), true)
			copyBounds := globalBounds(app.Find("id:copy-message-turn|fixture"))
			timeBounds := globalBounds(app.Find("text:时间未知"))
			if role == "user" && copyBounds.Origin.X < timeBounds.Origin.X+timeBounds.Size.Width ||
				role == "assistant" && timeBounds.Origin.X < copyBounds.Origin.X+copyBounds.Size.Width {
				t.Fatalf("wrong footer order for %s: copy %v, time %v", role, copyBounds, timeBounds)
			}
		})
	}
}
