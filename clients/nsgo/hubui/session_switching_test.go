package hubui

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/monkeyWie/reignhub/pkg/persistence"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

type switchingSessionAPI struct {
	*demoAPI
	histories map[string]Object
}

func (f *switchingSessionAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if (method == "client.call" || method == "client.cached") && str(r, "method") == "transcript" {
		data, err := json.Marshal(f.histories[str(r, "session")])
		if err != nil {
			return nil, err
		}
		var result Object
		err = json.Unmarshal(data, &result)
		return result, err
	}
	if method == "client.list" {
		return []Object{{"id": "demo-device", "name": "Windows fixture", "platform": "windows", "connected": true}}, nil
	}
	return f.demoAPI.Call(ctx, method, p, target, r)
}

func TestRapidSessionSwitching(t *testing.T) {
	f := &switchingSessionAPI{demoAPI: newDemoAPI(), histories: map[string]Object{}}
	f.sessions = nil
	counts := []int{32, 1, 8}
	for index, count := range counts {
		id := fmt.Sprintf("switch-%d", index)
		session := Object{"id": id, "providerId": "codex-app", "title": id, "cwd": `C:\fixture`, "createdAt": index + 1}
		f.sessions = append(f.sessions, session)
		turns := []Object{}
		for turn := range count {
			turns = append(turns, Object{"id": fmt.Sprintf("%s-%d", id, turn), "status": "completed", "items": []Object{
				{"id": "user", "role": "user", "text": fmt.Sprintf("Fixture input %d", turn)},
				{"id": "answer", "role": "assistant", "text": "## Fixture answer\n\n" + strings.Repeat("Body paragraph.\n\n", index*3+1) + "```go\nfunc example() {}\n```"},
			}})
		}
		f.histories[id] = Object{"session": session, "turns": turns}
	}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return RootWithAPI(cx, f, true)
	}, nsgotest.Size(1320, 880), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	for step := range 30 {
		id := fmt.Sprintf("switch-%d", step%3)
		selector := sessionSelector("demo-device", "codex-app", id)
		app.WaitFor(selector, 3*time.Second)
		app.Press(selector)
		app.WaitFor("id:chat-input", time.Second)
		app.WaitFor(fmt.Sprintf("id:message-%s-%d|answer", id, counts[step%3]-1), 3*time.Second)
		app.Focus("id:chat-input")
		if counts[step%3] > 1 {
			app.Scroll("id:chat-message-list", -180)
		}
		app.Settle()
	}
}

func TestDetachedConversationScrollUpdates(t *testing.T) {
	var shown, content *core.Signal[bool]
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		installScrollbars(cx)
		shown, content = core.NewSignal(cx, true), core.NewSignal(cx, false)
		held := core.NewHolder(cx, func(cx *core.Scope) core.Node {
			return core.Show{When: content.Get, Then: func(*core.Scope) core.Node {
				return core.Scroll{TestID: "held-conversation-scroll", Style: core.Style{Height: core.Pt(120)}, Children: []core.Node{
					core.View{Style: core.Style{Position: core.Sticky, Top: core.Pt(0), Height: core.Pt(24)}, Children: []core.Node{core.Text{Content: "Fixture header"}}},
					core.View{Style: core.Style{Height: core.Pt(800)}},
				}}
			}}
		})
		return core.Show{When: shown.Get, Then: func(*core.Scope) core.Node { return held.Place() }}
	}, nsgotest.Size(400, 300))
	// Retained content can finish building after its conversation has left the
	// mounted tree. Native sticky updates must not target a missing scroll view.
	app.Post(func() { shown.Set(false) })
	app.Settle()
	app.Post(func() { content.Set(true) })
	app.Settle()
	if app.Exists("id:held-conversation-scroll") {
		t.Fatal("detached content became visible")
	}
	app.Post(func() { shown.Set(true) })
	app.WaitFor("id:held-conversation-scroll", time.Second)
	app.Scroll("id:held-conversation-scroll", 100)
}

type switchPageAPI struct {
	*demoAPI
	slow string
}

func (f *switchPageAPI) ReadPage(ctx context.Context, k persistence.Session, q persistence.PageQuery) (persistence.HistoryPage, error) {
	if q.Limit == persistence.WindowSize {
		return persistence.HistoryPage{}, nil
	} // snapshot writer
	if k.ID == f.slow {
		<-ctx.Done()
		return persistence.HistoryPage{}, ctx.Err()
	}
	return persistence.HistoryPage{Transcript: outgoingTranscript(Object{"id": k.ID, "role": "assistant", "text": "Cached page for " + k.ID})}, nil
}
func (*switchPageAPI) ApplyHistory(context.Context, persistence.Session, persistence.HistoryPatch) error {
	return nil
}

func TestSwitchCommitsCachedMessagesWithSelection(t *testing.T) {
	var s *State
	var emptyFrame atomic.Bool
	slow := Object{"id": "slow", "providerId": "pi", "deviceId": "local", "title": "Slow fixture"}
	fast := Object{"id": "fast", "providerId": "pi", "deviceId": "local", "title": "Fast fixture"}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = workspaceLayoutFixture(cx)
		s.snapshots = newSnapshotWriter(s.ctx, &switchPageAPI{newDemoAPI(), "slow"})
		cx.Effect(func() {
			s.read()
			if str(s.selected, "id") == "fast" && len(s.messageRows) == 0 {
				emptyFrame.Store(true)
			}
		})
		return s.workspaceView(cx)
	}, nsgotest.Size(1100, 800))
	app.Post(func() { s.cx.Post(func() { s.selectSession(slow); s.selectSession(fast) }) })
	app.WaitFor("text:Cached page for fast", time.Second)
	app.Post(func() { s.cx.Post(func() { s.selectSession(slow) }) })
	app.WaitFor("id:transcript-loading", time.Second)
	if app.Exists("text:Cached page for fast") {
		t.Fatal("previous transcript displayed under a different conversation")
	}
	app.Post(func() { s.cx.Post(func() { s.selectSession(fast) }) })
	app.WaitFor("text:Cached page for fast", time.Second)
	if emptyFrame.Load() {
		t.Fatal("cached destination opened with an empty message list")
	}
}

func TestCachedSessionVerificationKeepsListViewport(t *testing.T) {
	ctx := context.Background()
	dao := timelineTestDAO(t)
	env := "fixture-" + persistence.Revision()
	defer dao.ForgetHistory(ctx, env)
	key := persistence.Session{Env: env, Provider: "pi", ID: "destination"}
	dst := Object{"id": key.ID, "providerId": key.Provider, "deviceId": env, "title": "Destination"}
	items := make([]Object, 80)
	for i := range items {
		items[i] = Object{"id": fmt.Sprint(i), "role": "assistant", "text": fmt.Sprintf("Cached reply %d", i)}
	}
	transcript := outgoingTranscript(items...)
	if err := dao.ApplyHistory(ctx, key, persistence.Diff("", nil, transcript)); err != nil {
		t.Fatal(err)
	}
	api := &independentStreamAPI{demoAPI: newDemoAPI(), snapshots: make(chan snapshotRequest, 4), events: make(chan Object, 4), approvalsStarted: make(chan struct{})}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = workspaceLayoutFixture(cx)
		s.target = env
		s.devices = []Object{{"id": env, "connected": true}}
		s.api = api
		s.snapshots = newSnapshotWriter(s.ctx, &scrollingHistoryAPI{newDemoAPI(), dao})
		return s.workspaceView(cx)
	}, nsgotest.Size(1100, 800))
	app.Post(func() { s.selectSession(dst) })
	const list = "id:chat-message-list"
	const row = "id:message-turn|79"
	app.WaitFor("text:Cached reply 79", 3*time.Second)
	app.WaitUntil(3*time.Second, func() bool { return len(api.snapshots) > 0 })
	height := app.Find(list).Content.Height
	id := app.Find(row).ID
	at := globalBounds(app.Find(row)).Origin.Y
	corrected := append([]Object{}, items...)
	corrected[79] = Object{"id": "79", "role": "assistant", "text": "Updated reply 79"}
	(<-api.snapshots).reply <- outgoingTranscript(corrected...)
	app.WaitFor("text:Updated reply 79", 3*time.Second)
	if app.Find(list).Content.Height != height || app.Find(row).ID != id || globalBounds(app.Find(row)).Origin.Y != at {
		t.Fatal("verification moved the cached visible row or changed the list extent")
	}
	// Scroll in short wheel gestures so each backend reports the current native offset.
	for n := 0; n < 16 && s.reading == nil; n++ {
		offset := app.Find(list).Offset.Y
		if offset < 1 {
			break
		}
		app.Scroll(list, -min(600, offset))
	}
	app.WaitUntil(3*time.Second, func() bool { return s.reading != nil && s.messageRows[0].ID == "turn|0" })
}

func TestOpeningFoldedTurnKeepsFollowupsAndAnswers(t *testing.T) {
	ctx := context.Background()
	dao := timelineTestDAO(t)
	key := persistence.Session{Env: "fixture-" + persistence.Revision(), Provider: "codex-app", ID: "dense"}
	defer dao.ForgetHistory(ctx, key.Env)
	var turns []Object
	for i, count := range []int{2, 103, 19} {
		items := make([]Object, count)
		for j := range items {
			item := Object{"id": fmt.Sprint(j), "role": "tool", "type": "commandExecution"}
			switch {
			case j == count-1:
				item["role"], item["text"] = "assistant", fmt.Sprintf("Answer %d", i)
			case j == 0 || i == 1 && (j == 8 || j == 80):
				item["role"], item["text"] = "user", fmt.Sprintf("Prompt %d/%d", i, j)
			}
			items[j] = item
		}
		turns = append(turns, Object{"id": fmt.Sprint(i), "status": "completed", "items": items})
	}
	full := Object{"turns": turns}
	if err := dao.ApplyHistory(ctx, key, persistence.Diff("", nil, full)); err != nil {
		t.Fatal(err)
	}
	page, err := dao.ReadPage(ctx, key, openingPageQuery())
	if err != nil || page.Before == nil || page.Start != (persistence.PageCursor{Turn: 1, Item: 0}) {
		t.Fatalf("opening boundary %v, continuation %v, err %v", page.Start, page.Before, err)
	}
	api := &independentStreamAPI{demoAPI: newDemoAPI(), snapshots: make(chan snapshotRequest, 1), events: make(chan Object), approvalsStarted: make(chan struct{})}
	app, s := conversationFixture(t, api, 720, nil, true)
	app.Post(func() { s.startSessionWatch() })
	app.WaitUntil(time.Second, func() bool { return len(api.snapshots) == 1 })
	req := <-api.snapshots
	app.Post(func() {
		// Slow disk delivers the opening page after verification has begun.
		s.snapshots = newSnapshotWriter(s.ctx, &scrollingHistoryAPI{newDemoAPI(), dao})
		s.applyCachedTranscript(page.View())
		s.stream.applySnapshotPreview(s.stream.epoch, outgoingTranscript(Object{"id": "fragment", "role": "assistant", "text": "fragment"}), false, false)
	})
	app.WaitFor("text:Answer 2", time.Second)
	check := func() {
		t.Helper()
		ids := make([]string, len(s.messageRows))
		for i, row := range s.messageRows {
			ids[i] = row.ID
		}
		if got := strings.Join(ids, " "); got != "1|0 1|work|1|tools|1 1|8 1|80 1|102 2|0 2|work|2|tools|1 2|18" {
			t.Fatalf("displayed rows %s", got)
		}
	}
	check()
	req.reply <- full
	app.WaitUntil(time.Second, func() bool { app.Settle(); return s.stream.verified })
	check()
}
