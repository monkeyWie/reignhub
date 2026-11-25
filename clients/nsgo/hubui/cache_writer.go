package hubui

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/monkeyWie/reignhub/pkg/persistence"
)

type historyStorage interface {
	ApplyHistory(context.Context, persistence.Session, persistence.HistoryPatch) error
	ReadPage(context.Context, persistence.Session, persistence.PageQuery) (persistence.HistoryPage, error)
}
type snapshotWrite struct {
	target, provider, session string
	transcript                Object
	removed                   map[string]bool
}

// A single writer preserves ordering. Coalesce repeated streaming updates so
// IndexedDB/SQLite writes never block painting or grow an unbounded queue.
type snapshotWriter struct {
	mu      sync.Mutex
	pending map[persistence.Session]snapshotWrite
	wake    chan struct{}
	storage historyStorage
}

func newSnapshotWriter(ctx context.Context, api API, notify ...func(snapshotWrite, Object, error)) *snapshotWriter {
	w := &snapshotWriter{pending: map[persistence.Session]snapshotWrite{}, wake: make(chan struct{}, 1)}
	w.storage, _ = api.(historyStorage)
	go func() {
		var previous Object
		var previousKey persistence.Session
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.wake:
			}
			w.mu.Lock()
			pending := w.pending
			w.pending = map[persistence.Session]snapshotWrite{}
			w.mu.Unlock()
			for _, item := range pending {
				if ctx.Err() != nil {
					return
				}
				if w.storage != nil {
					key := persistence.Session{Env: item.target, Provider: item.provider, ID: item.session}
					var err error
					if previousKey != key {
						var page persistence.HistoryPage
						page, err = w.storage.ReadPage(ctx, key, persistence.PageQuery{Limit: persistence.WindowSize})
						previous = page.View()
					}
					if err == nil {
						previous, err = w.save(ctx, key, previous, item)
						previousKey = key
					}
					if err != nil {
						previousKey = persistence.Session{}
					}
					for _, f := range notify {
						f(item, previous, err)
					}

				} else {
					_, _ = api.Call(ctx, "client.snapshot", Object{"provider": item.provider, "session": item.session, "transcript": item.transcript}, item.target, nil)
				}
			}
		}
	}()
	return w
}

// Retry only a rejected local compare-and-swap. The Agent command has its
// own acknowledgement and is never involved in recovering a storage write.
func (w *snapshotWriter) save(ctx context.Context, key persistence.Session, previous Object, item snapshotWrite) (Object, error) {
	revision := str(previous, "_historyRevision")
	value := snapshotWindow(previous, item.transcript)
	delta, patch := snapshotPatch(previous, value, item.removed)
	delete(value, "_baseline")
	deliveryOnly := flag(item.transcript, "_deliveryOnly")
	current := previous
	refresh := flag(previous, "_refreshHistory") && item.transcript["resume"] == nil && !flag(item.transcript, "_baseline")
	for attempt := 1; ; attempt++ {
		oldPos, newPos := object(current["resume"]), object(value["resume"])
		behind := str(oldPos, "epoch") != "" && str(oldPos, "epoch") == str(newPos, "epoch") && number(oldPos, "cursor") > number(newPos, "cursor")
		if behind || refresh || deliveryOnly {
			// The committed history may already include events this sender has
			// not seen. Change only delivery rows it actually edited.
			head := persistence.HistoryHead(current)
			patch = persistence.HistoryPatch{Base: str(current, "_historyRevision"), Revision: persistence.Revision(), Head: head}
			patch.Overlay = delta.DeliveryChanges(previous, current)
			value = copyObject(current)
			value["localMessages"] = item.transcript["localMessages"]
			refresh = refresh || behind
		}
		err := w.storage.ApplyHistory(ctx, key, patch)
		if err == nil {
			return committedTail(value, patch, refresh), nil
		}
		if !errors.Is(err, persistence.ErrConflict) || attempt == 3 {
			return nil, err
		}
		page, err := w.storage.ReadPage(ctx, key, persistence.PageQuery{Limit: persistence.WindowSize})
		if err != nil {
			return nil, err
		}
		current = page.View()
		if current == nil {
			return nil, persistence.ErrConflict
		}
		if persistence.OnlyOlderPagesChanged(revision, page.Revision) && !refresh {
			// Reuse the original row delta. Diffing the newly read page against
			// the old UI window would delete the prefix that just arrived.
			value["nextCursor"], value["partialTurn"] = current["nextCursor"], current["partialTurn"]
			value["_localOlder"] = flag(current, "_localOlder") || persistence.Origin(current).Less(persistence.Origin(value))
			patch = delta
			patch.Head["nextCursor"], patch.Head["partialTurn"] = current["nextCursor"], current["partialTurn"]
			patch.Base, patch.Replace = page.Revision, false
		} else {
			refresh = !deliveryOnly
		}
	}
}

func snapshotWindow(previous, input Object) Object {
	value := copyObject(input)
	deliveryOnly := flag(input, "_deliveryOnly")
	if deliveryOnly {
		value = copyObject(previous)
		value["localMessages"] = input["localMessages"]
	}
	value = persistence.Align(previous, value)
	// A pending UI snapshot may still start at the previous paging boundary.
	// Retain the committed prefix only when both windows overlap in the same turn.
	before, after := persistence.Origin(previous), persistence.Origin(value)
	offset := after.Turn - before.Turn
	priorTurns, nextTurns := objects(previous["turns"]), objects(value["turns"])
	if !flag(value, "_baseline") && str(previous, "streamEpoch") == str(value, "streamEpoch") &&
		!after.Less(before) && (flag(previous, "_localOlder") || before.Less(after)) &&
		offset < len(priorTurns) && len(nextTurns) > 0 && str(priorTurns[offset], "id") == str(nextTurns[0], "id") {
		// The turn ID alone cannot place a suffix inside a long turn.
		rows := objects(nextTurns[0]["items"])
		overlap := len(rows) == 0 && str(value, "partialTurn") == ""
		if len(rows) > 0 {
			overlap = historyItemIndex(objects(priorTurns[offset]["items"]), str(rows[0], "id")) >= 0
		}
		if overlap {
			value["nextCursor"], value["partialTurn"], value["_localOlder"] = previous["nextCursor"], previous["partialTurn"], true
		}
	}
	return value
}

func snapshotPatch(previous, value Object, removed map[string]bool) (persistence.HistoryPatch, persistence.HistoryPatch) {
	revision := str(previous, "_historyRevision")
	delta := persistence.Diff(revision, previous, value)
	// An absent row can belong to another tab. Remove a whole delivery
	// record only after this UI has explicitly reconciled its message ID.
	unseen := make(map[string]bool)
	for _, row := range delta.Overlay {
		if row.Table == persistence.Outgoing && row.Value == nil && !removed[strings.TrimPrefix(row.ID, "id:")] {
			unseen[row.ID] = true
		}
	}
	delta.Overlay = slices.DeleteFunc(delta.Overlay, func(row persistence.OverlayRow) bool {
		return row.Table != persistence.Queued && unseen[row.ID]
	})
	patch := delta
	if flag(value, "_baseline") && flag(previous, "_localOlder") && !flag(value, "_localOlder") {
		patch = persistence.Diff(revision, nil, value)
		patch.PreserveOutgoing = true
		patch.Overlay = slices.DeleteFunc(patch.Overlay, func(row persistence.OverlayRow) bool { return row.Table != persistence.Queued })
		patch.Overlay = append(patch.Overlay, delta.DeliveryChanges(previous, previous)...)
	}
	return delta, patch
}

func committedTail(value Object, patch persistence.HistoryPatch, refresh bool) Object {
	tail := copyObject(persistence.VisibleTail(value, persistence.WindowSize))
	tail["_historyRevision"] = patch.Revision
	tail["_refreshNavigation"] = len(patch.Turns) > 0
	for _, row := range patch.Messages {
		if row.Value == nil || row.Value["role"] == "user" {
			tail["_refreshNavigation"] = true
			break
		}
	}
	if refresh {
		tail["_refreshHistory"] = true
	} else {
		delete(tail, "_refreshHistory")
	}
	return tail
}

func (s *State) persistSnapshot() {
	if s.snapshots == nil || s.selected == nil || s.transcript == nil {
		return
	}
	s.writeSnapshot(s.target, str(s.selected, "providerId"), str(s.selected, "id"), s.snapshotProjection())
}

func (s *State) writeSnapshot(target, provider, session string, transcript Object) {
	if s.snapshots == nil {
		return
	}
	transcript = copyObject(transcript)
	if pending, ok := transcript["localMessages"].([]*outgoingMessage); ok {
		transcript["localMessages"] = snapshotOutgoing(pending)
	}
	w := s.snapshots
	w.mu.Lock()
	key := persistence.Session{Env: target, Provider: provider, ID: session}
	if transcript["turns"] == nil && transcript["_deliveryOnly"] == true {
		if prior, ok := w.pending[key]; ok {
			merged := copyObject(prior.transcript)
			merged["localMessages"] = transcript["localMessages"]
			transcript = merged
		}
	}
	w.pending[key] = snapshotWrite{target, provider, session, transcript, maps.Clone(s.outgoingRemoved[target+"|"+provider+"|"+session])}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (s *State) scheduleSnapshot() {
	if s.snapshotScheduled {
		return
	}
	s.snapshotScheduled = true
	time.AfterFunc(500*time.Millisecond, func() {
		s.cx.Post(func() {
			s.snapshotScheduled = false
			if s.ctx.Err() == nil {
				s.persistSnapshot()
			}
		})
	})
}

// Shrink the live projection only after its rows and checkpoint commit. A
// newer stream event owns a different turn slice and gets its own acknowledgement.
func (s *State) historySaved(w snapshotWrite, tail Object, err error) {
	s.cx.Post(func() {
		if s.ctx.Err() != nil {
			return
		}
		if err == nil && len(w.removed) > 0 {
			key := w.target + "|" + w.provider + "|" + w.session
			for id := range w.removed {
				delete(s.outgoingRemoved[key], id)
			}
			if len(s.outgoingRemoved[key]) == 0 {
				delete(s.outgoingRemoved, key)
			}
		}
		if s.target != w.target || str(s.selected, "providerId") != w.provider || str(s.selected, "id") != w.session {
			return
		}
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				s.showNotice("此会话的本地记录未能保存")
				if errors.Is(err, persistence.ErrConflict) && s.stream != nil && s.stream.current() && !s.stream.snapshotInFlight {
					s.stream.refreshSnapshot()
				}
			}
			return
		}
		if flag(tail, "_refreshHistory") {
			if s.stream != nil && s.stream.current() && !s.stream.snapshotInFlight {
				s.stream.refreshSnapshot()
			}
			return
		}
		a, b := reflect.ValueOf(s.transcript["turns"]), reflect.ValueOf(w.transcript["turns"])
		if flag(w.transcript, "_deliveryOnly") {
			return
		}
		if !a.IsValid() || !b.IsValid() || a.Kind() != reflect.Slice || b.Kind() != reflect.Slice || a.Pointer() != b.Pointer() || a.Len() != b.Len() {
			return
		}
		t := copyObject(s.transcript)
		for _, key := range []string{"turns", "_historyRevision", "_turnStart", "_itemStart", "_localOlder"} {
			t[key] = tail[key]
		}
		if str(t, "nextCursor") == str(w.transcript, "nextCursor") {
			t["nextCursor"], t["partialTurn"] = tail["nextCursor"], tail["partialTurn"]
		}
		delete(t, "_baseline")
		s.transcript = t
		if flag(t, "_localOlder") && (s.landmarksKey != s.key() || flag(tail, "_refreshNavigation")) {
			s.loadLandmarks()
		}
		s.rebuildMessages()
		s.changed()
	})
}
