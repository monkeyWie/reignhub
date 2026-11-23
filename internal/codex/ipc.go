package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"sync"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/google/uuid"
	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

// Outgoing owner state remains v11. Followed conversations independently accept
// the verified v11/v13 snapshots and Immer patches; unknown schemas fail closed.
var ipcVersions = map[string]int{
	"thread-owner-discovery": 1, "thread-stream-following-changed": 1,
	"thread-stream-following-status-requested": 1, "thread-stream-state-changed": 11,
	"ipc-connection-reset": 1, "thread-unarchived": 1, "thread-queued-followups-changed": 3,
	"thread-follower-start-turn": 2, "thread-follower-steer-turn": 1,
	"thread-follower-interrupt-turn": 4, "thread-follower-load-complete-history": 1,
	"thread-follower-command-approval-decision": 1, "thread-follower-file-approval-decision": 1,
	"thread-follower-submit-user-input": 1, "thread-follower-set-queued-follow-ups-state": 3,
	"thread-follower-remove-queued-message": 1, "thread-follower-clear-queued-messages": 1,
	"thread-follower-permissions-request-approval-response": 1, "thread-follower-submit-mcp-server-elicitation-response": 1,
	"thread-follower-compact-thread":      1,
	"thread-follower-read-model-settings": 1, "thread-follower-update-thread-settings": 2,
}

type followedThread struct {
	directory                              bool
	owner                                  string
	version                                int
	revision                               int64
	state                                  json.RawMessage // Immutable once published. Readers may retain a revision.
	err                                    error
	changed                                chan struct{}
	queue                                  *provider.MessageQueue
	serverQueue                            *serverQueue
	queueRevision                          uint64
	snapshots                              uint64
	refresh                                *followerRefresh
	refreshAfter                           time.Time
	refreshErr                             error
	refreshGeneration, refreshedGeneration uint64
	accepted                               map[string]bool
}
type ownedSend struct {
	result      json.RawMessage
	fingerprint string
	pending     bool
	uncertain   bool
}
type ownedThread struct {
	metadata      map[string]any
	pubMu         chan struct{}
	published     map[string]any
	subscribers   uint64
	version       uint64
	turnVersions  map[string]uint64
	turns         []map[string]any
	settings      map[string]any
	followers     map[string]bool
	revision      int64
	loaded        bool
	announced     bool
	queue         []map[string]any
	serverQueue   *serverQueue
	queueRevision uint64
	draining      bool
	uncertain     bool
	blocked       string
	sent          map[string]bool
	inflight      string
	sends         map[string]*ownedSend
}
type appIPC struct {
	p                *Provider
	home             string
	queueFiles       []string
	mu               sync.Mutex
	conn             *codexipc.Client
	connectErr       error
	ownerUnavailable bool
	followed         map[string]*followedThread
	owned            map[string]*ownedThread
	ctx              context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	dirty            chan string
}

func newAppIPC(p *Provider) *appIPC {
	ctx, cancel := context.WithCancel(context.Background())
	return &appIPC{p: p, home: codexProjectHome(), queueFiles: vscodeQueueFiles(), followed: map[string]*followedThread{}, owned: map[string]*ownedThread{}, ctx: ctx, cancel: cancel, done: make(chan struct{}), dirty: make(chan string, 128)}
}
func (b *appIPC) client() *codexipc.Client { b.mu.Lock(); defer b.mu.Unlock(); return b.conn }
func (b *appIPC) connectionError() error   { b.mu.Lock(); defer b.mu.Unlock(); return b.connectErr }
func (b *appIPC) needsSharedDesktop() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ownerUnavailable
}

func (b *appIPC) connect(ctx context.Context) error {
	c, err := codexipc.Dial(ctx, codexipc.Endpoint(b.home), b)
	b.mu.Lock()
	b.connectErr = err
	b.mu.Unlock()
	if err != nil {
		return err
	}
	if b.ctx.Err() != nil {
		_ = c.Close()
		return b.ctx.Err()
	}
	b.mu.Lock()
	b.conn = c
	for _, o := range b.owned {
		o.followers = map[string]bool{}
		o.published = nil
		o.subscribers++
	}
	ids := make([]string, 0, len(b.followed))
	for id, f := range b.followed {
		f.state = nil
		b.invalidateQueue(id, f)
		f.owner = ""
		f.err = nil
		b.signal(f)
		ids = append(ids, id)
	}
	b.mu.Unlock()
	for _, id := range ids {
		b.scheduleFollow(id)
	}
	b.mu.Lock()
	owned := []string{}
	for id := range b.owned {
		owned = append(owned, id)
	}
	b.mu.Unlock()
	for _, id := range owned {
		_ = b.broadcast(ctx, "thread-stream-following-status-requested", map[string]any{"hostId": "local", "conversationId": id}, nil)
	}
	return nil
}
func (b *appIPC) run() {
	defer close(b.done)
	go b.publisher()
	for {
		c := b.client()
		if c != nil {
			select {
			case <-c.Done():
			case <-b.ctx.Done():
				return
			}
		}
		b.mu.Lock()
		for id, f := range b.followed {
			f.owner = ""
			f.state = nil
			b.invalidateQueue(id, f)
			f.err = errors.New("Codex IPC 已断开")
			b.signal(f)
		}
		b.mu.Unlock()
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(time.Second):
		}
		ctx, stop := context.WithTimeout(b.ctx, 3*time.Second)
		err := b.connect(ctx)
		stop()
		if err != nil {
			continue
		}
	}
}
func (b *appIPC) close() {
	b.cancel()
	if c := b.client(); c != nil {
		_ = c.Close()
	}
	<-b.done
}
func (b *appIPC) signal(f *followedThread) {
	if f.changed != nil {
		close(f.changed)
	}
	f.changed = make(chan struct{})
}
func (b *appIPC) owns(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.owned[id]
	return ok && b.backendAlive()
}
func (b *appIPC) backendAlive() bool {
	b.p.mu.Lock()
	defer b.p.mu.Unlock()
	return b.p.client != nil && b.p.client.Alive()
}
func ipcID(params json.RawMessage) (string, string) {
	var v struct{ ConversationID, HostID string }
	_ = json.Unmarshal(params, &v)
	return v.ConversationID, v.HostID
}
func (b *appIPC) CanHandle(m codexipc.Message) bool {
	version, ok := ipcVersions[m.Method]
	if !ok || version != m.Version || m.HostID != "" {
		return false
	}
	id, host := ipcID(m.Params)
	if host != "" && host != "local" {
		return false
	}
	return id != "" && b.owns(id)
}
func (b *appIPC) request(ctx context.Context, method string, params any, owner string) (codexipc.Message, error) {
	c := b.client()
	if c == nil || !c.Alive() {
		return codexipc.Message{}, errors.New("Codex IPC 尚未连接")
	}
	return c.Request(ctx, method, ipcVersions[method], params, owner)
}
func (b *appIPC) broadcast(ctx context.Context, method string, params any, targets []string) error {
	c := b.client()
	if c == nil || !c.Alive() {
		return errors.New("Codex IPC 尚未连接")
	}
	return c.Broadcast(ctx, method, ipcVersions[method], params, targets)
}
func (b *appIPC) follow(ctx context.Context, id string) (string, error) {
	return b.followFor(ctx, id, false)
}
func (b *appIPC) followFor(ctx context.Context, id string, directory bool) (string, error) {
	if b.owns(id) {
		return "", nil
	}
	c := b.client()
	if c == nil || !c.Alive() {
		return "", errors.New("Codex App IPC 不可用，请确认 App 正在运行")
	}
	attempt, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	response, err := b.request(attempt, "thread-owner-discovery", map[string]any{"hostId": "local", "conversationId": id}, "")
	if errors.Is(err, codexipc.RemoteError("no-client-found")) {
		return "", nil
	}
	if err != nil {
		var unknown *codexipc.UnknownOutcome
		if errors.As(err, &unknown) {
			// This RPC only locates an owner. Its timeout says nothing about
			// delivery of the user's message, which has not been submitted.
			return "", &ownerDiscoveryError{cause: unknown.Err}
		}
		return "", fmt.Errorf("会话归属发现失败: %w", err)
	}
	if response.HandledBy == "" || response.HandledBy == c.ID() {
		return "", errors.New("IPC 返回无效会话持有者")
	}
	b.mu.Lock()
	f := b.followed[id]
	if f == nil {
		if len(b.followed) >= 128 {
			b.mu.Unlock()
			return "", errors.New("同时跟随的 Codex 会话过多")
		}
		f = &followedThread{changed: make(chan struct{}), directory: directory}
		b.followed[id] = f
	}
	if !directory {
		f.directory = false
	}
	if f.owner != response.HandledBy {
		f.owner = response.HandledBy
		f.state = nil
		b.invalidateQueue(id, f)
		f.revision = 0
		f.err = nil
		b.signal(f)
	}
	owner := f.owner
	b.mu.Unlock()
	err = b.broadcast(attempt, "thread-stream-following-changed", map[string]any{"hostId": "local", "conversationId": id, "following": true}, []string{owner})
	return owner, err
}
func (b *appIPC) scheduleFollow(id string) {
	go func() { ctx, stop := context.WithTimeout(b.ctx, 4*time.Second); defer stop(); _, _ = b.follow(ctx, id) }()
}
func (b *appIPC) waitState(ctx context.Context, id, owner string) (json.RawMessage, error) {
	ctx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	for {
		b.mu.Lock()
		f := b.followed[id]
		if f == nil || f.owner != owner {
			b.mu.Unlock()
			return nil, errors.New("Codex 会话持有者已变化")
		}
		state, err, ch := f.state, f.err, f.changed
		b.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if len(state) > 0 {
			return state, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("等待 Codex App 会话状态: %w", ctx.Err())
		case <-ch:
		}
	}
}
func (b *appIPC) Broadcast(m codexipc.Message) {
	if m.Method == "ipc-connection-reset" && m.Version == 1 {
		if c := b.client(); c != nil {
			_ = c.Close()
		}
		return
	}
	if m.Method == "client-status-changed" {
		var v struct{ ClientID, Status string }
		if json.Unmarshal(m.Params, &v) != nil || v.Status != "disconnected" {
			return
		}
		b.mu.Lock()
		for id, f := range b.followed {
			if f.owner == v.ClientID {
				f.owner = ""
				f.state = nil
				b.invalidateQueue(id, f)
				f.err = errors.New("Codex 会话持有者已断开")
				b.signal(f)
			}
		}
		for _, o := range b.owned {
			delete(o.followers, v.ClientID)
		}
		b.mu.Unlock()
		return
	}
	id, host := ipcID(m.Params)
	if id == "" || host != "local" {
		return
	}
	if m.Method == "thread-queued-followups-changed" {
		b.receiveQueue(m, id)
		return
	}
	if m.Method == "thread-stream-following-changed" && m.Version == 1 {
		var v struct{ Following bool }
		_ = json.Unmarshal(m.Params, &v)
		b.mu.Lock()
		o := b.owned[id]
		if o != nil {
			if v.Following {
				o.followers[m.Source] = true
				o.published = nil
				o.subscribers++
			} else {
				delete(o.followers, m.Source)
			}
		}
		b.mu.Unlock()
		if o != nil && v.Following {
			b.markDirty(id)
		}
		return
	}
	if m.Method == "thread-stream-following-status-requested" && m.Version == 1 {
		b.mu.Lock()
		f := b.followed[id]
		if f != nil {
			// An owner can change while the old IPC client stays connected.
			// Treat this as a hint to rediscover, never as authority to route
			// commands or accept state from an unsolicited owner.
			f.err = errors.New("Codex 会话流请求重新订阅")
			f.refreshGeneration++
			b.signal(f)
		}
		b.mu.Unlock()
		if f != nil {
			b.p.stream().log.Publish(provider.StreamEvent{Kind: "snapshot", SessionID: id})
		}
		return
	}
	if m.Method != "thread-stream-state-changed" {
		return
	}
	b.mu.Lock()
	f := b.followed[id]
	if f == nil || f.owner != m.Source {
		b.mu.Unlock()
		return
	}
	if m.Version != 11 && m.Version != 13 {
		f.err = fmt.Errorf("暂不支持 Codex IPC 状态版本 %d（当前支持 11、13）", m.Version)
		b.signal(f)
		b.mu.Unlock()
		return
	}
	var v struct {
		Change struct {
			Type                   string
			Revision, BaseRevision int64
			ConversationState      json.RawMessage
			Patches                json.RawMessage
		}
	}
	if json.Unmarshal(m.Params, &v) != nil {
		b.mu.Unlock()
		return
	}
	change := v.Change
	rebased := change.Type == "snapshot" && change.Revision < f.revision
	old := f.state
	var next json.RawMessage
	var err error
	switch change.Type {
	case "snapshot":
		// Snapshots establish a new revision base. The owner's backend may
		// reconnect and reset its counter without replacing the IPC client.
		// Only patches are ordered against the previous revision.
		next = change.ConversationState
	case "patches":
		if change.Revision == f.revision {
			b.mu.Unlock()
			return
		}
		if len(f.state) == 0 || f.version != m.Version || change.BaseRevision != f.revision || change.Revision <= change.BaseRevision {
			err = errors.New("IPC 状态游标不连续")
		} else {
			var patch jsonpatch.Patch
			patch, err = decodeIPCPatches(change.Patches)
			if err == nil {
				next, err = applyIPCPatches(f.state, patch)
			}
		}
	default:
		b.mu.Unlock()
		return
	}
	var accepted map[string]bool
	var state map[string]any
	var turns []map[string]any
	if err == nil {
		if len(next) > codexipc.MaxFrame || json.Unmarshal(next, &state) != nil || stringValue(state["id"]) != id {
			err = errors.New("IPC 会话状态结构无效")
		} else {
			turns, _, err = stateTurns(state)
			if err == nil {
				accepted = acceptedQueueMessages(turns)
			}
		}
	}
	if err != nil {
		f.err = err
		f.refreshGeneration++
		b.signal(f)
		b.mu.Unlock()
		// Wake existing watches immediately. Their next read coalesces one
		// bounded re-follow and waits for its fresh snapshot before trusting it.
		b.p.stream().log.Publish(provider.StreamEvent{Kind: "snapshot", SessionID: id})
		return
	}
	f.state = next
	f.revision = change.Revision
	f.version = m.Version
	f.err = nil
	if change.Type == "snapshot" {
		f.refreshErr = nil
		f.refreshAfter = time.Time{}
		f.snapshots++ // An unchanged revision still acknowledges a fresh follow.
	}
	queueChanged := !maps.Equal(f.accepted, accepted)
	f.accepted = accepted
	b.signal(f)
	b.mu.Unlock()
	b.p.observeIPCState(id, m.Source, old, state, turns)
	if queueChanged || rebased {
		b.p.stream().log.Publish(provider.StreamEvent{Kind: "snapshot", SessionID: id})
	}
}

func (p *Provider) appBridge() *appIPC { p.mu.Lock(); defer p.mu.Unlock(); return p.ipc }

// writeRoute never resumes a thread while another executor holds its writer
// lock, including discovery failure. Read/subscribe paths never call this.
func (p *Provider) writeRoute(ctx context.Context, id string) (*rpc.Client, string, error) {
	b := p.appBridge()
	if b == nil {
		c, err := p.threadClient(ctx, id)
		return c, "", err
	}
	if b.owns(id) {
		p.mu.Lock()
		c := p.client
		p.mu.Unlock()
		return c, "", nil
	}
	if !safeThreadID(id) {
		return nil, "", errors.New("无效 Codex 会话 ID")
	}
	owner, err := b.follow(ctx, id)
	var discovery *ownerDiscoveryError
	if err != nil && !errors.As(err, &discovery) {
		return nil, "", err
	}
	if owner != "" {
		return nil, owner, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	locked, err := codexipc.WriterLocked(filepath.Join(b.home, "thread-writer-locks", id+".lock"))
	if err != nil {
		return nil, "", fmt.Errorf("无法确认会话写锁: %w", err)
	}
	if locked {
		b.mu.Lock()
		b.ownerUnavailable = true
		b.mu.Unlock()
		if discovery != nil {
			return nil, "", discovery
		}
		return nil, "", errors.New("会话仍由其他 Codex 窗口持有，但 IPC 暂不可达；不会强行接管，请在原 App 打开后重试")
	}
	// Resume must acquire the official server's writer lock. If another
	// client acquired it after the probe, the resume fails before submission.
	c, err := p.threadClient(ctx, id)
	return c, "", err
}

type ownerDiscoveryError struct{ cause error }

func (e *ownerDiscoveryError) Error() string {
	return "操作尚未提交。暂时无法联系持有此会话的 Codex，请在原 Codex App 中继续，或在被控设备的 ReignHub 中重新接入 Codex 后重试。"
}

func (e *ownerDiscoveryError) Unwrap() error { return e.cause }

func safeThreadID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-') {
			return false
		}
	}
	return true
}
func (p *Provider) sendInput(ctx context.Context, id string, input []any, model string) error {
	input = ipcInput(input)
	c, owner, err := p.writeRoute(ctx, id)
	if err != nil {
		return err
	}
	if owner != "" {
		b := p.appBridge()
		if b == nil {
			return provider.ErrUnavailable
		}
		raw, err := b.waitState(ctx, id, owner)
		if err != nil {
			return err
		}
		var state map[string]any
		_ = json.Unmarshal(raw, &state)
		request := ipcTurnStart(state, id, input, model)
		_, err = b.request(ctx, "thread-follower-start-turn", map[string]any{"conversationId": id, "turnStart": map[string]any{"request": request, "context": map[string]any{"useAppServerPermissionDefault": true}}}, owner)
		return ipcWriteError("thread-follower-start-turn", err)
	}
	params := map[string]any{"threadId": id, "input": input}
	if model != "" {
		params["model"] = model
	}
	return p.threadWrite(ctx, c, "turn/start", params, nil)
}
func (p *Provider) steerInput(ctx context.Context, id, turn string, input []any) error {
	input = ipcInput(input)
	c, owner, err := p.writeRoute(ctx, id)
	if err != nil {
		return err
	}
	if owner != "" {
		b := p.appBridge()
		if b == nil {
			return provider.ErrUnavailable
		}
		raw, err := b.waitState(ctx, id, owner)
		if err != nil {
			return err
		}
		var state map[string]any
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
		// The controller may still show the previous active turn while its final
		// event is in flight. Route from the owner's current state before writing.
		turns, _, err := stateTurns(state)
		if err != nil {
			return err
		}
		if len(turns) == 0 || turns[len(turns)-1]["status"] != "inProgress" {
			return p.sendInput(ctx, id, input, "")
		}
		_, err = b.request(ctx, "thread-follower-steer-turn", ipcSteerParams(state, id, input), owner)
		return ipcWriteError("thread-follower-steer-turn", err)
	}
	return p.threadWrite(ctx, c, "turn/steer", map[string]any{"threadId": id, "expectedTurnId": turn, "input": input}, nil)
}

func ipcWriteError(method string, err error) error {
	var remote codexipc.RemoteError
	if !errors.As(err, &remote) {
		return err
	}
	// These owner rejections happen before submission. Arbitrary JavaScript
	// errors can occur after dispatch or persistence and do not prove failure.
	message := string(remote)
	if message == "no-client-found" || message == "no-handler-for-request" ||
		strings.HasPrefix(message, "Cannot steer conversation ") && strings.HasSuffix(message, " because its active turn already ended") {
		return err
	}
	return &codexipc.UnknownOutcome{Method: method, Err: err}
}

// App inputs are stricter than app-server inputs: its optimistic user-message
// renderer dereferences text_elements before the server canonicalizes the input.
// Normalize at this boundary for plain text, attachment references and steers.
// Preserve existing rich text elements and leave caller-owned values untouched.
func ipcInput(input []any) []any {
	var normalized []any
	_ = json.Unmarshal(rawJSON(input), &normalized)
	for _, value := range normalized {
		item := object(value)
		if item["type"] == "text" && item["text_elements"] == nil {
			item["text_elements"] = []any{}
		}
	}
	return normalized
}

// ipcRestoreMessage follows the App composer shape, not the app-server schema.
func ipcRestoreMessage(state map[string]any, input []any, id string) map[string]any {
	text := []string{}
	for _, item := range input {
		if v := decodeObject(rawJSON(item)); stringValue(v["type"]) == "text" {
			text = append(text, stringValue(v["text"]))
		}
	}
	prompt := strings.Join(text, "\n")
	roots := []any{}
	if cwd := stringValue(state["cwd"]); cwd != "" {
		roots = append(roots, cwd)
	}
	return map[string]any{"id": id, "text": prompt, "cwd": state["cwd"], "createdAt": time.Now().UnixMilli(), "context": map[string]any{"prompt": prompt, "addedFiles": []any{}, "fileAttachments": []any{}, "imageAttachments": []any{}, "ideContext": nil, "workspaceRoots": roots, "collaborationMode": state["latestCollaborationMode"], "turnTrigger": "composer"}}
}

func ipcTurnStart(state map[string]any, id string, input []any, model string) map[string]any {
	request := map[string]any{"threadId": id, "turnTrigger": "composer", "clientUserMessageId": uuid.NewString(), "input": input, "cwd": state["cwd"], "model": nil, "effort": nil, "collaborationMode": state["latestCollaborationMode"], "useAppServerPermissionDefault": true}
	// Preserve owner policy. Never supply a second profile-based permissions field.
	for _, source := range []string{"currentPermissions", "latestThreadSettings"} {
		if settings, ok := state[source].(map[string]any); ok {
			for _, key := range []string{"approvalPolicy", "approvalsReviewer", "sandboxPolicy"} {
				if v, ok := settings[key]; ok {
					request[key] = v
				}
			}
		}
	}
	if model != "" {
		request["model"] = model
		if cm := object(state["latestCollaborationMode"]); cm != nil {
			if settings := object(cm["settings"]); settings != nil {
				settings["model"] = model
			}
		}
	}
	return request
}

func ipcSteerParams(state map[string]any, id string, input []any) map[string]any {
	messageID := uuid.NewString()
	// The App restores a rejected steer from this composer snapshot.
	return map[string]any{"conversationId": id, "input": input, "clientUserMessageId": messageID, "restoreMessage": ipcRestoreMessage(state, input, messageID), "attachments": []any{}}
}
