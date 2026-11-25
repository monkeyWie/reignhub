package hubui

import "github.com/nsgo-dev/nsgo/core"

// The end-aligned List owns anchoring during layout and content measurement. Track
// user intent only: issuing another ScrollTo from its callbacks competes with
// its pending relative correction and can visibly oscillate during resizing.
type messageScrollAnchor struct {
	list            core.Ref
	atBottom        *core.Signal[bool]
	pendingRow      *core.ListPosition
	lastVisible     string
	forwardPage     bool
	size            core.Size
	content         core.Size
	reportedSize    core.Size
	offset          core.Points
	rebasing        bool
	following       bool
	restoring       bool
	dragging        bool
	momentum        bool
	boundsCorrected bool
}

func (a *messageScrollAnchor) jumpToLatest() {
	if a == nil {
		return
	}
	a.pendingRow = nil
	a.following, a.restoring = true, true
	a.rebasing = false
	a.boundsCorrected = false
	a.atBottom.Set(true)
	a.list.ScrollToEnd(false)
}

// Inserting a page before the viewport can move its offset past the old end.
// After the list reconciles its keys, restore the same row and inset. A new
// gesture, navigation or submission supersedes the pending restoration.
func (a *messageScrollAnchor) preserveReading(cx *core.Scope, previous []Message, s *State) {
	forward := a.forwardPage
	a.forwardPage = false
	if a.following || a.pendingRow != nil || len(previous) == 0 {
		return
	}
	p, ok := a.list.ListPosition()
	if !ok || p.Index >= len(previous) {
		return
	}
	old := previous[p.Index]
	id := old.ID
	if !forward && p.Index < len(s.messageRows) && s.messageRows[p.Index].ID == id {
		return
	}
	a.pendingRow = &p
	cx.Post(func() {
		if a.pendingRow != &p || s.timeline != a {
			return
		}
		a.pendingRow = nil
		for i, row := range s.messageRows {
			if row.ID == id || old.Role == "work" && row.Role == "work" && row.Turn == old.Turn {
				a.rebasing = true
				a.list.ScrollToIndexAt(i, 0, -p.Offset, false)
				return
			}
		}
	})
}

// A sliding window can shrink during the final wheel event. The renderer
// then has a valid list position but the scroll surface can still be beyond
// its new end. Reapply the measured row after the gesture has settled.
func (a *messageScrollAnchor) settle(e core.ScrollEvent) {
	if !a.needsBoundsCorrection(e) {
		return
	}
	if a.following {
		a.boundsCorrected = true
		a.list.ScrollToEnd(false)
		return
	}
	if p, ok := a.list.ListPosition(); ok {
		// ScrollToIndexAt can itself produce more offset reports while rows
		// are being measured. One correction suffices for this reading window.
		a.boundsCorrected = true
		a.rebasing = true
		a.list.ScrollToIndexAt(p.Index, 0, -p.Offset, false)
	}
}

func (a *messageScrollAnchor) needsBoundsCorrection(e core.ScrollEvent) bool {
	// While following, the native list normally clamps its own offset.
	// Intervene only when the entire viewport has ended up beyond the rows.
	disjoint := e.Offset.Y >= e.ContentSize.Height || e.Offset.Y+e.Size.Height <= 0
	return !a.boundsCorrected && (!a.following || disjoint) && !a.dragging && !a.momentum &&
		a.pendingRow == nil && outsideScrollRange(e)
}

func outsideScrollRange(e core.ScrollEvent) bool {
	return !e.Stale && e.Size.Height > 0 && (e.Offset.Y < -1 || e.Offset.Y > max(0, e.ContentSize.Height-e.Size.Height)+1)
}

func (a *messageScrollAnchor) layout(size core.Size) {
	if size.Height <= 0 {
		return
	}
	previous := a.size
	a.size = size
	if size == previous {
		return
	}
	a.boundsCorrected = false
	if a.following && !a.dragging {
		a.restoring = true
	}
}

// scroll returns the direction of input-driven travel, for history paging.
func (a *messageScrollAnchor) scroll(e core.ScrollEvent) int {
	// Native page transitions can publish offsets before a usable viewport.
	// They cannot establish whether the user is reading history.
	if e.Size.Height <= 0 || e.Stale || a.pendingRow != nil {
		return 0
	}
	// Some platforms deliver the resized scroll event before OnLayout.
	a.layout(e.Size)
	if outsideScrollRange(e) {
		return 0
	}
	stable := a.reportedSize == e.Size && a.content == e.ContentSize
	a.reportedSize = e.Size
	// Initial history, streaming prefixes and image/Markdown measurements can
	// report an old offset against new content before native anchoring lands.
	// Preserve intent until the List applies its correction or the user
	// scrolls in the new geometry.
	if a.content != e.ContentSize {
		a.content = e.ContentSize
		if a.following && !a.dragging {
			a.restoring = true
		}
	}
	end := max(0, e.ContentSize.Height-e.Size.Height)
	previous := a.offset
	a.offset = min(max(0, e.Offset.Y), end)
	distance := end - a.offset
	// The first applied report from a row jump sets a new reference point.
	// It must not reverse an in-flight history read.
	rebasing := a.rebasing
	a.rebasing = false
	moved := !rebasing && (a.dragging || stable) && a.offset != previous
	if a.restoring {
		if distance <= 1 {
			a.restoring = false
		} else if moved && a.offset < previous {
			// A mouse wheel can move the viewport without a drag-start
			// event. An upward step at the same geometry wins over alignment.
			a.following, a.restoring = false, false
		}
	} else if !rebasing {
		// Match ListAlignEnd's one-point layout rounding tolerance.
		a.following = distance <= 1
	}
	if moved {
		return choose(a.offset > previous, 1, -1)
	}
	return 0
}
