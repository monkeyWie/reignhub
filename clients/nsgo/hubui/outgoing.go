package hubui

import (
	"encoding/json"
	"fmt"
	"github.com/monkeyWie/reignhub/pkg/persistence"
	"strings"
	"time"
)

func (s *State) refreshAfterSend(key, target, provider, session string, reconcile bool) {
	if w := s.stream; w != nil && w.current() && w.key == key {
		// A joined event stream already delivers the user item, turn state and
		// assistant deltas. Resending the last 30 turns after every successful
		// send needlessly competes with those small live updates on relays.
		if !w.baseline || reconcile {
			w.refreshSnapshot()
		}
		return
	}
	started, generation := time.Now(), s.sessionGeneration
	go func() {
		v, err := rpc(s.ctx, s.api, target, provider, session, "transcript", Object{"fresh": true})
		s.cx.Post(func() {
			if s.ctx.Err() != nil || s.key() != key || generation != s.sessionGeneration || err != nil {
				return
			}
			s.setTranscript(object(v))
			s.reconcileOutgoing(started)
			s.rebuildMessages()
			s.persistSnapshot()
			s.changed()
		})
	}()
}

// Local messages are an overlay, never synthetic Agent turns. Only a fresh
// complete snapshot may clear a definitively failed submission without an echo.
type outgoingMessage struct {
	ID          string          `json:"id"`
	Text        string          `json:"text"`
	Model       string          `json:"model,omitempty"`
	Status      string          `json:"status"`
	Error       string          `json:"error,omitempty"`
	Timestamp   int64           `json:"timestamp"`
	FinishedAt  time.Time       `json:"finishedAt"`
	Attachments []Object        `json:"attachments,omitempty"`
	Known       map[string]bool `json:"known"`
	Anchors     []string        `json:"anchors"`
}

func (s *State) restoreOutgoing(t Object) {
	if s.outgoing == nil {
		s.outgoing = map[string][]*outgoingMessage{}
	}
	if _, exists := s.outgoing[s.key()]; exists {
		return
	}
	if _, exists := t["localMessages"]; !exists {
		return
	}
	var pending []*outgoingMessage
	b, _ := json.Marshal(t["localMessages"])
	_ = json.Unmarshal(b, &pending)
	for _, m := range pending {
		if m.Status == "sending" {
			m.Status, m.Error, m.FinishedAt = "uncertain", "发送结果尚未确认，连接恢复后将同步", time.Now()
		}
	}
	s.outgoing[s.key()] = pending
}

func (s *State) snapshotProjection() Object {
	t := copyObject(s.transcript)
	t["localMessages"] = snapshotOutgoing(s.outgoing[s.key()])
	delete(t, "resume")
	if s.stream != nil && s.stream.current() && s.stream.baseline && !s.stream.snapshotInFlight && s.streamKey == s.key() && s.streamEpoch != "" {
		t["resume"] = Object{"epoch": s.streamEpoch, "cursor": s.streamCursor}
	}
	return t
}

func snapshotOutgoing(messages []*outgoingMessage) []*outgoingMessage {
	// Outgoing delivery status is mutable; history objects are immutable.
	// Capture the small local overlay so the writer can serialize off the UI loop.
	pending := make([]*outgoingMessage, 0, len(messages))
	for _, message := range messages {
		value := *message
		if message.Anchors != nil {
			value.Anchors = append([]string{}, message.Anchors...)
		}
		value.Known = make(map[string]bool, len(message.Known))
		for id, known := range message.Known {
			value.Known[id] = known
		}
		pending = append(pending, &value)
	}
	return pending
}

func (s *State) queueOutgoing(body string, uploads []*Upload) *outgoingMessage {
	if s.reading != nil {
		s.latestPage()
	}
	s.restoreOutgoing(s.transcript)
	now := time.Now()
	m := &outgoingMessage{ID: fmt.Sprintf("local-%d", now.UnixNano()), Text: body, Model: s.model, Status: "sending", Timestamp: now.UnixMilli(), Known: map[string]bool{}}
	m.Anchors = []string{}
	for i := len(s.messageRows) - 1; i >= max(0, len(s.messageRows)-32); i-- {
		m.Anchors = append(m.Anchors, s.messageRows[i].ID)
	}
	for _, row := range s.messageRows {
		if row.Role == "user" {
			m.Known[row.ID] = true
		}
	}
	for _, u := range uploads {
		kind := "file"
		if strings.HasPrefix(u.MIME, "image/") {
			kind = "image"
		}
		a := Object{"id": u.ID, "name": u.Name, "mime": u.MIME, "kind": kind, "size": len(u.Data), "sha256": u.SHA256, "state": "ready"}
		m.Attachments = append(m.Attachments, a)
		if kind == "image" {
			s.cacheImage(s.imageKey(a, false), u.Data)
		}
	}
	s.outgoing[s.key()] = append(s.outgoing[s.key()], m)
	s.submittedDraft, s.submittedDraftSession = s.draft.GetUntracked(), s.key()
	s.draft.Set("")
	s.uploads = nil
	s.persistDraft()
	// Append the local row directly. Hashing and snapshot serialization must
	// not delay the send button's first pending frame.
	s.messageRows = append(s.messageRows, Message{ID: m.ID, Role: "user", Text: m.Text, Timestamp: m.Timestamp, Attachments: m.Attachments, Delivery: m.Status})
	s.persistSending()
	s.timeline.jumpToLatest()
	return m
}

// Queue delivery immediately, including when the first transcript is still loading.
func (s *State) persistSending() {
	if s.snapshots != nil && s.snapshots.storage != nil {
		s.writeSnapshot(s.target, str(s.selected, "providerId"), str(s.selected, "id"), Object{"_deliveryOnly": true, "localMessages": s.outgoing[s.key()]})
		return
	}
	s.scheduleSnapshot()
}

func (s *State) retryOutgoing(id string) {
	if !s.canSubmitMessage() {
		return
	}
	for _, m := range s.outgoing[s.key()] {
		if m.ID != id || m.Status != "failed" {
			continue
		}
		ids := []string{}
		for _, a := range m.Attachments {
			ids = append(ids, str(a, "id"))
		}
		method := "send"
		if activeTurn(s.transcript) != nil {
			method = "steer"
		}
		extra := Object{"text": m.Text, "attachments": ids, "turn": str(activeTurn(s.transcript), "id")}
		if m.Model != "" {
			extra["model"] = m.Model
		}
		m.Status, m.Error, m.FinishedAt = "sending", "", time.Time{}
		// Change only the local delivery row before the first pending frame;
		// retrying does not require projecting the entire remote history again.
		rows := append([]Message(nil), s.messageRows...)
		for i := range rows {
			if rows[i].ID == m.ID {
				rows[i].Delivery, rows[i].DeliveryError = m.Status, ""
			}
		}
		s.messageRows = rows
		s.persistSending()
		s.submitMessage(method, extra, m)
		return
	}
}

func matchesOutgoing(m *outgoingMessage, row Message) bool {
	if row.Role != "user" || m.Known[row.ID] || strings.TrimSpace(displayText(row)) != m.Text {
		return false
	}
	if len(m.Attachments) != len(row.Attachments) {
		return false
	}
	// Attachment IDs change when Codex imports inputs; content hashes remain stable.
	used := map[int]bool{}
	for _, a := range m.Attachments {
		found := false
		for i, remote := range row.Attachments {
			if !used[i] && str(a, "sha256") != "" && str(a, "sha256") == str(remote, "sha256") {
				found, used[i] = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (s *State) reconcileOutgoing(freshSince time.Time) {
	if len(s.outgoing[s.key()]) == 0 {
		return
	}
	rows := messages(s.transcript)
	complete := !freshSince.IsZero() && str(s.transcript, "nextCursor") == "" && str(s.transcript, "partialTurn") == "" && !flag(s.transcript, "_localOlder")
	used := map[string]bool{}
	kept := []*outgoingMessage{}
	for _, m := range s.outgoing[s.key()] {
		found := false
		for _, row := range rows {
			if !used[row.ID] && matchesOutgoing(m, row) {
				used[row.ID], found = true, true
				if s.confirmedRowKeys == nil {
					s.confirmedRowKeys = make(map[string]string)
				}
				s.confirmedRowKeys[row.ID] = m.ID
				s.replaceOutgoingAnchor(m.ID, []string{row.ID})
				for _, other := range s.outgoing[s.key()] {
					if other != m {
						if other.Known == nil {
							other.Known = map[string]bool{}
						}
						other.Known[row.ID] = true
					}
				}
				break
			}
		}
		if found {
			m.Status = "confirmed"
			s.markOutgoingRemoved(m.ID)
			continue
		}
		// An accepted command can precede its history echo, even after a fresh read.
		if complete && m.Status == "failed" && !m.FinishedAt.IsZero() && !m.FinishedAt.After(freshSince) {
			s.replaceOutgoingAnchor(m.ID, m.Anchors)
			s.markOutgoingRemoved(m.ID)
			continue
		}
		kept = append(kept, m)
	}
	s.outgoing[s.key()] = kept
}

// A different controller may own other pending sends in this same session.
func (s *State) markOutgoingRemoved(id string) {
	if s.snapshots == nil || s.snapshots.storage == nil {
		return
	}
	if s.outgoingRemoved == nil {
		s.outgoingRemoved = make(map[string]map[string]bool)
	}
	key := s.key()
	if s.outgoingRemoved[key] == nil {
		s.outgoingRemoved[key] = make(map[string]bool)
	}
	s.outgoingRemoved[key][id] = true
}

func (s *State) replaceOutgoingAnchor(id string, replacements []string) {
	for _, m := range s.outgoing[s.key()] {
		anchors := make([]string, 0, 32)
		seen := map[string]bool{}
		add := func(anchor string) {
			if anchor != id && !seen[anchor] && len(anchors) < 32 {
				anchors = append(anchors, anchor)
				seen[anchor] = true
			}
		}
		for _, anchor := range m.Anchors {
			if anchor == id {
				for _, replacement := range replacements {
					add(replacement)
				}
			} else {
				add(anchor)
			}
		}
		if m.Anchors != nil {
			m.Anchors = anchors
		}
	}
}

func (s *State) liveDisplay() Object {
	display := s.livePage
	if prefix := s.historyPrefix; len(prefix.pages) > 0 {
		if prefix.end == persistence.Origin(display) {
			return joinPages(pageView(prefix.pages), display, persistence.WindowSize)
		}
		// The live window moved. Its old prefix is no longer contiguous.
		s.historyPrefix = historyPrefix{}
	}
	return display
}

func (s *State) rebuildMessages() {
	s.livePage = s.projectLivePage()
	if s.reading != nil {
		s.messageRows = messages(s.reading)
		return
	}
	display := s.liveDisplay()
	s.messageRows = messages(display)
	if len(s.confirmedRowKeys) > 0 {
		retained := make(map[string]string, len(s.confirmedRowKeys))
		for _, row := range s.messageRows {
			if key := s.confirmedRowKeys[row.ID]; key != "" {
				retained[row.ID] = key
			}
		}
		s.confirmedRowKeys = retained
	}
	if len(s.animateText) > 0 {
		live := map[string]bool{}
		for _, row := range s.messageRows {
			if row.Live && s.animateText[row.ID] {
				live[row.ID] = true
			}
		}
		s.animateText = live
	}
	if len(s.outgoing[s.key()]) == 0 {
		return
	}
	// Place each overlay after its nearest surviving predecessor. Do not sort
	// remote history by timestamps (many Providers have no item timestamps), or
	// append a failed send after replies which arrived later.
	positions := map[string]int{}
	for i, row := range s.messageRows {
		positions[row.ID] = i
		// A completed process group replaces its earlier live rows. Pending
		// submissions keep their predecessor when those rows are folded.
		for _, detail := range row.Details {
			positions[detail.ID] = i
		}
	}
	// A turn-wide process summary can include records after a visible steer.
	// Keep pending submissions after that steer, even when their immediate
	// predecessor has moved into the earlier summary.
	for _, turn := range objects(display["turns"]) {
		previous := -1
		for _, item := range presentationItems(turn) {
			id := str(turn, "id") + "|" + str(item, "id")
			if str(item, "role") == "tool" {
				id = str(turn, "id") + "|tools|" + str(item, "id")
			}
			if position, ok := positions[id]; ok {
				previous = max(previous, position)
				positions[id] = previous
			}
		}
	}
	buckets := make([][]Message, len(s.messageRows)+1)
	for _, m := range s.outgoing[s.key()] {
		position := -1
		if m.Anchors == nil {
			position = len(s.messageRows) - 1
		}
		for _, anchor := range m.Anchors {
			if i, ok := positions[anchor]; ok {
				position = i
				break
			}
		}
		positions[m.ID] = position
		buckets[position+1] = append(buckets[position+1], Message{ID: m.ID, Role: "user", Text: m.Text, Timestamp: m.Timestamp, Attachments: m.Attachments, Delivery: m.Status, DeliveryError: m.Error})
	}
	rows := make([]Message, 0, len(s.messageRows)+len(s.outgoing[s.key()]))
	rows = append(rows, buckets[0]...)
	for i, row := range s.messageRows {
		rows = append(rows, row)
		rows = append(rows, buckets[i+1]...)
	}
	s.messageRows = rows
}
