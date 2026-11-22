//go:build !js

package hubui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/monkeyWie/reignhub/internal/assets"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type attachmentFixture struct {
	*demoAPI
	store     *assets.Store
	finished  atomic.Bool
	cache     *assets.Cache
	skipCache atomic.Bool
}

type previewUploadAPI struct {
	*attachmentFixture
	writes chan struct{}
	fail   atomic.Bool
}

func (f *previewUploadAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.call" && str(r, "method") == "attachment.write" {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.writes:
		}
		if f.fail.Load() {
			return nil, fmt.Errorf("fixture: upload unavailable")
		}
	}
	return f.attachmentFixture.Call(ctx, method, p, target, r)
}

func TestBitmapPreviewBeforeUploadAndRetryRecovery(t *testing.T) {
	f := &previewUploadAPI{attachmentFixture: &attachmentFixture{demoAPI: newDemoAPI(), store: assets.New(t.TempDir())}, writes: make(chan struct{})}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = newState(cx, f)
		s.applyColors()
		s.target, s.selected = "fixture", Object{"id": "session", "providerId": "codex-app"}
		s.devices = []Object{{"id": "fixture", "connected": true}}
		return core.View{Style: fill.Merge(core.Style{Padding: core.All(20)}), StyleFunc: func() core.Style { return core.Style{Background: palette(cx).background} }, Children: []core.Node{
			button(cx, "paste-bitmap", "Paste image", nil, "ghost", func() {
				s.paste(cx, core.PasteEvent{Files: []core.PastedFile{{MIME: "image/bmp", Data: clipboardBitmapFixture()}}})
			}),
			s.uploadPreviews(cx), core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) }),
		}}
	}, nsgotest.Size(390, 700))
	app.Press("id:paste-bitmap")
	app.WaitFor("id:upload-image-clipboard.png", 3*time.Second)
	image := app.Find("id:upload-image-clipboard.png")
	if _, err := png.Decode(bytes.NewReader(image.Image.Source.Data)); err != nil {
		t.Fatal("preview is not PNG", err)
	}
	if f.finished.Load() {
		t.Fatal("fixture must still be uploading")
	}
	app.Press("id:preview-upload-clipboard.png")
	app.WaitFor("id:preview-image", time.Second)
	if _, err := png.Decode(bytes.NewReader(app.Find("id:preview-image").Image.Source.Data)); err != nil {
		t.Fatal("pending preview lost local bytes", err)
	}
	app.Screenshot("bitmap-preview-before-upload")
	app.Press("id:preview-close")
	f.fail.Store(true)
	close(f.writes)
	app.WaitFor("id:retry-upload-clipboard.png", 4*time.Second)
	if app.Exists("id:upload-progress-clipboard.png") || app.Exists("id:upload-bar-clipboard.png") {
		t.Fatal("failure still shows upload progress")
	}
	card, retry := globalBounds(app.Find("id:preview-upload-clipboard.png")), globalBounds(app.Find("id:retry-upload-clipboard.png"))
	if retry.Origin.X+retry.Size.Width/2 != card.Origin.X+card.Size.Width/2 {
		t.Fatalf("retry not centered beneath image: %v %v", card, retry)
	}
	app.Screenshot("upload-failed-centered-retry")
	f.fail.Store(false)
	app.Press("id:retry-upload-clipboard.png")
	waitForReadyUploads(app, s, 1)
	if app.Find("id:upload-image-clipboard.png").ID != image.ID {
		t.Fatal("upload retry remounted its image")
	}
	a, _, err := f.store.Input(assets.Scope("codex-app", "session"), s.uploads[0].ID)
	if err != nil || a.MIME != "image/png" {
		t.Fatalf("host did not receive PNG: %+v %v", a, err)
	}
}

func TestBitmapPreparationStaysWithItsDraftAfterNavigation(t *testing.T) {
	f := &previewUploadAPI{attachmentFixture: &attachmentFixture{demoAPI: newDemoAPI(), store: assets.New(t.TempDir())}, writes: make(chan struct{})}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s = newState(cx, f)
		s.target, s.selected = "fixture", Object{"id": "original", "providerId": "codex-app"}
		s.devices = []Object{{"id": "fixture", "connected": true}}
		return core.View{Style: fill, Children: []core.Node{
			button(cx, "paste-and-navigate", "Paste and navigate", nil, "ghost", func() {
				s.addUpload("clipboard.bmp", "image/bmp", clipboardBitmapFixture())
				s.rememberDraft()
				s.selected = Object{"id": "other", "providerId": "codex-app"}
				s.uploads = nil
				s.changed()
			}),
			button(cx, "restore-original", "Restore draft", nil, "ghost", func() {
				s.selected = Object{"id": "original", "providerId": "codex-app"}
				s.uploads = s.drafts[s.key()].Uploads
				s.changed()
			}), s.uploadPreviews(cx),
		}}
	})
	app.Press("id:paste-and-navigate")
	close(f.writes)
	app.WaitUntil(3*time.Second, f.finished.Load)
	if app.Exists("id:upload-clipboard.png") || len(s.uploads) != 0 {
		t.Fatal("prepared image leaked into the current session")
	}
	app.Press("id:restore-original")
	app.Find("id:upload-image-clipboard.png")
	if len(s.uploads) != 1 || s.uploads[0].Session != "original" || s.uploads[0].MIME != "image/png" {
		t.Fatal("original draft lost its normalized image")
	}
}

func (f *attachmentFixture) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if f.cache != nil {
		switch method {
		case "client.assetCache.put":
			b, err := base64.StdEncoding.DecodeString(str(p, "data"))
			if err != nil {
				return nil, err
			}
			return nil, f.cache.Put(target, str(p, "key"), b)
		case "client.assetCache.get":
			if f.skipCache.Load() {
				return nil, nil
			}
			b, err := f.cache.Get(target, str(p, "key"))
			if err != nil || len(b) == 0 {
				return nil, err
			}
			return Object{"data": base64.StdEncoding.EncodeToString(b)}, nil
		}
	}
	if method == "client.call" && strings.HasPrefix(str(r, "method"), "attachment.") {
		b, _ := json.Marshal(r["params"])
		var params assets.Params
		_ = json.Unmarshal(b, &params)
		v, e := f.store.Handle(ctx, assets.Scope(str(r, "provider"), str(r, "session")), str(r, "method"), params)
		if e != nil {
			return nil, e
		}
		if str(r, "method") == "attachment.finish" {
			f.finished.Store(true)
		}
		b, _ = json.Marshal(v)
		var out any
		_ = json.Unmarshal(b, &out)
		return out, nil
	}
	return f.demoAPI.Call(ctx, method, p, target, r)
}
func TestClipboardUploadAndImageGallery(t *testing.T) {
	f := &attachmentFixture{demoAPI: newDemoAPI(), store: assets.New(t.TempDir()), cache: &assets.Cache{Root: t.TempDir()}}
	img := image.NewRGBA(image.Rect(0, 0, 128, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 128; x++ {
			img.Set(x, y, color.RGBA{R: 80, G: 120, B: 200, A: 255})
		}
	}
	draw.Draw(img, image.Rect(40, 30, 88, 50), image.NewUniform(color.RGBA{R: 235, G: 154, B: 50, A: 255}), image.Point{}, draw.Src)
	var buffer bytes.Buffer
	_ = png.Encode(&buffer, img)
	photo := buffer.Bytes()
	var thumb bytes.Buffer
	if err := jpeg.Encode(&thumb, img.SubImage(image.Rect(0, 0, 32, 20)), &jpeg.Options{Quality: 50}); err != nil {
		t.Fatal(err)
	}
	var refresh func()
	var pasteFixture, publishFixture func()
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s := newState(cx, f)
		refresh = func() { cx.Post(s.changed) }
		s.target = "fixture"
		s.selected = Object{"id": "session", "providerId": "codex-app", "title": "附件测试"}
		s.devices = []Object{{"id": "fixture", "connected": true}}
		s.setTranscript(Object{"turns": []Object{}})
		pasteFixture = func() {
			cx.Post(func() {
				s.paste(cx, core.PasteEvent{Files: []core.PastedFile{{Name: "fixture.png", MIME: "image/png", Data: photo}}})
			})
		}
		publishFixture = func() {
			cx.Post(func() {
				if len(s.uploads) == 0 || s.uploads[0].Status != "ready" {
					return
				}
				a, _, e := f.store.Input(assets.Scope("codex-app", "session"), s.uploads[0].ID)
				if e != nil {
					panic(e)
				}
				b, _ := json.Marshal(a)
				meta := Object{}
				_ = json.Unmarshal(b, &meta)
				other := copyObject(meta)
				other["id"] = "second"
				s.images[s.key()+"|second|true"] = thumb.Bytes()
				f.skipCache.Store(true)
				s.setTranscript(Object{"turns": []Object{{"id": "t", "status": "completed", "items": []Object{{"id": "u", "role": "user", "text": "两张图片", "attachments": []Object{meta, other}}}}}})
			})
		}
		return core.View{Style: fill, Children: []core.Node{s.conversation(cx, false), core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) })}}
	}, nsgotest.Size(1000, 800))
	if nsgotest.Target() == "" {
		app.Paste("id:chat-input", core.PasteEvent{Files: []core.PastedFile{{Name: "fixture.png", MIME: "image/png", Data: photo}}})
	} else {
		pasteFixture()
	}

	app.WaitFor("id:upload-fixture.png", 3*time.Second)
	uploadImage := app.Find("role:image fixture.png").ID
	app.WaitUntil(3*time.Second, func() bool { return f.finished.Load() && !app.Exists("id:upload-progress-fixture.png") })
	for range 3 {
		refresh()
		app.Settle()
		if app.Find("role:image fixture.png").ID != uploadImage {
			t.Fatal("progress or polling remounted the upload image")
		}
	}
	publishFixture()
	app.WaitFor("id:image-second", 3*time.Second)
	app.Press("id:image-second")
	app.Find("id:image-preview")
	app.Find("text:2 / 2")
	app.WaitFor("id:preview-retry", 3*time.Second)
	if !bytes.Equal(app.Find("id:preview-image").Image.Source.Data, thumb.Bytes()) {
		t.Fatal("lost visible thumbnail while original was unavailable")
	}
	f.skipCache.Store(false)
	cached, cacheErr := f.cache.Get("fixture", originalImageKey("codex-app", "session", hashBytes(photo)))
	if cacheErr != nil || !bytes.Equal(cached, photo) {
		t.Fatal("upload original missing from local cache", cacheErr)
	}
	app.Press("id:preview-retry")
	app.WaitUntil(3*time.Second, func() bool {
		return !app.Exists("id:preview-retry") && bytes.Equal(app.Find("id:preview-image").Image.Source.Data, photo)
	})
	imgNode := app.Find("id:preview-image")
	decoded, err := png.Decode(bytes.NewReader(imgNode.Image.Source.Data))
	if err != nil || decoded.Bounds() != img.Bounds() || color.RGBAModel.Convert(decoded.At(42, 42)) != img.RGBAAt(42, 42) || imgNode.Frame.Size.Width <= 100 || imgNode.Frame.Size.Height <= 100 {
		t.Fatal("sent original could not be displayed after its ID changed", err, imgNode.Frame)
	}

	app.Press("id:preview-previous")
	app.WaitFor("text:1 / 2", time.Second)
	app.WaitUntil(3*time.Second, func() bool {
		v := app.Find("id:preview-image")
		return len(v.Image.Source.Data) > 0 && v.Frame.Size.Width > 100 && v.Frame.Size.Height > 100
	})
	previewImage := app.Find("id:preview-image").ID
	refresh()
	app.Settle()
	if app.Find("id:preview-image").ID != previewImage {
		t.Fatal("status update remounted the full-size image")
	}
	app.Screenshot("attachment-gallery")
	app.Press("id:preview-close")
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:image-preview") })
	// Independently read back the finished bytes through the same scoped RPCs.
	scope := assets.Scope("codex-app", "session")
	begin, e := f.store.Handle(context.Background(), scope, "attachment.begin", assets.Params{Name: "readback.txt", Size: 4, SHA256: hashBytes([]byte("file"))})
	if e != nil {
		t.Fatal(e)
	}
	u := begin.(assets.UploadState)
	_, _ = f.store.Handle(context.Background(), scope, "attachment.write", assets.Params{ID: u.ID, Data: base64.StdEncoding.EncodeToString([]byte("file"))})
	end, e := f.store.Handle(context.Background(), scope, "attachment.finish", assets.Params{ID: u.ID})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(end.(assets.UploadState).Attachment)
	a := Object{}
	_ = json.Unmarshal(b, &a)
	s := &State{api: f}
	data, e := s.attachmentBytes(context.Background(), "fixture", "codex-app", "session", a, false)
	if e != nil || string(data) != "file" {
		t.Fatal("attachment readback", string(data), e)
	}
}

// Drop successful responses rather than requests: retry must reconcile the
// receiver's already-written bytes and finished object without a second upload.
type unreliableUploadAPI struct {
	*attachmentFixture
	writes                           chan struct{}
	finish                           chan struct{}
	lostBegin, lostWrite, lostFinish atomic.Bool
	starts, completed                atomic.Int32
}

func (f *unreliableUploadAPI) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	m := str(r, "method")
	if method == "client.call" && (m == "attachment.write" || m == "attachment.finish") {
		gate := f.writes
		if m == "attachment.finish" {
			gate = f.finish
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-gate:
		}
	}
	v, err := f.attachmentFixture.Call(ctx, method, p, target, r)
	if method != "client.call" || err != nil {
		return v, err
	}
	lost := false
	switch m {
	case "attachment.begin":
		f.starts.Add(1)
		lost = f.lostBegin.CompareAndSwap(false, true)
	case "attachment.write":
		lost = f.lostWrite.CompareAndSwap(false, true)
	case "attachment.finish":
		f.completed.Add(1)
		lost = f.lostFinish.CompareAndSwap(false, true)
	}
	if lost {
		return nil, fmt.Errorf("fixture: acknowledgement lost")
	}
	return v, nil
}

func waitForReadyUploads(app *nsgotest.App, s *State, count int) {
	var ready atomic.Bool
	app.WaitUntil(4*time.Second, func() bool {
		// Query only reports visible views. A queued card below the viewport
		// has no visible progress either, so absence cannot prove completion.
		app.Post(func() {
			s.cx.Post(func() {
				complete := len(s.uploads) == count && s.uploadActive == 0
				for _, u := range s.uploads {
					complete = complete && u.Status == "ready"
				}
				ready.Store(complete)
			})
		})
		return ready.Load()
	})
}

func TestFourUploadsQueueRetryLostACKsAndAwaitConfirmationBelow100(t *testing.T) {
	f := &unreliableUploadAPI{attachmentFixture: &attachmentFixture{demoAPI: newDemoAPI(), store: assets.New(t.TempDir())}, writes: make(chan struct{}), finish: make(chan struct{})}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s = newState(cx, f)
		s.target = "fixture"
		s.selected = Object{"id": "session", "providerId": "codex-app"}
		s.devices = []Object{{"id": "fixture", "connected": true}}
		return core.View{Style: fill, Children: []core.Node{button(cx, "four-files", "Choose files", nil, "outline", func() {
			for _, name := range []string{"one.txt", "two.txt", "three.txt", "four.txt"} {
				s.addUpload(name, "text/plain", bytes.Repeat([]byte("sample"), 15000))
			}
		}), s.uploadPreviews(cx)}}
	}, nsgotest.Size(390, 844), nsgotest.Environment(core.Environment{Locale: "en-US"}))
	app.Press("id:four-files")
	app.WaitUntil(2*time.Second, func() bool { return f.starts.Load() >= 3 })
	app.Find("id:upload-four.txt")
	app.Post(func() {
		s.cx.Post(func() {
			if s.uploadActive != 2 || s.uploads[2].Status != "queued" || s.uploads[3].Status != "queued" {
				t.Error("fourth upload did not wait safely")
			}
		})
	})
	app.Settle()
	close(f.writes)
	app.WaitUntil(3*time.Second, func() bool {
		return app.Find("id:upload-progress-one.txt").Text == "99%" && app.Find("id:upload-progress-two.txt").Text == "99%"
	})
	if app.Exists("text:已上传，等待设备校验确认") || app.Exists("text:上传 100%") {
		t.Fatal("compact upload shows prose or premature completion")
	}
	app.Screenshot("four-uploads-awaiting-confirmation")
	close(f.finish)
	waitForReadyUploads(app, s, 4)
}

func TestUploadRemovalCancelsActiveAndSkipsQueuedFile(t *testing.T) {
	f := &unreliableUploadAPI{attachmentFixture: &attachmentFixture{demoAPI: newDemoAPI(), store: assets.New(t.TempDir())}, writes: make(chan struct{}), finish: make(chan struct{})}
	var s *State
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s = newState(cx, f)
		s.target = "fixture"
		s.selected = Object{"id": "session", "providerId": "codex-app"}
		s.devices = []Object{{"id": "fixture", "connected": true}}
		return core.View{Style: fill, Children: []core.Node{button(cx, "choose-files", "Choose files", nil, "outline", func() {
			for _, name := range []string{"one.txt", "two.txt", "three.txt", "four.txt"} {
				s.addUpload(name, "text/plain", []byte("fixture"))
			}
		}), s.uploadPreviews(cx)}}
	}, nsgotest.Size(390, 844))
	app.Press("id:choose-files")
	app.WaitUntil(2*time.Second, func() bool { return f.starts.Load() >= 3 })
	app.Press("id:upload-four.txt >> role:button")
	app.Press("id:upload-one.txt >> role:button")
	app.WaitUntil(time.Second, func() bool { return f.starts.Load() >= 4 })
	if app.Exists("id:upload-one.txt") || app.Exists("id:upload-four.txt") {
		t.Fatal("removed upload remains visible")
	}
	close(f.writes)
	close(f.finish)
	waitForReadyUploads(app, s, 2)
	if f.starts.Load() != 4 {
		t.Fatal("cancelled queued upload reached host", f.starts.Load())
	}
}
