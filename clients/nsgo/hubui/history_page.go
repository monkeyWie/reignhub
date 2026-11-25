package hubui

import (
	"context"
	"errors"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/pkg/persistence"
)

// Additional leading pages may be needed to fill the viewport when
// many raw items fold into a single row. Keep them separate from the live
// transcript so stream reconciliation and persistence still own only the tail.
type historyPrefix struct {
	pages []persistence.HistoryPage
	end   persistence.PageCursor
	bytes int
}

// Only complete database pages carry continuation cursors. The initial live
// overlap is for display until the next indexed read.
const historyWindowPages = persistence.WindowSize / persistence.PageSize

func pageView(pages []persistence.HistoryPage) Object {
	if len(pages) == 0 {
		return nil
	}
	result := pages[0].Transcript
	for _, p := range pages[1:] {
		result = joinPages(result, p.Transcript, persistence.PageSize)
	}
	return result
}

// A database page can consist entirely of hidden provider records. Seek in the
// requested direction on the worker, leaving the mounted window intact until
// there is a page to display. A neighbouring answer may expose tools in its
// work disclosure, so check those turns before skipping the page.
func readDisplayedPage(ctx context.Context, dao historyStorage, route persistence.Session, query persistence.PageQuery, adjacent Object) (persistence.HistoryPage, error) {
	forward := query.After != nil
	var result persistence.HistoryPage
	remaining := persistence.PageSize
	bytes := 0
	for {
		if err := ctx.Err(); err != nil {
			return persistence.HistoryPage{}, err
		}
		query.Limit = remaining
		page, err := dao.ReadPage(ctx, route, query)
		if err != nil {
			return persistence.HistoryPage{}, err
		}
		turns := objects(page.Transcript["turns"])
		if len(turns) == 0 {
			if result.Transcript == nil {
				return page, nil
			}
			break
		}
		if forward && !query.After.Less(page.Start) || !forward && query.Before != nil && !page.End.Less(*query.Before) {
			return persistence.HistoryPage{}, errors.New("消息的分页位置未更新，请重试")
		}
		last := forward && page.After == nil || !forward && page.Before == nil
		remoteBoundary := !forward && last && str(page.Transcript, "nextCursor") != ""
		visible := len(messages(page.Transcript))
		if visible > 0 || pageAddsWork(turns, adjacent) || remoteBoundary {
			// Keep the physical boundary intact for provider overlap checks.
			n := 0
			if remoteBoundary {
				n = remaining
			} else {
				page.Transcript, n = displayRecords(page.Transcript)
				// Without an answer, commentary remains individually readable.
				n = max(n, visible)
			}
			size := persistence.PagePayloadBytes(page.Transcript)
			if result.Transcript != nil && bytes+size > persistence.PageBytes {
				return result, nil // The next request starts before this fragment.
			}
			remaining -= n
			bytes += size
			if result.Transcript == nil {
				result = page
			} else if forward {
				result.Transcript = joinPages(result.Transcript, page.Transcript, persistence.PageSize)
				result.End, result.After = page.End, page.After
			} else {
				result.Transcript = joinPages(page.Transcript, result.Transcript, persistence.PageSize)
				result.Start, result.Before = page.Start, page.Before
			}
			adjacent = page.Transcript
			if remaining <= persistence.PageSize/2 || bytes >= persistence.PageBytes {
				return result, nil
			}
		}
		if last {
			if result.Transcript == nil {
				result = page
				result.Transcript = copyObject(page.Transcript)
				delete(result.Transcript, "turns")
			}
			break
		}
		if forward {
			query.After = &page.End
		} else {
			query.Before = &page.Start
		}
	}
	if forward {
		result.After = nil
	} else {
		result.Before = nil
	}
	return result, nil
}

// A completed turn may contain hundreds of tool calls and commentary items
// behind one disclosure. Store each contiguous run in a database page as a
// single presentation slot. Keep only the fields used by the disclosure and
// charge its contents to the byte budget. Never write these groups to storage.
func displayRecords(t Object) (Object, int) {
	var turns []Object
	count := 0
	for _, turn := range objects(t["turns"]) {
		var items, process []Object
		flush := func() {
			if len(process) != 0 {
				items = append(items, Object{"id": process[0]["id"], "_historyProcess": process})
				process = nil
			}
		}
		terminal := terminalTurn(turn)
		for _, item := range objects(turn["items"]) {
			role := str(item, "role")
			switch {
			case str(item, "type") == "contextCompaction":
				flush()
				items = append(items, item)
			case visibleTool(item):
				tool := Object{"id": item["id"], "role": "tool", "type": item["type"], "status": item["status"], "activities": item["activities"]}
				if terminal {
					process = append(process, tool)
				} else {
					items = append(items, tool)
				}
			case (role == "user" || role == "assistant") && messageHasContent(item):
				if terminal && role == "assistant" && str(item, "phase") == "commentary" {
					process = append(process, item)
				} else {
					flush()
					items = append(items, item)
				}
			}
		}
		flush()
		if len(items) != 0 {
			projected := copyObject(turn)
			projected["items"] = items
			turns = append(turns, projected)
			count += len(items)
		}
	}
	projected := copyObject(t)
	projected["turns"] = turns
	return projected, count
}

func pageAddsWork(turns []Object, adjacent Object) bool {
	for _, turn := range turns {
		for _, item := range objects(turn["items"]) {
			if !visibleTool(item) {
				continue
			}
			for _, next := range objects(adjacent["turns"]) {
				if str(next, "id") == str(turn, "id") && (foldsWork(next) || hasHistoryProcess(next)) {
					return true
				}
			}
			break
		}
	}
	return false
}

func hasHistoryProcess(turn Object) bool {
	for _, item := range objects(turn["items"]) {
		if _, ok := item["_historyProcess"].([]Object); ok {
			return true
		}
	}
	return false
}

// A verification may return many turns before the first cached row already
// on screen. Keep those rows pageable without inserting them above the native
// viewport during the handoff. New messages and edits still render immediately.
type historyRowID struct{ turn, item string }

func firstHistoryRow(t Object) historyRowID {
	turns := objects(t["turns"])
	if len(turns) == 0 {
		return historyRowID{}
	}
	id := historyRowID{turn: str(turns[0], "id")}
	if items := objects(turns[0]["items"]); len(items) > 0 {
		id.item = str(items[0], "id")
	}
	return id
}
func (s *State) projectLivePage() Object {
	if s.snapshots == nil || s.snapshots.storage == nil {
		return s.transcript
	}
	page := persistence.VisibleTail(s.transcript, persistence.WindowSize)
	floor := s.openingRow
	if floor.turn == "" || firstHistoryRow(page) == floor {
		return page
	}
	turns := objects(page["turns"])
	for i, turn := range turns {
		if str(turn, "id") != floor.turn {
			continue
		}
		items := objects(turn["items"])
		j := 0
		if floor.item != "" {
			for j < len(items) && str(items[j], "id") != floor.item {
				j++
			}
			if j == len(items) {
				return page
			}
		}
		if i == 0 && j == 0 {
			return page
		}
		visible := append([]Object{}, turns[i:]...)
		visible[0] = copyObject(turn)
		visible[0]["items"] = append([]Object{}, items[j:]...)
		view := copyObject(page)
		view["turns"] = visible
		origin := persistence.Origin(page)
		if i == 0 {
			j += origin.Item
		}
		return persistence.WithOrigin(view, persistence.PageCursor{Turn: origin.Turn + i, Item: j}, true)
	}
	return page
}
func (s *State) historyView() Object {
	if s.reading != nil {
		return s.reading
	}
	if p := s.historyPrefix.pages; len(p) > 0 {
		return p[0].Transcript
	}
	return s.livePage
}
func (s *State) latestPage() {
	s.reading = nil
	s.readingPages = nil
	s.readingOverlap = nil
	s.rebuildMessages()
	s.changed()
}

// A raw cut in a tool-heavy live turn can exclude the answer that the
// viewport still shows. Attach the first pages to the complete live tail
// until its visible rows can survive detaching.
func (s *State) needsLiveOverlap(older persistence.HistoryPage) bool {
	a := s.timeline
	if a == nil || a.lastVisible == "" || len(s.historyPrefix.pages) >= historyWindowPages-1 {
		return false
	}
	live, count := displayRecords(s.livePage)
	if count <= persistence.PageSize {
		return false
	}
	pages := append([]persistence.HistoryPage{older}, s.historyPrefix.pages...)
	for _, row := range messages(joinPages(pageView(pages), live, persistence.PageSize)) {
		if row.ID == a.lastVisible {
			return false
		}
	}
	return true
}

// Keep an overlap at the page boundary so the List can preserve its reading anchor.
func joinPages(older, newer Object, limit int) Object {
	result := copyObject(older)
	turns := append([]Object{}, objects(older["turns"])...)
	left := limit
	for _, turn := range objects(newer["turns"]) {
		if left == 0 {
			break
		}
		items := objects(turn["items"])
		n := min(len(items), left)
		t := copyObject(turn)
		t["items"] = append([]Object{}, items[:n]...)
		left -= max(1, n)
		if len(turns) > 0 && str(turns[len(turns)-1], "id") == str(turn, "id") {
			a := append([]Object{}, objects(turns[len(turns)-1]["items"])...)
			t["items"] = append(a, items[:n]...)
			turns[len(turns)-1] = t
		} else {
			turns = append(turns, t)
		}
	}
	result["turns"] = turns
	return result
}
func (s *State) olderPage(owner context.Context, fill bool, current func() bool) bool {
	view := s.historyView()
	if s.snapshots == nil || s.snapshots.storage == nil {
		return false
	}
	dao := s.snapshots.storage
	before := persistence.Origin(view)
	route := persistence.Session{Env: s.target, Provider: str(s.selected, "providerId"), ID: str(s.selected, "id")}
	key := s.key()
	generation := s.sessionGeneration
	remote := str(view, "nextCursor")
	local := flag(view, "_localOlder")
	connected := s.connected()
	// A remotely fetched page may arrive before the first local commit.
	// Seed storage from the immutable source rows, never a folded display page.
	source := s.transcript
	adjacent := view
	if len(s.readingPages) > 0 {
		adjacent = s.readingPages[0].Transcript
	}
	readBefore := func(ctx context.Context) (any, error) {
		page, err := readDisplayedPage(ctx, dao, route, persistence.PageQuery{Before: &before}, adjacent)
		if err != nil {
			return nil, err
		}
		page.View()
		return page, nil
	}
	s.runScopedAction(owner, "history:"+key, false, func(ctx context.Context) (any, error) {
		if !local && remote == "" {
			return nil, nil
		}
		if local {
			return readBefore(ctx)
		}
		if !connected {
			return nil, errors.New("更早的消息尚未缓存，请连接设备后加载")
		}
		options := Object{"cursor": remote}
		if route.Env != control.LocalTarget {
			options["historyItems"] = persistence.PageSize
		}
		v, e := rpc(ctx, s.api, route.Env, route.Provider, route.ID, "transcript", options)
		if e != nil {
			return nil, e
		}
		page := object(v)
		if str(page, "nextCursor") == remote {
			return nil, errors.New("更早消息的分页位置未更新，请重试")
		}
		latest, e := dao.ReadPage(ctx, route, persistence.PageQuery{Limit: 1})
		if e != nil {
			return nil, e
		}
		if latest.Transcript == nil {
			initial := persistence.Diff("", nil, source)
			if e = dao.ApplyHistory(ctx, route, initial); e != nil {
				return nil, e
			}
			latest.Transcript = initial.Head
			latest.Revision = initial.Revision
		}
		if str(latest.Transcript, "nextCursor") != remote {
			// Another reader may already have committed this older page,
			// including a request superseded by a reversed gesture.
			return readBefore(ctx)
		}
		turns := objects(page["turns"])
		pos := persistence.PageCursor{Turn: before.Turn - len(turns)}
		first := objects(view["turns"])
		overlap := len(turns) > 0 && len(first) > 0 && str(turns[len(turns)-1], "id") == str(first[0], "id")
		if overlap {
			pos.Turn++
		}
		persistence.WithOrigin(page, pos, false)
		patch := persistence.Diff(latest.Revision, nil, page)
		patch.Revision = persistence.OlderPageRevision(latest.Revision)
		patch.Replace = false
		if overlap {
			at := len(turns) - 1
			last := patch.Turns[at].Turn
			skip := map[string]bool{}
			for _, row := range presentationItems(first[0]) {
				skip[str(row, "id")] = true
			}
			n := 0
			for _, row := range patch.Messages {
				if row.Turn == last && !skip[str(row.Value, "id")] {
					n++
				}
			}
			j := before.Item - n
			kept := patch.Messages[:0]
			for _, row := range patch.Messages {
				if row.Turn == last {
					if skip[str(row.Value, "id")] {
						continue
					}
					row.Position = j
					j++
				}
				kept = append(kept, row)
			}
			patch.Messages = kept
			patch.Turns = patch.Turns[:at]
		}
		// Store only header fields. The latest page also contains reconstructed
		// delivery and queue rows, which already live in their own tables.
		patch.Head = persistence.HistoryHead(latest.Transcript)
		patch.Overlay = nil
		patch.Head["nextCursor"], patch.Head["partialTurn"] = page["nextCursor"], page["partialTurn"]
		if e = dao.ApplyHistory(ctx, route, patch); e != nil {
			return nil, e
		}
		return readBefore(ctx)
	}, func(v any) {
		if persistence.Origin(s.historyView()) != before {
			return
		}
		if v == nil {
			return
		}
		stored := v.(persistence.HistoryPage)
		page := stored.Transcript
		t := copyObject(s.transcript)
		t["nextCursor"], t["partialTurn"] = page["nextCursor"], page["partialTurn"]
		t["_localOlder"] = flag(t, "_localOlder") || len(objects(page["turns"])) > 0
		s.transcript = t
		if len(objects(page["turns"])) == 0 {
			// An empty provider page can still advance or exhaust its cursor.
			head := copyObject(s.historyView())
			head["nextCursor"], head["_localOlder"] = page["nextCursor"], false
			switch {
			case s.reading != nil:
				s.reading = head
			case len(s.historyPrefix.pages) > 0:
				s.historyPrefix.pages[0].Transcript = head
			default:
				// The presentation window can start after the full snapshot.
				// A disk read can race its commit. Only update paging metadata.
				t["nextCursor"], t["_localOlder"] = page["nextCursor"], false
			}
		} else if s.reading == nil && (fill || s.needsLiveOverlap(stored)) {
			prefix := &s.historyPrefix
			if len(prefix.pages) == 0 {
				prefix.end = before
			}
			prefix.pages = append([]persistence.HistoryPage{stored}, prefix.pages...)
			prefix.bytes += persistence.PagePayloadBytes(page)
		} else {
			previous := s.readingPages
			if s.reading == nil {
				previous = s.historyPrefix.pages
				live, _ := displayRecords(s.livePage)
				s.readingOverlap = joinPages(Object{}, live, persistence.PageSize)
			}
			s.readingPages = append([]persistence.HistoryPage{stored}, previous[:min(len(previous), historyWindowPages-1)]...)
			s.reading = pageView(s.readingPages)
			if len(s.readingPages) < historyWindowPages && s.readingOverlap != nil {
				s.reading = joinPages(s.reading, s.readingOverlap, persistence.PageSize)
			} else {
				s.readingOverlap = nil
			}
			s.historyPrefix = historyPrefix{}
		}
		if !local {
			s.loadLandmarks()
		}
		s.rebuildMessages()
		s.changed()
		if a := s.timeline; fill && a != nil && s.reading == nil {
			// Run after the prepended cells have committed their measurements.
			s.cx.Post(func() {
				if s.timeline == a && a.following && !a.dragging && s.reading == nil {
					a.list.ScrollToEnd(false)
				}
			})
		}
	}, func() bool { return generation == s.sessionGeneration && key == s.key() && current() })
	return true
}

func (s *State) newerPage(owner context.Context, current func() bool) {
	if len(s.readingPages) == 0 {
		return
	}
	pages := s.readingPages
	after := pages[len(pages)-1].End
	livePage, adjacent := s.livePage, pages[len(pages)-1].Transcript
	liveStart := persistence.Origin(livePage)
	key, generation := s.key(), s.sessionGeneration
	route := persistence.Session{Env: s.target, Provider: str(s.selected, "providerId"), ID: str(s.selected, "id")}
	dao := s.snapshots.storage
	s.runScopedAction(owner, "history:"+key, false, func(ctx context.Context) (any, error) {
		if !after.Less(liveStart) {
			// A landmark can open directly inside the live tail. Fetch its
			// immediate predecessor to preserve the seam on reattachment.
			return readDisplayedPage(ctx, dao, route, persistence.PageQuery{Before: &liveStart}, livePage)
		}
		return readDisplayedPage(ctx, dao, route, persistence.PageQuery{After: &after, Before: &liveStart}, adjacent)
	}, func(v any) {
		page := v.(persistence.HistoryPage)
		s.readingOverlap = nil
		// A platform may end-align an append at the bottom of the old
		// window. Restore the reading row even when its index is unchanged.
		if s.timeline != nil {
			s.timeline.forwardPage = true
		}
		overlap := !after.Less(liveStart)
		if overlap {
			pages = nil
		}
		if len(objects(page.Transcript["turns"])) > 0 {
			page.View()
			if !overlap {
				page.Transcript["_localOlder"] = true
			}
			pages = append(append([]persistence.HistoryPage{}, pages[max(0, len(pages)-historyWindowPages+1):]...), page)
		}
		if page.After == nil && liveStart == persistence.Origin(s.livePage) {
			// Keep up to two preceding pages while the visible anchor moves
			// into the live rows. A later stream update owns the live content.
			prefix := historyPrefix{end: liveStart, pages: append([]persistence.HistoryPage{}, pages[max(0, len(pages)-historyWindowPages+1):]...)}
			for _, p := range prefix.pages {
				prefix.bytes += persistence.PagePayloadBytes(p.Transcript)
			}
			s.historyPrefix = prefix
			s.readingPages = nil
			s.reading = nil
		} else {
			s.readingPages = pages
			s.reading = pageView(pages)
		}
		s.rebuildMessages()
		s.changed()
	}, func() bool { return key == s.key() && generation == s.sessionGeneration && current() })
}

func (s *State) loadLandmarks() {
	dao, ok := s.snapshots.storage.(persistence.NavigationDAO)
	if !ok {
		return
	}
	key, generation := s.key(), s.sessionGeneration
	s.landmarksKey = key
	s.landmarksVersion++
	version := s.landmarksVersion
	route := persistence.Session{Env: s.target, Provider: str(s.selected, "providerId"), ID: str(s.selected, "id")}
	go func() {
		rows, err := dao.Navigation(s.ctx, route)
		s.cx.Post(func() {
			if s.ctx.Err() == nil && s.key() == key && s.sessionGeneration == generation && s.landmarksVersion == version {
				if err != nil {
					s.landmarksKey = ""
					return
				}
				s.landmarks = rows
				s.changed()
			}
		})
	}()
}
func (s *State) openLandmark(a persistence.Landmark, navigate func(int)) {
	key, generation := s.key(), s.sessionGeneration
	route := persistence.Session{Env: s.target, Provider: str(s.selected, "providerId"), ID: str(s.selected, "id")}
	s.runAction("history:"+key, func(ctx context.Context) (any, error) {
		// Leave room for the response in the same turn. Retry the tighter bound
		// when long intervening messages consume the page's byte budget.
		for _, after := range []int{16, 1} {
			before := a.Position
			before.Item += after
			page, e := s.snapshots.storage.ReadPage(ctx, route, persistence.PageQuery{Before: &before})
			if e != nil {
				return nil, e
			}
			v := page.View()
			for _, r := range messages(v) {
				if r.ID == a.ID {
					return page, nil
				}
			}
		}
		return nil, errors.New("消息位置已变化，请刷新会话")
	}, func(v any) {
		if s.key() != key || generation != s.sessionGeneration {
			return
		}
		page := v.(persistence.HistoryPage)
		s.reading = page.Transcript
		s.readingPages = []persistence.HistoryPage{page}
		s.readingOverlap = nil
		s.historyPrefix = historyPrefix{}
		s.rebuildMessages()
		s.changed()
		s.cx.Post(func() {
			if s.key() != key || generation != s.sessionGeneration {
				return
			}
			for i, row := range s.messageRows {
				if row.ID == a.ID {
					navigate(i)
					break
				}
			}
		})
	}, func() bool { return s.key() == key && generation == s.sessionGeneration })
}
