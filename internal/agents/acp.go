package agents

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"
	"github.com/monkeyWie/reignhub/internal/provider"
)

type acpRuntime struct {
	host            *Provider
	localID         string
	process         *process
	conn            *acp.ClientSideConnection
	init            acp.InitializeResponse
	mu              sync.Mutex
	native          acp.SessionId
	modes           *acp.SessionModeState
	config          []acp.SessionConfigOption
	replaying       bool
	replay          []provider.Turn
	promptCancel    context.CancelFunc
	approvalContext context.Context
}

func newACP(ctx context.Context, p *Provider, id string) (runtimeAgent, error) {
	cwd := ""
	p.mu.Lock()
	if s := p.sessions[id]; s != nil {
		cwd = s.Session.CWD
	}
	p.mu.Unlock()
	proc, err := spawn(p.life, p.def, nil, cwd)
	if err != nil {
		return nil, err
	}
	r := &acpRuntime{host: p, localID: id, process: proc}
	r.conn = acp.NewClientSideConnection(r, proc.in, proc.out)
	r.conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Agents execute their own file and terminal tools. Do not claim client-side
	// FS/terminal capabilities that would silently bypass their permission model.
	r.init, err = r.conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber, ClientInfo: &acp.Implementation{Name: "reignhub", Version: "1"}})
	if err == nil && r.init.ProtocolVersion != acp.ProtocolVersionNumber {
		err = fmt.Errorf("ACP 协议版本不兼容: %d", r.init.ProtocolVersion)
	}
	if err != nil {
		proc.Close()
		return nil, err
	}
	return r, nil
}
func (r *acpRuntime) Alive() bool {
	select {
	case <-r.conn.Done():
		return false
	default:
		return true
	}
}
func (r *acpRuntime) Capabilities() provider.Capabilities {
	return provider.Capabilities{ComposerSettings: true, MaxAttachmentBytes: 20 << 20, Streaming: true, History: true, Create: true, Send: true, Interrupt: true, Approvals: true, ImageInput: r.init.AgentCapabilities.PromptCapabilities.Image, FileInput: r.init.AgentCapabilities.PromptCapabilities.EmbeddedContext}
}
func (r *acpRuntime) Open(ctx context.Context, cwd, native string) (string, error) {
	var modes *acp.SessionModeState
	var options []acp.SessionConfigOption
	if native != "" {
		if !r.init.AgentCapabilities.LoadSession {
			return "", errors.New("此 Agent 不支持恢复已有会话，请新建会话")
		}
		r.mu.Lock()
		r.native = acp.SessionId(native)
		r.replaying = true
		r.mu.Unlock()
		resp, err := r.conn.LoadSession(ctx, acp.LoadSessionRequest{SessionId: acp.SessionId(native), Cwd: cwd, McpServers: []acp.McpServer{}})
		r.mu.Lock()
		r.replaying = false
		replay := r.replay
		r.replay = nil
		r.mu.Unlock()
		if err != nil {
			return "", err
		}
		options = resp.ConfigOptions
		modes = resp.Modes
		if len(replay) > 0 {
			r.host.replaceHistory(r.localID, replay)
		}
	} else {
		resp, err := r.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
		if err != nil {
			return "", err
		}
		native = string(resp.SessionId)
		r.mu.Lock()
		r.native = resp.SessionId
		r.mu.Unlock()
		options = resp.ConfigOptions
		modes = resp.Modes
	}
	r.mu.Lock()
	r.config = options
	r.modes = modes
	r.mu.Unlock()
	return native, nil
}
func (r *acpRuntime) Models() []provider.Model {
	r.mu.Lock()
	defer r.mu.Unlock()
	return acpModels(r.config)
}
func acpModels(options []acp.SessionConfigOption) []provider.Model {
	out := []provider.Model{}
	for _, o := range options {
		v := o.Select
		if v == nil || !(string(v.Id) == "model" || v.Category != nil && string(*v.Category) == "model") {
			continue
		}
		for _, choice := range selectChoices(v, "model") {
			out = append(out, provider.Model{ID: choice.ID, Name: choice.Label, Default: choice.ID == string(v.CurrentValue)})
		}
	}
	return out
}
func (r *acpRuntime) Prompt(ctx context.Context, text string, attachments []provider.Attachment, paths []string, model string) error {
	approvalContext, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	native := r.native
	r.promptCancel = cancel
	r.approvalContext = approvalContext
	options := append([]acp.SessionConfigOption{}, r.config...)
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.promptCancel = nil; r.mu.Unlock() }()
	if model != "" {
		found := false
		for _, o := range options {
			v := o.Select
			if v == nil || !(string(v.Id) == "model" || v.Category != nil && string(*v.Category) == "model") {
				continue
			}
			for _, m := range acpModels([]acp.SessionConfigOption{o}) {
				if m.ID == model {
					found = true
				}
			}
			if found && string(v.CurrentValue) == model {
				break
			}
			if found {
				resp, err := r.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: native, ConfigId: v.Id, Value: acp.SessionConfigValueId(model)}})
				if err != nil {
					return err
				}
				r.mu.Lock()
				r.config = resp.ConfigOptions
				r.mu.Unlock()
				break
			}
		}
		if !found {
			return errors.New("Agent 未提供所选模型")
		}
	}
	content := []acp.ContentBlock{acp.TextBlock(text)}
	for i, a := range attachments {
		if i >= len(paths) {
			return errors.New("附件路径不存在")
		}
		data, err := os.ReadFile(paths[i])
		if err != nil {
			return err
		}
		if len(data) > 20<<20 {
			return errors.New("附件超过 20 MiB")
		}
		if a.Kind == "image" {
			if !r.init.AgentCapabilities.PromptCapabilities.Image {
				return errors.New("此 Agent 不支持图片输入")
			}
			content = append(content, acp.ContentBlock{Image: &acp.ContentBlockImage{Type: "image", Data: base64.StdEncoding.EncodeToString(data), MimeType: a.MIME}})
		} else {
			if !r.init.AgentCapabilities.PromptCapabilities.EmbeddedContext {
				return errors.New("此 Agent 不支持文件输入")
			}
			resource := acp.EmbeddedResourceResource{BlobResourceContents: &acp.BlobResourceContents{Uri: "reignhub://attachment/" + a.ID, MimeType: &a.MIME, Blob: base64.StdEncoding.EncodeToString(data)}}
			if utf8.Valid(data) && (strings.HasPrefix(a.MIME, "text/") || a.MIME == "application/json") {
				resource = acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{Uri: "reignhub://attachment/" + a.ID, MimeType: &a.MIME, Text: string(data)}}
			}
			content = append(content, acp.ContentBlock{Resource: &acp.ContentBlockResource{Type: "resource", Resource: resource}})
		}
	}
	resp, err := r.conn.Prompt(ctx, acp.PromptRequest{SessionId: native, Prompt: content})
	if err == nil && resp.StopReason == "cancelled" {
		return context.Canceled
	}
	return err
}
func (r *acpRuntime) Cancel(ctx context.Context) error {
	r.mu.Lock()
	native := r.native
	cancel := r.promptCancel
	r.mu.Unlock()
	err := r.conn.Cancel(ctx, acp.CancelNotification{SessionId: native})
	if cancel != nil {
		cancel()
	}
	return err
}
func (r *acpRuntime) Steer(context.Context, string) error {
	return errors.New("ACP 不支持在生成中追加消息，请等待或停止生成")
}
func (r *acpRuntime) Close() error { return r.process.Close() }
func (r *acpRuntime) Discover(ctx context.Context) ([]nativeSession, error) {
	if !r.init.AgentCapabilities.LoadSession || r.init.AgentCapabilities.SessionCapabilities.List == nil {
		return nil, nil
	}
	var out []nativeSession
	cursor := ""
	seen := map[string]bool{}
	for len(out) < 10000 {
		req := acp.ListSessionsRequest{}
		if cursor != "" {
			req.Cursor = &cursor
		}
		resp, err := r.conn.ListSessions(ctx, req)
		if err != nil {
			return nil, err
		}
		for _, s := range resp.Sessions {
			title := "Agent 会话"
			if s.Title != nil && *s.Title != "" {
				title = *s.Title
			}
			updated := int64(0)
			if s.UpdatedAt != nil {
				if t, err := time.Parse(time.RFC3339Nano, *s.UpdatedAt); err == nil {
					updated = t.UnixMilli()
				}
			}
			out = append(out, nativeSession{ID: string(s.SessionId), CWD: s.Cwd, Title: title, UpdatedAt: updated})
		}
		if resp.NextCursor == nil || *resp.NextCursor == "" || seen[*resp.NextCursor] {
			break
		}
		cursor = *resp.NextCursor
		seen[cursor] = true
	}
	return out, nil
}
func (r *acpRuntime) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	r.mu.Lock()
	valid := req.SessionId == r.native && r.localID != ""
	approvalContext := r.approvalContext
	r.mu.Unlock()
	cancelled := acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}
	if !valid {
		return cancelled, nil
	}
	options := []permissionOption{}
	for _, o := range req.Options {
		options = append(options, permissionOption{ID: string(o.OptionId), Name: o.Name, Kind: string(o.Kind)})
	}
	title := "Agent 请求授权"
	if req.ToolCall.Title != nil {
		title = *req.ToolCall.Title
	}
	if approvalContext != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(approvalContext, cancel)
		defer stop()
	}
	choice, err := r.host.permission(ctx, r.localID, title, options)
	if err != nil {
		return cancelled, nil
	}
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: acp.PermissionOptionId(choice)}}}, nil
}
func (r *acpRuntime) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	r.mu.Lock()
	if n.SessionId != r.native {
		r.mu.Unlock()
		return nil
	}
	u := n.Update
	if u.CurrentModeUpdate != nil && r.modes != nil {
		r.modes.CurrentModeId = u.CurrentModeUpdate.CurrentModeId
	}
	if u.ConfigOptionUpdate != nil {
		r.config = u.ConfigOptionUpdate.ConfigOptions
	}
	if r.replaying {
		r.replay = appendReplay(r.replay, u)
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	if u.CurrentModeUpdate != nil || u.ConfigOptionUpdate != nil {
		r.host.log.Publish(provider.StreamEvent{Kind: "composerSettings", SessionID: r.localID})
	}
	if u.AgentMessageChunk != nil {
		r.host.text(r.localID, acpContentText(u.AgentMessageChunk.Content))
	}
	if t := u.ToolCall; t != nil {
		r.host.tool(r.localID, string(t.ToolCallId), t.Title, string(t.Status), string(t.Kind))
	}
	if t := u.ToolCallUpdate; t != nil {
		title, status, kind := "", "", ""
		if t.Title != nil {
			title = *t.Title
		}
		if t.Status != nil {
			status = string(*t.Status)
		}
		if t.Kind != nil {
			kind = string(*t.Kind)
		}
		r.host.tool(r.localID, string(t.ToolCallId), title, status, kind)
	}
	if u.SessionInfoUpdate != nil {
		r.host.metadata(r.localID, u.SessionInfoUpdate.Title, u.SessionInfoUpdate.UpdatedAt)
	}
	return nil
}
func appendReplay(turns []provider.Turn, u acp.SessionUpdate) []provider.Turn {
	role, text := "", ""
	if u.UserMessageChunk != nil {
		role = "user"
		text = acpContentText(u.UserMessageChunk.Content)
	}
	if u.AgentMessageChunk != nil {
		role = "assistant"
		text = acpContentText(u.AgentMessageChunk.Content)
	}
	if role == "" || text == "" {
		return turns
	}
	if len(turns) == 0 || role == "user" && len(turns[len(turns)-1].Items) > 0 && turns[len(turns)-1].Items[len(turns[len(turns)-1].Items)-1].Role != "user" {
		turns = append(turns, provider.Turn{ID: fmt.Sprintf("replay-%d", len(turns)), Status: "completed", Items: []provider.Item{}})
	}
	t := &turns[len(turns)-1]
	if len(t.Items) == 0 || t.Items[len(t.Items)-1].Role != role {
		typ := "agentMessage"
		if role == "user" {
			typ = "userMessage"
		}
		t.Items = append(t.Items, provider.Item{ID: fmt.Sprintf("%s-%d", t.ID, len(t.Items)), Role: role, Type: typ})
	}
	t.Items[len(t.Items)-1].Text += text
	return turns
}

// These services are intentionally not advertised during initialize.
func (r *acpRuntime) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound("fs/read_text_file")
}
func (r *acpRuntime) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound("fs/write_text_file")
}
func (r *acpRuntime) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound("terminal/create")
}
func (r *acpRuntime) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, acp.NewMethodNotFound("terminal/kill")
}
func (r *acpRuntime) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound("terminal/output")
}
func (r *acpRuntime) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, acp.NewMethodNotFound("terminal/release")
}
func (r *acpRuntime) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, acp.NewMethodNotFound("terminal/wait_for_exit")
}

func acpContentText(c acp.ContentBlock) string {
	switch {
	case c.Text != nil:
		return c.Text.Text
	case c.Image != nil:
		return "\n[Image output]\n"
	case c.Audio != nil:
		return "\n[Audio output]\n"
	case c.ResourceLink != nil:
		return "\n" + c.ResourceLink.Name + ": " + c.ResourceLink.Uri + "\n"
	case c.Resource != nil:
		if t := c.Resource.Resource.TextResourceContents; t != nil {
			return t.Text
		}
		return "\n[File output]\n"
	}
	return ""
}
