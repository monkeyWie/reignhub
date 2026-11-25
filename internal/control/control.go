// Package control is the transport-independent application API.
package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/terminal"
	"github.com/monkeyWie/reignhub/internal/workspace"
)

type Request struct {
	ReceiptOwner   string                 `json:"-"`
	Cancel         string                 `json:"cancel,omitempty"`
	Progress       bool                   `json:"progress,omitempty"`
	Probe          bool                   `json:"probe,omitempty"`
	Fresh          bool                   `json:"fresh,omitempty"`
	Params         json.RawMessage        `json:"params,omitempty"`
	Epoch          string                 `json:"epoch,omitempty"`
	After          int64                  `json:"after,omitempty"`
	ID             string                 `json:"id"`
	Revision       string                 `json:"revision,omitempty"`
	Method         string                 `json:"method"`
	Provider       string                 `json:"provider,omitempty"`
	Session        string                 `json:"session,omitempty"`
	Cursor         string                 `json:"cursor,omitempty"`
	Text           string                 `json:"text,omitempty"`
	Attachments    []string               `json:"attachments,omitempty"`
	Model          string                 `json:"model,omitempty"`
	HistoryItems   int                    `json:"historyItems,omitempty"`
	RecentTurns    int                    `json:"recentTurns,omitempty"`
	Turn           string                 `json:"turn,omitempty"`
	Approval       string                 `json:"approval,omitempty"`
	Options        provider.CreateOptions `json:"options,omitempty"`
	Decision       provider.Decision      `json:"decision,omitempty"`
	Expected       []provider.Instance    `json:"expected,omitempty"`
	ConfirmRestart bool                   `json:"confirmRestart,omitempty"`
}

type Response struct {
	Code        string          `json:"code,omitempty"`
	Sequence    uint32          `json:"sequence,omitempty"`
	Partial     bool            `json:"partial,omitempty"`
	ID          string          `json:"id"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	Revision    string          `json:"revision,omitempty"`
	NotModified bool            `json:"notModified,omitempty"`
}

type Caller interface {
	Call(context.Context, Request, any) error
	Path() string
	Close() error
}

// LocalTarget identifies the in-process environment. It never enters pairing,
// discovery or the encrypted transport; it implements the same Caller contract.
const LocalTarget = "local"

type PeerStatus struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}
type NetworkStatus struct {
	Platform string       `json:"platform,omitempty"`
	State    string       `json:"state"`
	Relay    string       `json:"relay,omitempty"`
	Detail   string       `json:"detail,omitempty"`
	Peers    []PeerStatus `json:"peers"`
}
type Dispatcher struct {
	Terminals             *terminal.Manager
	RemoteTerminalAllowed func() bool
	Assets                interface {
		Handle(context.Context, string, string, provider.AttachmentParams) (any, error)
	}
	mutationMu      [64]sync.Mutex
	Execute         func(Request, func() Response) Response
	Providers       map[string]provider.Provider
	ProviderEnabled func(string) bool
	Network         func() NetworkStatus
	Local           func(context.Context, Request) (any, error)
	Sync            func(context.Context, provider.Provider, Request) (any, error)
}

func (d *Dispatcher) Handle(ctx context.Context, r Request) Response {
	result, err := d.dispatch(ctx, r)
	if err == nil && strings.HasPrefix(r.Method, "terminal.") {
		if _, remote := ctx.Value(remoteIdentityKey{}).(string); remote && (d.RemoteTerminalAllowed == nil || !d.RemoteTerminalAllowed()) {
			err = &ResponseError{Code: "TERMINAL_DENIED", Message: "请在被控设备的「设置 → 连接」中开启远程终端"}
		}
	}
	out := Response{ID: r.ID}
	if err != nil {
		out.Error = err.Error()
		out.Code = ErrorCode(err)
		return out
	}
	out.Result, err = json.Marshal(workspace.Result(result))
	if err != nil {
		out.Error = err.Error()
	}
	if err == nil && ReadOnly(r.Method) {
		digest := sha256.Sum256(out.Result)
		out.Revision = fmt.Sprintf("%x", digest)
		if r.Revision == out.Revision {
			out.NotModified = true
			out.Result = nil
		}
	}
	return out
}
func (d *Dispatcher) HandleRemote(ctx context.Context, r Request) Response {
	if _, ok := ctx.Value(remoteIdentityKey{}).(string); !ok {
		ctx = WithRemoteIdentity(ctx, "")
	}
	if strings.HasPrefix(r.Method, "host.") || strings.HasPrefix(r.Method, "client.") || r.Method == "connectDesktop" {
		return Response{ID: r.ID, Error: "此操作仅允许在本机执行"}
	}
	return d.HandleRequest(ctx, r)
}

// HandleRequest applies the same command ordering and receipt semantics to
// local and remote callers. Remote authorization is checked before this layer.
func (d *Dispatcher) HandleRequest(ctx context.Context, r Request) Response {
	if strings.HasPrefix(r.Method, "host.") || strings.HasPrefix(r.Method, "client.") || strings.HasPrefix(r.Method, "terminal.") {
		return d.Handle(ctx, r)
	}
	if d.Execute != nil && !ReadOnly(r.Method) && r.Method != "watch" && r.Method != "network" && !strings.HasPrefix(r.Method, "attachment.") {
		digest := sha256.Sum256([]byte(r.Provider + "/" + r.Session))
		lock := &d.mutationMu[int(digest[0])%len(d.mutationMu)]
		lock.Lock()
		defer lock.Unlock()
		if err := ctx.Err(); err != nil {
			return Response{ID: r.ID, Error: err.Error()}
		}
		r.ReceiptOwner = "local"
		if peer, ok := ctx.Value(remoteIdentityKey{}).(string); ok {
			r.ReceiptOwner = "remote:" + peer
		}
		return d.Execute(r, func() Response { return d.Handle(ctx, r) })
	}
	return d.Handle(ctx, r)
}
func watch(ctx context.Context, p provider.Provider, r Request) (provider.StreamBatch, error) {
	stream, ok := p.(provider.StreamingProvider)
	var read func(context.Context, string, string, int64) (provider.StreamBatch, error)
	if ok {
		read = stream.Watch
	}
	if probe, ok := p.(provider.StreamProber); r.Probe && ok {
		read = probe.Probe
	}
	if read == nil {
		return provider.StreamBatch{}, fmt.Errorf("此 Agent 暂不支持实时订阅")
	}
	if r.Turn != "" {
		ctx = provider.WithWatchedTurn(ctx, r.Turn)
	}
	batch, err := read(ctx, r.Session, r.Epoch, r.After)
	if err == nil && r.Turn != "" {
		// Enforce the wire projection for external providers as well. Their
		// watch implementation may deliver a broader set of events.
		events := make([]provider.StreamEvent, 0)
		for _, e := range batch.Events {
			if e.Kind == "turn" && e.SessionID == r.Session && e.TurnID == r.Turn {
				events = append(events, provider.StreamEvent{ID: e.ID, SessionID: r.Session, Kind: "turn", TurnID: r.Turn, Status: e.Status})
			}
		}
		batch.Events = events
	}
	return batch, err
}

func (d *Dispatcher) dispatch(ctx context.Context, r Request) (any, error) {
	if strings.HasPrefix(r.Method, "terminal.") {
		return d.terminal(ctx, r)
	}
	if strings.HasPrefix(r.Method, "host.") || strings.HasPrefix(r.Method, "client.") {
		if d.Local == nil {
			return nil, fmt.Errorf("本机接口不可用")
		}
		return d.Local(ctx, r)
	}
	if r.Method == "network" {
		status := NetworkStatus{State: "disabled", Peers: []PeerStatus{}}
		if d.Network != nil {
			status = d.Network()
		}
		status.Platform = runtime.GOOS
		return status, nil
	}
	if r.Method == "providers" {
		statuses := []provider.Status{}
		for id, p := range d.Providers {
			s := p.Status(ctx)
			s.Capabilities.Terminal = d.Terminals != nil
			s.Capabilities.TerminalProfiles = d.Terminals != nil && terminal.SupportsProfiles
			s.Enabled = d.ProviderEnabled == nil || d.ProviderEnabled(id)
			if !s.Enabled && s.State != "unsupported" {
				s.State, s.Detail = "disabled", "已关闭"
			}
			s.Controllable = s.Ready()
			statuses = append(statuses, s)
		}
		sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
		return statuses, nil
	}
	p := d.Providers[r.Provider]
	if p == nil {
		return nil, fmt.Errorf("unknown provider %q", r.Provider)
	}
	if d.ProviderEnabled != nil && !d.ProviderEnabled(r.Provider) {
		return nil, fmt.Errorf("此 Agent 已在本机关闭")
	}
	if r.Method == "watch" {
		return watch(ctx, p, r)
	}
	if strings.HasPrefix(r.Method, "attachment.") {
		if d.Assets == nil {
			return nil, fmt.Errorf("附件服务不可用")
		}
		if _, ok := p.(provider.AttachmentSender); !ok {
			return nil, fmt.Errorf("此 Agent 暂不支持附件")
		}
		var params provider.AttachmentParams
		if err := json.Unmarshal(r.Params, &params); err != nil {
			return nil, fmt.Errorf("无效附件参数")
		}
		return d.Assets.Handle(ctx, r.Provider+"\x00"+r.Session, r.Method, params)
	}
	if r.Method != "connectDesktop" {
		s := p.Status(ctx)
		if !s.Ready() {
			return nil, fmt.Errorf("Agent 尚未就绪: %s", s.Detail)
		}
	}
	switch r.Method {
	case "session.remove":
		remover, ok := p.(provider.SessionRemover)
		if !ok || p.Status(ctx).Capabilities.SessionRemoval == "" {
			return nil, fmt.Errorf("此 Agent 暂不支持移除会话")
		}
		if strings.TrimSpace(r.Session) == "" {
			return nil, fmt.Errorf("请选择要移除的会话")
		}
		return nil, remover.RemoveSession(ctx, r.Session)
	case "sync":
		if d.Sync == nil {
			return nil, fmt.Errorf("同步不可用")
		}
		return d.Sync(ctx, p, r)
	case "sessions":
		return p.Sessions(ctx, r.Cursor)
	case "turn.status":
		var state provider.Turn
		if status, ok := p.(provider.TurnStatusProvider); ok {
			var err error
			state, err = status.TurnStatus(ctx, r.Session, r.Turn)
			if err != nil {
				return nil, err
			}
		} else {
			transcript, err := p.Transcript(ctx, r.Session, "")
			if err != nil {
				return nil, err
			}
			for _, turn := range transcript.Turns {
				if turn.ID == r.Turn {
					state = turn
					break
				}
			}
		}
		// Never transfer history, tool output or attachments for task polling.
		return provider.Turn{ID: r.Turn, Status: state.Status}, nil
	case "transcript":
		if r.HistoryItems != 0 {
			if r.HistoryItems < 1 || r.HistoryItems > 100 {
				return nil, fmt.Errorf("无效的消息分页大小")
			}
			ctx = provider.WithHistoryItems(ctx, r.HistoryItems)
		}
		if r.RecentTurns != 0 {
			if r.Cursor != "" || r.RecentTurns < 1 || r.RecentTurns > 30 {
				return nil, fmt.Errorf("无效的最近对话轮数")
			}
			ctx = provider.WithRecentTurns(ctx, r.RecentTurns)
		}
		if !r.Progress {
			return p.Transcript(ctx, r.Session, r.Cursor)
		}
		parent, observed := ctx, false
		ctx = provider.WithTranscriptObserver(ctx, func(fragment provider.Transcript) error {
			observed = true
			return provider.ObserveTranscript(parent, fragment)
		})
		transcript, err := p.Transcript(ctx, r.Session, r.Cursor)
		if err == nil && !observed {
			for _, turn := range transcript.Turns {
				if err = provider.EmitTranscriptTurn(ctx, transcript, turn); err != nil {
					break
				}
			}
		}
		return transcript, err
	case "create":
		return p.Create(ctx, r.Options)
	case "send":
		if len(r.Attachments) > 0 {
			if sender, ok := p.(provider.AttachmentSender); ok {
				return nil, sender.SendAttachments(ctx, r.Session, r.Text, r.Model, r.Attachments)
			}
			return nil, fmt.Errorf("此 Agent 暂不支持附件")
		}
		if r.Model != "" {
			if models, ok := p.(provider.ModelProvider); ok {
				return nil, models.SendWithModel(ctx, r.Session, r.Text, r.Model)
			}
			return nil, fmt.Errorf("此 Agent 暂不支持模型切换")
		}
		return nil, p.Send(ctx, r.Session, r.Text)
	case "composer.settings", "composer.configure":
		agent, ok := p.(provider.ComposerProvider)
		if !ok || r.Session == "" {
			return nil, fmt.Errorf("此会话不支持修改 Agent 设置")
		}
		if r.Method == "composer.settings" {
			return agent.ComposerControls(ctx, r.Session, r.Model)
		}
		var change provider.ComposerChange
		if json.Unmarshal(r.Params, &change) != nil || change.Value == "" || len(change.Value) > 2048 {
			return nil, fmt.Errorf("无效的会话设置")
		}
		return nil, agent.ConfigureComposer(ctx, r.Session, change)
	case "models":
		if models, ok := p.(provider.SessionModelProvider); ok && r.Session != "" {
			return models.SessionModels(ctx, r.Session)
		}
		if models, ok := p.(provider.ModelProvider); ok {
			return models.Models(ctx)
		}
		return []provider.Model{}, nil
	case "steer":
		if len(r.Attachments) > 0 {
			if sender, ok := p.(provider.AttachmentSender); ok {
				return nil, sender.SteerAttachments(ctx, r.Session, r.Turn, r.Text, r.Attachments)
			}
			return nil, fmt.Errorf("此 Agent 暂不支持附件")
		}
		return nil, p.Steer(ctx, r.Session, r.Turn, r.Text)
	case "interrupt":
		return nil, p.Interrupt(ctx, r.Session, r.Turn)
	case "approvals":
		if scoped, ok := p.(provider.SessionApprovalProvider); ok && r.Session != "" {
			return scoped.SessionApprovals(ctx, r.Session)
		}
		approvals := p.Approvals()
		if r.Session == "" {
			return approvals, nil
		}
		filtered := []provider.Approval{}
		for _, a := range approvals {
			if a.SessionID == r.Session {
				filtered = append(filtered, a)
			}
		}
		return filtered, nil
	case "decide":
		if r.Session != "" {
			found := false
			for _, a := range p.Approvals() {
				if a.ID == r.Approval && a.SessionID == r.Session {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("请求已处理、已过期或不属于此会话")
			}
		}
		return nil, p.Decide(ctx, r.Approval, r.Decision)
	case "connectDesktop":
		return nil, p.ConnectDesktop(ctx, r.Expected, r.ConfirmRestart)
	default:
		return nil, fmt.Errorf("unknown method %q", r.Method)
	}
}

// Only snapshots may be conditionally returned. Mutations always execute and
// are never cached or transparently retried.
func ReadOnly(method string) bool {
	switch method {
	case "providers", "network", "sessions", "transcript", "approvals", "sync", "models", "composer.settings", "turn.status":
		return true
	}
	return false
}
