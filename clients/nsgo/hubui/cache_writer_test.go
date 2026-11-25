package hubui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/pkg/persistence"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

type capturedSnapshot struct {
	params  Object
	release chan struct{}
}

type snapshotTestAPI struct{ calls chan capturedSnapshot }

func (*snapshotTestAPI) CanHost() bool { return false }
func (*snapshotTestAPI) Close()        {}
func (a *snapshotTestAPI) Call(ctx context.Context, _ string, params Object, _ string, _ Object) (any, error) {
	call := capturedSnapshot{params: params, release: make(chan struct{})}
	select {
	case a.calls <- call:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-call.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSnapshotWriterCapturesOverlayAndCoalescesHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &snapshotTestAPI{calls: make(chan capturedSnapshot)}
	s := &State{snapshots: newSnapshotWriter(ctx, api)}
	pending := &outgoingMessage{ID: "pending", Text: "draft", Status: "sending", Known: map[string]bool{"old": true}, Anchors: []string{"old"}}
	history := Object{"turns": []Object{{"id": "live", "status": "inProgress", "items": []Object{{"id": "answer", "text": "a"}}}}, "localMessages": []*outgoingMessage{pending}, "resume": Object{"epoch": "e", "cursor": 1}}
	nextCall := func() capturedSnapshot {
		t.Helper()
		select {
		case call := <-api.calls:
			return call
		case <-time.After(time.Second):
			t.Fatal("snapshot writer stalled")
			return capturedSnapshot{}
		}
	}
	s.writeSnapshot("local", "codex-app", "chat", history)
	first := nextCall()
	// Simulate mutable delivery state changing while the adapter is delayed.
	pending.Status, pending.Known["new"], pending.Anchors[0] = "uncertain", true, "new"
	for i := 1; i <= 20; i++ {
		history, _ = applyEvents(history, []Object{{"kind": "text", "turnId": "live", "itemId": "answer", "offset": i, "delta": "a"}})
		history["resume"] = Object{"epoch": "e", "cursor": i + 1}
		s.writeSnapshot("local", "codex-app", "chat", history)
	}
	// Exercise the adapter's serialization after UI updates, including concrete
	// outgoing structs; it must still see the content/cursor captured together.
	data, err := json.Marshal(first.params["transcript"])
	if err != nil {
		t.Fatal(err)
	}
	var saved Object
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	message := objects(saved["localMessages"])[0]
	if str(message, "status") != "sending" || flag(object(message["known"]), "new") || message["anchors"].([]any)[0] != "old" {
		t.Fatal("pending delivery state mutated an in-flight snapshot")
	}
	if number(object(saved["resume"]), "cursor") != 1 || str(objects(objects(saved["turns"])[0]["items"])[0], "text") != "a" {
		t.Fatal("live changes mutated persisted content or cursor")
	}
	close(first.release)
	last := nextCall()
	if number(object(object(last.params["transcript"])["resume"]), "cursor") != 21 {
		t.Fatal("writer did not coalesce the delayed checkpoints")
	}
	cancel() // A blocked adapter is cancelled with the writer's owner.
}

type historySendTestAPI struct {
	*delayedSendAPI
	persistence.HistoryDAO
}

func (a *historySendTestAPI) Call(ctx context.Context, method string, params Object, target string, request Object) (any, error) {
	if method == "client.call" && str(request, "method") == "transcript" {
		if str(request, "cursor") != "" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		t := outgoingTranscript(Object{"id": "prior", "role": "user", "text": "earlier request"}, Object{"id": "acknowledged", "role": "user", "text": "written once"})
		t["nextCursor"] = "earlier"
		return t, nil
	}
	return a.delayedSendAPI.Call(ctx, method, params, target, request)
}

func TestSendingWhileOlderHistoryCommits(t *testing.T) {
	dao := timelineTestDAO(t)
	ctx := context.Background()
	api := &historySendTestAPI{&delayedSendAPI{demoAPI: newDemoAPI(), release: make(chan struct{})}, dao}
	key := persistence.Session{Env: "demo-device", Provider: "codex-app", ID: "demo-chat"}
	base := outgoingTranscript(Object{"id": "prior", "role": "user", "text": "earlier request"})
	base["nextCursor"] = "earlier"
	app, s := conversationFixture(t, api, 390, base, true)
	app.Post(func() { s.cx.Post(s.persistSnapshot) })
	app.WaitUntil(3*time.Second, func() bool { return str(s.transcript, "_historyRevision") != "" })
	page, err := dao.ReadPage(ctx, key, persistence.PageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	older := persistence.WithOrigin(Object{"turns": []Object{{"id": "old", "items": []Object{{"id": "first", "role": "user", "text": "first request"}}}}}, persistence.PageCursor{Turn: -1}, false)
	patch := persistence.Diff(page.Revision, nil, older)
	patch.Revision = persistence.OlderPageRevision(page.Revision)
	patch.Replace = false
	patch.Head = Object{"nextCursor": "even-earlier"}
	if err := dao.ApplyHistory(ctx, key, patch); err != nil {
		t.Fatal(err)
	}
	app.Type("id:chat-input", "written once")
	app.Press("id:send-message")
	app.WaitFor("id:send-loading", time.Second)
	// Inspect the actual Send action while the Agent acknowledgement is held.
	app.WaitUntil(3*time.Second, func() bool {
		p, e := dao.ReadPage(ctx, key, persistence.PageQuery{})
		return e == nil && (len(objects(p.Transcript["localMessages"])) == 1 || s.notice != "")
	})
	if s.notice != "" {
		t.Fatalf("local send raised %q", s.notice)
	}
	close(api.release)
	app.WaitUntil(3*time.Second, func() bool {
		p, e := dao.ReadPage(ctx, key, persistence.PageQuery{})
		if e != nil {
			return false
		}
		rows := messages(p.Transcript)
		return len(objects(p.Transcript["localMessages"])) == 0 && len(rows) > 0 && rows[len(rows)-1].Text == "written once"
	})
	p, e := dao.ReadPage(ctx, key, persistence.PageQuery{})
	if e != nil {
		t.Fatal(e)
	}
	turns := objects(p.Transcript["turns"])
	if api.sends.Load() != 1 || len(turns) < 2 || str(turns[0], "id") != "old" || str(p.Transcript, "nextCursor") != "even-earlier" {
		t.Fatalf("send count %d, turns %d, paging cursor %q", api.sends.Load(), len(turns), str(p.Transcript, "nextCursor"))
	}
}

func TestStaleSenderPreservesCommittedHistoryAndOtherDeliveries(t *testing.T) {
	ctx := context.Background()
	dao := timelineTestDAO(t)
	w := snapshotWriter{storage: dao}
	key := persistence.Session{Env: "fixture-" + persistence.Revision(), Provider: "fixture", ID: "stale"}
	old := outgoingTranscript(Object{"id": "u", "role": "user", "text": "first"})
	old["resume"] = Object{"epoch": "test", "cursor": 1}
	old["localMessages"] = []Object{{"id": "confirmed-elsewhere", "status": "sending"}}
	previous, err := w.save(ctx, key, nil, snapshotWrite{transcript: old})
	if err != nil {
		t.Fatal(err)
	}
	current := outgoingTranscript(Object{"id": "u", "role": "user", "text": "first"}, Object{"id": "reply", "role": "assistant", "text": "newer reply"})
	current["resume"] = Object{"epoch": "test", "cursor": 3}
	current["localMessages"] = []Object{{"id": "another-tab", "status": "accepted"}}
	current["queue"] = Object{"messages": []Object{{"id": "queued", "text": "queued elsewhere"}}}
	if e := dao.ApplyHistory(ctx, key, persistence.Diff(str(previous, "_historyRevision"), old, current)); e != nil {
		t.Fatal(e)
	}
	stale := copyObject(old)
	stale["localMessages"] = []Object{{"id": "confirmed-elsewhere", "status": "accepted"}, {"id": "mine", "status": "accepted"}}
	saved, err := w.save(ctx, key, previous, snapshotWrite{transcript: stale})
	if err != nil || !flag(saved, "_refreshHistory") {
		t.Fatalf("competing live update: reconciliation %v, error %v", flag(saved, "_refreshHistory"), err)
	}
	// Repeat the same delayed callback after the storage revision is up to date.
	if _, err = w.save(ctx, key, saved, snapshotWrite{transcript: stale}); err != nil {
		t.Fatal(err)
	}
	page, err := dao.ReadPage(ctx, key, persistence.PageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	v := page.Transcript
	rows := messages(v)
	states := map[string]string{}
	for _, m := range objects(v["localMessages"]) {
		states[str(m, "id")] = str(m, "status")
	}
	if len(rows) != 2 || rows[1].Text != "newer reply" || number(object(v["resume"]), "cursor") != 3 || len(objects(object(v["queue"])["messages"])) != 1 || len(states) != 2 || states["mine"] != "accepted" || states["another-tab"] != "accepted" {
		t.Fatalf("unexpected saved history: %d rows, cursor %d, deliveries %v", len(rows), number(object(v["resume"]), "cursor"), states)
	}
}

type failingHistorySendAPI struct{ *historySendTestAPI }

func (*failingHistorySendAPI) ApplyHistory(context.Context, persistence.Session, persistence.HistoryPatch) error {
	return errors.New("fixture storage failure")
}

func TestSendReportsGenuineStorageFailure(t *testing.T) {
	api := &failingHistorySendAPI{&historySendTestAPI{&delayedSendAPI{demoAPI: newDemoAPI(), release: make(chan struct{})}, timelineTestDAO(t)}}
	app, s := conversationFixture(t, api, 390, nil, true)
	app.Type("id:chat-input", "written once")
	app.Press("id:send-message")
	app.WaitFor("id:send-loading", time.Second)
	close(api.release)
	app.WaitUntil(3*time.Second, func() bool {
		return !app.Exists("id:send-loading") && s.notice == "此会话的本地记录未能保存"
	})
	if api.sends.Load() != 1 || s.error != "" {
		t.Fatal("storage failure changed Agent delivery or repeated the send")
	}
}

// A mostly hidden 205-item turn spans five network pages. The echo of
// the initial prompt and a later steer both precede the newest 50 records.
func TestReconnectKeepsUserMessagesOnOfflineReopen(t *testing.T) {
	testReconnectUserMessages(t, false)
}

func TestReconnectRepairsPreviouslyTruncatedCache(t *testing.T) {
	testReconnectUserMessages(t, true)
}

func testReconnectUserMessages(t *testing.T, truncated bool) {
	t.Helper()
	ctx := context.Background()
	dao := timelineTestDAO(t)
	key := persistence.Session{Env: "fixture-" + persistence.Revision(), Provider: "codex-app", ID: "demo-chat"}
	defer dao.ForgetHistory(ctx, key.Env)
	api := &historyStreamFixture{independentStreamAPI: &independentStreamAPI{
		demoAPI: newDemoAPI(), snapshots: make(chan snapshotRequest, 4),
		events: make(chan Object, 8), approvalsStarted: make(chan struct{}),
	}, HistoryDAO: dao}
	message := func(id, role, text string) Object { return Object{"id": id, "role": role, "text": text} }
	before := Object{"id": "before", "status": "completed", "items": []Object{message("a", "assistant", "Previous reply")}}
	items := make([]Object, 205)
	for i := range items {
		items[i] = message(fmt.Sprint(i), "reasoning", "")
	}
	items[0] = message("prompt", "user", "Original request")
	items[90] = message("steer", "user", "A follow-up")
	items[204] = message("answer", "assistant", "Final reply")
	page := func(start, end int) Object {
		p := Object{"streamEpoch": "live", "streamCursor": 2, "nextCursor": "before-long", "turns": []Object{{"id": "long", "status": "completed", "items": items[start:end]}}}
		if start > 0 {
			p["partialTurn"], p["nextCursor"] = "long", fmt.Sprint(start)
		}
		return p
	}
	base := Object{"streamEpoch": "live", "resume": Object{"epoch": "live", "cursor": 1}, "nextCursor": "before-history",
		"turns": []Object{before, {"id": "long", "status": "inProgress", "items": items[:1]}}}
	if truncated {
		base["partialTurn"] = "long"
		base["turns"] = []Object{before, objects(page(155, 205)["turns"])[0]}
	}
	if err := dao.ApplyHistory(ctx, key, persistence.Diff("", nil, base)); err != nil {
		t.Fatal(err)
	}
	first, err := dao.ReadPage(ctx, key, persistence.PageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = newState(cx, api)
		s.devices = []Object{{"id": key.Env, "connected": true}}
		s.target = key.Env
		s.selected = Object{"id": key.ID, "providerId": key.Provider}
		s.setTranscript(first.View())
		return s.compactWorkspace(cx)
	}, nsgotest.Size(390, 844))
	app.Post(func() { s.startSessionWatch() })
	for end := len(items); end > 0; {
		app.WaitUntil(3*time.Second, func() bool { return len(api.snapshots) > 0 || s.stream != nil && s.stream.verified })
		if len(api.snapshots) == 0 {
			t.Fatal("accepted a suffix that skips the user's confirmed input")
		}
		if !truncated {
			app.Find("text:Original request")
		}
		start := max(0, end-persistence.PageSize)
		fragment := page(start, end)
		if start == 0 {
			fragment["turns"] = append([]Object{before}, objects(fragment["turns"])...)
			fragment["nextCursor"] = "before-history"
		}
		(<-api.snapshots).reply <- fragment
		end = start
	}
	app.WaitUntil(3*time.Second, func() bool { return s.stream.verified })
	app.WaitFor("text:Final reply", 3*time.Second)
	app.Post(s.persistSnapshot)
	var offline Object
	app.WaitUntil(5*time.Second, func() bool {
		tail, err := dao.ReadPage(ctx, key, persistence.PageQuery{})
		if err != nil {
			t.Fatal(err)
		}
		offline = tail.View()
		return tail.Before != nil && tail.End.Item == 204
	})
	if str(offline, "nextCursor") != "before-history" {
		t.Fatalf("lost prior paging boundary: %v", offline["nextCursor"])
	}
	// Navigate out and back offline so entry reads the initial 50 rows and
	// viewport fill has to recover the earlier bubbles from the database.
	app.Post(func() {
		s.devices = []Object{{"id": key.Env, "connected": false}}
		s.selectSession(Object{"id": "away", "providerId": key.Provider})
	})
	app.WaitUntil(3*time.Second, func() bool { return str(s.selected, "id") == "away" })
	app.Post(func() { s.selectSession(Object{"id": key.ID, "providerId": key.Provider}) })
	app.WaitFor("text:Original request", 5*time.Second)
	app.Find("text:A follow-up")
	app.Find("text:Final reply")
	app.Find("text:Previous reply")
	if api.reads.Load() != 5 {
		t.Fatalf("offline reopening or gap repair requested extra pages: %d", api.reads.Load())
	}
}

type historyStreamFixture struct {
	*independentStreamAPI
	persistence.HistoryDAO
}

func (f *historyStreamFixture) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.call" && str(r, "method") == "transcript" {
		if !flag(r, "fresh") {
			// Hold viewport prefetch at the older conversation boundary.
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if number(r, "historyItems") != persistence.PageSize {
			return nil, errors.New("expected an item-bounded history read")
		}
	}
	return f.independentStreamAPI.Call(ctx, method, p, target, r)
}
