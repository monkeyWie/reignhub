package hubui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/monkeyWie/reignhub/pkg/filelink"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/i18n"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
	"github.com/nsgo-dev/nsgo/ui/chat"
)

type Upload struct {
	Name, MIME, ID, Status, Error string
	SHA256                        string
	Data                          []byte
	Sent                          int64
	Target, Provider, Session     string
	cancelled                     atomic.Bool
	cancel                        context.CancelFunc
	active                        bool
}

func hashBytes(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func (s *State) chooseFiles(cx *core.Scope) {
	owner := s.key()
	cx.PickFiles(core.PickFilesOptions{Multiple: true, Limit: 16}, func(files []core.File, err error) {
		if err != nil {
			s.error = err.Error()
			s.changed()
			return
		}
		for _, f := range files {
			go func() {
				reader, err := f.Open()
				var b []byte
				if err == nil {
					b, err = io.ReadAll(io.LimitReader(reader, (50<<20)+1))
					reader.Close()
				}
				cx.Post(func() {
					if err != nil {
						s.error = err.Error()
						s.changed()
						return
					}
					if s.key() == owner {
						s.addUpload(f.Name, f.MIME, b)
					}
				})
			}()
		}
	})
}
func (s *State) paste(cx *core.Scope, e core.PasteEvent) {
	for _, f := range e.Files {
		name := f.Name
		if name == "" {
			name = "clipboard.png"
		}
		s.addUpload(name, f.MIME, f.Data)
	}
	s.drop(cx, e.URIs)
}
func (s *State) drop(cx *core.Scope, uris []string) {
	owner := s.key()
	for _, uri := range uris {
		uri := uri
		go func() {
			name, data, err := readDroppedFile(uri)
			cx.Post(func() {
				if err != nil {
					s.error = err.Error()
					s.changed()
					return
				}
				if s.key() == owner {
					s.addUpload(name, mime.TypeByExtension(path.Ext(name)), data)
				}
			})
		}()
	}
}
func (s *State) addUpload(name, mime string, data []byte) {
	if s.selected == nil || !s.connected() || !s.draftReady() {
		return
	}
	capabilities := s.agentCapabilities()
	if capabilities != nil {
		image := strings.HasPrefix(mime, "image/") || strings.HasPrefix(http.DetectContentType(data), "image/")
		if image && !flag(capabilities, "imageInput") || !image && !flag(capabilities, "fileInput") {
			s.error = "此 Agent 不支持这种附件类型"
			s.changed()
			return
		}
		if limit := number(capabilities, "maxAttachmentBytes"); limit > 0 && int64(len(data)) > limit {
			s.error = fmt.Sprintf("此 Agent 的单个附件上限为 %d MiB", limit/(1<<20))
			s.changed()
			return
		}
	}
	total := 0
	for _, u := range s.uploads {
		total += len(u.Data)
	}
	if len(data) > 50<<20 || total+len(data) > 100<<20 || len(s.uploads) >= 16 {
		s.error = "每条消息最多 16 个附件，单个不超过 50 MB，总量不超过 100 MB"
		s.changed()
		return
	}
	u := &Upload{Name: name, MIME: mime, Data: data, Status: "queued", Target: s.target, Provider: str(s.selected, "providerId"), Session: str(s.selected, "id")}
	s.uploads = append(s.uploads, u)
	s.persistDraft()
	s.changed()
	s.upload(u)
}

// All drafts share one FIFO, so navigation cannot bypass the concurrency bound.
func (s *State) upload(u *Upload) {
	if u.active || u.cancelled.Load() || u.Status == "ready" {
		return
	}
	if bitmapInput(u.Data) {
		s.prepareBitmapUpload(u)
		return
	}
	for _, queued := range s.uploadQueue {
		if queued == u {
			return
		}
	}
	u.Status, u.Error = "queued", ""
	s.uploadQueue = append(s.uploadQueue, u)
	s.pumpUploads()
	s.changed()
}
func (s *State) pumpUploads() {
	for s.uploadActive < 2 && len(s.uploadQueue) > 0 {
		u := s.uploadQueue[0]
		s.uploadQueue = s.uploadQueue[1:]
		if u.cancelled.Load() {
			continue
		}
		if u.ID == "" {
			var id [32]byte
			if _, err := rand.Read(id[:]); err != nil {
				u.Status, u.Error = "failed", err.Error()
				continue
			}
			u.ID = hex.EncodeToString(id[:])
		}
		ctx, cancel := context.WithCancel(s.ctx)
		u.cancel, u.active, u.Status = cancel, true, "preparing"
		s.uploadActive++
		go s.runUpload(ctx, u)
	}
}
func (s *State) runUpload(ctx context.Context, u *Upload) {
	id := u.ID
	sum := hashBytes(u.Data)
	// Only these scoped, idempotent file operations may replay after a lost ACK.
	// Never apply this retry policy to Agent send/steer or other mutations.
	call := func(method string, p Object) (Object, error) {
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			request, stop := context.WithTimeout(ctx, 15*time.Second)
			v, e := rpc(request, s.api, u.Target, u.Provider, u.Session, method, Object{"params": p})
			stop()
			if e == nil {
				return object(v), nil
			}
			err = e
			if attempt < 2 {
				timer := time.NewTimer(time.Duration(attempt+1) * 300 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
			}
		}
		return nil, err
	}
	r, err := call("attachment.begin", Object{"id": id, "name": u.Name, "size": len(u.Data), "sha256": sum})
	offset := number(r, "offset")
	if err == nil && (str(r, "id") != id || offset < 0 || offset > int64(len(u.Data))) {
		err = fmt.Errorf("上传偏移无效")
	}
	chunkSize := int64(4 << 10)
	for err == nil && offset < int64(len(u.Data)) && ctx.Err() == nil {
		s.cx.Post(func() {
			if !u.cancelled.Load() {
				u.Status = "uploading"
				s.changed()
			}
		})
		end := min(offset+chunkSize, int64(len(u.Data)))
		started := time.Now()
		r, err = call("attachment.write", Object{"id": id, "offset": offset, "data": base64.StdEncoding.EncodeToString(u.Data[offset:end])})
		if err != nil {
			break
		}
		next := number(r, "offset")
		if next != end {
			err = fmt.Errorf("上传偏移无效")
			break
		}
		offset = next
		progress := offset
		s.cx.Post(func() {
			if !u.cancelled.Load() {
				u.Sent = progress
				s.changed()
			}
		})
		elapsed := time.Since(started)
		if elapsed < 500*time.Millisecond {
			chunkSize = min(chunkSize*2, 48<<10)
		} else if elapsed > 2*time.Second {
			chunkSize = max(chunkSize/2, 4<<10)
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		s.cx.Post(func() {
			if !u.cancelled.Load() {
				u.Status = "verifying"
				s.changed()
			}
		})
		r, err = call("attachment.finish", Object{"id": id})
		a := object(r["attachment"])
		if err == nil && (str(a, "id") != id || str(a, "sha256") != sum || number(a, "size") != int64(len(u.Data))) {
			err = fmt.Errorf("上传结果校验失败")
		}
	}
	if err == nil && !u.cancelled.Load() && strings.HasPrefix(http.DetectContentType(u.Data), "image/") {
		// The provider may import the same image under a different transcript ID.
		_, _ = s.api.Call(ctx, "client.assetCache.put", Object{"key": originalImageKey(u.Provider, u.Session, sum), "data": base64.StdEncoding.EncodeToString(u.Data)}, u.Target, nil)
	}
	if u.cancelled.Load() {
		cleanup, stop := context.WithTimeout(s.ctx, 3*time.Second)
		_, _ = rpc(cleanup, s.api, u.Target, u.Provider, u.Session, "attachment.abort", Object{"params": Object{"id": id}})
		stop()
	}
	s.cx.Post(func() {
		u.cancel()
		u.cancel, u.active = nil, false
		s.uploadActive--
		if !u.cancelled.Load() {
			u.Sent = offset
			if err != nil {
				u.Status, u.Error = "failed", err.Error()
			} else {
				u.Status, u.SHA256 = "ready", sum
			}
		}
		s.pumpUploads()
		s.changed()
	})
}
func (s *State) uploadPreviews(cx *core.Scope) core.Node {
	rows := core.Each(func() []*Upload { s.read(); return s.uploads }, func(u *Upload) *Upload { return u }, func(cx *core.Scope, get func() *Upload, _ func() int) core.Node {
		u := get()
		remove := func() {
			u.cancelled.Store(true)
			if u.cancel != nil {
				u.cancel()
			}
			for i, v := range s.uploads {
				if v == u {
					s.uploads = append(s.uploads[:i], s.uploads[i+1:]...)
					break
				}
			}
			s.persistDraft()
			s.changed()
		}
		// Bytes and metadata are immutable for this keyed upload. Only progress
		// changes; remounting the image on every poll restarts native decoding.
		var tile core.Node = chat.Attachment{Label: u.Name, Detail: fmt.Sprintf("%.1f KB", float64(len(u.Data))/1024), RemoveLabel: locales.For(cx).RemoveAttachment(), OnRemove: remove}
		isImage := strings.HasPrefix(u.MIME, "image/") || strings.HasPrefix(http.DetectContentType(u.Data), "image/")
		if isImage {
			unreadable := core.NewSignal(cx, bitmapInput(u.Data))
			image := core.Image{TestID: "upload-image-" + u.Name, Label: u.Name, Source: core.ImageSource{Data: u.Data}, ResizeMode: core.Cover,
				Style: core.Style{Width: core.Pct(100), Height: core.Pct(100)}, OnError: func(error) { unreadable.Set(true) }}
			preview := core.View{TestID: "preview-upload-" + u.Name, Role: core.RoleButton, Label: "预览图片 " + u.Name, Disabled: unreadable.Get,
				Tooltip: tooltipCard(cx, dynamicText(cx, func() string {
					if unreadable.Get() {
						return "图片暂时无法预览"
					}
					return u.Name
				}, 12, false)),
				OnPress: func(core.PressEvent) { s.previewDraftImage(u) }, Style: core.Style{Width: core.Pt(64), Height: core.Pt(64), CornerRadius: 8, Overflow: core.Hidden},
				StyleFunc: func() core.Style { return pressStyle(cx).Merge(core.Style{Background: palette(cx).muted}) }, Children: []core.Node{
					core.Show{When: func() bool { return !unreadable.Get() }, Then: func(*core.Scope) core.Node { return image }},
					core.Show{When: unreadable.Get, Then: func(cx *core.Scope) core.Node {
						return core.View{Style: fill.Merge(core.Style{AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter}), Children: []core.Node{glyph(cx, lucide.Image, 20)}}
					}},
				}}
			removeButton := contextualIconButton(cx, "remove-upload-"+u.Name, locales.For(cx).RemoveAttachment(), lucide.X, remove)
			removeButton.Style.Padding = core.All(5)
			removeButton.Children = []core.Node{core.View{Style: core.Style{Width: core.Pt(20), Height: core.Pt(20), CornerRadius: 10, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).elevated} }, Children: []core.Node{core.Icon{SVG: lucide.X(), Style: core.Style{Width: core.Pt(12), Height: core.Pt(12)}}}}}
			actions := core.View{TestID: "upload-actions-" + u.Name,
				Style:     core.Style{Position: core.Absolute, Top: core.Pt(-6), Right: core.Pt(-6)},
				StyleFunc: func() core.Style { return contextualAffordance(cx, "draft-image", false) },
				Children:  []core.Node{removeButton}}
			tile = ui.ContextMenu{TestID: "upload-menu-" + u.Name,
				Trigger: core.View{Style: core.Style{Group: "draft-image", Width: core.Pt(64), Height: core.Pt(64)}, Children: []core.Node{preview, actions}},
				Children: []core.Node{
					core.Show{When: func() bool { s.read(); return u.Status == "failed" }, Then: func(cx *core.Scope) core.Node {
						return ui.MenuItem{TestID: "upload-retry-action", Text: locales.For(cx).RetryUpload(), Icon: lucide.RotateCcw, OnSelect: func() { s.upload(u) }}
					}},
					ui.MenuItem{TestID: "upload-remove-action", Text: locales.For(cx).RemoveAttachment(), Icon: lucide.X, OnSelect: remove},
				}}
		}
		state := core.NewMemo(cx, func() string { s.read(); return u.Status })
		status := core.View{TestID: "upload-status-" + u.Name, Style: core.Style{Height: core.Pt(30), Direction: core.Row, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter, FlexShrink: 0}, Children: []core.Node{
			core.Dynamic(func(cx *core.Scope) core.Node {
				status := state.Get()
				if status == "ready" {
					return nil
				}
				messages := locales.For(cx)
				if status == "failed" {
					v := contextualIconButton(cx, "retry-upload-"+u.Name, messages.RetryUpload(), lucide.RotateCcw, func() { s.upload(u) })
					v.Tooltip = tooltipCard(cx, dynamicText(cx, func() string { s.read(); return messages.UploadFailed(u.Error) }, 12, false))
					return v
				}
				value := func() float32 { s.read(); return u.progressPercent() }
				progress := dynamicText(cx, func() string { return i18n.FormatNumberSkeleton(i18n.Language(cx), float64(value())/100, "percent .#") }, 11, true)
				progress.TestID = "upload-progress-" + u.Name
				return core.View{Role: core.RoleStatus, Label: choose(status == "queued", messages.UploadQueued(), messages.Upload()),
					Tooltip: tooltipCard(cx, label(cx, choose(status == "queued", messages.UploadQueued(), messages.Upload()), 12, false)), Children: []core.Node{progress}}
			}),
		}}
		return core.View{TestID: "upload-" + u.Name, Style: core.Style{FlexShrink: 0, MaxWidth: core.Pct(100)}, Children: []core.Node{tile, status}}
	})
	return core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8}, Children: []core.Node{rows}}
}

func originalImageKey(provider, session, digest string) string {
	return provider + "|" + session + "|original:" + digest
}

// The host can assign a new attachment ID when importing an uploaded image.
func attachmentCacheKey(provider, session string, a Object, thumbnail bool) string {
	if !thumbnail && str(a, "kind") == "image" && str(a, "sha256") != "" {
		return originalImageKey(provider, session, str(a, "sha256"))
	}
	return provider + "|" + session + "|" + str(a, "id") + fmt.Sprint("|", thumbnail)
}

func (s *State) imageKey(a Object, thumbnail bool) string {
	return s.target + "|" + attachmentCacheKey(str(s.selected, "providerId"), str(s.selected, "id"), a, thumbnail)
}

func (s *State) attachmentBytes(ctx context.Context, target, p, session string, a Object, thumbnail bool) ([]byte, error) {
	return s.attachmentBytesProgress(ctx, target, p, session, a, thumbnail, nil)
}

func (s *State) cachedAttachmentBytes(ctx context.Context, target, key, expectedDigest string) []byte {
	cached, err := s.api.Call(ctx, "client.assetCache.get", Object{"key": key}, target, nil)
	if err != nil || ctx.Err() != nil {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(str(object(cached), "data"))
	if err != nil || len(b) == 0 || expectedDigest != "" && hashBytes(b) != expectedDigest {
		return nil
	}
	return b
}

// Read immutable chunks with bounded concurrency, checking every offset and the
// final digest. Progress counts bytes actually received, never queued requests.
func (s *State) attachmentBytesProgress(ctx context.Context, target, p, session string, a Object, thumbnail bool, progress func(int64, int64)) ([]byte, error) {
	key := attachmentCacheKey(p, session, a, thumbnail)
	digest := str(a, "sha256")
	var b []byte
	if thumbnail {
		b = s.cachedAttachmentBytes(ctx, target, key, "")
		if len(b) == 0 && digest != "" && str(a, "kind") == "image" && ctx.Err() == nil {
			b = s.cachedAttachmentBytes(ctx, target, originalImageKey(p, session, digest), digest)
		}
	} else if digest != "" {
		b = s.cachedAttachmentBytes(ctx, target, key, digest)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(b) > 0 {
		if progress != nil {
			progress(int64(len(b)), int64(len(b)))
		}
		return b, nil
	}
	if str(a, "state") != "ready" {
		return nil, fmt.Errorf("源文件已失效")
	}
	const chunkSize = 48 << 10
	read := func(ctx context.Context, offset int64) (Object, []byte, error) {
		var v any
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			v, err = rpc(ctx, s.api, target, p, session, "attachment.read", Object{"params": Object{"id": a["id"], "offset": offset, "thumbnail": thumbnail}})
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil, nil, err
		}
		r := object(v)
		chunk, err := base64.StdEncoding.DecodeString(str(r, "data"))
		if err != nil {
			return nil, nil, err
		}
		return r, chunk, nil
	}
	first, chunk, err := read(ctx, 0)
	if err != nil {
		return nil, err
	}
	size, expected := number(first, "size"), str(first, "sha256")
	if size < 0 || size > 50<<20 {
		return nil, fmt.Errorf("附件大小无效")
	}
	if !thumbnail && expected != str(a, "sha256") {
		return nil, fmt.Errorf("附件完整性校验失败")
	}
	out := make([]byte, int(size))
	accept := func(r Object, chunk []byte, offset int64) error {
		if number(r, "offset") != offset || number(r, "size") != size || str(r, "sha256") != expected {
			return fmt.Errorf("附件传输发生变化")
		}
		end := offset + int64(len(chunk))
		if int64(len(chunk)) != min(int64(chunkSize), size-offset) || end > size || flag(r, "eof") != (end == size) {
			return fmt.Errorf("附件分片无效")
		}
		copy(out[offset:end], chunk)
		return nil
	}
	if err = accept(first, chunk, 0); err != nil {
		return nil, err
	}
	var progressMu sync.Mutex
	received := int64(len(chunk))
	if progress != nil {
		progress(received, size)
	}
	group, downloadCtx := errgroup.WithContext(ctx)
	// One metadata chunk, then at most four in-flight reads. Keep capacity for
	// session watch and foreground commands on the host's eight RPC slots.
	group.SetLimit(4)
	for offset := int64(len(chunk)); offset < size; offset += chunkSize {
		if downloadCtx.Err() != nil {
			break
		}
		group.Go(func() error {
			r, b, err := read(downloadCtx, offset)
			if err != nil {
				return err
			}
			if err = accept(r, b, offset); err != nil {
				return err
			}
			progressMu.Lock()
			received += int64(len(b))
			if progress != nil {
				progress(received, size)
			}
			progressMu.Unlock()
			return nil
		})
	}
	if err = group.Wait(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if hashBytes(out) != expected {
		return nil, fmt.Errorf("附件完整性校验失败")
	}
	_, _ = s.api.Call(ctx, "client.assetCache.put", Object{"key": key, "data": base64.StdEncoding.EncodeToString(out)}, target, nil)
	return out, nil
}
func (s *State) loadImage(a Object, thumbnail bool) {
	key := s.imageKey(a, thumbnail)
	if s.images[key] != nil {
		s.touchImage(key)
		return
	}
	if s.loadingImages[key] {
		return
	}
	s.loadingImages[key] = true
	if s.imageContext == nil {
		s.imageContext, s.imageCancel = context.WithCancel(s.ctx)
	}
	ctx := s.imageContext
	target, p, id := s.target, str(s.selected, "providerId"), str(s.selected, "id")
	go func() {
		b, e := s.attachmentBytes(ctx, target, p, id, a, thumbnail)
		s.cx.Post(func() {
			if ctx.Err() != nil {
				return
			}
			delete(s.loadingImages, key)
			if e == nil {
				s.cacheImage(key, b)
			}
			s.changed()
		})
	}()
}
func (s *State) imageTile(cx *core.Scope, message func() Message, get func() Object) core.Node {
	a := core.Untracked(cx, get)
	thumb := flag(a, "thumbnail")
	s.loadImage(a, thumb)
	key := s.imageKey(a, thumb)
	image := core.Image{Label: str(a, "name"), SourceFunc: func() core.ImageSource { s.read(); return core.ImageSource{Data: s.images[key]} }, ResizeMode: core.Cover, Style: core.Style{Width: core.Pct(100), Height: core.Pt(128)}}
	return s.attachmentCard(cx, a, func() { s.previewAttachment(message(), a) }, image)
}
func (s *State) fileTile(cx *core.Scope, a Object) core.Node {
	// Unsupported previews are read-only content, not a misleading button.
	return s.attachmentCard(cx, a, nil)
}

func (s *State) attachmentCard(cx *core.Scope, a Object, preview func(), content ...core.Node) core.Node {
	name := str(a, "name")
	if name == "" {
		name = "附件"
	}
	caption := singleLine(cx, func() string { return name }, 12, false)
	captionView := core.View{TestID: "attachment-name-" + str(a, "id"), Tooltip: tooltipCard(cx, label(cx, name, 12, false)), Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0)}, Children: []core.Node{caption}}
	footer := []core.Node{}
	if str(a, "kind") != "image" {
		footer = append(footer, glyph(cx, lucide.FileText, 18))
	}
	footer = append(footer, captionView)
	content = append(content, core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, MinWidth: core.Pt(0), MinHeight: core.Pt(48), Padding: core.Edges{Top: 6, Bottom: 6, Left: 10, Right: 52}, Gap: 6}, Children: footer})
	body := core.View{Style: core.Style{MinWidth: core.Pt(0)}, Children: content}
	if preview != nil {
		body.TestID, body.Role, body.Label = "image-"+str(a, "id"), core.RoleButton, "预览图片 "+name
		body.OnPress = func(core.PressEvent) { preview() }
		body.StyleFunc = func() core.Style { return pressStyle(cx) }
	}
	card := core.View{TestID: "attachment-" + str(a, "id"), Style: core.Style{MinWidth: core.Pt(0), Width: core.Pct(100), AlignSelf: core.AlignStart, CornerRadius: 10, Overflow: core.Hidden, BorderWidth: 1}, StyleFunc: func() core.Style {
		return core.Style{BorderColor: palette(cx).border, Background: palette(cx).muted}
	}, Children: []core.Node{body}}
	return s.attachmentActions(cx, func() Object { return a }, card, false)
}

// Download and preview have independent hit targets. Touch uses the same
// context menu as desktop secondary-click instead of relying on hidden icons.
func (s *State) attachmentActions(cx *core.Scope, get func() Object, card core.View, thumbnail bool) core.Node {
	id := str(core.Untracked(cx, get), "id")
	downloadTitle := func() string {
		name := str(get(), "name")
		if name == "" {
			name = "附件"
		}
		return "下载 " + name
	}
	download := func() { s.download(cx, get()) }
	card.Style.Group = "attachment"
	if thumbnail {
		// Contrast belongs to the image surface, not a filled tile on the icon.
		card.Children = append(card.Children, core.View{PointerEvents: core.PointerEventsNone,
			Style: core.Style{Position: core.Absolute, Left: core.Pt(0), Right: core.Pt(0), Bottom: core.Pt(0), Height: core.Pt(42)},
			StyleFunc: func() core.Style {
				st := contextualAffordance(cx, "attachment", false)
				st.PointerEvents = core.PointerEventsNone
				if st.GroupStyle != nil {
					st.GroupStyle.Hover.PointerEvents = core.PointerEventsNone
				}
				c := palette(cx).background
				st.Gradients = []core.Gradient{core.LinearGradient(180, c.Faded(0), c.Faded(.96))}
				return st
			}})
	}
	card.Children = append(card.Children, core.View{TestID: "attachment-actions-" + id,
		Style:     core.Style{Position: core.Absolute, Right: core.Pt(choose(thumbnail, core.Points(2), core.Points(6))), Bottom: core.Pt(choose(thumbnail, core.Points(2), core.Points(9)))},
		StyleFunc: func() core.Style { return contextualAffordance(cx, "attachment", false) },
		Children:  []core.Node{contextualIconButtonWithLabel(cx, "download-"+id, dynamicText(cx, downloadTitle, 12, false), lucide.Download, download)}})
	return ui.ContextMenu{TestID: "attachment-menu-" + id,
		Style:   core.Style{Width: card.Style.Width, Height: card.Style.Height, MinWidth: core.Pt(0), AlignSelf: core.AlignStart},
		Trigger: card, Children: []core.Node{ui.MenuItem{TestID: "attachment-download-action", Text: "下载", Icon: lucide.Download, OnSelect: download}}}
}

func (s *State) previewAttachment(message Message, a Object) {
	if str(a, "kind") != "image" {
		return
	}
	images := []Object{}
	index := 0
	for _, v := range message.Attachments {
		if str(v, "kind") == "image" {
			if str(v, "id") == str(a, "id") {
				index = len(images)
			}
			images = append(images, v)
		}
	}
	s.preview = images
	s.previewIndex.Set(index)
	s.dialog.Set("preview")
}

const attachmentCardMinimum core.Points = 180
const attachmentCardGap core.Points = 12

func (s *State) attachmentGallery(cx *core.Scope, get func() Message) core.Node {
	attachments := core.NewMemoFunc(cx, func() []Object {
		out := []Object{}
		for _, a := range get().Attachments {
			if _, location := filelink.Path(str(a, "source")); location && flag(a, "inline") {
				continue
			}
			// Inline images render at their Markdown location. Inline file
			// links still need an explicit download action in this collection.
			if (get().Role != "user" || str(a, "kind") != "image") && (!flag(a, "inline") || str(a, "kind") != "image") {
				out = append(out, a)
			}
		}
		return out
	}, nil)
	return core.Show{When: func() bool { return len(attachments.Get()) > 0 }, Then: func(cx *core.Scope) core.Node {
		grid := cardGrid(cx, "attachments-"+get().ID, attachmentCardMinimum, attachmentCardGap,
			core.Each(attachments.Get, func(a Object) string { return str(a, "id") }, func(cx *core.Scope, a func() Object, _ func() int) core.Node {
				if str(a(), "kind") == "image" {
					return s.imageTile(cx, get, a)
				}
				return s.fileTile(cx, a())
			}))
		return core.View{Style: core.Style{Width: core.Pct(100), MinWidth: core.Pt(0), Padding: core.Edges{Top: 12}}, StyleFunc: func() core.Style {
			// Keep sparse galleries compact while dense collections fill the
			// same message column.
			count := len(attachments.Get())
			limit := choose(get().Role == "user", core.Points(680), core.Points(820))
			return core.Style{MaxWidth: core.Pt(min(limit, core.Points(count)*attachmentCardMinimum+core.Points(max(0, count-1))*attachmentCardGap))}
		}, Children: []core.Node{grid}}
	}}
}

func (u *Upload) progressPercent() float32 {
	if u.Status == "ready" {
		return 100
	}
	return min(99, max(0, float32(u.Sent)*100/float32(max(1, len(u.Data)))))
}

// User images are compact composer-style previews above the right-aligned
// bubble; Agent files and generated images use the full gallery below text.
func (s *State) userImageGallery(cx *core.Scope, get func() Message) core.Node {
	type imageKey struct {
		content    string
		occurrence int
	}
	type entry struct {
		key  imageKey
		meta Object
	}
	images := core.NewMemoFunc(cx, func() []entry {
		var out []entry
		counts := map[string]int{}
		for _, a := range get().Attachments {
			if str(a, "kind") == "image" && !flag(a, "inline") {
				content := s.imageKey(a, false)
				out = append(out, entry{imageKey{content, counts[content]}, a})
				counts[content]++
			}
		}
		return out
	}, nil)
	return core.Show{When: func() bool { return len(images.Get()) > 0 }, Then: func(cx *core.Scope) core.Node {
		return core.View{TestID: "user-images-" + get().ID, Style: core.Style{Width: core.Pct(100), MinWidth: core.Pt(0), Direction: core.Row, Wrap: core.Wrap, JustifyContent: core.JustifyEnd, Gap: 8, Padding: core.Edges{Bottom: 8}}, Children: []core.Node{
			core.Each(images.Get, func(e entry) imageKey { return e.key }, func(cx *core.Scope, current func() entry, _ func() int) core.Node {
				return s.userImageTile(cx, get, func() Object { return current().meta })
			}),
		}}
	}}
}

func (s *State) userImageTile(cx *core.Scope, message func() Message, attachment func() Object) core.Node {
	meta := core.Untracked(cx, attachment)
	id := str(meta, "id")
	name := func() string { return str(attachment(), "name") }
	original := s.imageKey(meta, false)

	// Keep using the same cached source across updates to the host's resource.
	// Only the cache owns the bytes, so eviction and clearing still take effect.
	displayedKey := original
	resource := core.NewMemoFunc(cx, attachment, func(a, b Object) bool {
		return str(a, "id") == str(b, "id") && str(a, "state") == str(b, "state") && flag(a, "thumbnail") == flag(b, "thumbnail")
	})
	cx.Effect(func() {
		a := resource.Get()
		if len(s.images[displayedKey]) == 0 && len(s.images[original]) == 0 {
			s.loadImage(a, flag(a, "thumbnail"))
		}
	})
	source := func() core.ImageSource {
		s.read()
		a := resource.Get()
		if len(s.images[displayedKey]) == 0 {
			displayedKey = original
			if len(s.images[original]) == 0 {
				displayedKey = s.imageKey(a, flag(a, "thumbnail"))
			}
		}
		return core.ImageSource{Data: s.images[displayedKey]}
	}
	preview := core.View{
		TestID: "image-" + id, Role: core.RoleButton, LabelFunc: func() string { return "预览图片 " + name() },
		Tooltip: tooltipCard(cx, dynamicText(cx, name, 12, false)), OnPress: func(core.PressEvent) { s.previewAttachment(message(), attachment()) },
		Style: core.Style{Width: core.Pct(100), Height: core.Pct(100)}, StyleFunc: func() core.Style { return pressStyle(cx) },
		Children: []core.Node{core.Image{LabelFunc: name, SourceFunc: source, ResizeMode: core.Cover, Style: core.Style{Width: core.Pct(100), Height: core.Pct(100)}}},
	}
	card := core.View{TestID: "attachment-" + id, Style: core.Style{Width: core.Pt(88), MaxWidth: core.Pct(100), Height: core.Pt(88), CornerRadius: 10, Overflow: core.Hidden, BorderWidth: 1}, StyleFunc: func() core.Style { return core.Style{BorderColor: palette(cx).border, Background: palette(cx).muted} }, Children: []core.Node{preview}}
	return s.attachmentActions(cx, attachment, card, true)
}

func (s *State) previewDraftImage(selected *Upload) {
	images := []Object{}
	index := 0
	for _, u := range s.uploads {
		if !strings.HasPrefix(u.MIME, "image/") && !strings.HasPrefix(http.DetectContentType(u.Data), "image/") {
			continue
		}
		if u == selected {
			index = len(images)
		}
		images = append(images, Object{"name": u.Name, "mime": u.MIME, "kind": "image", "size": len(u.Data), "localData": u.Data})
	}
	s.preview = images
	s.previewIndex.Set(index)
	s.dialog.Set("preview")
}

const imageCacheBytes = 64 << 20

func (s *State) touchImage(key string) {
	for i, k := range s.imageOrder {
		if k == key {
			s.imageOrder = append(s.imageOrder[:i], s.imageOrder[i+1:]...)
			break
		}
	}
	s.imageOrder = append(s.imageOrder, key)
}
func (s *State) cacheImage(key string, data []byte) {
	if s.images == nil {
		s.images = map[string][]byte{}
	}
	s.imageBytes += len(data) - len(s.images[key])
	s.images[key] = data
	s.touchImage(key)
	for s.imageBytes > imageCacheBytes && len(s.imageOrder) > 0 {
		victim := s.imageOrder[0]
		s.imageOrder = s.imageOrder[1:]
		s.imageBytes -= len(s.images[victim])
		delete(s.images, victim)
	}
}
func (s *State) resetImages() {
	if s.imageCancel != nil {
		s.imageCancel()
	}
	s.imageContext = nil
	s.imageCancel = nil
	s.loadingImages = map[string]bool{}
	s.images = map[string][]byte{}
	s.imageOrder = nil
	s.imageBytes = 0
}
