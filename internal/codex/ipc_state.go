package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/readretry"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

func decodeObject(raw json.RawMessage) map[string]any {
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	return v
}
func stringValue(v any) string      { s, _ := v.(string); return s }
func rawJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func object(v any) map[string]any   { m, _ := v.(map[string]any); return m }
func array(v any) []any             { a, _ := v.([]any); return a }

// The canonical history's islands define ordering; map iteration never does.
func stateTurns(state map[string]any) ([]map[string]any, bool, error) {
	container := object(state["turnHistory"])
	if state["turnHistory"] != nil && (container["kind"] != "canonical" || object(container["history"]) == nil) {
		return nil, false, errors.New("未知 IPC 历史结构")
	}
	history := object(container["history"])
	entries := []any{}
	entities := object(history["entitiesByKey"])
	for _, raw := range array(history["islands"]) {
		entries = append(entries, array(object(raw)["entries"])...)
	}
	complete, _ := history["isComplete"].(bool)
	turns := []map[string]any{}
	seen := map[string]bool{}
	indices := map[string]int{}
	for _, raw := range entries {
		key := stringValue(object(raw)["value"])
		if key == "" {
			key = stringValue(object(raw)["key"])
		}
		entity := object(entities[key])
		if entity == nil {
			return nil, false, errors.New("IPC 历史引用了不存在的 turn")
		}
		id := stringValue(entity["turnId"])
		if id == "" {
			return nil, false, errors.New("IPC turn 缺少 ID")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		t := map[string]any{"id": id, "status": entity["status"], "items": entity["items"], "durationMs": entity["durationMs"]}
		if ms, ok := ipcTimestamp(entity["turnStartedAtMs"]); ok {
			t["startedAt"] = ms / 1000
			t["startedAtMs"] = ms
		}
		indices[id] = len(turns)
		turns = append(turns, t)
	}
	// Canonical history and the live tail coexist in both verified schemas.
	// The owner may patch only turns while entitiesByKey keeps an older snapshot.
	for _, value := range array(state["turns"]) {
		t := object(value)
		id := stringValue(t["turnId"])
		if id == "" {
			id = stringValue(t["id"])
		}
		if id == "" {
			return nil, false, errors.New("未知 IPC turn 结构")
		}
		// Only projection fields are changed. Items are read-only and the merge
		// helpers allocate their own slices; copying every message through JSON
		// here repeats the full history several times for each live patch.
		copy := maps.Clone(t)
		copy["id"] = id
		if ms, ok := ipcTimestamp(t["turnStartedAtMs"]); ok {
			copy["startedAt"] = ms / 1000
			copy["startedAtMs"] = ms
		}
		if index, exists := indices[id]; exists {
			old := turns[index]
			for key, value := range old {
				if _, exists := copy[key]; !exists {
					copy[key] = value
				}
			}
			copy["items"] = mergeIPCItems(array(old["items"]), array(copy["items"]))
			turns[index] = copy
			continue
		}
		indices[id] = len(turns)
		turns = append(turns, copy)
	}
	for _, turn := range turns {
		turn["items"] = ipcMessageItems(array(turn["items"]))
	}
	if history != nil {
		return turns, complete, nil
	}
	return turns, state["historyMode"] != "paginated", nil
}
func stateThread(s map[string]any) thread {
	var t thread
	_ = json.Unmarshal(rawJSON(map[string]any{"id": s["id"], "name": s["title"], "cwd": s["cwd"], "model": s["latestModel"], "source": s["source"], "originator": s["originator"]}), &t)
	if ms, ok := ipcTimestamp(s["updatedAt"]); ok {
		t.UpdatedAt = ms
	}
	if ms, ok := ipcTimestamp(s["createdAt"]); ok {
		t.CreatedAt = ms
	}
	t.Status.Type = "idle"
	if runtime := object(s["threadRuntimeStatus"]); stringValue(runtime["type"]) != "" {
		_ = json.Unmarshal(rawJSON(runtime), &t.Status)
	}
	return t
}

// IPC snapshots can serialize dates as milliseconds, numeric strings or ISO dates.
func ipcTimestamp(value any) (int64, bool) {
	switch value := value.(type) {
	case float64:
		return int64(value), true
	case string:
		if ms, err := strconv.ParseFloat(value, 64); err == nil {
			return int64(ms), true
		}
		if date, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return date.UnixMilli(), true
		}
	}
	return 0, false
}
func (p *Provider) observeIPCState(id, owner string, oldRaw json.RawMessage, next map[string]any, turns []map[string]any) {
	log := p.stream().log
	if len(oldRaw) == 0 {
		log.Publish(provider.StreamEvent{Kind: "snapshot", SessionID: id})
		if next["currentPermissions"] != nil || next["latestThreadSettings"] != nil || next["latestReasoningEffort"] != nil {
			log.Publish(provider.StreamEvent{Kind: "composerSettings", SessionID: id})
		}
	} else {
		priorState := decodeObject(oldRaw)
		// Settings can change without a turn/item event. Wake the existing
		// snapshot path so controllers receive the new session model as well.
		if stringValue(priorState["latestModel"]) != stringValue(next["latestModel"]) {
			log.Publish(provider.StreamEvent{Kind: "snapshot", SessionID: id})
		}
		if !reflect.DeepEqual(priorState["currentPermissions"], next["currentPermissions"]) || !reflect.DeepEqual(priorState["latestThreadSettings"], next["latestThreadSettings"]) || stringValue(priorState["latestReasoningEffort"]) != stringValue(next["latestReasoningEffort"]) {
			log.Publish(provider.StreamEvent{Kind: "composerSettings", SessionID: id})
		}
		previous, _, _ := stateTurns(priorState)
		oldTurns := map[string]map[string]any{}
		for _, v := range previous {
			oldTurns[stringValue(v["id"])] = v
		}
		for _, v := range turns {
			prior, known := oldTurns[stringValue(v["id"])]
			if known && reflect.DeepEqual(prior, v) {
				continue // Unchanged history cannot produce a normalized event.
			}
			t := normalizeTurn(rawJSON(v))
			old := normalizeTurn(rawJSON(prior))
			turnChanged := !known || old.Status != t.Status || !reflect.DeepEqual(old.DurationMS, t.DurationMS) || old.RecencyAt != t.RecencyAt
			if turnChanged && t.Status == "inProgress" {
				log.Publish(provider.StreamEvent{Kind: "turn", SessionID: id, TurnID: t.ID, Status: t.Status, DurationMS: t.DurationMS, RecencyAt: t.RecencyAt})
			}
			items := map[string]provider.Item{}
			oldOrder := []string{}
			for _, i := range old.Items {
				items[i.ID] = i
				if i.Role != "internal" {
					oldOrder = append(oldOrder, i.ID)
				}
			}
			newOrder := []string{}
			rawItems := map[string]map[string]any{}
			for _, item := range array(v["items"]) {
				rawItems[stringValue(object(item)["id"])] = object(item)
			}
			refresh := false
			for _, i := range t.Items {
				if i.Role == "internal" {
					continue
				}
				newOrder = append(newOrder, i.ID)
				prev, exists := items[i.ID]
				if exists && reflect.DeepEqual(prev, i) {
					continue
				}
				// Keep existing cards until imported metadata is ready; publishing
				// an unprocessed envelope here would replace images with file paths.
				if i.Role == "user" && userAttachmentSnapshot(rawJSON(rawItems[i.ID])) || i.Type == "imageGeneration" {
					refresh = true
					continue
				}
				event := provider.StreamEvent{Kind: "item", SessionID: id, TurnID: t.ID, ItemID: i.ID, Item: &i}
				if t.Status == "inProgress" && exists && i.Role == "assistant" && prev.Type == i.Type && prev.Phase == i.Phase && strings.HasPrefix(i.Text, prev.Text) && len(i.Text) > len(prev.Text) {
					event.Kind = "text"
					event.Item = nil
					// Replay retains only the suffix across successive revisions.
					event.Delta = strings.Clone(strings.TrimPrefix(i.Text, prev.Text))
					event.Offset = utf16Length(prev.Text)
				}
				log.Publish(event)
			}
			// Asset import belongs to cancellable history reads, not notifications.
			// Item events append at the controller. Reconcile when the owner
			// inserts before existing output, reorders, or replaces a local steer
			// with its server representation instead of duplicating the bubble.
			if refresh || len(newOrder) < len(oldOrder) || !reflect.DeepEqual(oldOrder, newOrder[:min(len(oldOrder), len(newOrder))]) {
				log.Publish(provider.StreamEvent{Kind: "snapshot", SessionID: id})
			}
			// Deliver the authoritative final items before marking the turn
			// terminal; controllers intentionally ignore late streaming deltas.
			if turnChanged && t.Status != "inProgress" {
				log.Publish(provider.StreamEvent{Kind: "turn", SessionID: id, TurnID: t.ID, Status: t.Status, DurationMS: t.DurationMS, RecencyAt: t.RecencyAt})
			}
		}
	}
	p.syncIPCApprovals(id, owner, next, turns)
}
func (p *Provider) syncIPCApprovals(id, owner string, state map[string]any, turns []map[string]any) {
	route := "ipc:" + id + ":" + owner
	methods := map[string]string{"item/commandExecution/requestApproval": "thread-follower-command-approval-decision", "item/fileChange/requestApproval": "thread-follower-file-approval-decision", "item/tool/requestUserInput": "thread-follower-submit-user-input", "item/permissions/requestApproval": "thread-follower-permissions-request-approval-response", "mcpServer/elicitation/request": "thread-follower-submit-mcp-server-elicitation-response"}
	active := map[string]bool{}
	requests := append([]any{}, array(state["requests"])...)
	requests = append(requests, asyncQuestionRequests(id, turns, p.dismissedQuestions(id, owner, ""))...)
	latestTurn := ""
	if len(turns) > 0 {
		latestTurn = stringValue(turns[len(turns)-1]["id"])
	}
	for _, raw := range requests {
		r := object(raw)
		method := stringValue(r["method"])
		ipcMethod := methods[method]
		if ipcMethod == "" && method != asyncQuestionMethod {
			continue
		}
		requestID := rawJSON(r["requestId"])
		if string(requestID) == "null" {
			requestID = rawJSON(r["id"])
		}
		key := string(requestID)
		active[key] = true
		p.mu.Lock()
		exists := false
		for _, v := range p.pending {
			if v.route == route && v.requestID == key {
				exists = true
				break
			}
		}
		p.mu.Unlock()
		if exists {
			continue
		}
		params := object(r["params"])
		if params == nil {
			params = map[string]any{}
		}
		params["threadId"] = id
		bridge := p.appBridge()
		reply := func(ctx context.Context, result any) error {
			if bridge == nil {
				return provider.ErrUnavailable
			}
			if method == asyncQuestionMethod {
				return bridge.answerAsyncQuestions(ctx, id, owner, r, result)
			}
			v := map[string]any{"conversationId": id, "requestId": json.RawMessage(requestID)}
			if ipcMethod == "thread-follower-submit-user-input" || ipcMethod == "thread-follower-permissions-request-approval-response" || ipcMethod == "thread-follower-submit-mcp-server-elicitation-response" {
				v["response"] = result
			} else {
				v["decision"] = object(result)["decision"]
				if v["decision"] == nil {
					var d struct{ Decision string }
					_ = json.Unmarshal(rawJSON(result), &d)
					v["decision"] = d.Decision
				}
			}
			_, err := bridge.request(ctx, ipcMethod, v, owner)
			return ipcWriteError(ipcMethod, err)
		}
		p.addPending(rpc.Message{ID: requestID, Method: method, Params: rawJSON(params)}, reply, route)
	}
	p.mu.Lock()
	for key, v := range p.pending {
		// Async skip/submission tombstones outlive the owner's reply echo, so
		// skipped siblings cannot reappear as a smaller form on the next patch.
		if v.route == route && v.claimed && v.approval.Method == asyncQuestionMethod && v.approval.TurnID == latestTurn {
			continue
		}
		if strings.HasPrefix(v.route, "ipc:"+id+":") && (v.route != route || !active[v.requestID]) {
			delete(p.pending, key)
		}
	}
	p.mu.Unlock()
}
func (p *Provider) ipcTranscript(ctx context.Context, id, cursor string) (provider.Transcript, bool, error) {
	b := p.appBridge()
	if b == nil || b.owns(id) {
		return provider.Transcript{}, false, nil
	}
	epoch, boundary := p.stream().log.Position()
	b.mu.Lock()
	f := b.followed[id]
	var raw json.RawMessage
	var err error
	var owner string
	if f != nil {
		raw = f.state
		err = f.err
		owner = f.owner
	}
	b.mu.Unlock()
	if strings.HasPrefix(cursor, "ipc:") && len(raw) == 0 {
		return provider.Transcript{}, true, errors.New("IPC 会话状态已变化，请刷新历史")
	}
	if len(raw) == 0 {
		if err != nil {
			return provider.Transcript{}, true, err
		}
		return provider.Transcript{}, false, nil
	}
	if err != nil {
		return provider.Transcript{}, true, err
	}
	state := decodeObject(raw)
	turns, complete, err := stateTurns(state)
	if err != nil {
		return provider.Transcript{}, true, err
	}
	end := len(turns)
	if cursor != "" {
		if !strings.HasPrefix(cursor, "ipc:") {
			return provider.Transcript{}, false, nil
		}
		boundary, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, "ipc:"))
		if e != nil {
			return provider.Transcript{}, true, e
		}
		found := false
		for i, t := range turns {
			if stringValue(t["id"]) == string(boundary) {
				end = i
				found = true
				break
			}
		}
		if !found {
			return provider.Transcript{}, true, errors.New("IPC 历史边界已过期，请刷新")
		}
		if end == 0 && !complete {
			ctx, stop := context.WithTimeout(ctx, 8*time.Second)
			defer stop()
			if _, err = b.request(ctx, "thread-follower-load-complete-history", map[string]any{"conversationId": id}, owner); err != nil {
				return provider.Transcript{}, true, err
			}
			// The owner broadcasts before acknowledging history completion. Wait for
			// the serialized state worker to apply it before resolving this boundary.
			for {
				b.mu.Lock()
				f = b.followed[id]
				updated := f != nil && !reflect.DeepEqual(f.state, raw)
				var ch <-chan struct{}
				if f != nil {
					ch = f.changed
				}
				b.mu.Unlock()
				if updated {
					return p.ipcTranscript(ctx, id, cursor)
				}
				select {
				case <-ch:
				case <-ctx.Done():
					return provider.Transcript{}, true, ctx.Err()
				}
			}
		}
	}
	anchor := historyBoundary(ctx)
	bounded, found, e := throughBoundary(turns[:end], anchor)
	if e != nil || !found {
		return provider.Transcript{}, true, errHistoryBoundary
	}
	end = len(bounded)
	start := max(0, end-provider.RecentTurns(ctx))
	out := provider.Transcript{Session: p.session(stateThread(state)), Turns: []provider.Turn{}, StreamEpoch: epoch, StreamCursor: boundary}
	if anchor == nil {
		out.Queue = p.messageQueue(ctx, id, cursor, acceptedQueueMessages(turns))
	}
	if (start > 0 || !complete) && end > 0 {
		out.NextCursor = "ipc:" + base64.RawURLEncoding.EncodeToString([]byte(stringValue(turns[start]["id"])))
	}
	visible, next, partial := itemWindow(bounded[start:end], provider.HistoryItems(ctx), id, cursor, out.NextCursor, true)
	out.NextCursor, out.PartialTurn = next, partial
	if err = provider.ObserveTranscript(ctx, out); err != nil {
		return out, true, err
	}
	for _, t := range visible {
		raw := rawJSON(t)
		normalized := normalizeTurn(raw)
		p.attachments(ctx, id, out.Session.CWD, &normalized, raw)
		out.Turns = append(out.Turns, normalized)
		out.Session.RecencyAt = max(out.Session.RecencyAt, provider.ConversationRecency([]provider.Turn{normalized}))
		progress := out
		progress.Queue = nil // Do not repeat the queue for every history text part.
		if err = provider.EmitTranscriptTurn(ctx, progress, normalized); err != nil {
			return out, true, err
		}
	}
	if anchor == nil && end == len(turns) && len(out.Turns) > 0 && out.Turns[len(out.Turns)-1].Status == "inProgress" {
		out.Session.ActiveTurnID = out.Turns[len(out.Turns)-1].ID
	}
	return out, true, nil
}

func (b *appIPC) register(id string, result json.RawMessage, created bool) {
	response := decodeObject(result)
	metadata := object(response["thread"])
	if metadata == nil {
		metadata = map[string]any{"id": id}
	}
	if metadata["createdAt"] == nil {
		metadata["createdAt"] = float64(time.Now().Unix())
	}
	if metadata["updatedAt"] == nil {
		metadata["updatedAt"] = metadata["createdAt"]
	}
	b.mu.Lock()
	delete(b.unreachableOwners, id)
	if b.owned[id] == nil {
		b.owned[id] = &ownedThread{pubMu: make(chan struct{}, 1), metadata: metadata, settings: response, followers: map[string]bool{}, loaded: created, turns: []map[string]any{}, announced: !created, sent: map[string]bool{}}
	}
	b.mu.Unlock()
}
func (b *appIPC) markDirty(id string) {
	select {
	case b.dirty <- id:
	case <-b.ctx.Done():
	default:
	}
}
func (b *appIPC) publisher() {
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	type jobResult struct {
		id  string
		err error
	}
	results := make(chan jobResult, 4)
	pending, active := map[string]bool{}, map[string]bool{}
	policies, next := map[string]*readretry.Backoff{}, map[string]time.Time{}
	for {
		select {
		case id := <-b.dirty:
			pending[id] = true
		case result := <-results:
			delete(active, result.id)
			if result.err == nil {
				delete(policies, result.id)
				delete(next, result.id)
				continue
			}
			policy := policies[result.id]
			if policy == nil {
				policy = &readretry.Backoff{}
				policies[result.id] = policy
			}
			next[result.id] = time.Now().Add(policy.Next(result.err))
			pending[result.id] = true
			b.p.mu.Lock()
			b.p.lastError = result.err.Error()
			b.p.mu.Unlock()
		case now := <-ticker.C:
			for id := range pending {
				if len(active) >= 4 {
					break
				}
				if active[id] || now.Before(next[id]) {
					continue
				}
				delete(pending, id)
				active[id] = true
				go func(id string) {
					ctx, stop := context.WithTimeout(b.ctx, 5*time.Second)
					err := b.publish(ctx, id, false)
					stop()
					select {
					case results <- jobResult{id, err}:
					case <-b.ctx.Done():
					}
				}(id)
			}
		case <-b.ctx.Done():
			return
		}
	}
}
func (b *appIPC) loadOwned(ctx context.Context, id string, complete bool) error {
	b.mu.Lock()
	o := b.owned[id]
	if o == nil {
		b.mu.Unlock()
		return errors.New("no-client-found")
	}
	loaded, version := o.loaded, o.version
	metadata := decodeObject(rawJSON(o.metadata))
	cursor := stringValue(metadata["_olderCursor"])
	b.mu.Unlock()
	if loaded && (!complete || cursor == "") {
		return nil
	}
	if !loaded {
		var meta struct{ Thread map[string]any }
		if err := b.p.call(ctx, "thread/read", map[string]any{"threadId": id}, &meta); err != nil {
			return err
		}
		if meta.Thread == nil {
			return errors.New("Codex history missing thread metadata")
		}
		metadata, cursor = meta.Thread, ""
	}
	seen := map[string]bool{}
	first := !loaded
	for {
		if seen[cursor] {
			return errors.New("Codex 历史返回重复游标")
		}
		seen[cursor] = true
		var page struct {
			Data       []map[string]any
			NextCursor string
		}
		params := map[string]any{"threadId": id, "limit": 30, "sortDirection": "desc", "itemsView": "full", "itemSnapshot": "latest"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := b.p.call(ctx, "thread/turns/list", params, &page); err != nil {
			return err
		}
		if page.NextCursor != "" && seen[page.NextCursor] {
			return errors.New("Codex 历史返回重复游标")
		}
		for i, j := 0, len(page.Data)-1; i < j; i, j = i+1, j-1 {
			page.Data[i], page.Data[j] = page.Data[j], page.Data[i]
		}
		b.mu.Lock()
		if b.owned[id] != o {
			b.mu.Unlock()
			return errors.New("Codex 会话持有者已变化")
		}
		all := append([]map[string]any(nil), page.Data...)
		positions := map[string]int{}
		for i, t := range all {
			positions[stringValue(t["id"])] = i
		}
		for _, t := range o.turns {
			tid := stringValue(t["id"])
			// The initial page is authoritative except for notifications newer than
			// its read boundary. Older-page reads retain all already committed turns.
			if first && o.turnVersions[tid] <= version {
				continue
			}
			if i, ok := positions[tid]; ok {
				all[i] = t
			} else {
				positions[tid] = len(all)
				all = append(all, t)
			}
		}
		if len(rawJSON(all)) > 24<<20 {
			b.mu.Unlock()
			return errors.New("IPC 历史超过单次安全大小，请在原 App 查看更早历史")
		}
		meta := decodeObject(rawJSON(metadata))
		for _, key := range []string{"name", "updatedAt"} {
			if value := o.metadata[key]; value != nil && (o.version > version || !first) {
				meta[key] = value
			}
		}
		meta["_olderCursor"] = page.NextCursor
		// Commit content with its next-page cursor. A later page failure resumes
		// here instead of repeatedly downloading the successful prefix.
		o.metadata, o.turns, o.loaded = meta, all, true
		b.mu.Unlock()
		b.markDirty(id)
		if !complete || page.NextCursor == "" {
			return nil
		}
		cursor, first = page.NextCursor, false
	}
}
func (b *appIPC) publish(ctx context.Context, id string, complete bool) error {
	b.mu.Lock()
	o := b.owned[id]
	has := o != nil && len(o.followers) > 0
	b.mu.Unlock()
	if !has {
		return nil
	}
	select {
	case o.pubMu <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-o.pubMu }()
	if err := b.loadOwned(ctx, id, complete); err != nil {
		return err
	}
	b.mu.Lock()
	o = b.owned[id]
	if o == nil {
		b.mu.Unlock()
		return nil
	}
	revision := o.revision + 1
	subscribers := o.subscribers
	targets := []string{}
	for target := range o.followers {
		targets = append(targets, target)
	}
	state := b.ownerState(id, o)
	change := map[string]any{"type": "snapshot", "revision": revision, "conversationState": state}
	if o.published != nil {
		patches := []any{}
		diffState([]any{}, o.published, state, &patches)
		if len(patches) == 0 {
			b.mu.Unlock()
			return nil
		}
		change = map[string]any{"type": "patches", "baseRevision": o.revision, "revision": revision, "patches": patches, "acceptedTextChanges": []any{}}
	}
	b.mu.Unlock()
	if err := b.broadcast(ctx, "thread-stream-state-changed", map[string]any{"conversationId": id, "hostId": "local", "change": change}, targets); err != nil {
		return err
	}
	b.mu.Lock()
	o.revision = revision
	if o.subscribers == subscribers {
		o.published = state
	}
	b.mu.Unlock()
	return nil
}
func (b *appIPC) ownerState(id string, o *ownedThread) map[string]any {
	// Called under b.mu; return independent JSON values for the writer goroutine.
	metadata := decodeObject(rawJSON(o.metadata))
	settings := decodeObject(rawJSON(o.settings))
	turns := []any{}
	entities := map[string]any{}
	entries := []any{}
	active := false
	for _, t := range o.turns {
		t = decodeObject(rawJSON(t))
		for _, value := range array(t["items"]) {
			item := object(value)
			switch item["type"] {
			case "userMessage":
				item["content"] = ipcInput(array(item["content"]))
			case "steeringUserMessage":
				item["input"] = ipcInput(array(item["input"]))
			}
		}
		turnID := stringValue(t["id"])
		key := "turn:" + turnID
		done := t["status"] != "inProgress"
		active = activeOwnedTurn(o) != ""
		completed := map[string]bool{}
		for _, i := range array(t["items"]) {
			if done {
				completed[stringValue(object(i)["id"])] = true
			}
		}
		entities[key] = map[string]any{"turnId": turnID, "status": t["status"], "items": t["items"], "params": map[string]any{"threadId": id, "input": []any{}, "turnTrigger": "composer"}, "permissionParamsSource": "inferred", "completedItemIds": completed, "itemsPagination": map[string]any{"olderCursor": nil, "isLoadingOlder": false, "hasLoadedOldest": true}, "localMetadata": map[string]any{}, "hookRuns": []any{}, "mcpAppModelContextAttachments": []any{}, "error": t["error"], "diff": nil, "durationMs": t["durationMs"], "turnStartedAtMs": secondsMillis(t["startedAt"]), "firstTurnWorkItemStartedAtMs": nil, "finalAssistantStartedAtMs": nil, "requestedReasoningEffort": nil}
		entries = append(entries, map[string]any{"key": key, "value": key})
	}
	requests := []any{}
	b.p.mu.Lock()
	for _, a := range b.p.pending {
		if a.route == "service" && a.approval.SessionID == id {
			requests = append(requests, map[string]any{"method": a.approval.Method, "requestId": json.RawMessage(a.requestID), "id": json.RawMessage(a.requestID), "params": a.approval.Params})
		}
	}
	b.p.mu.Unlock()
	complete := metadata["_olderCursor"] == nil || metadata["_olderCursor"] == ""
	olderBoundary := map[string]any{"status": "exhausted", "boundaryId": "tail:0:older"}
	if !complete {
		var oldest any
		if len(o.turns) > 0 {
			oldest = o.turns[0]["id"]
		}
		handle := map[string]any{"cursor": metadata["_olderCursor"], "oldestLoadedTurnId": oldest}
		olderBoundary = map[string]any{"status": "available", "boundaryId": "tail:0:older", "handle": handle, "progressKey": string(rawJSON([]any{metadata["_olderCursor"], oldest}))}
	}
	roots := []any{}
	if cwd := stringValue(metadata["cwd"]); cwd != "" {
		roots = append(roots, cwd)
	}
	permissions := map[string]any{"runtimeWorkspaceRoots": roots, "approvalPolicy": settings["approvalPolicy"], "approvalsReviewer": settings["approvalsReviewer"], "sandboxPolicy": settings["sandbox"], "activePermissionProfile": settings["activePermissionProfile"]}
	for _, key := range []string{"approvalPolicy", "approvalsReviewer", "sandboxPolicy", "activePermissionProfile"} {
		if v := object(settings["threadSettings"])[key]; v != nil {
			permissions[key] = v
		}
	}
	model := settings["model"]
	if model == nil {
		model = metadata["model"]
	}
	collaboration := map[string]any{"mode": "default", "settings": map[string]any{"model": model, "reasoning_effort": settings["reasoningEffort"], "developer_instructions": nil}}
	if value := object(settings["threadSettings"])["collaborationMode"]; value != nil {
		collaboration = object(value)
	}
	latest := map[string]any{"cwd": metadata["cwd"], "model": model, "effort": settings["reasoningEffort"], "disabledPluginIds": []any{}, "collaborationMode": collaboration}
	for k, v := range permissions {
		latest[k] = v
	}
	now := time.Now().UnixMilli()
	createdAt, updatedAt := any(now), any(now)
	if value, ok := metadata["createdAt"].(float64); ok {
		createdAt = int64(value) * 1000
	}
	if value, ok := metadata["updatedAt"].(float64); ok {
		updatedAt = int64(value) * 1000
	}
	status := "idle"
	if active {
		status = "active"
	}
	result := map[string]any{"id": id, "hostId": "local", "sessionId": id, "title": metadata["name"], "cwd": metadata["cwd"], "rolloutPath": metadata["path"], "source": metadata["source"], "originator": metadata["originator"], "threadSource": "user", "threadStartKind": "default", "threadRuntimeStatus": map[string]any{"type": status}, "resumeState": "resumed", "historyMode": "paginated", "requests": requests, "turns": turns,
		"turnHistory":     map[string]any{"kind": "canonical", "history": map[string]any{"entitiesByKey": entities, "generation": 0, "isComplete": complete, "islands": []any{map[string]any{"id": "tail:0", "entries": entries, "olderBoundary": olderBoundary, "newerBoundary": map[string]any{"status": "exhausted", "boundaryId": "tail:0:newer"}}}}},
		"turnsPagination": map[string]any{"olderCursor": metadata["_olderCursor"], "isLoadingOlder": false, "hasLoadedOldest": complete}, "paginatedHistory": map[string]any{"itemsBackwardsCursor": nil}, "latestModel": model, "latestReasoningEffort": settings["reasoningEffort"], "latestCollaborationMode": collaboration, "latestThreadSettings": latest, "currentPermissions": permissions, "modelProvider": settings["modelProvider"], "mode": "default", "createdAt": createdAt, "updatedAt": updatedAt, "recencyAt": updatedAt, "hasUnreadTurn": false, "unreadMessageCount": 0, "gitInfo": metadata["gitInfo"], "environments": []any{map[string]any{"environmentId": "local", "cwd": metadata["cwd"], "runtimeWorkspaceRoots": roots}}, "environmentSelectionEvidence": map[string]any{"source": "live", "updatedAt": float64(now) / 1000}, "workspaceKind": "projectless", "workspaceBrowserRoot": metadata["cwd"], "daybreakEnabled": nil, "threadGoal": nil, "threadGoalResumeConfirmation": nil, "latestTokenUsageInfo": nil, "latestTurnStartMessageId": nil, "previousTurnModel": nil, "forkedFromId": metadata["forkedFromId"], "parentThreadId": metadata["parentThreadId"], "agentNickname": metadata["agentNickname"], "projectlessOutputDirectory": nil, "shellEnvironmentPolicy": nil, "boundedCloudHistory": nil, "canonicalVoiceHistory": nil}
	return result
}

// Mirror native notifications in memory, avoiding a history RPC for each token.
func (b *appIPC) observe(m rpc.Message) {
	var v struct {
		ThreadID, TurnID, ItemID, Delta string
		Turn, Item, ThreadSettings      map[string]any
		Name                            string
		SummaryIndex                    int
	}
	if json.Unmarshal(m.Params, &v) != nil || v.ThreadID == "" {
		return
	}
	b.mu.Lock()
	o := b.owned[v.ThreadID]
	if o == nil {
		b.mu.Unlock()
		return
	}
	if m.Method == "thread/closed" {
		delete(b.owned, v.ThreadID)
		b.mu.Unlock()
		return
	}
	o.version++
	if m.Method == "thread/settings/updated" && v.ThreadSettings != nil {
		o.settings["threadSettings"] = v.ThreadSettings
		o.settings["model"] = v.ThreadSettings["model"]
		o.settings["reasoningEffort"] = v.ThreadSettings["effort"]
	}
	if m.Method == "thread/name/updated" {
		o.metadata["name"] = v.Name
	}
	announce := !o.announced && (m.Method == "turn/started" || m.Method == "turn/completed")
	if announce && m.Method == "turn/completed" {
		o.announced = true
	}
	turnID := v.TurnID
	if strings.HasPrefix(m.Method, "turn/") {
		turnID = stringValue(v.Turn["id"])
	}
	if turnID != "" {
		if o.turnVersions == nil {
			o.turnVersions = map[string]uint64{}
		}
		o.turnVersions[turnID] = o.version
		var t map[string]any
		for _, item := range o.turns {
			if item["id"] == turnID {
				t = item
				break
			}
		}
		if t == nil {
			t = map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}
			o.turns = append(o.turns, t)
		}
		switch m.Method {
		case "turn/started", "turn/completed":
			for k, value := range v.Turn {
				if k != "items" {
					t[k] = value
				}
			}
			if m.Method == "turn/completed" && t["status"] == nil {
				t["status"] = "completed"
			}
		case "item/started", "item/completed":
			items := array(t["items"])
			found := false
			for i, item := range items {
				if object(item)["id"] == v.Item["id"] {
					items[i] = v.Item
					found = true
					break
				}
			}
			if !found {
				items = append(items, v.Item)
			}
			t["items"] = items
		case "item/reasoning/summaryTextDelta":
			for _, item := range array(t["items"]) {
				i := object(item)
				if i["id"] != v.ItemID || v.SummaryIndex < 0 || v.SummaryIndex > 1024 {
					continue
				}
				summary := array(i["summary"])
				for len(summary) <= v.SummaryIndex {
					summary = append(summary, "")
				}
				summary[v.SummaryIndex] = stringValue(summary[v.SummaryIndex]) + v.Delta
				i["summary"] = summary
			}
		case "item/agentMessage/delta":
			for _, item := range array(t["items"]) {
				i := object(item)
				if i["id"] == v.ItemID {
					i["text"] = stringValue(i["text"]) + v.Delta
					break
				}
			}
		}
	}
	idle := m.Method == "turn/completed"
	if idle || m.Method == "turn/started" {
		o.metadata["updatedAt"] = float64(time.Now().Unix())
	}
	if idle && len(o.turns) > 60 {
		for _, t := range o.turns[:len(o.turns)-30] {
			delete(o.turnVersions, stringValue(t["id"]))
		}
		o.turns = o.turns[len(o.turns)-30:]
		o.loaded = false
	}
	b.mu.Unlock()
	if announce {
		go func() {
			_ = b.broadcast(b.ctx, "thread-unarchived", map[string]any{"hostId": "local", "conversationId": v.ThreadID}, nil)
		}()
	}
	b.markDirty(v.ThreadID)
	if idle {
		go b.drainQueue(v.ThreadID)
	}
}

var _ codexipc.Handler = (*appIPC)(nil)

func secondsMillis(value any) any {
	if n, ok := value.(float64); ok {
		return int64(n) * 1000
	}
	return nil
}

// Immer updates keep IPC token output proportional to the changed fields.
// Paths retain string keys and numeric array indices for native App consumers.
func diffState(path []any, old, next any, patches *[]any) {
	if reflect.DeepEqual(old, next) {
		return
	}
	a, aok := old.(map[string]any)
	b, bok := next.(map[string]any)
	if aok && bok {
		for key := range a {
			if _, exists := b[key]; !exists {
				*patches = append(*patches, map[string]any{"op": "remove", "path": ipcPath(path, key)})
			}
		}
		for key, value := range b {
			pointer := ipcPath(path, key)
			if previous, exists := a[key]; exists {
				diffState(pointer, previous, value, patches)
			} else {
				*patches = append(*patches, map[string]any{"op": "add", "path": pointer, "value": value})
			}
		}
		return
	}
	aa, aok := old.([]any)
	bb, bok := next.([]any)
	if aok && bok && len(aa) == len(bb) {
		for i, value := range bb {
			diffState(ipcPath(path, i), aa[i], value, patches)
		}
		return
	}
	if a, ok := old.([]any); ok {
		if b, ok := next.([]any); ok {
			for i := 0; i < min(len(a), len(b)); i++ {
				diffState(ipcPath(path, i), a[i], b[i], patches)
			}
			for i := len(a) - 1; i >= len(b); i-- {
				*patches = append(*patches, map[string]any{"op": "remove", "path": ipcPath(path, i)})
			}
			for i := len(a); i < len(b); i++ {
				*patches = append(*patches, map[string]any{"op": "add", "path": ipcPath(path, i), "value": b[i]})
			}
			return
		}
	}
	*patches = append(*patches, map[string]any{"op": "replace", "path": path, "value": next})
}
func ipcPath(path []any, part any) []any {
	return append(append([]any{}, path...), part)
}
func jsonPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
