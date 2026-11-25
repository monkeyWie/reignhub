package hubui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/pkg/persistence"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

const viewportList = "id:chat-message-list"

func startViewportFixture(t *testing.T, turns []Object) (*nsgotest.App, *State) {
	ctx := context.Background()
	dao := timelineTestDAO(t)
	key := persistence.Session{Env: "fixture-" + persistence.Revision(), Provider: "demo", ID: "viewport"}
	t.Cleanup(func() { dao.ForgetHistory(ctx, key.Env) })
	if err := dao.ApplyHistory(ctx, key, persistence.Diff("", nil, Object{"turns": turns})); err != nil {
		t.Fatal(err)
	}
	page, err := dao.ReadPage(ctx, key, persistence.PageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = workspaceLayoutFixture(cx)
		s.snapshots = newSnapshotWriter(s.ctx, &scrollingHistoryAPI{newDemoAPI(), dao})
		s.target = key.Env
		s.selected = Object{"id": key.ID, "providerId": key.Provider, "title": "Fixture"}
		s.transcript = page.View()
		s.rebuildMessages()
		return s.workspaceView(cx)
	}, nsgotest.Size(1320, 880), nsgotest.Measure())
	return app, s
}

// Read text geometry, including nested table and Markdown nodes. A recycled
// cell or a synthetic scroll spacer alone cannot satisfy these assertions.
func viewportTextBands(app *nsgotest.App) [3]bool {
	v := app.Find(viewportList)
	b := globalBounds(v)
	var hit [3]bool
	var walk func(*nsgotest.View)
	walk = func(n *nsgotest.View) {
		if n.Frame.Size.Height <= 0 {
			return
		}
		if n.Text != "" {
			rect := globalBounds(n)
			y := rect.Origin.Y - b.Origin.Y
			for i := range hit {
				sample := b.Size.Height * (core.Points(i) + 0.5) / 3
				hit[i] = hit[i] || y <= sample+65 && y+rect.Size.Height >= sample-65
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(v)
	return hit
}

func TestDenseCompletedTurnsFillReadingViewport(t *testing.T) {
	var turns []Object
	for i := range 18 {
		items := []Object{{"id": "u", "role": "user", "text": fmt.Sprintf("Fixture question %d", i)}}
		for j := range 160 {
			item := Object{"id": fmt.Sprint(j), "role": "tool", "type": "commandExecution", "activities": []Object{{"kind": "read", "title": "Read fixture"}}}
			if j%4 == 0 {
				item["type"] = "functionCallOutput"
			}
			if j%31 == 0 {
				item["role"] = "assistant"
				item["phase"] = "commentary"
				item["text"] = "Fixture progress"
			}
			items = append(items, item)
		}
		items = append(items, Object{"id": "answer", "role": "assistant", "text": fmt.Sprintf("Fixture answer %d", i)})
		turns = append(turns, Object{"id": fmt.Sprint(i), "status": "completed", "items": items})
	}
	app, _ := startViewportFixture(t, turns)
	app.WaitFor("text:Fixture answer 17", 3*time.Second)
	check := func() {
		t.Helper()
		app.WaitUntil(3*time.Second, func() bool { return viewportTextBands(app) == [3]bool{true, true, true} })
		if nsgotest.Target() != "" {
			checkViewportPixels(t, app)
		}
	}
	check()
	for _, dy := range []core.Points{-420, -500, -540, 280, 460, -500} {
		v := app.Find(viewportList)
		maxOffset := v.Content.Height - v.Frame.Size.Height
		d := min(maxOffset-v.Offset.Y, max(-v.Offset.Y, dy))
		if d != 0 {
			app.Scroll(viewportList, d)
		}
		check()
	}
}

func TestMixedMarkdownScrollStopsWithTextInView(t *testing.T) {
	var turns []Object
	for i := range 15 {
		answer := fmt.Sprintf("Fixture answer %d.\n\n", i)
		if i%3 != 0 {
			answer += strings.Repeat("一些用于测试的段落文字。用于模拟长回复换行，以及有序的段落。\n\n| Column A | Column B |\n|---|---|\n| sample text | fixture content |\n| more text | more content |\n\n", 3+(i%3)*3)
		}
		turns = append(turns, Object{"id": fmt.Sprint(i), "status": "completed", "items": []Object{{"id": "u", "role": "user", "text": fmt.Sprintf("Fixture question %d", i)}, {"id": "a", "role": "assistant", "text": answer}}})
	}
	app, _ := startViewportFixture(t, turns)
	// The final answer spans multiple viewports; the question before it can
	// already be outside the mounted window when the list settles at the end.
	app.WaitFor("id:message-body-14|a", 4*time.Second)
	for _, dy := range []core.Points{-420, -710, -480, -820, 400, 720, -490, 780} {
		app.Scroll(viewportList, dy)
		app.WaitUntil(time.Second, func() bool { return viewportTextBands(app) == [3]bool{true, true, true} })
		if nsgotest.Target() != "" {
			checkViewportPixels(t, app)
		}
	}
}

func checkViewportPixels(t *testing.T, app *nsgotest.App) {
	t.Helper()
	img := app.Capture()
	v := globalBounds(app.Find(viewportList))
	root := app.Find("id:conversation").Parent
	for root.Parent != nil {
		root = root.Parent
	}
	sx := float64(img.Bounds().Dx()) / float64(root.Frame.Size.Width)
	sy := float64(img.Bounds().Dy()) / float64(root.Frame.Size.Height)
	for band := range 3 {
		left, right := int(float64(v.Origin.X+60)*sx), int(float64(v.Origin.X+v.Size.Width-40)*sx)
		top, bottom := int(float64(v.Origin.Y+v.Size.Height*core.Points(band)/3+20)*sy), int(float64(v.Origin.Y+v.Size.Height*core.Points(band+1)/3-35)*sy)
		hist := [256]int{}
		for y := top; y < bottom; y += 2 {
			for x := left; x < right; x += 2 {
				_, g, _, _ := img.At(x, y).RGBA()
				hist[g>>8]++
			}
		}
		bg, ink := 0, 0
		for i, n := range hist {
			if n > hist[bg] {
				bg = i
			}
		}
		for i, n := range hist {
			if i < bg-40 || i > bg+40 {
				ink += n
			}
		}
		if ink < 16 {
			t.Fatalf("no painted text in band %d of the resting viewport (ink=%d, bounds=%v)", band, ink, v)
		}
	}
}
