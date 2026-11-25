package persistence

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// A cursor identifies a stored item or an empty turn. Page bounds are exclusive.
// Both ordinals can be negative when older Provider pages are prepended.
type PageCursor struct{ Turn, Item int }

func (a PageCursor) Less(b PageCursor) bool {
	return a.Turn < b.Turn || a.Turn == b.Turn && a.Item < b.Item
}

// After selects ascending index traversal. Before can also cap a forward
// read at the live window. Returned records always use conversation order.
type PageQuery struct {
	Before, After *PageCursor
	Limit         int
	// On a tail read, the caller may finish the turn split by Limit.
	// Items are in read order. WindowSize and PageBytes still bound the page.
	ExtendTurn func(turn Object, items []Object) bool
}

const PageSize = 50
const WindowSize = 150
const PageBytes = 2 << 20

type HistoryPage struct {
	Transcript Object
	Revision   string
	Before     *PageCursor
	Start, End PageCursor
	After      *PageCursor
}

// PageBuilder consumes turns and their items in query order. The first row may
// exceed the byte limit, so even an unusually large message remains reachable.
type PageBuilder struct {
	Page                      HistoryPage
	forward                   bool
	extendTurn                func(Object, []Object) bool
	upper                     *PageCursor
	turns                     []Object
	items                     []Object
	current                   Object
	turn, count, bytes, limit int
	itemLimit                 int
}

func NewPage(head Object, revision string, query PageQuery) *PageBuilder {
	limit := query.Limit
	if limit <= 0 {
		limit = PageSize
	}
	return &PageBuilder{Page: HistoryPage{Transcript: head, Revision: revision}, limit: min(limit, WindowSize), forward: query.After != nil, upper: query.Before, extendTurn: query.ExtendTurn}
}
func (b *PageBuilder) full(size, limit int) bool {
	if b.count > 0 && (b.count >= limit || b.bytes+size > PageBytes) {
		if b.forward {
			at := b.Page.End
			b.Page.After = &at
		} else {
			at := b.Page.Start
			b.Page.Before = &at
		}
		return true
	}
	return false
}
func (b *PageBuilder) flush() {
	if b.current != nil {
		if !b.forward {
			slices.Reverse(b.items)
		}
		b.current["items"] = b.items
		b.turns = append(b.turns, b.current)
		b.current = nil
		b.items = nil
	}
}
func (b *PageBuilder) Turn(position int, raw []byte) (bool, error) {
	b.flush()
	if b.forward && b.upper != nil && position > b.upper.Turn {
		return false, nil
	}
	if b.full(len(raw), b.limit) {
		return false, nil
	}
	if err := json.Unmarshal(raw, &b.current); err != nil {
		return false, err
	}
	b.itemLimit = b.limit
	b.turn = position
	b.bytes += len(raw)
	return true, nil
}
func (b *PageBuilder) Item(position int, raw []byte) (bool, error) {
	at := PageCursor{b.turn, position}
	bounded := b.forward && b.upper != nil && !at.Less(*b.upper)
	if b.count == b.limit && b.extendTurn != nil && !b.forward && b.upper == nil && b.extendTurn(b.current, b.items) {
		b.itemLimit = WindowSize
	}
	if bounded || b.full(len(raw), b.itemLimit) {
		if len(b.items) == 0 {
			b.current = nil
		}
		return false, nil
	}
	var row Object
	if err := json.Unmarshal(raw, &row); err != nil {
		return false, err
	}
	b.items = append(b.items, row)
	b.record(position)
	b.bytes += len(raw)
	return true, nil
}
func (b *PageBuilder) record(position int) {
	at := PageCursor{b.turn, position}
	if b.count == 0 || b.forward {
		b.Page.End = at
	}
	if b.count == 0 || !b.forward {
		b.Page.Start = at
	}
	b.count++
}
func (b *PageBuilder) EndTurn(resumed bool) {
	if len(b.items) == 0 {
		if resumed || b.forward && b.upper != nil && !(PageCursor{b.turn, 0}).Less(*b.upper) {
			b.current = nil
			return
		}
		b.record(0)
	}
}
func (b *PageBuilder) Finish() HistoryPage {
	b.flush()
	if !b.forward {
		slices.Reverse(b.turns)
	}
	b.Page.Transcript["turns"] = b.turns
	return b.Page
}

// Tail discards backing arrays as well as the references to older rows.
func Tail(t Object, limit int) (Object, PageCursor, bool) {
	turns := Objects(t["turns"])
	left := limit
	size := 0
	for i := len(turns) - 1; i >= 0; i-- {
		items := Objects(turns[i]["items"])
		j := len(items)
		for j > 0 && left > 0 && (size < PageBytes || left == limit) {
			j--
			left--
			v := items[j]
			size += valueBytes(v, PageBytes-size)
		}
		if len(items) == 0 {
			left--
		}
		if j > 0 || (left <= 0 || size >= PageBytes) && i > 0 {
			visible := slices.Clone(turns[i:])
			visible[0] = maps.Clone(visible[0])
			visible[0]["items"] = slices.Clone(items[j:])
			out := maps.Clone(t)
			out["turns"] = visible
			return out, PageCursor{i, j}, true
		}
	}
	return t, PageCursor{}, false
}

func (p HistoryPage) View() Object {
	if p.Transcript == nil {
		return nil
	}
	t := p.Transcript
	t["_historyRevision"] = p.Revision
	return WithOrigin(t, p.Start, p.Before != nil)
}
func WithOrigin(t Object, pos PageCursor, more bool) Object {
	t["_turnStart"] = pos.Turn
	t["_itemStart"] = pos.Item
	t["_localOlder"] = more
	return t
}
func ordinal(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	}
	return 0
}
func Origin(t Object) PageCursor {
	return PageCursor{ordinal(t["_turnStart"]), ordinal(t["_itemStart"])}
}
func VisibleTail(t Object, limit int) Object {
	tail, shift, cut := Tail(t, limit)
	if !cut {
		return t
	}
	origin := Origin(t)
	if shift.Turn == 0 {
		shift.Item += origin.Item
	}
	shift.Turn += origin.Turn
	return WithOrigin(tail, shift, true)
}

// Align an overlapping remote page to the ordinal space of the committed rows.
func Align(old, next Object) Object {
	if _, ok := next["_turnStart"]; ok || old == nil {
		return next
	}
	before, after := Objects(old["turns"]), Objects(next["turns"])
	if len(after) == 0 {
		return next
	}
	pos := Origin(old)
	offsets := make(map[string]int, len(before))
	for i, t := range before {
		if id, _ := t["id"].(string); id != "" {
			offsets[id] = i
		}
	}
	for j, t := range after {
		i, ok := offsets[fmt.Sprint(t["id"])]
		if !ok {
			continue
		}
		origin := PageCursor{Turn: pos.Turn + i - j}
		if i == 0 && j == 0 && pos.Item != 0 {
			a, b := Objects(before[0]["items"]), Objects(t["items"])
			if len(a) > 0 {
				for n, v := range b {
					if v["id"] == a[0]["id"] {
						origin.Item = pos.Item - n
						break
					}
				}
			}
		}
		more := old["_localOlder"] == true && (origin.Turn > pos.Turn || origin.Turn == pos.Turn && origin.Item >= pos.Item || origin.Item > 0)
		return WithOrigin(next, origin, more)
	}
	return next
}

// PagePayloadBytes estimates a page's retained payload up to the page byte
// budget, without marshaling message or tool bodies.
func PagePayloadBytes(t Object) int {
	return min(PageBytes, valueBytes(t["turns"], PageBytes))
}

// Count retained JSON string payloads without serializing rows. Stop walking a
// large tool details array as soon as this page reaches its byte budget.
func valueBytes(v any, remaining int) int {
	if remaining <= 0 {
		return 0
	}
	switch a := v.(type) {
	case string:
		return len(a)
	case []byte:
		return len(a)
	case Object:
		n := 0
		for k, val := range a {
			n += len(k) + valueBytes(val, remaining-n)
			if n >= remaining {
				break
			}
		}
		return n
	case []any:
		n := 0
		for _, val := range a {
			n += valueBytes(val, remaining-n)
			if n >= remaining {
				break
			}
		}
		return n
	case []Object:
		n := 0
		for _, val := range a {
			n += valueBytes(val, remaining-n)
			if n >= remaining {
				break
			}
		}
		return n
	default:
		return 8
	}
}
