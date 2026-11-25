package hubui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestHistoryPreviewsCoalesceWithoutBlockingDownload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	posts := make(chan func(), 8)
	var latest Object
	observe := observeTranscriptPreviews(ctx, func(fn func()) { posts <- fn }, func(v Object) { latest = v })
	fragment := func(i int) provider.Transcript {
		return provider.Transcript{StreamEpoch: "live", Turns: []provider.Turn{{ID: "turn", Status: "completed", Items: []provider.Item{{ID: fmt.Sprint(i), Role: "assistant", Text: fmt.Sprint("message ", i)}}}}}
	}
	if err := observe(fragment(0)); err != nil {
		t.Fatal(err)
	}
	var first func()
	select {
	case first = <-posts:
	case <-time.After(time.Second):
		t.Fatal("first message not delivered")
	}
	// Simulate a busy UI. Hundreds of fragments must finish downloading without
	// adding hundreds of cumulative render callbacks to the input queue.
	downloaded := make(chan struct{})
	go func() {
		defer close(downloaded)
		for i := 1; i <= 1000; i++ {
			_ = observe(fragment(i))
		}
	}()
	select {
	case <-downloaded:
	case <-time.After(3 * time.Second):
		t.Fatal("UI blocked history receiver")
	}
	if len(posts) != 0 {
		t.Fatal("history renders accumulated behind the UI")
	}
	first()
	if len(objects(objects(latest["turns"])[0]["items"])) != 1 {
		t.Fatal("queued preview was mutated")
	}
	select {
	case fn := <-posts:
		fn()
	case <-time.After(time.Second):
		t.Fatal("trailing preview lost during server pause")
	}
	if len(objects(objects(latest["turns"])[0]["items"])) != 1001 {
		t.Fatal("coalescing dropped messages")
	}
	_ = observe(fragment(1001))
	select {
	case fn := <-posts:
		cancel()
		fn()
	case <-time.After(time.Second):
		t.Fatal("preview not queued")
	}
	if len(objects(objects(latest["turns"])[0]["items"])) != 1001 {
		t.Fatal("cancelled preview changed another session")
	}
}

func TestLoadingSpinnersRemainMountedDuringSyncUpdates(t *testing.T) {
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s = newState(cx, newDemoAPI())
		s.target, s.selected = "demo-device", Object{"id": "chat"}
		s.cacheLoading, s.transcriptLoading = true, true
		return core.View{Style: fill, Children: []core.Node{s.emptyCatalogue(cx), s.emptyTranscript(cx)}}
	})
	sessions := app.Find("id:sessions-loading").ID
	messages := app.Find("id:transcript-loading").ID
	for i := 0; i < 24; i++ {
		app.Post(func() { s.cx.Post(s.changed) })
		app.Settle()
		if app.Find("id:sessions-loading").ID != sessions || app.Find("id:transcript-loading").ID != messages {
			t.Fatal("state update restarted a loading animation")
		}
	}
}

func TestSyncDotsOverlayDoesNotMoveMessages(t *testing.T) {
	app, s := conversationFixture(t, newDemoAPI(), 390, Object{"turns": []Object{{"id": "turn", "status": "completed", "items": []Object{{"id": "answer", "role": "assistant", "text": "现有消息保持在原位"}}}}}, true)
	ids := []string{"timeline", "chat-message-list", "message-turn|answer", "composer-card"}
	before := make([]core.Bounds, len(ids))
	for i, id := range ids {
		before[i] = globalBounds(app.Find("id:" + id))
	}
	app.Post(func() { s.cx.Post(func() { s.transcriptSyncing, s.syncVisible = true, true; s.changed() }) })
	app.WaitFor("id:transcript-syncing", time.Second)
	if app.Find("id:transcript-syncing").Props.PointerEvents != core.PointerEventsNone {
		t.Fatal("dots intercept list input")
	}
	dots := globalBounds(app.Find("id:transcript-syncing"))
	if dots.Origin.Y+dots.Size.Height > before[3].Origin.Y {
		t.Fatal("dots overlap the input")
	}
	for i, id := range ids {
		if globalBounds(app.Find("id:"+id)) != before[i] {
			t.Fatalf("showing dots moved %s", id)
		}
	}
	app.Screenshot("sync-dots-overlay")
	app.Post(func() { s.cx.Post(func() { s.transcriptSyncing, s.syncVisible = false, false; s.changed() }) })
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:transcript-syncing") })
	for i, id := range ids {
		if globalBounds(app.Find("id:"+id)) != before[i] {
			t.Fatalf("hiding dots moved %s", id)
		}
	}
}

func TestLargeProgressiveHistoryKeepsNavigationResponsive(t *testing.T) {
	f := &independentStreamAPI{demoAPI: newDemoAPI(), snapshots: make(chan snapshotRequest, 4), events: make(chan Object, 8), approvalsStarted: make(chan struct{})}
	// Cold entry displays progressive fragments while the full read is pending.
	f.demoAPI.transcript = nil
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node { return RootWithAPI(cx, f, true) }, nsgotest.Size(390, 844))
	showSessionDirectory(t, app)
	session := sessionSelector("demo-device", "codex-app", "demo-chat")
	app.WaitFor(session, 5*time.Second)
	app.Press(session)
	app.WaitFor("id:chat-input", time.Second)
	var request snapshotRequest
	select {
	case request = <-f.snapshots:
	case <-time.After(time.Second):
		t.Fatal("history did not start")
	}
	fragment := provider.Transcript{StreamEpoch: "live"}
	for i := 0; i < 3000; i++ {
		fragment.Turns = append(fragment.Turns, provider.Turn{ID: fmt.Sprint("history-", i), Status: "completed", Items: []provider.Item{{ID: "answer", Role: "assistant", Text: fmt.Sprintf("历史消息 %d %s", i, strings.Repeat("数据", 128))}}})
	}
	request.fragments <- fragment
	app.WaitFor("id:message-history-2999|answer", 5*time.Second)
	app.WaitFor("id:transcript-syncing", time.Second)
	// These are real UI actions while the final response is still outstanding.
	app.Press("id:connection-status-demo-device")
	app.WaitFor("id:dialog-connection-status", time.Second)
	app.Press("id:dialog-connection-status-close")
	app.Press("id:mobile-menu")
	app.WaitFor("id:session-sidebar", time.Second)
	app.WaitFor(sessionSelector("demo-device", "codex-app", "demo-chat"), time.Second)
	app.Press(sessionSelector("demo-device", "codex-app", "demo-chat"))
	app.WaitFor("id:chat-input", time.Second)
	showSessionDirectory(t, app)
	app.Screenshot("large-history-navigation-responsive")
}
