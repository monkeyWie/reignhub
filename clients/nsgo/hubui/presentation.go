package hubui

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/monkeyWie/reignhub/pkg/messagecontent"
)

type Directory struct {
	Key, Path, Title string
}
type Message struct {
	ID, Role, Text, Turn    string
	Delivery, DeliveryError string
	Live                    bool
	Timestamp               int64
	DurationMS              *int64
	Attachments             []Object
	Tools                   []Object
	Details                 []Message
}

func activeTurn(t Object) Object {
	turns := objects(t["turns"])
	if len(turns) > 0 && str(turns[len(turns)-1], "status") == "inProgress" {
		return turns[len(turns)-1]
	}
	return nil
}
func visibleTool(item Object) bool {
	switch str(item, "type") {
	case "functionCallOutput", "hookPrompt", "contextCompaction", "enteredReviewMode", "exitedReviewMode":
		return false
	}
	return str(item, "role") == "tool"
}
func activityLabel(m Message) string {
	labels := map[string]string{"read": "读取文件", "list": "列出目录", "search": "搜索", "command": "运行命令", "edit": "编辑文件", "web": "浏览网页", "tool": "调用工具", "agent": "子 Agent", "image": "图片", "plan": "更新计划"}
	counts := map[string]int{}
	order := []string{}
	running := ""
	for _, tool := range m.Tools {
		acts := objects(tool["activities"])
		live := m.Live && len(acts) > 0 && str(tool, "status") == "inProgress"
		if len(acts) == 0 {
			acts = []Object{{"kind": "tool", "title": "执行工具 " + str(tool, "type")}}
		}
		for _, a := range acts {
			kind := str(a, "kind")
			if counts[kind] == 0 {
				order = append(order, kind)
			}
			counts[kind]++
			if live {
				title := str(a, "title")
				if kind == "command" || title == "" || len(title) > 120 || strings.ContainsAny(title, "\r\n\u2028\u2029") {
					title = labels[kind]
				}
				if title != "" {
					running = "正在" + title
				}
			}
		}
	}
	if running != "" {
		return running
	}
	parts := []string{}
	for _, kind := range order {
		label := labels[kind]
		if label == "" {
			label = "活动"
		}
		parts = append(parts, fmt.Sprintf("%s %d", label, counts[kind]))
	}
	return strings.Join(parts, " · ")
}

func messageHasContent(item Object) bool {
	return strings.TrimSpace(str(item, "text")) != "" || len(objects(item["attachments"])) > 0
}

func terminalTurn(turn Object) bool {
	switch str(turn, "status") {
	case "completed", "failed", "interrupted":
		return true
	}
	return false
}

func foldsWork(turn Object) bool {
	if terminalTurn(turn) {
		for _, item := range objects(turn["items"]) {
			if str(item, "role") == "assistant" && str(item, "phase") != "commentary" && messageHasContent(item) {
				return true
			}
		}
	}
	return false
}

// Expand private in-memory process fragments in chronological order. Ordinary
// live and provider rows use their original slice without allocating.
func presentationItems(turn Object) []Object {
	items := objects(turn["items"])
	var expanded []Object
	for i, item := range items {
		if group, ok := item["_historyProcess"].([]Object); ok {
			if expanded == nil {
				expanded = append(make([]Object, 0, len(items)), items[:i]...)
			}
			expanded = append(expanded, group...)
		} else if expanded != nil {
			expanded = append(expanded, item)
		}
	}
	if expanded != nil {
		return expanded
	}
	return items
}

func messages(t Object) []Message {
	out := []Message{}
	active := str(activeTurn(t), "id")
	for _, turn := range objects(t["turns"]) {
		id := str(turn, "id")
		live := id != "" && id == active
		// Only fold explicit process messages after a terminal turn has a
		// visible answer. Unknown phases and incomplete history remain readable.
		fold := foldsWork(turn)
		var details []Message
		workRow, workGroups := -1, 0
		flushDetails := func() {
			if len(details) > 0 {
				if workRow < 0 {
					workRow = len(out)
					workGroups++
					out = append(out, Message{ID: id + "|work|" + details[0].ID, Role: "work", Turn: id})
				}
				out[workRow].Details = append(out[workRow].Details, details...)
				details = nil
			}
		}
		tools := []Object{}
		flush := func(current bool) {
			if len(tools) > 0 {
				m := Message{ID: id + "|tools|" + str(tools[0], "id"), Role: "activity", Turn: id, Live: current, Tools: tools}
				if fold {
					details = append(details, m)
				} else {
					out = append(out, m)
				}
				tools = nil
			}
		}
		for _, item := range presentationItems(turn) {
			role := str(item, "role")
			if str(item, "type") == "contextCompaction" {
				flush(false)
				flushDetails()
				workRow = -1
				out = append(out, Message{ID: id + "|" + str(item, "id"), Role: "compaction", Turn: id})
				continue
			}
			if visibleTool(item) && (live || fold) {
				tools = append(tools, item)
				continue
			}
			if role != "user" && role != "assistant" {
				continue
			}
			body := str(item, "text")
			attachments := objects(item["attachments"])
			if strings.TrimSpace(body) == "" && len(attachments) == 0 {
				continue
			}
			flush(false)
			m := Message{ID: id + "|" + str(item, "id"), Role: role, Text: body, Turn: id, Live: live && role == "assistant", Timestamp: number(item, "timestampMs"), Attachments: attachments}
			if fold && role == "assistant" && str(item, "phase") == "commentary" {
				details = append(details, m)
				continue
			}
			// One disclosure belongs to the turn. Steers remain visible in
			// order without acquiring their own process summary underneath.
			flushDetails()
			out = append(out, m)
		}
		// Only the activity group at the live tail represents the current work.
		// Earlier groups stop animating as soon as a later message arrives.
		flush(live)
		flushDetails()
		if workRow >= 0 && workGroups == 1 && turn["durationMs"] != nil && number(turn, "durationMs") >= 0 {
			out[workRow].DurationMS = new(number(turn, "durationMs"))
		}
	}
	return out
}

var attachmentHeader = regexp.MustCompile(`(?m)^## [^\n]+?: (.+)$`)
var attachmentLink = regexp.MustCompile(`\]\(<([^>]+)>\)`)

func displayText(m Message) string {
	body := m.Text
	if m.Role != "user" || len(m.Attachments) == 0 {
		return body
	}
	// Older cached transcripts may contain a provider's synthetic image marker.
	// Remove only trailing markers backed by actual image attachments.
	for _, a := range m.Attachments {
		if str(a, "kind") == "image" {
			trimmed := strings.TrimSpace(body)
			for _, marker := range []string{"[localImage]", "[image]"} {
				if strings.HasSuffix(trimmed, marker) {
					body = strings.TrimSpace(strings.TrimSuffix(trimmed, marker))
					break
				}
			}
		}
	}
	sources := map[string]bool{}
	for _, a := range m.Attachments {
		sources[str(a, "source")] = true
	}
	const marker = "## My request:"
	if i := strings.Index(body, marker); i >= 0 && strings.HasPrefix(strings.TrimSpace(body), "# Files mentioned by the user:") {
		entries := attachmentHeader.FindAllStringSubmatch(body[:i], -1)
		valid := len(entries) > 0
		for _, e := range entries {
			valid = valid && sources[strings.TrimSpace(e[1])]
		}
		if valid {
			body = strings.TrimSpace(body[i+len(marker):])
		}
	}
	for _, upload := range []string{
		"ReignHub 上传的附件（已保存在本机，可按需读取）：",
		"Agent Hub 上传的附件（已保存在本机，可按需读取）：",
		"RAS 上传的附件（已保存在本机，可按需读取）：",
	} {
		if i := strings.Index(body, upload); i >= 0 {
			entries := attachmentLink.FindAllStringSubmatch(body[i:], -1)
			valid := len(entries) > 0
			for _, e := range entries {
				valid = valid && sources[e[1]]
			}
			if valid {
				body = strings.TrimSpace(body[:i])
			}
		}
	}
	return body
}

func messageCopyText(m Message) string {
	if m.Role == "user" {
		return messagecontent.PlainText(messagecontent.Parse(displayText(m)))
	}
	return m.Text
}

func messagePreviewText(m Message) string {
	text := displayText(m)
	if m.Role == "user" {
		return messagecontent.PlainText(messagecontent.Parse(text))
	}
	return text
}

// Retain one position per turn, with the last received value winning. Besides
// normal history reads, this repairs snapshots written with duplicated tails.
// Always return a new slice: published history belongs to the cache writer too.
func uniqueTurns(turns []Object) []Object {
	out := make([]Object, 0, len(turns))
	indices := make(map[string]int, len(turns))
	for _, turn := range turns {
		id := str(turn, "id")
		if i, ok := indices[id]; id != "" && ok {
			out[i] = turn
			continue
		}
		indices[id] = len(out)
		out = append(out, turn)
	}
	return out
}

func mergeLatest(old, latest Object) Object {
	out := copyObject(latest)
	if queue := object(latest["queue"]); queue != nil {
		out["queue"] = mergeMessageQueue(object(old["queue"]), queue)
	}
	previous, next := uniqueTurns(objects(old["turns"])), uniqueTurns(objects(latest["turns"]))
	// A recovered connection can first return its last persisted snapshot.
	// Preserve the live tail until a newer/final snapshot actually supersedes it.
	if activeTurn(old) != nil {
		if len(next) == 0 {
			out["turns"], out["nextCursor"] = previous, old["nextCursor"]
			return out
		}
		for i, turn := range previous {
			if str(turn, "id") == str(next[len(next)-1], "id") && i < len(previous)-1 {
				// A refreshed prefix can reorder known turns. Appending the whole
				// old suffix repeats those turns and can double it on each refresh.
				seen := make(map[string]bool, len(next))
				for _, turn := range next {
					seen[str(turn, "id")] = true
				}
				for _, turn := range previous[i+1:] {
					if id := str(turn, "id"); !seen[id] {
						seen[id] = true
						next = append(next, turn)
					}
				}
				break
			}
		}
	}
	byID := map[string]Object{}
	for _, turn := range previous {
		byID[str(turn, "id")] = turn
	}
	// Join a partial first turn only across a known item boundary in the
	// same epoch. Otherwise the incoming cursor owns the gap.
	partial := str(latest, "partialTurn")
	sameEpoch := str(old, "streamEpoch") == str(latest, "streamEpoch")
	joinedPartial := false
	if partial != "" && len(next) > 0 && str(next[0], "id") == partial && sameEpoch {
		prior := objects(byID[partial]["items"])
		items := objects(next[0]["items"])
		if len(items) > 0 {
			for j, item := range prior {
				if str(item, "id") != "" && str(item, "id") == str(items[0], "id") {
					first := copyObject(next[0])
					first["items"] = append(append([]Object{}, prior[:j]...), items...)
					next[0] = first
					out["nextCursor"] = old["nextCursor"]
					joinedPartial = true
					break
				}
			}
		}
	}
	for i, turn := range next {
		if str(turn, "id") == partial && !joinedPartial {
			continue
		}
		prior := byID[str(turn, "id")]
		if prior == nil || str(turn, "status") != "inProgress" {
			continue // A final snapshot is authoritative, including corrected text.
		}
		if str(prior, "status") != "inProgress" {
			// A current snapshot can correct a persisted terminal status or
			// describe a resumed turn. The stream reconciles its boundary and
			// replays newer events before merging; old status is not a veto.
			continue
		}
		merged := copyObject(turn)
		items := append([]Object{}, objects(prior["items"])...)
		if flag(old, "_localOlder") && len(previous) > 0 && str(previous[0], "id") == str(turn, "id") && len(items) > 0 {
			incoming := objects(turn["items"])
			for j, item := range incoming {
				if str(item, "id") == str(items[0], "id") {
					items = append(append([]Object{}, incoming[:j]...), items...)
					break
				}
			}
		}
		indices := map[string]int{}
		for j, item := range items {
			indices[str(item, "id")] = j
		}
		for _, item := range objects(turn["items"]) {
			j, exists := indices[str(item, "id")]
			if !exists {
				indices[str(item, "id")] = len(items)
				items = append(items, item)
				continue
			}
			value := copyObject(item)
			if text := str(items[j], "text"); strings.HasPrefix(text, str(item, "text")) {
				value["text"] = text
			}
			if str(items[j], "status") == "completed" && str(item, "status") == "inProgress" {
				value["status"] = "completed"
			}
			items[j] = value
		}
		merged["items"] = items
		next[i] = merged
	}
	out["turns"] = next
	anchor := -1
	if len(next) > 0 {
		for i, t := range previous {
			if str(t, "id") == str(next[0], "id") {
				anchor = i
				break
			}
		}
	}
	// The retained prefix carries a source cursor. A fresh replay epoch
	// can have a different owner or pagination base.
	oldEpoch, newEpoch := str(old, "streamEpoch"), str(latest, "streamEpoch")
	if anchor > 0 && (partial == "" || joinedPartial) && (oldEpoch == "" || newEpoch == "" || oldEpoch == newEpoch) {
		seen := map[string]bool{}
		for _, t := range next {
			seen[str(t, "id")] = true
		}
		kept := []Object{}
		for _, t := range previous[:anchor] {
			if !seen[str(t, "id")] {
				kept = append(kept, t)
			}
		}
		out["turns"] = append(kept, next...)
		out["nextCursor"] = old["nextCursor"]
	}
	return out
}

// Event offsets are UTF-16, matching Codex and the shared event protocol.
// Snapshots can already contain an event; overlap must be idempotent.
func applyEvents(source Object, events []Object) (Object, bool) {
	if len(events) == 0 {
		return source, false
	}
	// Copy only the paths changed by this batch. Serializing the entire history
	// for every live delta blocks the UI loop on large conversations.
	out := copyObject(source)
	recency := number(object(source["session"]), "recencyAt")
	turns := append([]Object(nil), objects(source["turns"])...)
	indices := make(map[string]int, len(turns))
	for i, turn := range turns {
		indices[str(turn, "id")] = i
	}
	editable := map[string]bool{}
	reset := false
	for _, e := range events {
		kind := str(e, "kind")
		if kind == "snapshot" {
			reset = true
			continue
		}
		if kind == "queue" {
			if queue := object(e["queue"]); queue != nil {
				out["queue"] = mergeMessageQueue(object(out["queue"]), queue)
			}
			continue
		}
		id := str(e, "turnId")
		if id == "" {
			continue
		}
		i, exists := indices[id]
		if !exists {
			i = len(turns)
			indices[id] = i
			turns = append(turns, Object{"id": id, "status": "inProgress", "items": []Object{}})
		}
		if !editable[id] {
			turns[i] = copyObject(turns[i])
			turns[i]["items"] = append([]Object(nil), objects(turns[i]["items"])...)
			editable[id] = true
		}
		turn := turns[i]
		terminal := str(turn, "status") == "completed" || str(turn, "status") == "failed" || str(turn, "status") == "interrupted"
		if kind == "turn" {
			// Explicit lifecycle events are ordered by the subscription cursor.
			// They may reopen a turn; item/text events alone must not do so.
			turn["status"] = e["status"]
			turn["recencyAt"] = max(number(turn, "recencyAt"), number(e, "recencyAt"))
			recency = max(recency, number(turn, "recencyAt"))
			if e["durationMs"] != nil && number(e, "durationMs") >= 0 {
				turn["durationMs"] = e["durationMs"]
			} else if terminal && str(e, "status") == "inProgress" {
				delete(turn, "durationMs")
			}
			if str(e, "status") != "inProgress" {
				reset = true
			}
			continue
		}
		items := objects(turn["items"])
		index := -1
		for i, v := range items {
			if str(v, "id") == str(e, "itemId") {
				index = i
				break
			}
		}
		old := Object{}
		if index >= 0 {
			old = items[index]
		}
		next := copyObject(old)
		switch kind {
		case "item":
			if terminal && str(object(e["item"]), "status") == "inProgress" {
				continue
			}
			for k, v := range object(e["item"]) {
				next[k] = v
			}
			partial := str(next, "status") == "" || str(next, "status") == "inProgress"
			if !terminal && partial && str(old, "phase") == str(next, "phase") && strings.HasPrefix(str(old, "text"), str(next, "text")) {
				next["text"] = old["text"]
			}
			if str(old, "status") == "completed" && str(next, "status") == "inProgress" {
				next["status"] = old["status"]
			}
		case "summary":
			if terminal {
				continue
			}
			next["id"] = e["itemId"]
			next["role"] = "reasoning"
			next["reasoningSummary"] = e["summary"]
		case "text":
			if terminal {
				continue
			}
			s, d := utf16.Encode([]rune(str(old, "text"))), utf16.Encode([]rune(str(e, "delta")))
			offset := int(number(e, "offset"))
			if offset < 0 || offset > len(s) {
				reset = true
				continue
			}
			overlap := min(len(s)-offset, len(d))
			if fmt.Sprint(s[offset:offset+overlap]) != fmt.Sprint(d[:overlap]) {
				reset = true
				continue
			}
			next["id"] = e["itemId"]
			if str(next, "role") == "" {
				next["role"] = "assistant"
			}
			next["text"] = string(utf16.Decode(append(s, d[overlap:]...)))
		default:
			continue
		}
		if str(next, "role") == "user" && str(next, "status") != "failed" && str(next, "status") != "rejected" {
			recency = max(recency, number(next, "timestampMs"))
		}
		if index < 0 {
			items = append(items, next)
		} else {
			items[index] = next
		}
		turn["items"] = items
	}
	out["turns"] = turns
	if recency > number(object(source["session"]), "recencyAt") {
		summary := copyObject(object(source["session"]))
		summary["recencyAt"] = recency
		out["session"] = summary
	}
	return out, reset
}
