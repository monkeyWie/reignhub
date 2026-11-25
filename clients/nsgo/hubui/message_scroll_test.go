package hubui

import (
	"fmt"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestKeyboardResizeIntermediateOffsetsDoNotLeaveLatest(t *testing.T) {
	for _, layoutFirst := range []bool{false, true} {
		a := messageScrollAnchor{following: true}
		a.layout(core.Size{Width: 390, Height: 600})
		for _, height := range []core.Points{300, 420, 600} {
			size := core.Size{Width: 390, Height: height}
			if layoutFirst {
				a.layout(size)
			}
			// Native clamping can report a position away from the bottom
			// before the anchoring scroll reaches the platform.
			a.scroll(core.ScrollEvent{Size: size, ContentSize: core.Size{Height: 2000}, Offset: core.Point{Y: 1100}})
			if !a.following {
				t.Fatal("keyboard resize was mistaken for reading history")
			}
			a.layout(size)
			a.scroll(core.ScrollEvent{Size: size, ContentSize: core.Size{Height: 2000}, Offset: core.Point{Y: 2000 - height}})
			if a.restoring || !a.following {
				t.Fatal("bottom restoration did not finish")
			}
		}
		// A real drag interrupts restoration immediately.
		a.layout(core.Size{Width: 390, Height: 300})
		a.dragging, a.restoring = true, false
		a.scroll(core.ScrollEvent{Size: a.size, ContentSize: core.Size{Height: 2000}, Offset: core.Point{Y: 900}})
		a.layout(core.Size{Width: 390, Height: 600})
		if a.following || a.restoring {
			t.Fatal("resize pulled the reader out of history")
		}
	}
}

func TestInitialMeasurementsAndProgressiveContentKeepLatestIntent(t *testing.T) {
	a := messageScrollAnchor{following: true}
	a.scroll(core.ScrollEvent{ContentSize: core.Size{Height: 2400}})
	if !a.following {
		t.Fatal("unlaid page lost latest intent")
	}
	size := core.Size{Width: 390, Height: 600}
	a.layout(size)
	if !a.following {
		t.Fatal("first viewport must keep latest intent")
	}
	// Android can report the unadjusted offset while first history and measured
	// Markdown replace the empty/estimated list. Neither event is user input.
	for _, height := range []core.Points{900, 2400, 3600} {
		a.scroll(core.ScrollEvent{Size: size, ContentSize: core.Size{Height: height}, Offset: core.Point{Y: 0}})
		if !a.following {
			t.Fatal("initial content was mistaken for a history scroll")
		}
		a.scroll(core.ScrollEvent{Size: size, ContentSize: core.Size{Height: height}, Offset: core.Point{Y: height - size.Height}})
	}
	// An actual user drag must win, including when another row measures.
	a.dragging = true
	a.restoring = false
	a.scroll(core.ScrollEvent{Size: size, ContentSize: core.Size{Height: 3800}, Offset: core.Point{Y: 900}})
	a.dragging = false
	a.scroll(core.ScrollEvent{Size: size, ContentSize: core.Size{Height: 4000}, Offset: core.Point{Y: 900}})
	if a.following || a.restoring {
		t.Fatal("measurement pulled a reader out of history")
	}
}

func TestStaleScrollCannotUndoHistoryNavigation(t *testing.T) {
	a := messageScrollAnchor{size: core.Size{Width: 900, Height: 500}, content: core.Size{Height: 2000}}
	// An old bottom report may arrive after an explicit history jump. It is
	// still awaiting the framework's pending scroll and is not user intent.
	a.scroll(core.ScrollEvent{Size: a.size, ContentSize: a.content, Offset: core.Point{Y: 1500}, Stale: true})
	if a.following {
		t.Fatal("stale bottom report undid history navigation")
	}
	a.scroll(core.ScrollEvent{Size: a.size, ContentSize: a.content, Offset: core.Point{Y: 900}})
	if a.following || a.restoring {
		t.Fatal("settled history position changed latest intent")
	}
}

func TestFirstWheelInterruptsLatestRestoration(t *testing.T) {
	a := messageScrollAnchor{following: true}
	report := core.ScrollEvent{
		Size: core.Size{Height: 600}, ContentSize: core.Size{Height: 2000},
		Offset: core.Point{Y: 1200},
	}
	a.scroll(report)
	if !a.following || !a.restoring {
		t.Fatal("initial measurement must keep following")
	}
	// A mouse wheel does not emit a drag-start event on macOS.
	report.Offset.Y -= 65
	if direction := a.scroll(report); direction != -1 || a.following || a.restoring {
		t.Fatalf("first wheel step did not start reading history: direction=%d, following=%t, restoring=%t", direction, a.following, a.restoring)
	}
}

func TestHistoryPageAlignmentDoesNotReverseScroll(t *testing.T) {
	var a messageScrollAnchor
	report := core.ScrollEvent{
		Size: core.Size{Height: 600}, ContentSize: core.Size{Height: 2000},
		Offset: core.Point{Y: 120},
	}
	a.scroll(report)
	a.rebasing = true
	// The page may have the same measured height as the one it replaced.
	report.Offset.Y = 700
	report.Stale = true
	a.scroll(report)
	report.Stale = false
	if direction := a.scroll(report); direction != 0 {
		t.Fatalf("page alignment was reported as input in direction %d", direction)
	}
	report.Offset.Y -= 65
	if direction := a.scroll(report); direction != -1 {
		t.Fatalf("next wheel step reported direction %d, want -1", direction)
	}
}

func TestLatestScrollRecoversFullyClippedViewport(t *testing.T) {
	if nsgotest.Target() != "" {
		t.Skip("injects native scroll reports using the test renderer")
	}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := newState(cx, newDemoAPI())
		s.width.Set(1100)
		s.target, s.selected = "fixture", Object{"id": "fixture", "providerId": "fixture"}
		items := make([]Object, 50)
		for i := range items {
			items[i] = Object{"id": fmt.Sprint(i), "role": "assistant", "text": fmt.Sprintf("reply %d", i)}
		}
		s.transcript = Object{"turns": []Object{{"id": "t", "items": items}}}
		s.rebuildMessages()
		return s.conversation(cx, false)
	}, nsgotest.Size(1100, 800))
	app.WaitFor("text:reply 49", time.Second)
	v := app.Find(viewportList)
	before := v.Applied
	app.ScrollTo(viewportList, core.Point{Y: v.Content.Height + 50})
	app.Advance(40 * time.Millisecond)
	v = app.Find(viewportList)
	if v.Applied == before || v.Offset.Y >= v.Content.Height {
		t.Fatalf("fully clipped viewport did not recover: offset=%v, height=%v", v.Offset.Y, v.Content.Height)
	}
	app.WaitFor("text:reply 49", time.Second)
}

func TestHistoryScrollCorrectsRepeatedOvershootOnce(t *testing.T) {
	if nsgotest.Target() != "" {
		t.Skip("injects native scroll reports using the test renderer")
	}
	transcript := func(count int) Object {
		items := make([]Object, count)
		for i := range items {
			items[i] = Object{"id": fmt.Sprint(i), "role": "assistant", "text": fmt.Sprintf("reply %d", i)}
		}
		return Object{"turns": []Object{{"id": "t", "items": items}}}
	}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := newState(cx, newDemoAPI())
		s.width.Set(1100)
		s.target, s.selected = "fixture", Object{"id": "fixture", "providerId": "fixture"}
		s.transcript = transcript(100)
		s.reading = transcript(50)
		s.rebuildMessages()
		return s.conversation(cx, false)
	}, nsgotest.Size(1100, 800))
	app.DragScroll(viewportList, core.Point{Y: 1200}, 0)
	app.WaitFor("id:jump-to-latest", time.Second)
	reportOldEnd := func() {
		v := app.Find(viewportList)
		app.ScrollTo(viewportList, core.Point{Y: v.Content.Height - v.Frame.Size.Height + 43})
		app.Advance(40 * time.Millisecond)
	}
	// Simulate a native offset arriving after the reading window shrinks.
	initial := app.Find(viewportList).Applied
	reportOldEnd()
	before := app.Find(viewportList).Applied
	if before == initial {
		t.Fatal("the first out-of-range offset did not trigger a correction")
	}
	for range 5 {
		reportOldEnd()
	}
	if after := app.Find(viewportList).Applied; after != before {
		t.Fatalf("duplicate out-of-range reports issued %d more scroll commands", after-before)
	}
	app.Press("id:jump-to-latest")
	app.WaitFor("text:reply 99", time.Second)
	app.WaitForGone("id:jump-to-latest", time.Second)
}
