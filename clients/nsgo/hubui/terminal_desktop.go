//go:build (darwin && !ios) || windows || (linux && !android)

package hubui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/monkeyWie/reignhub/clients/nsgo/terminalengine"
	"github.com/monkeyWie/reignhub/internal/bridge"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/device"
	model "github.com/monkeyWie/reignhub/internal/terminal"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/base/terminal"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

var terminalHostOnce sync.Once

func initializeTerminalHost() {
	terminalHostOnce.Do(func() { bridge.SetTerminalFactory(terminalengine.New) })
}

func (s *State) selectedTerminal() *terminalPanel {
	if s.terminalPanels == nil {
		s.terminalPanels = map[string]*terminalPanel{}
	}
	k := s.key()
	p := s.terminalPanels[k]
	if p == nil {
		p = &terminalPanel{height: 260}
		p.addTab()
		s.terminalPanels[k] = p
	}
	return p
}
func (s *State) toggleTerminal() {
	if s.selected == nil || !s.terminalVisible() && !s.agentCapability("terminal") {
		return
	}
	p := s.selectedTerminal()
	if !p.visible && p.active == nil {
		p.addTab()
	}
	p.visible = !p.visible
	s.changed()
	if !p.visible && s.composerRef != nil {
		s.composerRef.Focus()
	}
}
func (s *State) terminalButton(cx *core.Scope) core.Node {
	return core.Dynamic(func(cx *core.Scope) core.Node {
		supported := core.NewMemo(cx, func() bool { s.read(); return s.agentCapability("terminal") })
		return core.Dynamic(func(cx *core.Scope) core.Node {
			ok := supported.Get()
			title := locales.For(cx).ToggleTerminal() + " (" + cx.KeyLabel("ctrl+`") + ")"
			if !ok {
				title = locales.For(cx).TerminalUnsupported()
			}
			v := contextualIconButton(cx, "terminal-toggle", title, lucide.SquareTerminal, s.toggleTerminal)
			v.Desktop.WindowDrag = core.WindowDragOff
			v.Disabled = func() bool { return !ok }
			return v
		})
	})
}
func (s *State) withTerminal(cx *core.Scope, chat core.Node) core.Node {
	p := s.selectedTerminal()
	cx.OnKeyWith("ctrl+`", core.KeyOptions{When: core.KeyAlways}, s.toggleTerminal)
	visible := func() bool { s.read(); return p.visible }
	available := core.NewSignal(cx, core.Untracked(cx, func() core.Points { return cx.Env().Window().Height }))
	height := core.NewSignal(cx, p.height)
	limits := func() (core.Points, core.Points) {
		const chatMin, terminalMin, terminalMax = 180, 100, 600
		h := available.Get()
		lo := min(terminalMin, h*terminalMin/(chatMin+terminalMin))
		return lo, max(lo, min(terminalMax, h-chatMin))
	}
	panelHeight := func() core.Points {
		lo, hi := limits()
		return min(max(height.Get(), lo), hi)
	}
	return core.View{TestID: "conversation-split", Style: fill, OnLayout: func(b core.Bounds) {
		available.Set(b.Size.Height)
	}, Children: []core.Node{chat, core.Show{When: visible, Then: func(cx *core.Scope) core.Node {
		var start core.Points
		handle := base.ResizeHandle{TestID: "conversation-split-handle-0", Axis: base.Vertical, Label: locales.For(cx).ResizeTerminal(),
			StyleFunc: func() core.Style { return core.Style{Background: palette(cx).border} },
			Value: func() core.AccessibilityValue {
				lo, hi := limits()
				h := available.Get()
				return core.AccessibilityValue{Min: float32(h - hi), Max: float32(h - lo), Now: float32(h - panelHeight())}
			},
			OnDragStart: func() { start = panelHeight() },
			OnDragMove: func(offset core.Points) {
				lo, hi := limits()
				p.height = min(max(start-offset, lo), hi)
				height.Set(p.height)
			},
		}
		return core.Group{handle, core.View{StyleFunc: func() core.Style {
			return core.Style{Height: core.Pt(panelHeight()), FlexShrink: 0, MinWidth: core.Pt(0)}
		}, Children: []core.Node{s.terminalPane(cx, p)}}}
	}}}}
}

func (p *terminalPanel) addTab() {
	p.nextNumber++
	p.active = &terminalTab{id: device.Random(), number: p.nextNumber}
	p.tabs = append(p.tabs, p.active)
}

// Closing belongs to the conversation even if its terminal view is hidden or
// another tab is selected while the host is completing the request.
func (s *State) closeTerminal(p *terminalPanel, tab *terminalTab, reopen bool) {
	if tab == nil || tab.closing {
		return
	}
	tab.closing, tab.closeError = true, ""
	key, id := s.key(), tab.id
	call := terminalCall(s.api, s.target, str(s.selected, "providerId"), str(s.selected, "id"))
	s.changed()
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
		params := model.Params{ID: id}
		_, err := call(ctx, "terminal.close", params)
		var response *control.ResponseError
		unconfirmed := err != nil && (ctx.Err() != nil || transportError(err) || control.OutcomeUnknown(err) ||
			errors.Is(err, model.ErrCloseUnconfirmed) || errors.As(err, &response) && response.Code == "TERMINAL_CLOSE_UNCONFIRMED")
		cancel()
		if unconfirmed && s.ctx.Err() == nil {
			status, stop := context.WithTimeout(s.ctx, 3*time.Second)
			_, err = call(status, "terminal.closeStatus", params)
			stop()
		}
		s.cx.Post(func() {
			tab.closing = false
			switch {
			case err != nil:
				tab.closeError = err.Error()
				if unconfirmed {
					tab.closeError = locales.For(s.cx).TerminalCloseUnconfirmed()
				}
			case reopen:
				tab.id, tab.title, tab.openDone = device.Random(), "", nil
				tab.reportedPath, tab.reportedShell = "", ""
				tab.revision++
			default:
				i := slices.Index(p.tabs, tab)
				if i < 0 {
					return
				}
				p.tabs = slices.Delete(p.tabs, i, i+1)
				if len(p.tabs) == 0 {
					wasVisible := p.visible
					p.active, p.visible = nil, false
					if wasVisible && s.key() == key && s.composerRef != nil {
						s.composerRef.Focus()
					}
				} else if p.active == tab {
					p.active = p.tabs[min(i, len(p.tabs)-1)]
				}
			}
			s.changed()
		})
	}()
}

func terminalCall(api API, target, provider, session string) model.Call {
	return func(ctx context.Context, method string, p model.Params) (model.Snapshot, error) {
		b, err := json.Marshal(p)
		if err != nil {
			return model.Snapshot{}, err
		}
		var params Object
		if err := json.Unmarshal(b, &params); err != nil {
			return model.Snapshot{}, err
		}
		out, err := rpc(ctx, api, target, provider, session, method, Object{"params": params})
		if err != nil {
			return model.Snapshot{}, err
		}
		var snapshot model.Snapshot
		b, err = json.Marshal(out)
		if err == nil {
			err = json.Unmarshal(b, &snapshot)
		}
		return snapshot, err
	}
}

func (s *State) terminalContent(cx *core.Scope, p *terminalPanel, tab *terminalTab) core.Node {
	ctx, cancel := context.WithCancel(s.ctx)
	cx.Cleanup(cancel)
	id := tab.id
	call := terminalCall(s.api, s.target, str(s.selected, "providerId"), str(s.selected, "id"))
	live := core.NewSignal[*terminal.Terminal](cx, nil)
	problem := core.NewSignal(cx, "")
	busy := core.NewSignal(cx, true)
	exited := core.NewSignal(cx, false)
	missing := core.NewSignal(cx, false)
	post := func(f func()) {
		cx.Post(func() {
			if ctx.Err() == nil {
				f()
			}
		})
	}
	profile := tab.profile
	create := tab.openDone == nil
	if create {
		tab.openDone = make(chan struct{})
	}
	openDone := tab.openDone
	go func() {
		if !create {
			select {
			case <-ctx.Done():
				return
			case <-openDone:
			}
		}
		for attempt := 0; attempt < 5; attempt++ {
			if attempt > 0 {
				timer := time.NewTimer(time.Duration(attempt) * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			var conn *model.Conn
			var err error
			if create {
				conn, err = model.OpenConn(ctx, s.ctx, call, id, profile)
				close(openDone)
			} else {
				conn, err = model.AttachConn(ctx, call, id)
			}
			if ctx.Err() != nil {
				if conn != nil {
					_ = conn.Close()
				}
				return
			}
			if err != nil {
				retry := !create && attempt < 4 && retryTerminalConnection(err)
				post(func() { problem.Set(err.Error()); busy.Set(retry); missing.Set(terminalGone(err)) })
				if retry {
					continue
				}
				return
			}
			create = false
			t, err := terminal.New(terminal.Options{Conn: conn, ResizeByConn: true, Scrollback: 512 << 10, OnTitle: func(title string) {
				title = strings.TrimSpace(title)
				post(func() {
					if tab.setTitle(title) {
						s.changed()
					}
				})
			}})
			if err != nil {
				_ = conn.Close()
				post(func() { problem.Set(err.Error()); busy.Set(false) })
				return
			}
			failure := make(chan error, 1)
			post(func() {
				busy.Set(false)
				problem.Set("")
				missing.Set(false)
				exited.Set(false)
				conn.Bind(func(size model.Size) { t.ResizePixels(size.Cols, size.Rows, size.CellWidth, size.CellHeight) }, func(err error, ended bool) {
					if err != nil {
						select {
						case failure <- err:
						default:
						}
					}
					post(func() {
						exited.Set(ended)
						missing.Set(terminalGone(err))
						if err != nil {
							problem.Set(err.Error())
						}
					})
				})
				live.Set(t)
			})
			<-conn.Done()
			post(func() { live.Set(nil) })
			_ = t.Close()
			if ctx.Err() != nil {
				return
			}
			var cause error
			select {
			case cause = <-failure:
			default:
			}
			if !retryTerminalConnection(cause) {
				post(func() { busy.Set(false) })
				return
			}
			if attempt == 4 {
				post(func() { busy.Set(false) })
				return
			}
			post(func() { busy.Set(true) })
		}
	}()
	retry := func() {
		if exited.GetUntracked() || missing.GetUntracked() {
			s.closeTerminal(p, tab, true)
			return
		}
		tab.revision++
		s.changed()
	}
	return core.View{Style: fill, Children: []core.Node{
		core.Show{When: busy.Get, Then: func(cx *core.Scope) core.Node {
			return s.loadingRing("terminal-connecting", locales.For(cx).TerminalConnecting(), 12)
		}},
		core.Show{When: func() bool {
			s.read()
			return !tab.closing && (problem.Get() != "" || exited.Get())
		}, Then: func(cx *core.Scope) core.Node {
			status := dynamicText(cx, func() string {
				if err := problem.Get(); err != "" {
					return err
				}
				return locales.For(cx).TerminalExited()
			}, 11, true)
			reconnect := core.Dynamic(func(cx *core.Scope) core.Node {
				title := locales.For(cx).ReconnectTerminal()
				if exited.Get() || missing.Get() {
					title = locales.For(cx).ReopenTerminal()
				}
				v := contextualIconButton(cx, "terminal-reconnect", title, lucide.RefreshCw, retry)
				v.Disabled = func() bool { s.read(); return tab.closing || busy.Get() }
				return v
			})
			return core.View{TestID: "terminal-error", Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Padding: core.Symmetric(2, 12), Gap: 6}, Children: []core.Node{
				core.Show{When: func() bool { s.read(); return tab.closeError == "" }, Then: func(*core.Scope) core.Node { return status }}, reconnect,
			}}
		}},
		core.Dynamic(func(cx *core.Scope) core.Node {
			t := live.Get()
			if t == nil {
				return core.View{Style: fill}
			}
			ref := &terminal.Ref{}
			light, dark := terminal.LightTheme(), terminal.DarkTheme()
			light.Background, light.Foreground = core.RGB(lightColors.page), core.RGB(lightColors.text)
			dark.Background, dark.Foreground = core.RGB(darkColors.page), core.RGB(darkColors.text)
			view := reservedTerminalView{View: terminal.View{Terminal: t, Ref: ref, TestID: "conversation-terminal", Label: locales.For(cx).Terminal(), AutoFocus: true, Theme: light, DarkTheme: dark, Style: fill}, toggle: s.toggleTerminal}
			return ui.ContextMenu{TestID: "terminal-menu", Style: fill, Trigger: view, Children: []core.Node{
				ui.MenuItem{Text: locales.For(cx).TerminalCopy(), Icon: lucide.Copy, Disabled: func() bool { return !ref.Selected() }, OnSelect: ref.Copy},
				ui.MenuItem{Text: locales.For(cx).TerminalPaste(), Icon: lucide.ClipboardPaste, OnSelect: ref.Paste},
				ui.MenuItem{Text: locales.For(cx).TerminalSelectAll(), OnSelect: ref.SelectAll},
			}}
		}),
	}}
}

func terminalGone(err error) bool {
	var response *control.ResponseError
	return errors.Is(err, model.ErrMissing) || errors.As(err, &response) && response.Code == "TERMINAL_GONE"
}
func retryTerminalConnection(err error) bool {
	if errors.Is(err, model.ErrInputUnconfirmed) || terminalGone(err) || errors.Is(err, model.ErrDetached) || errors.Is(err, model.ErrClosing) {
		return false
	}
	var response *control.ResponseError
	if errors.As(err, &response) {
		switch response.Code {
		case "TERMINAL_DENIED", "TERMINAL_DETACHED", "TERMINAL_CLOSING", "TERMINAL_UNSUPPORTED", control.ConnectionAuthorizationExpired:
			return false
		}
	}
	return true
}

// NSGo v0.2.0 binds every Ctrl character in scoped Show branches on focus and
// after IME composition. Decorate those public render nodes so the application's
// reserved shortcut is registered last in exactly the same lifetime, without
// changing the framework or intercepting shell Ctrl+C/Ctrl+Space bytes.
type reservedTerminalView struct {
	terminal.View
	toggle func()
}

func (v reservedTerminalView) Render(cx *core.Scope) core.Node {
	root := v.View.Render(cx).(core.View)
	for i, node := range root.Children {
		if show, ok := node.(core.Show); ok && show.Then != nil {
			then := show.Then
			show.Then = func(cx *core.Scope) core.Node {
				n := then(cx)
				cx.OnKeyWith("ctrl+`", core.KeyOptions{When: core.KeyAlways}, v.toggle)
				return n
			}
			root.Children[i] = show
		}
	}
	return root
}

func (s *State) terminalSettings(cx *core.Scope) core.Node {
	checked := func() bool { s.read(); return flag(s.host, "remoteTerminalEnabled") }
	look := ui.NewSwitchLook(cx, checked, ui.SizeSmall)
	return row(core.View{Style: core.Style{FlexGrow: 1, Gap: 4}, Children: []core.Node{
		label(cx, locales.For(cx).RemoteTerminal(), 14, false), label(cx, locales.For(cx).RemoteTerminalHint(), 12, true),
	}}, base.Switch{TestID: "remote-terminal-enabled", Label: locales.For(cx).RemoteTerminal(), Checked: checked,
		Disabled: func() bool { return s.actionPending("remote-terminal-enabled") }, TrackStyleFunc: look.Track, ThumbStyleFunc: look.Thumb,
		OnChange: func(value bool) {
			s.call("remote-terminal-enabled", "host.terminal.enabled", Object{"enabled": value}, func(any) { s.host = copyObject(s.host); s.host["remoteTerminalEnabled"] = value })
		},
	})
}
