package hubui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/pkg/filelink"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

type linkRoutingAPI struct {
	*downloadFixture
	routes chan string
}

func (f *linkRoutingAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.call" && str(r, "method") == "attachment.read" {
		f.routes <- target + "/" + str(r, "provider") + "/" + str(r, "session")
	}
	return f.downloadFixture.Call(ctx, method, p, target, r)
}

func TestMessageFileLinksFollowExecutionDevice(t *testing.T) {
	if nsgotest.Target() != "" {
		t.Skip("isolated file-manager and save service inspection")
	}
	cwd := t.TempDir()
	file := filepath.Join(cwd, "build file.zip")
	if err := os.WriteFile(file, []byte("local fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, target, href                          string
		attachment, ready, download, reveal, notice bool
	}{
		{"local relative", "local", "build%20file.zip", true, true, false, true, false},
		{"local absolute", "local", filepath.ToSlash(file), true, true, false, true, false},
		{"remote matching local path", "remote", filepath.ToSlash(file), true, true, true, false, false},
		{"remote Windows path", "remote", `C:\project\build.zip`, true, true, true, false, false},
		{"remote unavailable", "remote", "build%20file.zip", true, false, false, false, true},
		{"remote unexported", "remote", "build%20file.zip", false, false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &linkRoutingAPI{downloadFixture: &downloadFixture{demoAPI: newDemoAPI(), data: []byte("remote fixture")}, routes: make(chan string, 4)}
			a := f.attachment()
			a["source"] = tc.href
			if !tc.ready {
				a["state"] = "missing"
			}
			m := Message{ID: "link", Role: "assistant", Text: "[Download file](<" + tc.href + ">)"}
			if tc.attachment {
				m.Attachments = []Object{a}
			}
			var s *State
			app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
				cx.SetTheme(core.DefaultTheme())
				s = newState(cx, f)
				s.target, s.selected = tc.target, Object{"providerId": "codex-app", "id": "fixture", "cwd": cwd}
				return core.View{Style: fill, Children: []core.Node{s.message(cx, func() Message { return m }), core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) })}}
			}, nsgotest.Size(800, 600))
			link := app.Find("text:Download file")
			pointer := false
			for _, span := range link.Spans {
				pointer = pointer || span.Cursor == core.CursorPointer
			}
			if !pointer {
				t.Fatal("message link has no pointer cursor")
			}
			app.Press("text:Download file")
			if tc.download {
				app.WaitFor("id:attachment-download-save", time.Second)
				if len(app.Saves) != 1 || !bytes.Equal(app.Saves[0].Data, f.data) {
					t.Fatal("remote link did not save verified bytes")
				}
				select {
				case route := <-f.routes:
					if route != "remote/codex-app/fixture" {
						t.Fatal("wrong route", route)
					}
				default:
					t.Fatal("remote attachment was not read")
				}
			} else if f.reads.Load() != 0 || app.Exists("id:attachment-download") {
				t.Fatal("local/unavailable link downloaded")
			}
			if tc.reveal {
				if len(app.RevealedFiles) != 1 || app.RevealedFiles[0] != file {
					t.Fatal("wrong revealed path", app.RevealedFiles)
				}
			} else if len(app.RevealedFiles) != 0 {
				t.Fatal("remote path reached local file manager")
			}
			if tc.notice && s.notice == "" {
				t.Fatal("unavailable file has no feedback")
			}
		})
	}
}

func TestLocalLinkFailureAndWebLinkFeedback(t *testing.T) {
	if nsgotest.Target() != "" {
		t.Skip("isolated platform service callbacks")
	}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s = newState(cx, newDemoAPI())
		s.target, s.selected = "local", Object{"cwd": t.TempDir()}
		return core.View{Children: []core.Node{
			button(cx, "missing", "Missing", nil, "ghost", func() { s.openLink(cx, Message{}, "missing.zip") }),
			button(cx, "web", "Web", nil, "ghost", func() { s.openLink(cx, Message{}, "https://example.invalid/docs") }),
		}}
	})
	app.Press("id:missing")
	if s.error == "" {
		t.Fatal("missing local file failed silently")
	}
	app.Press("id:web")
	if len(app.OpenedURLs) != 1 || app.OpenedURLs[0] != "https://example.invalid/docs" {
		t.Fatal("web link was reinterpreted as a file")
	}
	for _, bad := range []string{"javascript:alert(1)", "file://foreign-host/private", "#anchor", "https://example.invalid/a", "data:text/plain,x"} {
		if p := filelink.Resolve(bad, t.TempDir()); p != "" {
			t.Fatalf("nonlocal reference resolved: %s", bad)
		}
	}
}

func TestMessageSourceReferenceShowsHoverPath(t *testing.T) {
	api := &linkRoutingAPI{downloadFixture: &downloadFixture{demoAPI: newDemoAPI()}, routes: make(chan string, 1)}
	a := api.attachment()
	a["source"], a["inline"] = "src/shared_connection.go:156", true
	m := Message{ID: "source", Role: "assistant", Text: "相关代码在 **[实现](src/shared_connection.go:156)**。", Attachments: []Object{a}}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s := newState(cx, api)
		s.target, s.selected = "remote", Object{"cwd": `C:\project`}
		return s.message(cx, func() Message { return m })
	}, nsgotest.Size(680, 300), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	const caption = "text:shared_connection.go (line 156)"
	const path = `text:C:\project\src\shared_connection.go (line 156)`
	app.WaitFor(caption, time.Second)
	app.Hover(caption)
	app.Advance(500 * time.Millisecond)
	app.WaitFor(path, time.Second)
	app.Press(caption)
	if len(app.RevealedFiles) != 0 || api.reads.Load() != 0 || app.Exists("id:attachments-source") {
		t.Fatal("source reference exposed a file operation")
	}
}

func TestLongSourceReferenceTooltipWraps(t *testing.T) {
	const name = "useMiniAppInstall.ts (line 268)"
	const path = `D:\code\workspace_2025\app\src\main-process\features\installation\services\hooks\useMiniAppInstall.ts (line 268)`
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s := newState(cx, newDemoAPI())
		m := Message{ID: "fixture", Role: "assistant", Text: "代码在 [实现](<D:/code/workspace_2025/app/src/main-process/features/installation/services/hooks/useMiniAppInstall.ts:268>)。"}
		return core.View{Style: core.Style{Padding: core.Edges{Top: 150, Left: 25}}, Children: []core.Node{s.message(cx, func() Message { return m })}}
	}, nsgotest.Size(1000, 300), nsgotest.Measure(), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop}))
	app.Hover("text:" + name)
	app.Advance(500 * time.Millisecond)
	app.WaitFor("text:"+path, time.Second)
	hint := globalBounds(app.Find("text:" + path))
	app.Screenshot("source-reference-wrapped-path")
	if hint.Size.Height < 36 || hint.Origin.X < 0 || hint.Origin.X+hint.Size.Width > 1000 {
		t.Fatalf("long path must wrap inside the viewport: %+v", hint)
	}
}
