package hubui

import (
	"errors"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

func (s *State) scan(_ *core.Scope) { s.scanned = nil; s.dialog.Set("scan") }

func (s *State) scanView(cx *core.Scope, close func()) core.Node {
	l := locales.For(cx)
	errorText := core.NewSignal(cx, "")
	pending := core.NewSignal(cx, "")
	start := func() {
		if pending.GetUntracked() != "" {
			return
		}
		pending.Set("camera")
		errorText.Set("")
		cx.ScanCode(core.ScanOptions{Kinds: []core.CodeKind{core.CodeQR}}, func(code *core.ScannedCode, err error) {
			pending.Set("")
			switch {
			case errors.Is(err, core.ErrNoScanner):
				errorText.Set(l.ScanCameraUnavailable())
			case err != nil:
				errorText.Set(err.Error())
			case code != nil && !s.acceptQR(code.Text):
				errorText.Set(l.ScanInvalidDevice())
			}
		})
	}
	// The picker and its callbacks belong to this dialog's mounted scope.
	cx.Post(start)
	camera := busyButton(cx, "scan-camera", l.ScanOpenCamera(), lucide.ScanLine, "primary", func() bool {
		return pending.Get() == "camera"
	}, start).(core.View)
	gallery := busyButton(cx, "scan-gallery", l.ScanFromPhotos(), lucide.Image, "outline", func() bool {
		return pending.Get() == "gallery"
	}, func() {
		if pending.GetUntracked() != "" {
			return
		}
		pending.Set("gallery")
		errorText.Set("")
		s.scanPhoto(cx, func(message string) {
			errorText.Set(message)
			pending.Set("")
		})
	}).(core.View)
	busy := func() bool { return pending.Get() != "" }
	camera.Disabled, gallery.Disabled = busy, busy
	manual := button(cx, "scan-manual", l.ScanEnterID(), lucide.Keyboard, "ghost", func() {
		s.dialog.Set("connect")
	})
	return modalDialog(cx, ui.Dialog{
		TestID: "dialog-scan", Title: l.ScanDeviceCode(), Open: func() bool { return true },
		OnOpenChange: func(open bool, _ base.Reason) {
			if !open {
				close()
			}
		},
		Children: []core.Node{
			core.Show{When: func() bool { return errorText.Get() != "" }, Then: func(cx *core.Scope) core.Node {
				return dynamicText(cx, errorText.Get, 13, true)
			}},
			camera, gallery, manual,
		},
	}, close)
}
