package hubui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/monkeyWie/reignhub/pkg/messagecontent"
	"github.com/nsgo-dev/nsgo/base/textview"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
	"github.com/nsgo-dev/nsgo/ui/chat"
)

func (s *State) conversation(cx *core.Scope, narrow bool) core.Node {
	if s.selected == nil {
		return s.home(cx)
	}
	children := []core.Node{}
	floatingHeader := s.width.GetUntracked() < compactBreakpoint
	anchor := &messageScrollAnchor{following: true, atBottom: core.NewSignal(cx, true)}
	s.timeline = anchor
	cx.Cleanup(func() {
		if s.timeline == anchor {
			s.timeline = nil
		}
	})
	listRef := &anchor.list
	historyHeader, historyFooter, checkHistory, scrollHistory := s.historyScroll(cx, anchor, choose(floatingHeader, core.Points(88), core.Points(0)))
	viewport := core.NewSignal(cx, core.Size{})
	// A native resize may finish with the last pre-resize offset. Wait for
	// two quiet layout frames before one absolute alignment; never combine it
	// with the List's relative correction in the same layout/scroll callback.
	var cancelAlignment func()
	settleResize := func() {
		if cancelAlignment != nil {
			cancelAlignment()
			cancelAlignment = nil
		}
		if !anchor.following || !anchor.restoring || anchor.dragging {
			return
		}
		cancelAlignment = cx.RequestAnimationFrame(func(time.Time) {
			cancelAlignment = cx.RequestAnimationFrame(func(time.Time) {
				cancelAlignment = nil
				if anchor.following && anchor.restoring && !anchor.dragging {
					anchor.list.ScrollToEnd(false)
				}
			})
		})
	}
	var lastOffset core.Points
	var cancelBoundsCheck func()
	updateScroll := func(e core.ScrollEvent) {
		size, content := anchor.size, anchor.content
		anchor.scroll(e)
		if cancelBoundsCheck != nil {
			cancelBoundsCheck()
			cancelBoundsCheck = nil
		}
		// A delayed page can shrink the list after the last gesture event.
		// Let the platform apply its own correction before reconciling it.
		if outsideScrollRange(e) && !anchor.dragging && !anchor.momentum {
			cancelBoundsCheck = cx.RequestAnimationFrame(func(time.Time) {
				cancelBoundsCheck = nil
				anchor.settle(e)
			})
		}
		if !e.Stale && e.Size.Height > 0 {
			if s.reading != nil {
				anchor.following, anchor.restoring = false, false
			}
			if anchor.pendingRow == nil && (anchor.dragging || content == e.ContentSize) && e.Offset.Y != lastOffset {
				scrollHistory(choose(e.Offset.Y > lastOffset, 1, -1))
			}
			lastOffset = e.Offset.Y
		}
		if size != anchor.size {
			settleResize()
		}
		anchor.atBottom.Set(anchor.following)
	}
	// Connection/approval polling must not rebuild the entire list's data and
	// navigation index. Message projections are replaced, never edited in place.
	var previousRows []Message
	rows := core.NewMemoFunc(cx, func() []Message {
		s.read()
		anchor.preserveReading(cx, previousRows, s)
		previousRows = s.messageRows
		return s.messageRows
	}, func(a, b []Message) bool {
		return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
	})
	tail := func() bool {
		s.read()
		active := activeTurn(s.transcript)
		if active == nil || !s.connected() {
			return false
		}
		for _, i := range objects(active["items"]) {
			if visibleTool(i) || str(i, "role") == "assistant" && strings.TrimSpace(str(i, "text")) != "" {
				return false
			}
		}
		return true
	}
	tailCount := func() int {
		s.read()
		if s.reading != nil {
			return 0
		}
		n := len(s.approvals)
		if tail() {
			n++
		}
		return n
	}
	// Pending approvals follow the transcript, with the first nearest the composer.
	approvalIndex := func(i int) int { return len(rows.Get()) + tailCount() - 1 - i }
	rowKey := func(i int) string {
		messages := rows.Get()
		if i < len(messages) {
			id := messages[i].ID
			if local := s.confirmedRowKeys[id]; local != "" {
				return local
			}
			return id
		}
		a := approvalIndex(i)
		if a < 0 {
			return "" // Recycled cell from a longer list.
		}
		if a < len(s.approvals) {
			return "approval:" + str(s.approvals[a], "id")
		}
		return "thinking"
	}
	navigate := func(index int) {
		// Declare reading intent before a virtualized jump measures new rows.
		// Those measurements must not restore the previous bottom anchor.
		anchor.following, anchor.restoring = false, false
		anchor.pendingRow = nil
		anchor.atBottom.Set(false)
		listRef.ScrollToIndexAt(index, .5, 0, false)
	}
	list := core.List{TestID: "chat-message-list", Ref: listRef, Alignment: core.ListAlignEnd, EstimatedSize: 120, Style: fill,
		Count: func() int { return len(rows.Get()) + tailCount() }, Key: func(i int) any { return rowKey(i) },
		OnLayout: func(b core.Bounds) {
			viewport.Set(b.Size)
			checkHistory()
			previous := anchor.size
			anchor.layout(b.Size)
			if previous != anchor.size {
				settleResize()
			}
		}, OnScroll: updateScroll,

		OnViewableItemsChange: func(items core.ViewableItems) {
			anchor.lastVisible = ""
			if n := len(items.Viewable); n > 0 {
				anchor.lastVisible, _ = items.Viewable[n-1].Key.(string)
			}
			checkHistory()
		},
		OnScrollDragStart: func(e core.ScrollEvent) {
			anchor.pendingRow = nil
			anchor.dragging, anchor.restoring = true, false
			updateScroll(e)
		}, OnScrollDragEnd: func(e core.ScrollEvent) {
			updateScroll(e)
			anchor.dragging = false
		},
		OnMomentumStart: func(core.ScrollEvent) { anchor.momentum = true },
		OnMomentumEnd: func(e core.ScrollEvent) {
			anchor.momentum = false
			anchor.settle(e)
		},
		OnScrollEnd: func(e core.ScrollEvent) {
			anchor.settle(e)
			checkHistory()
		},
		Row: func(cx *core.Scope, r core.ListRow) core.Node {
			key := core.NewMemo(cx, func() string { return rowKey(r.Index()) })
			return core.Dynamic(func(cx *core.Scope) core.Node {
				id := key.Get()
				if strings.HasPrefix(id, "approval:") || id == "thinking" {
					i := core.Untracked(cx, func() int { return approvalIndex(r.Index()) })
					if i >= 0 && i < len(s.approvals) {
						return s.approvalCard(cx, s.approvals[i])
					}
					return s.messageInset(cx, core.View{Style: core.Style{Padding: core.Edges{Top: 12, Bottom: 20}}, Children: []core.Node{chat.Shimmer{Text: func() string { return "Thinking…" }, Size: 13}}})
				}
				get := func() Message {
					index := r.Index()
					v := rows.Get()
					if index < 0 || index >= len(v) {
						return Message{}
					}
					return v[index]
				}
				return s.messageInset(cx, s.message(cx, get))
			})
		}, Footer: historyFooter, Header: historyHeader,
	}
	timelineChildren := []core.Node{list, s.emptyTranscript(cx), s.syncIndicator(cx), core.Show{When: func() bool { return !s.narrowLayout() }, Then: func(cx *core.Scope) core.Node { return s.messageNavigation(cx, rows, viewport, navigate) }}, core.Show{When: func() bool { s.read(); return s.reading != nil || !anchor.atBottom.Get() }, Then: func(cx *core.Scope) core.Node {
		v := button(cx, "jump-to-latest", "", lucide.ArrowDown, "outline", func() { s.latestPage(); anchor.jumpToLatest() }).(core.View)
		v.Label, v.Tooltip = locales.For(cx).JumpToLatest(), tooltipCard(cx, label(cx, locales.For(cx).JumpToLatest(), 12, false))
		v.Style.Width, v.Style.Height, v.Style.MinHeight = core.Pt(36), core.Pt(36), core.Pt(36)
		v.Style.Padding, v.Style.CornerRadius = core.All(8), 18
		if !cx.Env().Desktop() {
			v.Style.Width, v.Style.Height, v.Style.MinHeight, v.Style.CornerRadius = core.Pt(44), core.Pt(44), core.Pt(44), 22
		}
		return core.View{PointerEvents: core.PointerEventsBoxNone, Style: core.Style{Position: core.Absolute, Bottom: core.Pt(12), Left: core.Pt(0), Right: core.Pt(0), AlignItems: core.AlignCenter}, Children: []core.Node{v}}
	}}}
	if floatingHeader {
		timelineChildren = append(timelineChildren, s.compactConversationHeader(cx, narrow))
	}
	children = append(children, core.View{TestID: "timeline", Style: fill, Children: timelineChildren})
	children = append(children, s.composer(cx))
	return core.View{TestID: "conversation", Style: fill, StyleFunc: func() core.Style { return s.workspaceSurface(cx) }, Children: []core.Node{s.withTerminal(cx, core.View{Style: fill, Children: children})}}
}

// The narrow-layout toolbar floats over the recycled list. Only the beginning of the
// history has an inset, so scrolled messages pass beneath the fading backdrop.
func (s *State) compactConversationHeader(cx *core.Scope, menu bool) core.Node {
	pill := func(children ...core.Node) core.Node {
		return core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter, Height: core.Pt(52), CornerRadius: 26, FlexShrink: 0, Padding: core.Symmetric(0, 4)}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).background} }, Children: children}
	}
	buttons := []core.Node{}
	if menu {
		buttons = append(buttons, pill(s.mobileMenuButton(cx)))
	}
	buttons = append(buttons, core.View{TestID: "conversation-title-pill", Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Height: core.Pt(52), CornerRadius: 26, JustifyContent: core.JustifyCenter, Padding: core.Symmetric(6, 16), Gap: 2}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).background} }, Children: []core.Node{
		singleLine(cx, func() string { s.read(); return str(s.selected, "title") }, 14, false),
		singleLine(cx, func() string {
			s.read()
			cwd := strings.TrimRight(strings.ReplaceAll(str(s.selected, "cwd"), "\\", "/"), "/")
			if i := strings.LastIndex(cwd, "/"); i >= 0 {
				cwd = cwd[i+1:]
			}
			return cwd
		}, 11, true),
	}}, pill(s.conversationConnectionIndicator(cx), s.terminalButton(cx), labelledIconButton(cx, "attachment-cache", "管理附件缓存", lucide.HardDrive, func() { s.dialog.Set("clear-cache") })))
	return core.View{TestID: "conversation-header", PointerEvents: core.PointerEventsBoxNone, Style: core.Style{Position: core.Absolute, Top: core.Pt(0), Left: core.Pt(0), Right: core.Pt(0), Height: core.Pt(100), ZIndex: 5}, Children: []core.Node{
		core.View{TestID: "conversation-header-fade", PointerEvents: core.PointerEventsNone, Style: core.Style{Position: core.Absolute, Top: core.Pt(0), Bottom: core.Pt(0), Left: core.Pt(0), Right: core.Pt(0)}, StyleFunc: func() core.Style {
			bg := palette(cx).background
			mask := core.LinearGradient(180, core.RGB(0xffffff), core.RGB(0xffffff), core.RGB(0xffffff).Faded(0))
			return core.Style{Gradients: []core.Gradient{core.LinearGradient(180, bg.Faded(.9), bg.Faded(.6), bg.Faded(0))}, BackdropFilters: []core.Filter{core.Blur(10)}, Mask: &mask}
		}},
		core.View{PointerEvents: core.PointerEventsBoxNone, Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, Padding: core.Edges{Top: 8, Left: 12, Right: 12}, MinWidth: core.Pt(0)}, Children: buttons},
	}}
}
func (s *State) messageInset(cx *core.Scope, content core.Node) core.Node {
	return core.View{Style: core.Style{Width: core.Pct(100), MinWidth: core.Pt(0), AlignItems: core.AlignCenter}, Children: []core.Node{core.View{Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(988)}, StyleFunc: func() core.Style {
		inset := core.Points(40)
		width := s.width.Get()
		if width >= compactBreakpoint {
			width -= max(260, min(s.sessionWidth.Get(), width*.46)) + 5
		} else {
			inset = 20
		}
		if width >= wideBreakpoint {
			inset = 64
		}
		return core.Style{Padding: core.Symmetric(0, inset), MaxWidth: core.Pt(860 + inset*2)}
	}, Children: []core.Node{content}}}}
}
func (s *State) message(cx *core.Scope, get func() Message) core.Node {
	m := core.Untracked(cx, get)
	if m.Role == "compaction" {
		icon := core.Icon{SVG: lucide.FoldVertical(), Style: core.Style{Width: core.Pt(15), Height: core.Pt(15), FlexShrink: 0}, StyleFunc: func() core.Style { return core.Style{Color: palette(cx).mutedText} }}
		return core.View{TestID: "message-" + m.ID, Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, Padding: core.Symmetric(8, 0)}, Children: []core.Node{icon, label(cx, locales.For(cx).ContextCompacted(), 13, true)}}
	}
	if m.Role == "work" {
		return s.workSummary(cx, get)
	}
	if m.Role == "activity" {
		return s.activity(cx, get)
	}
	hover := core.NewSignal(cx, false)
	user := m.Role == "user"
	// User bubbles use their intrinsic content width; the parent provides the
	// available width for wrapping. Assistant articles fill the message column.
	bodyWidth := core.Pct(100)
	if user {
		bodyWidth = core.Length{}
	}
	content := []core.Node{}
	if user {
		content = append(content, s.userImageGallery(cx, get))
	}
	bodyText := smoothReply(cx, func() string {
		if user {
			return strings.TrimRight(displayText(get()), " \t\r\n")
		}
		return displayText(get())
	}, func() bool {
		s.read()
		return !user && s.animateText[get().ID] && !s.transcriptSyncing && s.connected()
	})
	body := ui.TextView{MarkdownFunc: bodyText, Options: textview.Options{Streaming: m.Live}, RenderImage: func(cx *core.Scope, im textview.Image) core.Node {
		for _, a := range get().Attachments {
			if str(a, "source") == im.Src {
				return s.imageTile(cx, get, func() Object { return a })
			}
		}
		return label(cx, choose(im.Alt != "", im.Alt, "图片不可用"), 13, true)
	}, Selectable: true, OpenLinks: false, OnLinkPress: func(e textview.LinkPressEvent) { s.openLink(cx, get(), e.URL) }, CodeBlockActions: []textview.CodeAction{{ID: textview.CopyAction, Label: "复制代码"}},
		RenderCodeBlockAction: func(cx *core.Scope, a textview.CodeBlockAction) core.Node {
			return core.View{TestID: "code-copy-icon", Tooltip: tooltipCard(cx, dynamicText(cx, func() string { return choose(a.Done(), "已复制", "复制代码") }, 12, false)), Children: []core.Node{core.Dynamic(func(cx *core.Scope) core.Node {
				return core.View{TestID: choose(a.Done(), "code-copy-done", "code-copy-ready"), Children: []core.Node{glyph(cx, choose(a.Done(), lucide.Check, lucide.Copy), 16)}}
			})}}
		}, Styles: textview.Styles{textview.PartRoot: core.Style{FontSize: 15, LineHeight: 25, MinWidth: core.Pt(0), Width: bodyWidth}}, StyleOf: func(p textview.Part) core.Style {
			c := palette(cx)
			switch p {
			case textview.PartRoot, textview.PartParagraph, textview.PartHeading:
				return core.Style{Color: c.foreground}
			case textview.PartTable, textview.PartTableScroll:
				// Constrain the viewport; the Markdown grid keeps its intrinsic column widths.
				return core.Style{MaxWidth: core.Pct(100), MinWidth: core.Pt(0)}
			case textview.PartCodeBlock:
				return core.Style{Background: c.code, BorderWidth: 1, BorderColor: c.border, CornerRadius: 12, Overflow: core.Hidden}
			case textview.PartCodeBlockAction:
				return contextualIconStyle(cx).Merge(core.Style{Width: core.Pt(30), Height: core.Pt(30), Padding: core.All(7), Background: core.RGBA(0, 0), BorderStyle: core.BorderNone, CornerRadius: 6})
			case textview.PartCodeBlockScroll:
				return core.Style{Background: c.code, Padding: core.Edges{Top: 38, Left: 18, Right: 18, Bottom: 18}}
			case textview.PartCode:
				return core.Style{Color: c.brand, Background: c.code, CornerRadius: 4}
			case textview.PartLink, textview.PartFootnoteReference:
				return core.Style{Color: c.brand, TextDecorationColor: c.brand.Faded(.5), Cursor: core.CursorPointer}
			}
			return core.Style{}
		}}
	var bodyContent core.Node = body
	if user {
		// A menu/selection belongs to these text views. Polls, delivery changes,
		// and other messages must not dispose them while this text is unchanged.
		text := core.NewMemo(cx, func() string { return displayText(get()) })
		bodyContent = core.Dynamic(func(cx *core.Scope) core.Node {
			children := []core.Node{}
			for _, part := range messagecontent.Parse(text.Get()) {
				if len(part.Replies) > 0 {
					children = append(children, questionReplyCard(cx, part.Replies))
				} else if strings.TrimSpace(part.Text) != "" {
					text := part.Text
					view := body
					view.MarkdownFunc = func() string { return strings.TrimRight(text, " \t\r\n") }
					children = append(children, view)
				}
			}
			return core.View{Style: core.Style{Gap: 12, MinWidth: core.Pt(0)}, Children: children}
		})
	}
	bubble := core.View{TestID: "message-body-" + m.ID, Selectable: true, Style: core.Style{Width: bodyWidth, FlexShrink: 1, MinWidth: core.Pt(0), MaxWidth: core.Pt(choose(user, core.Points(680), core.Points(820)))}, StyleFunc: func() core.Style {
		if user {
			return core.Style{Background: palette(cx).muted, CornerRadius: 16, Padding: core.Symmetric(10, 16)}
		}
		return core.Style{}
	}, Children: []core.Node{bodyContent}}
	var bubbleContent core.Node = bubble
	if user {
		bubbleContent = core.View{Style: core.Style{Direction: core.Row, Width: core.Pct(100), MinWidth: core.Pt(0), JustifyContent: core.JustifyEnd}, Children: []core.Node{bubble}}
	}
	content = append(content, core.Show{When: func() bool { return strings.TrimSpace(displayText(get())) != "" }, Then: func(cx *core.Scope) core.Node {
		messageOverflow.Provide(cx, true)
		return bubbleContent
	}})
	content = append(content, s.attachmentGallery(cx, get))
	delivery := core.Show{When: func() bool { return get().Delivery != "" }, Then: func(cx *core.Scope) core.Node {
		return core.View{TestID: "delivery-" + m.ID, Tooltip: tooltipCard(cx, dynamicText(cx, func() string { return get().DeliveryError }, 12, false)), Style: core.Style{JustifyContent: core.JustifyCenter}, Children: []core.Node{core.Show{When: func() bool { return get().Delivery == "sending" }, Then: func(*core.Scope) core.Node {
			return chat.Shimmer{Text: func() string { return "发送中…" }, Size: 11}
		}, Else: func(cx *core.Scope) core.Node {
			return dynamicText(cx, func() string {
				switch get().Delivery {
				case "failed":
					return "发送失败"
				case "uncertain":
					return "发送结果待确认"
				default:
					return "已发送"
				}
			}, 11, true)
		}}}}
	}}
	actions := core.Show{When: func() bool { return !get().Live && get().Delivery != "sending" }, Then: func(cx *core.Scope) core.Node {
		// Align the visible action icon with the article, while its full hit
		// target extends into the existing message gutter.
		inset := choose(user, core.Points(0), -buttonHorizontalPadding)
		return core.View{TestID: "message-actions-" + m.ID, Style: core.Style{Height: core.Pt(32), Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, Margin: core.Edges{Left: inset}}, StyleFunc: func() core.Style {
			visible := hover.Get() || s.narrowLayout() || get().Delivery == "failed"
			return core.Style{Opacity: core.Alpha(choose(visible, float32(1), float32(0))), PointerEvents: choose(visible, core.PointerEventsAuto, core.PointerEventsNone)}
		}, Children: []core.Node{core.Show{When: func() bool { return user && get().Delivery == "failed" }, Then: func(cx *core.Scope) core.Node {
			retry := button(cx, "retry-message-"+m.ID, "", lucide.RotateCcw, "ghost", func() { s.retryOutgoing(m.ID) }).(core.View)
			retry.Label = "重试发送"
			retry.Tooltip = tooltipCard(cx, label(cx, "重试发送", 12, false))
			retry.Disabled = func() bool { return !s.canSubmitMessage() }
			return retry
		}}, s.copyButton(cx, "copy-message-"+m.ID, "", "ghost", func() string { return messageCopyText(get()) }), dynamicText(cx, func() string {
			ts := get().Timestamp
			if ts == 0 {
				return "时间未知"
			}
			return s.localTime(time.UnixMilli(ts)).Format("15:04")
		}, 11, true)}}
	}}
	if user {
		content = append(content, core.View{Style: core.Style{Direction: core.Row, Height: core.Pt(32), AlignItems: core.AlignCenter, Gap: 8}, Children: []core.Node{delivery, actions}})
	} else {
		content = append(content, actions)
	}
	return core.View{TestID: "message-" + m.ID, OnMouseEnter: func(core.MouseEvent) { hover.Set(true) }, OnMouseLeave: func(core.MouseEvent) { hover.Set(false) }, Style: core.Style{AlignItems: choose(user, core.AlignEnd, core.AlignStretch), Padding: core.Edges{Top: 10, Bottom: 10}, Width: core.Pct(100), MinWidth: core.Pt(0)}, Children: content}
}
func (s *State) activity(cx *core.Scope, get func() Message) core.Node {
	open := core.NewSignal(cx, false)
	hover := core.NewSignal(cx, false)
	tools := func() []Object { return get().Tools }
	title := func() string { return activityLabel(get()) }
	color := func() core.Color {
		if hover.Get() {
			return palette(cx).foreground
		}
		return palette(cx).mutedText
	}
	icon := core.Icon{SVG: lucide.Wrench(), Style: core.Style{Width: core.Pt(16), Height: core.Pt(16), FlexShrink: 0}, StyleFunc: func() core.Style { return core.Style{Color: color()} }}
	return core.View{TestID: "tool-group", Style: core.Style{Padding: core.Edges{Top: 10, Bottom: 10}, Gap: 8}, Children: []core.Node{core.View{TestID: "tool-group-toggle", Role: core.RoleButton, LabelFunc: title, OnMouseEnter: func(core.MouseEvent) { hover.Set(true) }, OnMouseLeave: func(core.MouseEvent) { hover.Set(false) }, OnPress: func(core.PressEvent) { open.Set(!open.Get()) }, Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 9, Padding: core.Symmetric(8, 0), CornerRadius: 8}, StyleFunc: func() core.Style {
		return core.Style{Active: &core.Style{Opacity: core.Alpha(.65)}, FocusVisible: &core.Style{OutlineWidth: 2, OutlineColor: palette(cx).focus}}
	}, Children: []core.Node{icon, core.Show{When: func() bool { return get().Live && s.connected() }, Then: func(*core.Scope) core.Node { return chat.Shimmer{Text: title, Size: 12, Color: color} }, Else: func(cx *core.Scope) core.Node {
		text := singleLine(cx, title, 12, true)
		text.StyleFunc = func() core.Style { return core.Style{Color: color()} }
		return text
	}}, core.Show{When: open.Get, Then: func(cx *core.Scope) core.Node { return glyph(cx, lucide.ChevronUp, 12) }}}}, core.Show{When: open.Get, Then: func(cx *core.Scope) core.Node {
		return core.List{Count: func() int { return len(tools()) }, EstimatedSize: 36, StyleFunc: func() core.Style {
			return core.Style{Height: core.Pt(core.Points(min(8, len(tools()))) * 44), MaxHeight: core.Pt(352)}
		}, Row: func(cx *core.Scope, r core.ListRow) core.Node {
			return core.Dynamic(func(cx *core.Scope) core.Node {
				v := tools()
				if r.Index() >= len(v) {
					return nil
				}
				t := v[r.Index()]
				acts := objects(t["activities"])
				parts := []core.Node{}
				for _, a := range acts {
					title, d := str(a, "title"), str(a, "detail")
					if str(a, "kind") == "command" && d != "" {
						title = "运行命令"
					}
					parts = append(parts, row(glyph(cx, lucide.Wrench, 13), label(cx, title, 12, true)))
					if d != "" {
						parts = append(parts, label(cx, d, 11, true))
					}
				}
				if len(parts) == 0 {
					parts = append(parts, label(cx, choose(str(t, "status") == "completed", "操作已完成", "正在执行…"), 12, true))
				}
				return core.View{Style: core.Style{Padding: core.Symmetric(6, 0), Gap: 4}, Children: parts}
			})
		}}
	}}}}
}
func (s *State) composer(cx *core.Scope) core.Node {
	input := &core.Ref{}
	s.composerRef = input
	cx.Cleanup(func() {
		if s.composerRef == input {
			s.composerRef = nil
		}
	})
	key, generation := s.key(), s.sessionGeneration
	focusRequested := false
	cx.Effect(func() {
		s.read()
		if focusRequested || s.key() != key || s.sessionGeneration != generation || !s.draftReady() {
			return
		}
		focusRequested = true
		// Focus after the restored draft and enabled editor reach the platform.
		// This scope belongs to the entry, so navigation cancels a pending focus.
		cx.Post(func() {
			if s.key() == key && s.sessionGeneration == generation && s.draftReady() &&
				s.dialog.GetUntracked() == "" && s.hostAuthorization.GetUntracked() == "" && !s.terminalVisible() {
				input.Focus()
			}
		})
	})
	inputHeight := core.NewSignal(cx, core.Points(56))
	modelCatalog := core.NewMemo(cx, func() string {
		s.read()
		b, _ := json.Marshal(s.models)
		return string(b) + "|" + s.model + "|" + fmt.Sprint(activeTurn(s.transcript) != nil)
	})
	running := func() bool { s.read(); return s.connected() && activeTurn(s.transcript) != nil }
	disabled := func() bool { s.read(); return !s.connected() || !s.draftReady() }
	submit := core.SubmitBehaviorSubmit
	if isMobile(cx) {
		submit = core.SubmitBehaviorNewline
	}
	return core.View{Style: core.Style{AlignItems: core.AlignCenter, Padding: core.Edges{Top: 6, Left: 18, Right: 18, Bottom: 18}, PaddingEnv: core.EnvEdges{Bottom: core.EnvKeyboard}}, Children: []core.Node{core.Show{When: s.errorAtComposer, Then: func(cx *core.Scope) core.Node { return s.errorOverlay(cx, true) }}, s.messageQueueView(cx), core.Show{When: func() bool { s.read(); return s.draftSaveFailed }, Then: func(cx *core.Scope) core.Node {
		return button(cx, "retry-save-draft", "重试保存草稿", lucide.RotateCcw, "ghost", func() { s.draftSaveFailed = false; s.draftWriter.retry(); s.changed() })
	}}, core.Show{When: func() bool { s.read(); return s.draftLoads[s.key()] == "failed" }, Then: func(cx *core.Scope) core.Node {
		return button(cx, "retry-draft", "重试恢复草稿", lucide.RotateCcw, "ghost", func() { s.loadDraft(); s.changed() })
	}}, core.View{TestID: "composer-card", OnFileDrop: func(uris []string) {
		if !isMobile(cx) {
			s.drop(cx, uris)
		}
	}, Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(860), Padding: core.All(12), CornerRadius: 18, BorderWidth: 1, Gap: 10}, StyleFunc: func() core.Style {
		c := palette(cx)
		return core.Style{Background: c.elevated, BorderColor: c.border, Color: c.foreground, Shadows: []core.BoxShadow{{Offset: core.Point{Y: 5}, Blur: 20, Color: core.RGB(0x000000).Faded(choose(cx.Dark(), float32(.18), float32(.07)))}}}
	}, Children: []core.Node{
		core.Show{When: func() bool { s.read(); return len(s.uploads) > 0 }, Then: func(cx *core.Scope) core.Node { return s.uploadPreviews(cx) }},
		core.Input{Ref: input, TestID: "chat-input", Label: "消息", Disabled: func() bool { s.read(); return !s.draftReady() }, Placeholder: "发送消息…", Value: s.draft.Get, OnChange: s.changeDraft, Multiline: true, SubmitBehavior: submit,
			OnKeyPress: func(key string) {
				if key != "Enter" {
					s.beginDraftEdit()
				}
			},
			OnCompositionChange: func(c core.Composition) {
				if c.Composing {
					s.beginDraftEdit()
				}
			},
			OnSubmit: func(text string) { s.submitDraft(text, choose(running(), "steer", "send")) }, OnPaste: func(e core.PasteEvent) {
				s.beginDraftEdit()
				if !isMobile(cx) {
					s.paste(cx, e)
				}
			}, OnContentSizeChange: func(size core.Size) { inputHeight.Set(min(168, max(56, size.Height+12))) }, Style: core.Style{FontSize: 14, LineHeight: 23, Padding: core.Symmetric(10, 4)}, StyleFunc: func() core.Style {
				return textInputStyle(cx).Merge(core.Style{Height: core.Pt(inputHeight.Get())})
			}},
		core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 10}, Children: []core.Node{core.Show{When: func() bool { return s.agentCapability("imageInput") || s.agentCapability("fileInput") }, Then: func(cx *core.Scope) core.Node {
			v := labelledIconButton(cx, "attach-files", "添加附件", lucide.Plus, func() { s.chooseFiles(cx) }).(core.View)
			v.Disabled = disabled
			return v
		}}, spacer(), core.Dynamic(func(cx *core.Scope) core.Node {
			_ = modelCatalog.Get()
			options := []chat.Option{}
			found := false
			for _, m := range s.models {
				options = append(options, chat.Option{ID: str(m, "id"), Label: str(m, "name")})
				found = found || str(m, "id") == s.model
			}
			if s.model != "" && !found {
				options = append([]chat.Option{{ID: s.model, Label: s.model}}, options...)
			}
			if core.Untracked(cx, running) {
				name := s.model
				for _, m := range s.models {
					if str(m, "id") == s.model {
						name = str(m, "name")
						break
					}
				}
				if name == "" {
					name = "Agent 默认模型"
				}
				return label(cx, name, 12, true)
			}
			return core.View{Style: core.Style{MaxWidth: core.Pt(220), FlexShrink: 1, MinWidth: core.Pt(0)}, Children: []core.Node{chat.Picker{TestID: "model-selector", Label: func() string {
				s.read()
				for _, m := range s.models {
					if str(m, "id") == s.model {
						return str(m, "name")
					}
				}
				if s.model != "" {
					return s.model
				}
				return "Agent 默认模型"
			}, Options: options, Selected: func() string { s.read(); return s.model }, OnChange: func(id string) { s.model = id; s.persistDraft(); s.changed() }, Width: 300, SearchLabel: "搜索模型"}}}
		}), core.Show{When: func() bool { return running() && s.agentCapability("interrupt") }, Then: func(cx *core.Scope) core.Node {
			return core.View{TestID: "interrupt-generation", Role: core.RoleButton, Label: "停止生成", Tooltip: tooltipCard(cx, label(cx, "停止生成", 12, false)), Busy: func() bool { return s.pendingSend() == "interrupt" }, Disabled: func() bool { return !s.connected() || s.pendingSend() != "" }, OnPress: func(core.PressEvent) { s.send("interrupt") }, Style: core.Style{Width: core.Pt(36), Height: core.Pt(36), CornerRadius: 18, FlexShrink: 0, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter}, StyleFunc: func() core.Style {
				return filledButtonStyle(cx)
			}, Children: []core.Node{core.Show{When: func() bool { return s.pendingSend() == "interrupt" }, Then: func(cx *core.Scope) core.Node {
				theme := *cx.Theme()
				theme.Light.TextMuted, theme.Dark.TextMuted = theme.Light.Background, theme.Dark.Background
				cx.SetTheme(&theme)
				return actionSpinner(cx, "interrupt-loading", "正在停止生成", false, 18)
			}, Else: func(cx *core.Scope) core.Node {
				return core.View{Style: core.Style{Width: core.Pt(11), Height: core.Pt(11), CornerRadius: 2}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).background} }}
			}}}}
		}}, core.View{TestID: "send-message", Role: core.RoleButton, LabelFunc: func() string {
			if s.pendingSend() != "" && s.pendingSend() != "interrupt" {
				return "正在发送"
			}
			return "发送消息"
		}, Busy: func() bool { return s.pendingSend() != "" && s.pendingSend() != "interrupt" }, Disabled: func() bool { return !s.canSend() }, OnPress: func(core.PressEvent) { s.send(choose(running(), "steer", "send")) }, Style: core.Style{Width: core.Pt(36), Height: core.Pt(36), CornerRadius: 18, FlexShrink: 0, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter}, StyleFunc: func() core.Style {
			opacity := float32(1)
			if !s.canSend() && (s.pendingSend() == "" || s.pendingSend() == "interrupt") {
				opacity = .35
			}
			return filledButtonStyle(cx).Merge(core.Style{Opacity: core.Alpha(opacity)})
		}, Children: []core.Node{core.Show{When: func() bool { return s.pendingSend() != "" && s.pendingSend() != "interrupt" }, Then: func(cx *core.Scope) core.Node {
			theme := *cx.Theme()
			theme.Light.TextMuted = theme.Light.Background
			theme.Dark.TextMuted = theme.Dark.Background
			cx.SetTheme(&theme)
			return ui.Spinner{TestID: "send-loading", Label: "正在发送", Variant: ui.SpinnerSpokes, Size: 18}
		}, Else: func(cx *core.Scope) core.Node {
			return core.Icon{SVG: lucide.ArrowUp(), Style: core.Style{Width: core.Pt(16), Height: core.Pt(16)}}
		}}}}}},
	}}}}
}
func (s *State) approvalCard(cx *core.Scope, a Object) core.Node {
	if len(objects(a["questions"])) > 0 {
		return s.questionCard(cx, a)
	}
	params := object(a["params"])
	options := objects(params["options"])
	buttons := []core.Node{}
	for _, option := range options {
		o := option
		style := "outline"
		if str(o, "kind") == "allow_once" {
			style = "primary"
		}
		buttons = append(buttons, button(cx, "approval-option-"+str(a, "id")+"-"+str(o, "id"), str(o, "name"), nil, style, func() { s.decision(a, str(o, "id")) }))
	}
	if flag(params, "input") {
		value := core.NewSignal(cx, str(params, "value"))
		buttons = append(buttons, core.Input{TestID: "approval-input-" + str(a, "id"), Label: "Agent 请求的内容", Value: value.Get, OnChange: value.Set, Multiline: true, Style: core.Style{MinHeight: core.Pt(72), MaxHeight: core.Pt(180)}, StyleFunc: func() core.Style { return textInputStyle(cx) }}, button(cx, "approval-submit-"+str(a, "id"), "提交", nil, "primary", func() { s.answerApproval(a, value.Get()) }))
	}
	if len(buttons) == 0 {
		buttons = []core.Node{button(cx, "approve-"+str(a, "id"), "允许", nil, "primary", func() { s.decision(a, "accept") }), button(cx, "deny-"+str(a, "id"), "拒绝", nil, "outline", func() { s.decision(a, "decline") })}
	}
	for i, node := range buttons {
		if v, ok := node.(core.View); ok {
			v.Disabled = func() bool { return !s.connected() || s.actionPending(s.approvalKey(a)) }
			buttons[i] = v
		}
	}
	buttons = append(buttons, core.Show{When: func() bool { return s.actionPending(s.approvalKey(a)) }, Then: func(cx *core.Scope) core.Node {
		return row(actionSpinner(cx, "approval-loading-"+str(a, "id"), "正在提交授权", false, 16), label(cx, "正在提交…", 12, true))
	}})
	title := str(params, "title")
	if title == "" {
		title = str(a, "method")
	}
	return s.messageInset(cx, card(cx, label(cx, "Agent 请求授权", 14, false), label(cx, title, 12, true), core.View{Style: core.Style{Gap: 8}, Children: buttons}))
}

func (s *State) workspaceSurface(cx *core.Scope) core.Style {
	return core.Style{Background: palette(cx).background}
}
