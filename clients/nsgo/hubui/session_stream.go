package hubui

import (
	"context"
	"encoding/json"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/readretry"
	"github.com/monkeyWie/reignhub/pkg/persistence"
)

// All presentation state belongs to the UI loop. Watch, snapshots and approval
// reads use separate requests so a slow history transfer cannot hold up text.
type sessionStream struct {
	s                                     *State
	ctx                                   context.Context
	target, provider, session, key, epoch string
	cursor, resetCursor                   int64
	baseline, snapshotInFlight, invalid   bool
	snapshotAgain                         bool
	journal                               []Object
	journalBytes                          int
	syncVersion                           uint64
	approvals                             chan struct{}
	historyRetry                          readretry.Backoff
	historyRetryVersion                   uint64
	nextSnapshot                          time.Time
	verified                              bool
	historyError                          error
}

func (w *sessionStream) current() bool {
	return w.ctx.Err() == nil && w.s.ctx.Err() == nil && w.s.stream == w && w.s.key() == w.key
}
func (w *sessionStream) syncing(value bool) {
	if !w.current() {
		return
	}
	s := w.s
	if s.transcriptSyncing == value {
		return
	}
	w.syncVersion++
	version := w.syncVersion
	s.transcriptSyncing = value
	s.syncVisible = false
	if value {
		time.AfterFunc(300*time.Millisecond, func() {
			s.cx.Post(func() {
				if w.current() && s.transcriptSyncing && w.syncVersion == version {
					s.syncVisible = true
					s.changed()
				}
			})
		})
	}
	s.changed()
}
func (w *sessionStream) checkpoint() {
	if w.baseline && !w.snapshotInFlight {
		w.s.streamKey, w.s.streamEpoch, w.s.streamCursor = w.key, w.epoch, w.cursor
	}
}
func (w *sessionStream) refreshSnapshot() {
	if w.snapshotInFlight {
		w.snapshotAgain = true
		return
	}
	w.startSnapshot()
}

func (w *sessionStream) startSnapshot() {
	if !w.current() || w.snapshotInFlight || time.Now().Before(w.nextSnapshot) {
		return
	}
	w.historyRetryVersion++
	w.snapshotInFlight = true
	w.historyError = nil
	w.snapshotAgain = false
	w.invalid = false
	w.journal = nil
	w.journalBytes = 0
	// Once history has caught up, refreshing a running Agent's live tail is
	// normal streaming, not another history download. Only a missing/reset
	// baseline should display catch-up dots or suppress the typing effect.
	w.syncing(!w.baseline)
	started, epoch := time.Now(), w.epoch
	// When re-entering a cached conversation, older partial turns can move
	// its measured tail or temporarily shorten an already complete answer.
	// Keep that page readable until this initial verification completes.
	verifyingPage := !w.verified && len(objects(w.s.transcript["turns"])) > 0 && len(w.s.messageRows) > 0
	var coldStore historyStorage
	if w.target != control.LocalTarget && w.s.transcript == nil && w.s.snapshots != nil {
		coldStore = w.s.snapshots.storage
	}
	go func() {
		previewCtx, stopPreviews := context.WithCancel(w.ctx)
		defer stopPreviews()
		observe := observeTranscriptPreviews(previewCtx, w.s.cx.Post, func(partial Object) {
			w.applySnapshotPreview(epoch, partial, verifyingPage, coldStore != nil)
		})
		// Transport idle budgets reset on every received frame. A total deadline
		// would discard a healthy history transfer on a slow connection.
		readCtx, stopRead := context.WithCancel(w.ctx)
		defer stopRead()
		ctx := provider.WithTranscriptObserver(readCtx, observe)
		options := Object{"fresh": true, "progress": true}
		if w.target != control.LocalTarget {
			options["recentTurns"] = 4
			options["historyItems"] = persistence.PageSize
		}
		v, err := rpc(ctx, w.s.api, w.target, w.provider, w.session, "transcript", options)
		stopPreviews()
		t := object(v)
		var cached Object
		if err == nil && coldStore != nil && str(t, "partialTurn") != "" {
			// On a slow store the network may win the race with session entry.
			// Verify the durable boundary before a suffix can replace that cache.
			var page persistence.HistoryPage
			page, err = coldStore.ReadPage(w.ctx, persistence.Session{Env: w.target, Provider: w.provider, ID: w.session}, openingPageQuery())
			cached = page.View()
		}
		w.s.cx.Post(func() {
			if !w.current() {
				return
			}
			if cached != nil && w.s.transcript == nil {
				w.s.applyCachedTranscript(cached)
			}
			prior := w.s.transcript
			if err == nil && w.snapshotMatches(epoch, t) && w.target != control.LocalTarget && needsEarlierSnapshotPage(prior, t, !w.verified) {
				go func() {
					t, err := w.bridgeSnapshot(prior, t)
					w.s.cx.Post(func() {
						if w.current() {
							w.finishSnapshot(started, epoch, t, err)
						}
					})
				}()
				return
			}
			w.finishSnapshot(started, epoch, t, err)
		})
	}()
}

func (w *sessionStream) applySnapshotPreview(epoch string, partial Object, verifyingPage, coldStore bool) {
	if !w.current() || !w.snapshotMatches(epoch, partial) {
		return
	}
	if verifyingPage || (!w.verified && w.s.openingRow.turn != "" && len(w.s.messageRows) > 0) {
		return
	}
	if str(partial, "partialTurn") != "" && (coldStore || len(objects(w.s.transcript["turns"])) != 0) {
		return
	}
	if len(objects(partial["turns"])) == 0 {
		return
	}
	preview := previewTranscript(w.s.transcript, partial)
	if w.baseline {
		preview, _ = applyEvents(preview, w.replayAfter(partial))
	}
	w.s.setTranscript(preview)
}

func (w *sessionStream) finishSnapshot(started time.Time, epoch string, t Object, err error) {
	w.snapshotInFlight = false
	w.historyError = err
	w.s.setConnectionIssue(w.target, "transcript", err)
	if err != nil {
		w.journal = nil
		w.syncing(false)
		delay := w.historyRetry.Next(err)
		w.nextSnapshot = time.Now().Add(delay)
		w.historyRetryVersion++
		version := w.historyRetryVersion
		time.AfterFunc(delay, func() {
			w.s.cx.Post(func() {
				if w.current() && w.historyRetryVersion == version {
					w.startSnapshot()
				}
			})
		})
		return
	}
	w.historyRetry.Reset()
	w.nextSnapshot = time.Time{}
	if !w.snapshotMatches(epoch, t) {
		w.startSnapshot()
		return
	}
	w.verified = true
	// History is readable even if the live subscription has not joined yet.
	// Its event boundary can establish the initial baseline independently.
	if w.epoch == "" {
		w.epoch, w.cursor = str(t, "streamEpoch"), number(t, "streamCursor")
	}
	// A completed history response may predate live text already on screen.
	// Replay everything received since the request began; UTF-16 offsets make
	// overlapping snapshot/delta content idempotent.
	var refresh bool
	t, refresh = applyEvents(t, w.replayAfter(t))
	w.snapshotAgain = w.snapshotAgain || refresh
	w.journal = nil
	w.baseline = true
	if str(t, "nextCursor") == "" {
		t["_baseline"] = true
	}
	w.s.setTranscript(t)
	w.s.reconcileOutgoing(started)
	w.s.rebuildMessages()
	w.s.trackNativeTask(w.target, w.provider, w.session, t)
	if !w.snapshotAgain {
		w.checkpoint()
	}
	w.syncing(false)
	if w.snapshotAgain {
		w.startSnapshot()
	}
}

func (w *sessionStream) snapshotMatches(epoch string, t Object) bool {
	return !w.invalid && (epoch == "" || epoch == w.epoch) &&
		(str(t, "streamEpoch") == "" || w.epoch == "" ||
			str(t, "streamEpoch") == w.epoch && number(t, "streamCursor") >= w.resetCursor)
}

func (w *sessionStream) replayAfter(t Object) []Object {
	if str(t, "streamEpoch") == "" {
		return w.journal
	}
	events := []Object{}
	for _, event := range w.journal {
		if number(event, "id") > number(t, "streamCursor") {
			events = append(events, event)
		}
	}
	return events
}

// A fragment is an addition, never an authoritative deletion. Keep cached
// messages and the live tail until the complete snapshot can reconcile them.
func previewTranscript(old, partial Object) Object {
	previous := map[string]Object{}
	for _, turn := range objects(old["turns"]) {
		previous[str(turn, "id")] = turn
	}
	next := copyObject(partial)
	if _, present := next["queue"]; !present && old["queue"] != nil {
		next["queue"] = old["queue"]
	}
	turns := []Object{}
	for _, turn := range objects(partial["turns"]) {
		value := copyObject(turn)
		items := []Object{}
		seen := map[string]bool{}
		for _, item := range objects(turn["items"]) {
			seen[str(item, "id")] = true
			items = append(items, item)
		}
		for _, item := range objects(previous[str(turn, "id")]["items"]) {
			if !seen[str(item, "id")] {
				items = append(items, item)
			}
		}
		value["items"] = items
		turns = append(turns, value)
	}
	next["turns"] = turns
	next = mergeLatest(old, next)
	turns = append([]Object{}, objects(next["turns"])...)
	seen := map[string]bool{}
	for _, turn := range turns {
		seen[str(turn, "id")] = true
	}
	for _, turn := range objects(old["turns"]) {
		if !seen[str(turn, "id")] {
			seen[str(turn, "id")] = true
			turns = append(turns, turn)
		}
	}
	next["turns"] = turns
	return next
}
func (w *sessionStream) batch(b Object) {
	if !w.current() {
		return
	}
	events := objects(b["events"])
	if flag(b, "reset") {
		// The first subscription establishes the boundary of the independent
		// history read. A later reset invalidates an already established one.
		if w.snapshotInFlight && w.epoch != "" {
			w.invalid = true
		}
		w.baseline = false
		w.resetCursor = number(b, "cursor")
		w.s.streamEpoch, w.s.streamCursor = "", 0
	}
	refreshSettings := w.epoch == "" || flag(b, "reset")
	w.epoch, w.cursor = str(b, "epoch"), number(b, "cursor")
	for _, e := range events {
		if str(e, "kind") == "composerSettings" {
			refreshSettings = true
			break
		}
	}
	if refreshSettings {
		w.s.composerControlsRevision++
		w.s.changed()
	}
	if !w.baseline && !w.snapshotInFlight {
		w.startSnapshot()
	}
	if w.snapshotInFlight && !w.invalid {
		for _, event := range events {
			raw, _ := json.Marshal(event)
			w.journalBytes += len(raw)
			if len(w.journal) >= 1024 || w.journalBytes > 2<<20 {
				w.invalid = true
				w.journal = nil
				break
			}
			w.journal = append(w.journal, event)
		}
	}
	// A reset has no ordering baseline yet. Keep later watch events in the
	// journal until the ordered history prefix is complete; otherwise small
	// user items leap over missing assistant replies and appear as a false chat.
	if !w.baseline {
		return
	}
	if len(events) > 0 {
		s := w.s
		if w.baseline && !s.transcriptSyncing {
			if s.animateText == nil {
				s.animateText = map[string]bool{}
			}
			for _, event := range events {
				if str(event, "kind") == "text" {
					s.animateText[str(event, "turnId")+"|"+str(event, "itemId")] = true
				}
			}
		}
		next, refresh := applyEvents(s.transcript, events)
		s.transcript = next
		s.updateSelectedSummary()
		s.trackNativeTask(w.target, w.provider, w.session, next)
		s.reconcileOutgoing(time.Time{})
		s.rebuildMessages()
		s.scheduleSnapshot()
		s.changed()
		if refresh {
			w.startSnapshot()
		}
	}
	w.checkpoint()
	for _, event := range events {
		switch str(event, "kind") {
		case "approval", "turn", "snapshot":
			select {
			case w.approvals <- struct{}{}:
			default:
			}
			return
		}
	}
}
func (w *sessionStream) run(epoch string, cursor int64) {
	go w.pollApprovals()
	probe := true
	var retry readretry.Backoff
	for w.ctx.Err() == nil {
		v, err := rpc(w.ctx, w.s.api, w.target, w.provider, w.session, "watch", Object{"epoch": epoch, "after": cursor, "probe": probe})
		if w.ctx.Err() != nil {
			return
		}
		applied := make(chan struct{}, 1)
		w.s.cx.Post(func() {
			if w.current() {
				w.s.setConnectionIssue(w.target, "watch", err)
				if err == nil {
					w.batch(object(v))
				} else if !w.snapshotInFlight {
					w.syncing(false)
					// A broken subscription must not hide readable persisted
					// history. Recovery follows the watch's existing error backoff;
					// healthy heartbeats never trigger periodic history downloads.
					w.startSnapshot()
				}
				w.s.changed()
			}
			applied <- struct{}{}
		})
		select {
		case <-w.ctx.Done():
			return
		case <-applied:
		}
		if err != nil {
			if readretry.Wait(w.ctx, retry.Next(err)) != nil {
				return
			}
			continue
		}
		retry.Reset()
		b := object(v)
		epoch, cursor = str(b, "epoch"), number(b, "cursor")
		probe = false
		// Prevent a broken/unsupported adapter from spinning on empty replies.
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(16 * time.Millisecond):
		}
	}
}
func (w *sessionStream) pollApprovals() {
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for w.ctx.Err() == nil {
		v, err := rpc(w.ctx, w.s.api, w.target, w.provider, w.session, "approvals", nil)
		w.s.cx.Post(func() {
			if w.current() && err == nil {
				w.s.approvals = nil
				for _, a := range objects(v) {
					if str(a, "sessionId") == w.session {
						w.s.approvals = append(w.s.approvals, a)
					}
				}
				w.s.pruneQuestionDrafts()
				w.s.changed()
			}
		})
		select {
		case <-w.ctx.Done():
			return
		case <-timer.C:
		case <-w.approvals:
		}
		// Coalesce event bursts to at most one approval read per second.
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
