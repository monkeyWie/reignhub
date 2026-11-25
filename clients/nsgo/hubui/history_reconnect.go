package hubui

import (
	"errors"
	"slices"

	"github.com/monkeyWie/reignhub/pkg/persistence"
)

// Only a shared item can join a partial turn. A persisted suffix from an
// earlier client may already have lost its prefix and inherited its cursor.
func needsEarlierSnapshotPage(prior, page Object, verifying bool) bool {
	turnID := str(page, "partialTurn")
	cached := objects(prior["turns"])
	if turnID == "" || len(cached) == 0 {
		return false
	}
	first := firstHistoryRow(page)
	if first.turn != turnID || first.item == "" || str(prior, "streamEpoch") != str(page, "streamEpoch") {
		return true
	}
	for _, turn := range cached {
		if str(turn, "id") == turnID {
			items := objects(turn["items"])
			if verifying && (str(prior, "partialTurn") == turnID || !flag(prior, "_localOlder")) &&
				!slices.ContainsFunc(items, func(item Object) bool { return str(item, "role") == "user" }) {
				return true
			}
			return historyItemIndex(items, first.item) < 0
		}
	}
	return true
}

// Walk exclusive provider cursors only until the gap is covered. Preserve the
// latest page's event boundary and queue. Watch events after that boundary
// keep accumulating in the same snapshot journal during these reads.
func (w *sessionStream) bridgeSnapshot(prior, latest Object) (Object, error) {
	pages := []Object{latest}
	page := latest
	seen := map[string]bool{}
	for {
		cursor := str(page, "nextCursor")
		if cursor == "" || seen[cursor] {
			return nil, errors.New("历史消息的分页位置未更新，请重试")
		}
		seen[cursor] = true
		v, err := rpc(w.ctx, w.s.api, w.target, w.provider, w.session, "transcript", Object{"cursor": cursor, "historyItems": persistence.PageSize, "fresh": true})
		if err != nil {
			return nil, err
		}
		older := object(v)
		turns := objects(older["turns"])
		if len(turns) == 0 || str(older, "streamEpoch") != str(latest, "streamEpoch") ||
			str(turns[len(turns)-1], "id") != str(page, "partialTurn") || str(older, "nextCursor") == cursor {
			return nil, errors.New("历史消息的分页边界已变化，请重试")
		}
		pages = append(pages, older)
		page = older
		if !needsEarlierSnapshotPage(prior, page, false) {
			break
		}
	}

	// Copy every item slice once in source order. At an in-turn boundary
	// the newer fragment owns the turn's final status and timing.
	var turns []Object
	for _, p := range slices.Backward(pages) {
		for _, turn := range objects(p["turns"]) {
			items := objects(turn["items"])
			if n := len(turns); n > 0 && str(turns[n-1], "id") == str(turn, "id") {
				items = append(objects(turns[n-1]["items"]), items...)
				turns = turns[:n-1]
			} else {
				items = slices.Clone(items)
			}
			joined := copyObject(turn)
			joined["items"] = items
			turns = append(turns, joined)
		}
	}
	result := copyObject(latest)
	result["turns"], result["nextCursor"] = turns, page["nextCursor"]
	result["partialTurn"] = page["partialTurn"]
	return result, nil
}
