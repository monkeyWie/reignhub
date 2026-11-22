package hubui

import (
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestMessageTableOverflowThumbRevealsLastColumn(t *testing.T) {
	wide := "| 路径 | 地点 | 连接方式 | 最后一列 |\n|---|---|---|---|\n| 两端在同一网络中 | 从控制端发起新的连接 | 通过之前保存的地址恢复会话 | 独立节点地址 |"
	var message *core.Signal[string]
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := newState(cx, newDemoAPI())
		s.applyColors()
		installScrollbars(cx)
		message = core.NewSignal(cx, wide)
		return core.View{Style: fill.Merge(core.Style{Padding: core.All(20), MaxWidth: core.Pt(360)}), StyleFunc: func() core.Style { return core.Style{Background: palette(cx).background} }, Children: []core.Node{s.message(cx, func() Message {
			return Message{ID: "table", Role: "assistant", Text: message.Get()}
		})}}
	}, nsgotest.Size(360, 640), nsgotest.Environment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone}), nsgotest.Measure())
	const thumb = "label:水平滚动条"
	app.Advance(time.Second)
	bar := app.FindMounted(thumb)
	last := app.FindMounted("text:最后一列")
	bounds := globalBounds(bar)
	cell := globalBounds(last)
	if bar.Props.PointerEvents == core.PointerEventsNone || bounds.Size.Width > 320 || cell.Origin.X+cell.Size.Width <= bounds.Origin.X+bounds.Size.Width {
		t.Fatalf("wide table did not expose a viewport-sized scrollbar at rest: bar=%+v last=%+v events=%v", bounds, cell, bar.Props.PointerEvents)
	}
	app.Screenshot("message-table-overflow")
	// Pressing the far end of the track must bring the rightmost cell into view.
	app.PressAt(thumb, core.Point{X: bounds.Size.Width - 3, Y: bounds.Size.Height / 2})
	app.WaitUntil(time.Second, func() bool {
		cell := globalBounds(app.FindMounted("text:最后一列"))
		return cell.Origin.X+cell.Size.Width <= bounds.Origin.X+bounds.Size.Width+1
	})
	app.Post(func() { message.Set("| A | B |\n|---|---|\n| 1 | 2 |") })
	app.WaitFor("text:B", time.Second)
	app.Advance(time.Second)
	if bar := app.FindMounted(thumb); bar.Props.PointerEvents != core.PointerEventsNone {
		t.Fatal("a table that fits kept an interactive scrollbar")
	}
}
