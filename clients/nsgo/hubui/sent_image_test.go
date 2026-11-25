package hubui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"sync/atomic"
	"testing"
	"time"
)

type sentImageFixture struct {
	*delayedSendAPI
	transcript              Object
	cacheReads, remoteReads atomic.Int32
}

func (f *sentImageFixture) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.assetCache.get" {
		f.cacheReads.Add(1)
		return nil, nil
	}
	if method == "client.call" {
		switch str(r, "method") {
		case "attachment.read":
			f.remoteReads.Add(1)
			return nil, errors.New("unexpected remote image read")
		case "transcript":
			return f.transcript, nil
		}
	}
	return f.delayedSendAPI.Call(ctx, method, p, target, r)
}

func TestSentImageSurvivesAcknowledgementAndImport(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	digest := hashBytes(data.Bytes())
	// Two copies of the same image must remain independently actionable.
	remote := []Object{}
	for _, name := range []string{"first", "second"} {
		remote = append(remote, Object{"id": "import-" + name, "name": "import-" + name + ".png", "kind": "image", "sha256": digest, "thumbnail": true, "state": "ready"})
	}
	echo := outgoingTranscript(Object{"id": "native", "role": "user", "text": "", "attachments": remote})
	for _, earlyEcho := range []bool{true, false} {
		t.Run(choose(earlyEcho, "echo-before-ack", "ack-before-echo"), func(t *testing.T) {
			f := &sentImageFixture{delayedSendAPI: &delayedSendAPI{demoAPI: newDemoAPI(), release: make(chan struct{})}, transcript: echo}
			app, s := conversationFixture(t, f, 390, outgoingTranscript(), true)
			app.Post(func() {
				for _, name := range []string{"first", "second"} {
					s.uploads = append(s.uploads, &Upload{ID: "upload-" + name, Name: name + ".png", Status: "ready", MIME: "image/png", SHA256: digest, Data: data.Bytes()})
				}
				s.changed()
			})
			app.WaitUntil(time.Second, func() bool { return !app.Find("id:send-message").Props.Disabled })
			app.Press("id:send-message")
			app.WaitFor("role:image first.png", time.Second)
			first := app.Find("role:image first.png")
			second := app.Find("role:image second.png")
			if first.ID == second.ID || !bytes.Equal(first.Image.Source.Data, data.Bytes()) {
				t.Fatal("missing optimistic previews")
			}
			if earlyEcho {
				app.Post(func() { s.setTranscript(echo) })
				app.WaitFor("role:image import-first.png", time.Second)
			}
			close(f.release)
			app.WaitFor("role:image import-second.png", 2*time.Second)
			app.WaitUntil(time.Second, func() bool { return !app.Exists("id:send-loading") })
			firstAfter := app.Find("role:image import-first.png")
			secondAfter := app.Find("role:image import-second.png")
			if firstAfter.ID != first.ID || secondAfter.ID != second.ID ||
				!bytes.Equal(firstAfter.Image.Source.Data, data.Bytes()) || !bytes.Equal(secondAfter.Image.Source.Data, data.Bytes()) {
				t.Fatal("confirmation replaced the displayed image")
			}
			app.Press("role:button 预览图片 import-second.png")
			if len(s.preview) != 2 || str(s.preview[s.previewIndex.GetUntracked()], "id") != "import-second" {
				t.Fatal("preview still used upload metadata")
			}
			if f.cacheReads.Load() != 0 || f.remoteReads.Load() != 0 {
				t.Fatal("confirmation reloaded the image", f.cacheReads.Load(), f.remoteReads.Load())
			}
		})
	}
}

type sentImageCacheFixture struct {
	*downloadFixture
	original string
}

func (f *sentImageCacheFixture) Call(ctx context.Context, method string, p Object, target string, r Object) (any, error) {
	if method == "client.assetCache.get" && target == "fixture" && str(p, "key") == originalImageKey("codex-app", "session", hashBytes(f.data)) {
		return Object{"data": f.original}, nil
	}
	return f.downloadFixture.Call(ctx, method, p, target, r)
}

func TestImportedThumbnailUsesVerifiedLocalOriginal(t *testing.T) {
	f := &sentImageCacheFixture{downloadFixture: &downloadFixture{demoAPI: newDemoAPI(), data: []byte("original")}}
	s := &State{api: f}
	meta := f.attachment()
	meta["kind"], meta["id"] = "image", "new-id"
	for _, tc := range []struct {
		target, cached string
		wantRemote     bool
	}{{"fixture", "original", false}, {"fixture", "corrupt", true}, {"other-device", "original", true}} {
		f.original = base64.StdEncoding.EncodeToString([]byte(tc.cached))
		before := f.reads.Load()
		b, err := s.attachmentBytes(context.Background(), tc.target, "codex-app", "session", meta, true)
		if err != nil || !bytes.Equal(b, f.data) || (before != f.reads.Load()) != tc.wantRemote {
			t.Fatalf("unexpected thumbnail source for %v: %q %v", tc, b, err)
		}
	}
}
