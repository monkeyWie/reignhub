// Package agents adapts local agent runtimes into ReignHub's common provider API.
// Agent protocols stay on the host; controllers only receive presentation events.
package agents

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/monkeyWie/reignhub/pkg/appdb"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/monkeyWie/reignhub/internal/assets"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/pkg/persistence"
)

type runtimeAgent interface {
	controls(context.Context, string) ([]provider.ComposerControl, error)
	configure(context.Context, provider.ComposerChange) error
	Open(context.Context, string, string) (string, error)
	Prompt(context.Context, string, []provider.Attachment, []string, string) error
	Cancel(context.Context) error
	Steer(context.Context, string) error
	Models() []provider.Model
	Close() error
	Alive() bool
	Capabilities() provider.Capabilities
}
type session struct {
	provider.Transcript
	NativeID    string `json:"nativeId"`
	Imported    bool   `json:"imported,omitempty"`
	runtimeMu   sync.Mutex
	persisted   bool
	revision    string
	start       int
	replace     bool
	dirty       map[int]map[int]bool
	timer       *time.Timer
	runtime     runtimeAgent
	opening     bool
	configuring bool
	sending     bool
	removing    bool
	turn        string
	assistant   string
}
type sessionRef struct {
	Session  provider.Session `json:"session"`
	NativeID string           `json:"nativeId"`
	Imported bool             `json:"imported,omitempty"`
}
type pendingApproval struct {
	approval provider.Approval
	options  []permissionOption
	answer   chan string
	answered bool
	input    bool
}
type permissionOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type Provider struct {
	id              string
	def             config.Agent
	dir             string
	assets          *assets.Store
	life            context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	startMu         sync.Mutex
	persistMu       sync.Mutex
	removeMu        sync.Mutex
	removed         map[string]bool
	probe           runtimeAgent
	caps            provider.Capabilities
	discoverMu      sync.Mutex
	discovered      time.Time
	enabled, closed bool
	lastError       string
	sessions        map[string]*session
	approvals       map[string]*pendingApproval
	log             *provider.EventLog
}

func New(ctx context.Context, id string, def config.Agent, dir string, files *assets.Store) *Provider {
	life, cancel := context.WithCancel(ctx)
	return &Provider{id: id, def: def, dir: dir, assets: files, life: life, cancel: cancel, sessions: map[string]*session{}, approvals: map[string]*pendingApproval{}, log: provider.NewEventLog()}
}
func (p *Provider) path() string { return appdb.Path(p.dir) }
func (p *Provider) Status(context.Context) provider.Status {
	path, _ := resolveAgentExecutable(p.def)
	p.mu.Lock()
	defer p.mu.Unlock()
	s := provider.Status{ID: p.id, Name: p.def.Name, State: "unavailable", Installed: path != "", InstallPath: path, Capabilities: p.caps, LastError: p.lastError, Detail: "未安装"}
	if s.Name == "" {
		s.Name = p.id
	}
	if path != "" {
		s.Detail = "正在连接"
		if p.lastError != "" {
			s.Detail = "需要配置"
		}
	}
	if p.enabled && !p.closed && p.probe != nil && p.probe.Alive() {
		s.State = "ready"
		s.Detail = "已就绪"
		s.Running = true
		s.Controllable = true
	}
	return s
}

func (p *Provider) AutoConnect(ctx context.Context) error {
	s := p.Status(ctx)
	if s.Ready() || !s.Installed {
		return nil
	}
	return p.Start(ctx)
}
func (p *Provider) Start(ctx context.Context) error {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("Agent 已关闭")
	}
	if p.enabled && p.probe != nil && p.probe.Alive() {
		p.mu.Unlock()
		return nil
	}
	oldProbe := p.probe
	p.probe = nil
	p.mu.Unlock()
	if oldProbe != nil {
		_ = oldProbe.Close()
	}
	if _, err := resolveAgentExecutable(p.def); err != nil {
		return err
	}
	// Validate initialization before presenting the provider as ready. This probe
	// never starts a conversation or requests model output.
	rt, err := p.newRuntime(ctx, "")
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.lastError = err.Error()
		return err
	}
	if err := p.restoreDirectory(ctx); err != nil {
		_ = rt.Close()
		return err
	}
	p.probe = rt
	p.caps = rt.Capabilities()
	p.caps.SessionRemoval = "detach"
	p.enabled = true
	p.lastError = ""
	return nil
}
func (p *Provider) Close() error {
	p.cancel()
	p.startMu.Lock()
	defer p.startMu.Unlock()
	p.cancel()
	p.mu.Lock()
	p.closed = true
	runtimes := []runtimeAgent{}
	if p.probe != nil {
		runtimes = append(runtimes, p.probe)
		p.probe = nil
	}
	for _, s := range p.sessions {
		if s.timer != nil {
			s.timer.Stop()
			s.timer = nil
		}
		if s.runtime != nil {
			runtimes = append(runtimes, s.runtime)
			s.runtime = nil
		}
		s.sending = false
		if s.Session.ActiveTurnID != "" {
			s.Session.Status = "interrupted"
			s.Session.ActiveTurnID = ""
		}
		for i := range s.Turns {
			if s.Turns[i].Status == "inProgress" {
				s.Turns[i].Status = "interrupted"
				s.mark(i, -1)
			}
		}
	}
	p.mu.Unlock()
	var err error
	for _, rt := range runtimes {
		err = errors.Join(err, rt.Close())
	}
	for _, s := range p.sessionIDs() {
		err = errors.Join(err, p.persist(s))
	}
	return err
}
func (p *Provider) sessionIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := []string{}
	for id := range p.sessions {
		ids = append(ids, id)
	}
	return ids
}
func (p *Provider) ready() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled || p.closed {
		return provider.ErrUnavailable
	}
	return nil
}
func (p *Provider) Sessions(ctx context.Context, cursor string) (provider.SessionPage, error) {
	if err := p.ready(); err != nil {
		return provider.SessionPage{}, err
	}
	if cursor == "" {
		if err := p.discoverOnce(ctx); err != nil {
			return provider.SessionPage{}, err
		}
	}
	p.mu.Lock()
	rows := []provider.Session{}
	for _, s := range p.sessions {
		if p.removed[s.Session.ID] || s.removing || s.opening && !s.persisted {
			continue
		}
		rows = append(rows, s.Session)
	}
	p.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].UpdatedAt == rows[j].UpdatedAt {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].UpdatedAt > rows[j].UpdatedAt
	})
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 || n > len(rows) {
			return provider.SessionPage{}, errors.New("会话分页游标无效")
		}
		start = n
	}
	end := min(start+100, len(rows))
	page := provider.SessionPage{Sessions: rows[start:end]}
	if end < len(rows) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}
func (p *Provider) load(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[id]
	if s == nil || p.removed[id] || s.removing {
		return errors.New("会话不存在或已移除")
	}
	if s.Turns != nil {
		return nil
	}
	s.Turns = []provider.Turn{}
	return nil
}
func (p *Provider) Transcript(ctx context.Context, id, cursor string) (provider.Transcript, error) {
	if err := p.load(id); err != nil {
		return provider.Transcript{}, err
	}
	p.mu.Lock()
	s0 := p.sessions[id]
	needsHistory := s0.Imported && len(s0.Turns) == 0
	p.mu.Unlock()
	if needsHistory {
		if _, err := p.ensureRuntime(ctx, id); err != nil {
			return provider.Transcript{}, err
		}
	}
	if err := p.persist(id); err != nil {
		return provider.Transcript{}, err
	}
	db, err := appdb.Open(p.path())
	if err != nil {
		return provider.Transcript{}, err
	}
	defer db.Close()
	q := persistence.PageQuery{}
	if cursor != "" {
		var c persistence.PageCursor
		if _, err = fmt.Sscanf(cursor, "%d/%d", &c.Turn, &c.Item); err != nil {
			return provider.Transcript{}, errors.New("消息分页游标无效")
		}
		q.Before = &c
	}
	page, err := db.ReadPage(ctx, p.historyKey(id), q)
	if err != nil {
		return provider.Transcript{}, err
	}
	var t provider.Transcript
	raw, err := json.Marshal(page.Transcript)
	if err == nil {
		err = json.Unmarshal(raw, &t)
	}
	if c := page.Before; c != nil {
		t.NextCursor = fmt.Sprintf("%d/%d", c.Turn, c.Item)
	}
	return t, err
}
func (p *Provider) Create(ctx context.Context, o provider.CreateOptions) (provider.Session, error) {
	if err := p.ready(); err != nil {
		return provider.Session{}, err
	}
	cwd, err := filepath.Abs(o.CWD)
	if err != nil || strings.TrimSpace(o.CWD) == "" {
		return provider.Session{}, errors.New("请选择工作目录")
	}
	id := device.Random()[:24]
	now := time.Now().UnixMilli()
	s := &session{Transcript: provider.Transcript{Session: provider.Session{ID: id, ProviderID: p.id, Title: "新会话", CWD: cwd, Model: o.Model, SourceKind: "custom", CreatedAt: now, UpdatedAt: now}, Turns: []provider.Turn{}}, opening: true}
	p.mu.Lock()
	p.sessions[id] = s
	p.mu.Unlock()
	rt, err := p.newRuntime(ctx, id)
	native := ""
	if err == nil {
		native, err = rt.Open(ctx, cwd, "")
	}
	p.mu.Lock()
	s.opening = false
	if err == nil {
		s.runtime = rt
		s.NativeID = native
	} else {
		delete(p.sessions, id)
	}
	info := s.Session
	p.mu.Unlock()
	if err != nil {
		if rt != nil {
			rt.Close()
		}
		return provider.Session{}, err
	}
	if err = p.persist(id); err != nil {
		_ = rt.Close()
		p.mu.Lock()
		delete(p.sessions, id)
		p.mu.Unlock()
		return provider.Session{}, err
	}
	return info, nil
}
func (p *Provider) ensureRuntime(ctx context.Context, id string) (runtimeAgent, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if err := p.load(id); err != nil {
		return nil, err
	}
	p.mu.Lock()
	sessionLock := &p.sessions[id].runtimeMu
	p.mu.Unlock()
	sessionLock.Lock()
	defer sessionLock.Unlock()
	p.mu.Lock()
	s := p.sessions[id]
	if p.removed[id] || s.removing {
		p.mu.Unlock()
		return nil, errors.New("会话已移除")
	}
	if s.runtime != nil && s.runtime.Alive() {
		rt := s.runtime
		p.mu.Unlock()
		return rt, nil
	}
	if s.opening {
		p.mu.Unlock()
		return nil, errors.New("会话正在恢复")
	}
	s.opening = true
	stale := s.runtime
	s.runtime = nil
	cwd, native := s.Session.CWD, s.NativeID
	p.mu.Unlock()
	if stale != nil {
		_ = stale.Close()
	}
	rt, err := p.newRuntime(ctx, id)
	if err == nil {
		_, err = rt.Open(ctx, cwd, native)
	}
	p.mu.Lock()
	s.opening = false
	if err == nil {
		s.runtime = rt
	}
	p.mu.Unlock()
	if err != nil {
		if rt != nil {
			rt.Close()
		}
		return nil, err
	}
	return rt, nil
}
func (p *Provider) Send(ctx context.Context, id, text string) error {
	return p.SendWithModel(ctx, id, text, "")
}
func (p *Provider) SendWithModel(ctx context.Context, id, text, model string) error {
	return p.SendAttachments(ctx, id, text, model, nil)
}
func (p *Provider) SendAttachments(ctx context.Context, id, text, model string, ids []string) error {
	rt, err := p.ensureRuntime(ctx, id)
	if err != nil {
		return err
	}
	attachments := []provider.Attachment{}
	paths := []string{}
	for _, aid := range ids {
		if p.assets == nil {
			return errors.New("附件存储不可用")
		}
		a, path, err := p.assets.Input(assets.Scope(p.id, id), aid)
		if err != nil {
			return err
		}
		attachments = append(attachments, a)
		paths = append(paths, path)
	}
	p.mu.Lock()
	s := p.sessions[id]
	if p.removed[id] || s.removing {
		p.mu.Unlock()
		return errors.New("会话已移除")
	}
	if s.configuring {
		p.mu.Unlock()
		return errors.New("正在更新会话设置")
	}
	if s.sending {
		p.mu.Unlock()
		return errors.New("Agent 正在回复，请等待完成或停止生成")
	}
	if model == "" {
		model = s.Session.Model
	}
	turnID := device.Random()[:16]
	s.turn = turnID
	s.assistant = ""
	s.sending = true
	s.Session.ActiveTurnID = turnID
	s.Session.Status = "inProgress"
	s.Session.UpdatedAt = time.Now().UnixMilli()
	s.Session.RecencyAt = s.Session.UpdatedAt
	s.Session.Model = model
	if s.Session.Title == "新会话" {
		r := []rune(strings.TrimSpace(text))
		s.Session.Title = string(r[:min(len(r), 60)])
	}
	user := provider.Item{ID: device.Random()[:16], Role: "user", Type: "userMessage", Text: text, Attachments: attachments, TimestampMS: ptr(s.Session.RecencyAt)}
	s.Turns = append(s.Turns, provider.Turn{ID: turnID, Status: "inProgress", RecencyAt: s.Session.RecencyAt, Items: []provider.Item{user}})
	s.mark(len(s.Turns)-1, 0)
	p.log.Publish(provider.StreamEvent{SessionID: id, TurnID: turnID, Kind: "turn", Status: "inProgress", RecencyAt: s.Session.RecencyAt})
	p.log.Publish(provider.StreamEvent{SessionID: id, TurnID: turnID, ItemID: user.ID, Kind: "item", Item: &user})
	p.mu.Unlock()
	if err = p.persist(id); err != nil {
		p.finish(id, turnID, err)
		return err
	}
	// The remote request acknowledges submission. Agent execution belongs to the
	// host lifetime and continues if a controller disconnects or goes to sleep.
	go func() { err := rt.Prompt(p.life, text, attachments, paths, model); p.finish(id, turnID, err) }()
	return nil
}
func (p *Provider) finish(id, turn string, err error) {
	p.mu.Lock()
	s := p.sessions[id]
	if s == nil || s.turn != turn {
		p.mu.Unlock()
		return
	}
	status := "completed"
	if err != nil {
		status = "failed"
		if errors.Is(err, context.Canceled) {
			status = "interrupted"
		}
		p.lastError = err.Error()
	}
	s.sending = false
	s.Session.Status = status
	s.Session.ActiveTurnID = ""
	s.Session.UpdatedAt = time.Now().UnixMilli()
	if status == "completed" {
		s.Session.RecencyAt = max(s.Session.RecencyAt, s.Session.UpdatedAt)
	}
	if len(s.Turns) > 0 {
		s.mark(len(s.Turns)-1, -1)
		s.Turns[len(s.Turns)-1].Status = status
		s.Turns[len(s.Turns)-1].RecencyAt = s.Session.RecencyAt
		if err != nil && status == "failed" {
			item := provider.Item{ID: device.Random()[:16], Role: "system", Type: "error", Text: err.Error()}
			s.Turns[len(s.Turns)-1].Items = append(s.Turns[len(s.Turns)-1].Items, item)
			s.mark(len(s.Turns)-1, len(s.Turns[len(s.Turns)-1].Items)-1)
			p.log.Publish(provider.StreamEvent{SessionID: id, TurnID: turn, ItemID: item.ID, Kind: "item", Item: &item})
		}
	}
	p.log.Publish(provider.StreamEvent{SessionID: id, TurnID: turn, Kind: "turn", Status: status, RecencyAt: s.Session.RecencyAt})
	p.mu.Unlock()
	if e := p.persist(id); e != nil {
		p.mu.Lock()
		p.lastError = e.Error()
		p.mu.Unlock()
	}
}
func (p *Provider) text(id, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[id]
	if s == nil || !s.sending || text == "" {
		return
	}
	t := &s.Turns[len(s.Turns)-1]
	if s.assistant == "" {
		s.assistant = device.Random()[:16]
		t.Items = append(t.Items, provider.Item{ID: s.assistant, Role: "assistant", Type: "agentMessage"})
	}
	i := &t.Items[len(t.Items)-1]
	if i.ID != s.assistant {
		t.Items = append(t.Items, provider.Item{ID: s.assistant, Role: "assistant", Type: "agentMessage"})
		i = &t.Items[len(t.Items)-1]
	}
	offset := len(utf16.Encode([]rune(i.Text)))
	i.Text += text
	p.scheduleCommit(id, s)
	s.mark(len(s.Turns)-1, len(t.Items)-1)
	p.log.Publish(provider.StreamEvent{SessionID: id, TurnID: s.turn, ItemID: i.ID, Kind: "text", Delta: text, Offset: offset})
}
func (p *Provider) tool(id, toolID, title, status, kind string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[id]
	if s == nil || !s.sending {
		return
	}
	s.assistant = ""
	if status == "in_progress" || status == "pending" {
		status = "inProgress"
	}
	p.scheduleCommit(id, s)
	t := &s.Turns[len(s.Turns)-1]
	item := provider.Item{ID: toolID, Role: "tool", Type: "commandExecution", Text: title, Status: status, Activities: []provider.Activity{{Kind: kind, Title: title}}}
	found := false
	for i := range t.Items {
		if t.Items[i].ID == toolID {
			if status == "" {
				item.Status = t.Items[i].Status
			}
			if kind == "" {
				item.Activities = t.Items[i].Activities
			}
			if title == "" {
				item.Text = t.Items[i].Text
				item.Activities = t.Items[i].Activities
			}
			t.Items[i] = item
			s.mark(len(s.Turns)-1, i)
			found = true
			break
		}
	}
	if !found {
		t.Items = append(t.Items, item)
		s.mark(len(s.Turns)-1, len(t.Items)-1)
	}
	p.log.Publish(provider.StreamEvent{SessionID: id, TurnID: s.turn, ItemID: toolID, Kind: "item", Item: &item})
}
func (p *Provider) Interrupt(ctx context.Context, id, turn string) error {
	p.mu.Lock()
	s := p.sessions[id]
	if s == nil || !s.sending {
		p.mu.Unlock()
		return nil
	}
	if turn != "" && s.turn != turn {
		p.mu.Unlock()
		return errors.New("回复已变化")
	}
	rt := s.runtime
	p.mu.Unlock()
	return rt.Cancel(ctx)
}
func (p *Provider) Steer(ctx context.Context, id, turn, text string) error {
	p.mu.Lock()
	s := p.sessions[id]
	if s == nil || !s.sending || turn != "" && s.turn != turn {
		p.mu.Unlock()
		return errors.New("当前没有可追加的回复")
	}
	rt := s.runtime
	p.mu.Unlock()
	if err := rt.Steer(ctx, text); err != nil {
		return err
	}
	p.mu.Lock()
	s = p.sessions[id]
	if s != nil && s.sending {
		item := provider.Item{ID: device.Random()[:16], Role: "user", Type: "userMessage", Text: text, TimestampMS: ptr(time.Now().UnixMilli())}
		s.Turns[len(s.Turns)-1].Items = append(s.Turns[len(s.Turns)-1].Items, item)
		s.mark(len(s.Turns)-1, len(s.Turns[len(s.Turns)-1].Items)-1)
		s.Session.RecencyAt = max(s.Session.RecencyAt, *item.TimestampMS)
		s.Session.UpdatedAt = max(s.Session.UpdatedAt, s.Session.RecencyAt)
		s.assistant = ""
		p.log.Publish(provider.StreamEvent{SessionID: id, TurnID: s.turn, ItemID: item.ID, Kind: "item", Item: &item})
	}
	p.mu.Unlock()
	return p.persist(id)
}
func (p *Provider) SteerAttachments(ctx context.Context, id, turn, text string, ids []string) error {
	if len(ids) > 0 {
		return errors.New("此 Agent 不支持在生成中追加附件")
	}
	return p.Steer(ctx, id, turn, text)
}
func (p *Provider) Models(ctx context.Context) ([]provider.Model, error) {
	p.mu.Lock()
	rt := p.probe
	p.mu.Unlock()
	if rt == nil {
		return []provider.Model{}, nil
	}
	return rt.Models(), nil
}
func (p *Provider) SessionModels(ctx context.Context, id string) ([]provider.Model, error) {
	rt, err := p.ensureRuntime(ctx, id)
	if err != nil {
		return nil, err
	}
	return rt.Models(), nil
}
func (p *Provider) Watch(ctx context.Context, id, epoch string, after int64) (provider.StreamBatch, error) {
	return p.log.Watch(ctx, id, epoch, after)
}
func (p *Provider) Probe(ctx context.Context, id, epoch string, after int64) (provider.StreamBatch, error) {
	return p.log.Probe(ctx, id, epoch, after)
}
func (p *Provider) ConnectDesktop(context.Context, []provider.Instance, bool) error {
	return errors.New("此 Agent 使用本机命令接入，无需重启桌面应用")
}
func ptr[T any](v T) *T { return &v }

var _ provider.Provider = (*Provider)(nil)

type nativeSession struct {
	ID, CWD, Title string
	UpdatedAt      int64
	CreatedAt      int64
}

func (p *Provider) replaceHistory(id string, turns []provider.Turn) {
	p.mu.Lock()
	if s := p.sessions[id]; s != nil && !s.sending {
		s.Turns = turns
		s.start = 0
		s.replace = true
		s.dirty = nil
		for i, t := range turns {
			s.mark(i, -1)
			for j := range t.Items {
				s.mark(i, j)
			}
		}
		s.Session.RecencyAt = max(s.Session.RecencyAt, provider.ConversationRecency(turns))
		s.Imported = false
	}
	p.mu.Unlock()
}
func (p *Provider) metadata(id string, title, updated *string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[id]
	if s == nil {
		return
	}
	if title != nil && *title != "" {
		s.Session.Title = *title
	}
	if updated != nil {
		if t, err := time.Parse(time.RFC3339Nano, *updated); err == nil {
			s.Session.UpdatedAt = t.UnixMilli()
		}
	}
}
func (p *Provider) discover(ctx context.Context) error {
	p.mu.Lock()
	rt := p.probe
	p.mu.Unlock()
	source, ok := rt.(interface {
		Discover(context.Context) ([]nativeSession, error)
	})
	if !ok {
		return nil
	}
	rows, err := source.Discover(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		digest := sha256.Sum256([]byte(p.id + "\x00" + row.ID))
		id := fmtID(digest)
		p.mu.Lock()
		exists := false
		for _, s := range p.sessions {
			if s.NativeID == row.ID {
				exists = true
				break
			}
		}
		if !exists {
			p.sessions[id] = &session{Transcript: provider.Transcript{Session: provider.Session{ID: id, ProviderID: p.id, Title: row.Title, CWD: row.CWD, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, SourceKind: "custom"}, Turns: []provider.Turn{}}, NativeID: row.ID, Imported: true}
		}
		p.mu.Unlock()
		if !exists {
			if err := p.persist(id); err != nil {
				return err
			}
		}
	}
	return nil
}
func fmtID(d [32]byte) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 24)
	for i := range 12 {
		b[i*2] = hex[d[i]>>4]
		b[i*2+1] = hex[d[i]&15]
	}
	return string(b)
}
func (p *Provider) discoverOnce(ctx context.Context) error {
	p.discoverMu.Lock()
	defer p.discoverMu.Unlock()
	if time.Since(p.discovered) < time.Minute {
		return nil
	}
	if err := p.discover(ctx); err != nil {
		return err
	}
	p.discovered = time.Now()
	return nil
}

func (p *Provider) Managed() bool { return true }

func (p *Provider) scheduleCommit(id string, s *session) {
	if s.timer != nil || p.closed {
		return
	}
	s.timer = time.AfterFunc(250*time.Millisecond, func() {
		p.mu.Lock()
		s.timer = nil
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return
		}
		if err := p.persist(id); err != nil {
			p.mu.Lock()
			p.lastError = err.Error()
			p.mu.Unlock()
		}
	})
}
