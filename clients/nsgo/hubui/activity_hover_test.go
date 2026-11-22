package hubui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestActivityHoverLight(t *testing.T) { testActivityHover(t, false) }
func TestActivityHoverDark(t *testing.T)  { testActivityHover(t, true) }

func TestCommandContentOnlyAppearsInsideExpandedActivity(t *testing.T) {
	script := "python3 - <<'PY'\n" + strings.Repeat("print('fixture')\n", 20) + "PY"
	tool := Object{"id": "cmd", "role": "tool", "status": "inProgress", "activities": []Object{
		{"kind": "command", "title": "运行 " + script, "detail": script},
	}}
	var appendCommentary func()
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := newState(cx, newDemoAPI())
		s.devices, s.target = []Object{{"id": "fixture", "connected": true}}, "fixture"
		later := core.NewSignal(cx, false)
		appendCommentary = func() { later.Set(true) }
		return s.activity(cx, func() Message {
			items := []Object{tool}
			if later.Get() {
				items = append(items, Object{"role": "assistant", "phase": "commentary", "text": "progress"})
			}
			return messages(Object{"turns": []Object{{"id": "t", "status": "inProgress", "items": items}}})[0]
		})
	}, nsgotest.Size(390, 640))
	if !app.Exists("text:正在运行命令") {
		t.Fatal("missing compact running label")
	}
	if app.Exists("text:"+script) || app.Exists("text:正在运行 "+script) {
		t.Fatal("collapsed command exposed its script")
	}
	app.Press("id:tool-group-toggle")
	app.Find("text:" + script)
	app.Press("id:tool-group-toggle")
	app.Post(appendCommentary)
	app.Settle()
	app.Find("text:运行命令 1")
	if app.Exists("text:"+script) || app.Exists("text:正在运行命令") {
		t.Fatal("historical command retained details or active status")
	}
}

func testActivityHover(t *testing.T, dark bool) {
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s := newState(cx, newDemoAPI())
		s.applyColors()
		s.devices, s.target = []Object{{"id": "fixture", "connected": true}}, "fixture"
		m := Message{Role: "activity", Live: true, Tools: []Object{{"id": "tool", "role": "tool", "status": "inProgress", "activities": []Object{{"kind": "read", "title": "读取文件", "detail": "fixture.go"}}}}}
		return core.View{Style: fill.Merge(core.Style{Padding: core.All(24)}), StyleFunc: func() core.Style { return core.Style{Background: palette(cx).background} }, Children: []core.Node{s.activity(cx, func() Message { return m })}}
	}, nsgotest.Size(600, 300), nsgotest.Environment(core.Environment{Dark: dark}))
	before := app.Query("text:正在读取文件")[0]
	color, id := before.TextStyle.Color, before.ID
	background := app.Find("id:tool-group-toggle").Style.Background
	app.Screenshot(fmt.Sprintf("activity-idle-dark-%t", dark))
	app.Hover("id:tool-group-toggle")
	after := app.Query("text:正在读取文件")[0]
	if after.TextStyle.Color == color || after.ID != id {
		t.Fatal("hover must illuminate the existing shimmer text")
	}
	if app.Find("id:tool-group-toggle").Style.Background != background {
		t.Fatal("hover introduced a row background")
	}
	app.Screenshot(fmt.Sprintf("activity-hover-dark-%t", dark))
	app.Press("id:tool-group-toggle")
	app.Find("text:fixture.go")
}
