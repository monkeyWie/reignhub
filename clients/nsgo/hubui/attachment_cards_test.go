package hubui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func attachmentCardsFixture(t *testing.T, dark bool) (*nsgotest.App, *State, *downloadFixture) {
	return attachmentCardsRoleFixture(t, dark, "assistant")
}

func attachmentCardsRoleFixture(t *testing.T, dark bool, role string, options ...nsgotest.Option) (*nsgotest.App, *State, *downloadFixture) {
	t.Helper()
	var buffer bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 80, 60))
	for y := range 60 {
		for x := range 80 {
			img.Set(x, y, color.RGBA{R: 30, G: 140, B: 100, A: 255})
		}
	}
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	f := &downloadFixture{demoAPI: newDemoAPI(), data: buffer.Bytes(), gate: make(chan struct{})}
	var s *State
	opts := append([]nsgotest.Option{nsgotest.Size(820, 900), nsgotest.Environment(core.Environment{OS: core.OSMacOS, Idiom: core.IdiomDesktop, Dark: dark})}, options...)
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s = newState(cx, f)
		s.applyColors()
		s.target, s.selected = "fixture", Object{"id": "session", "providerId": "codex-app"}
		attachments := []Object{}
		for i := range 5 {
			a := f.attachment()
			a["id"], a["source"], a["kind"] = fmt.Sprint(i), fmt.Sprintf("attachment://fixture/%d", i), "file"
			a["name"] = strings.Repeat("long-filename-", 12) + ".apk"
			if i < 2 {
				a["kind"], a["name"] = "image", fmt.Sprintf("photo-%d.png", i)
				s.images[s.imageKey(a, false)] = f.data
			}
			if i == 4 {
				a["inline"] = true
			}
			attachments = append(attachments, a)
		}
		m := Message{ID: "cards", Role: role, Text: "Attachments", Attachments: attachments}
		// The fixture parent accepts clicks so browser tests can exercise a
		// real pointer activation on otherwise noninteractive file content.
		return core.View{Style: fill, OnPress: func(core.PressEvent) {}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).background} }, Children: []core.Node{
			s.message(cx, func() Message { return m }),
			core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) }),
		}}
	}, opts...)
	return app, s, f
}

func TestAttachmentCardsSeparatePreviewDownloadAndUnsupportedFiles(t *testing.T) {
	app, s, f := attachmentCardsFixture(t, false)
	app.WaitFor("id:download-4", time.Second) // Inline files still have a download action.
	app.Press("id:attachment-name-2")
	app.Settle()
	if app.Exists("id:image-preview") || app.Exists("id:attachment-download") || f.reads.Load() != 0 {
		t.Fatal("unsupported file activation started preview or download")
	}
	if app.Find("id:attachment-2").Props.Role == core.RoleButton {
		t.Fatal("unsupported preview advertised an interactive card")
	}
	app.Press("id:attachment-name-0")
	app.WaitFor("id:image-preview", time.Second)
	if app.Exists("id:attachment-download") || f.reads.Load() != 0 {
		t.Fatal("image preview started a download")
	}
	app.Press("id:preview-close")
	for _, id := range []string{"0", "2", "4"} {
		if nsgotest.Target() == "macos" {
			// Native automation cannot reliably focus hidden contextual controls;
			// exercise the secondary-click route here and the icon in the browser.
			app.RightClick("id:attachment-" + id)
			app.Press("id:attachment-download-action")
		} else {
			app.Focus("id:download-" + id)
			app.Press("id:download-" + id)
		}
		app.WaitFor("id:attachment-download", time.Second)
		if app.Exists("id:image-preview") {
			t.Fatal("download activation also opened image preview")
		}
		app.Press("id:attachment-download-cancel")
	}
	app.Post(func() {
		s.cx.Post(func() {
			s.openLink(s.cx, Message{Attachments: []Object{{"id": "2", "kind": "file", "source": "attachment://file", "state": "ready", "name": "file"}}}, "attachment://file")
		})
	})
	app.Settle()
	if !app.Exists("id:attachment-download") {
		t.Fatal("explicit remote attachment link did not start download")
	}
}

func TestAttachmentContextualDownloadAgentLight(t *testing.T) {
	testAttachmentContextualDownload(t, "assistant", false)
}
func TestAttachmentContextualDownloadAgentDark(t *testing.T) {
	testAttachmentContextualDownload(t, "assistant", true)
}
func TestAttachmentContextualDownloadUserLight(t *testing.T) {
	testAttachmentContextualDownload(t, "user", false)
}
func TestAttachmentContextualDownloadUserDark(t *testing.T) {
	testAttachmentContextualDownload(t, "user", true)
}

func testAttachmentContextualDownload(t *testing.T, role string, dark bool) {
	if nsgotest.Target() == "macos" {
		t.Skip("native harness hover is unreliable; browser exercises pointer behavior")
	}
	app, _, _ := attachmentCardsRoleFixture(t, dark, role)
	for _, id := range []string{"0", "2"} {
		actions, icon := "id:attachment-actions-"+id, "id:download-"+id
		if app.Find(actions).Style.Opacity != 0 {
			t.Fatal("download action is visible before hover/focus")
		}
		before := globalBounds(app.Find("id:attachment-" + id))
		app.Hover("id:attachment-" + id)
		app.WaitUntil(time.Second, func() bool { return app.Find(actions).Style.Opacity > .5 })
		colors := func() []core.Color {
			var result []core.Color
			for _, op := range app.Find(icon).Children[0].Ops {
				if op.Color != 0 {
					result = append(result, op.Color)
				}
			}
			return result
		}
		muted := colors()
		app.Hover(icon)
		app.Advance(500 * time.Millisecond)
		app.WaitUntil(time.Second, func() bool { return !slices.Equal(colors(), muted) })
		if app.Find(icon).Style.Background != 0 || globalBounds(app.Find("id:attachment-"+id)) != before {
			t.Fatal("download hover added an icon background or shifted the card")
		}
		if id == "0" {
			app.WaitFor("text:下载 photo-0.png", time.Second)
			app.Screenshot(fmt.Sprintf("attachment-download-hover-%s-dark-%t", role, dark))
		}
		app.Press(icon)
		app.WaitFor("id:attachment-download", time.Second)
		if app.Exists("id:image-preview") {
			t.Fatal("download action also opened the preview")
		}
		app.Press("id:attachment-download-cancel")
		app.HoverOut()
	}
	app.RightClick("id:attachment-0")
	app.WaitFor("id:attachment-download-action", time.Second)
	app.Press("id:attachment-download-action")
	app.WaitFor("id:attachment-download", time.Second)
	if app.Exists("id:image-preview") {
		t.Fatal("secondary click opened preview")
	}
	app.Press("id:attachment-download-cancel")
}

func TestAttachmentDownloadTouchMenu(t *testing.T) {
	if nsgotest.Target() != "" {
		t.Skip("desktop automation does not send a finger; headless phone fixture covers touch")
	}
	for _, role := range []string{"assistant", "user"} {
		t.Run(role, func(t *testing.T) {
			app, _, f := attachmentCardsRoleFixture(t, false, role, nsgotest.Size(390, 844), nsgotest.Environment(core.Environment{OS: core.OSAndroid, Idiom: core.IdiomPhone}))
			for _, id := range []string{"0", "2"} {
				app.LongPress("id:attachment-" + id)
				app.WaitFor("id:attachment-download-action", time.Second)
				if app.Exists("id:image-preview") || app.Exists("id:attachment-download") {
					t.Fatal("long press activated the attachment before selecting an action")
				}
				app.Press("id:attachment-download-action")
				app.WaitFor("id:attachment-download", time.Second)
				app.Press("id:attachment-download-cancel")
				app.WaitUntil(time.Second, func() bool { return f.active.Load() == 0 })
			}
		})
	}
}

func TestAttachmentCardsResponsiveGridLight(t *testing.T) { testAttachmentCardsGrid(t, false) }
func TestAttachmentCardsResponsiveGridDark(t *testing.T)  { testAttachmentCardsGrid(t, true) }

func testAttachmentCardsGrid(t *testing.T, dark bool) {
	app, s, _ := attachmentCardsFixture(t, dark)
	imageID := app.Find("role:image photo-0.png").ID
	cardID := app.Find("id:attachment-0").ID
	for step, width := range []core.Points{820, 564, 563, 372, 371, 320, 170, 820} {
		if nsgotest.Target() == "" || nsgotest.Target() == "web" {
			app.Resize(width, 900)
		}
		app.Settle()
		grid := globalBounds(app.Find("id:attachments-cards"))
		columns := max(1, int((grid.Size.Width+attachmentCardGap)/(attachmentCardMinimum+attachmentCardGap)))
		first := globalBounds(app.Find("id:attachment-0"))
		for i := range 5 {
			card := globalBounds(app.Find("id:attachment-" + fmt.Sprint(i)))
			if card.Size.Width <= 0 || card.Origin.X < grid.Origin.X-.5 || card.Origin.X+card.Size.Width > grid.Origin.X+grid.Size.Width+.5 {
				t.Fatalf("card overflow at width %g: grid=%v card=%v", width, grid, card)
			}
			if math.Abs(float64(card.Size.Width-first.Size.Width)) > 1 {
				t.Fatalf("unequal card columns at width %g", width)
			}
			if i < columns && math.Abs(float64(card.Origin.Y-first.Origin.Y)) > 1 || i == columns && card.Origin.Y <= first.Origin.Y {
				t.Fatalf("incorrect column transition at width %g: columns=%d card=%v", width, columns, card)
			}
			if s.cx.Env().Desktop() {
				download := globalBounds(app.Find("id:download-" + fmt.Sprint(i)))
				if download.Origin.X < card.Origin.X || download.Origin.X+download.Size.Width > card.Origin.X+card.Size.Width+.5 {
					t.Fatalf("download control clipped at width %g", width)
				}
			}
		}
		if app.Find("id:attachment-0").ID != cardID || app.Find("role:image photo-0.png").ID != imageID {
			t.Fatal("resizing remounted the attachment or decoded image")
		}
		if width == 320 || step == 0 {
			app.Screenshot(fmt.Sprintf("attachment-cards-%g-dark-%t", width, dark))
		}
	}
}

func TestUserMessageAttachmentPlacement(t *testing.T) { testMessageAttachmentPlacement(t, "user") }
func TestAgentMessageAttachmentPlacement(t *testing.T) {
	testMessageAttachmentPlacement(t, "assistant")
}
func testMessageAttachmentPlacement(t *testing.T, role string) {
	app, _, _ := attachmentCardsRoleFixture(t, false, role)
	for _, width := range []core.Points{820, 320} {
		if nsgotest.Target() == "" || nsgotest.Target() == "web" {
			app.Resize(width, 900)
		}
		app.Settle()
		body := globalBounds(app.Find("id:message-body-cards"))
		image := globalBounds(app.Find("id:attachment-0"))
		file := globalBounds(app.Find("id:attachment-2"))
		if file.Origin.Y < body.Origin.Y+body.Size.Height {
			t.Fatal("file attachments must follow the text")
		}
		if role == "user" {
			if image.Origin.Y+image.Size.Height > body.Origin.Y+.5 || image.Size.Width > 89 || image.Size.Height > 89 {
				t.Fatalf("user image is not a compact preview above the bubble: %v %v", image, body)
			}
			images := globalBounds(app.Find("id:user-images-cards"))
			last := globalBounds(app.Find("id:attachment-1"))
			if math.Abs(float64(last.Origin.X+last.Size.Width-images.Origin.X-images.Size.Width)) > 1 {
				t.Fatal("user previews are not right aligned")
			}
		} else if image.Origin.Y < body.Origin.Y+body.Size.Height {
			t.Fatal("Agent image appears above its text")
		}
		app.Screenshot(fmt.Sprintf("message-attachments-%s-%g", role, width))
	}
}
