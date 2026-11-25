package agents

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/monkeyWie/reignhub/internal/provider"
)

// Pi's native RPC is LF-delimited JSON; JSON strings containing U+2028/U+2029
// are data, not line separators. One reader handles replies and streaming events.
type piRuntime struct {
	host          *Provider
	localID       string
	process       *process
	mu            sync.Mutex
	writeMu       sync.Mutex
	pending       map[string]chan map[string]json.RawMessage
	models        []provider.Model
	native        string
	done          chan struct{}
	settled       chan struct{}
	promptCancel  context.CancelFunc
	promptContext context.Context
	failure       error
}

const piApprovalExtension = `export default function (pi) {
 pi.on("tool_call", async (event, ctx) => {
  if (new Set(["read", "grep", "find", "ls"]).has(event.toolName)) return;
  const allowed = await ctx.ui.confirm("Allow " + event.toolName + "?", JSON.stringify(event.input), {signal:ctx.signal});
  if (!allowed) return {block:true, reason:"Declined in ReignHub"};
 });
}`

func newPi(ctx context.Context, p *Provider, id string) (runtimeAgent, error) {
	if err := Validate(p.id, p.def); err != nil {
		return nil, err
	}
	dir := filepath.Join(p.dir, "pi", p.id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	extension := filepath.Join(dir, "reignhub-permissions.mjs")
	if err := os.WriteFile(extension, []byte(piApprovalExtension), 0600); err != nil {
		return nil, err
	}
	cwd := ""
	native := filepath.Join(dir, "probe.jsonl")
	if id != "" {
		p.mu.Lock()
		s := p.sessions[id]
		if s != nil {
			cwd = s.Session.CWD
			native = s.NativeID
		}
		p.mu.Unlock()
		if native == "" {
			native = filepath.Join(dir, id+".jsonl")
		}
	}
	// Isolate probe history and keep all real sessions durably resumable in Pi.
	extra := []string{"--mode", "rpc", "--extension", extension}
	if id != "" {
		extra = append(extra, "--session", native)
	} else {
		extra = append(extra, "--no-session")
	}
	proc, err := spawn(p.life, p.def, extra, cwd)
	if err != nil {
		return nil, err
	}
	r := &piRuntime{host: p, localID: id, process: proc, pending: map[string]chan map[string]json.RawMessage{}, native: native, done: make(chan struct{})}
	go r.read()
	data, err := r.request(ctx, map[string]any{"type": "get_available_models"})
	if err != nil {
		r.Close()
		return nil, err
	}
	var models struct {
		Models []struct {
			ID, Name, Provider string
			Input              []string
		}
	}
	_ = decodeValue(data, &models)
	for _, m := range models.Models {
		encoded, _ := json.Marshal(map[string]string{"provider": m.Provider, "id": m.ID})
		r.models = append(r.models, provider.Model{ID: string(encoded), Name: m.Name + " · " + m.Provider, ImageInput: slices.Contains(m.Input, "image")})
	}
	return r, nil
}
func decodeValue(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func (r *piRuntime) Alive() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}
func (r *piRuntime) Capabilities() provider.Capabilities {
	return provider.Capabilities{ComposerSettings: true, MaxAttachmentBytes: 20 << 20, Streaming: true, History: true, Create: true, Send: true, Steer: true, Interrupt: true, Approvals: true, ImageInput: true}
}
func (r *piRuntime) Open(ctx context.Context, cwd, native string) (string, error) {
	if native != "" && native != r.native {
		return "", errors.New("Pi 会话路径不匹配")
	}
	if _, err := r.request(ctx, map[string]any{"type": "get_state"}); err != nil {
		return "", err
	}
	if native != "" {
		data, err := r.request(ctx, map[string]any{"type": "get_messages"})
		if err != nil {
			return "", err
		}
		var result struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if decodeValue(data, &result) == nil {
			var turns []provider.Turn
			for _, m := range result.Messages {
				if m.Role != "user" && m.Role != "assistant" {
					continue
				}
				text := ""
				var content []struct{ Type, Text string }
				if json.Unmarshal(m.Content, &text) != nil {
					_ = json.Unmarshal(m.Content, &content)
				}
				for _, c := range content {
					if c.Type == "text" {
						text += c.Text
					}
				}
				if m.Role == "user" {
					turns = append(turns, provider.Turn{ID: fmt.Sprintf("pi-%d", len(turns)), Status: "completed", Items: []provider.Item{}})
				}
				if len(turns) == 0 {
					continue
				}
				t := &turns[len(turns)-1]
				typ := "agentMessage"
				if m.Role == "user" {
					typ = "userMessage"
				}
				t.Items = append(t.Items, provider.Item{ID: fmt.Sprintf("%s-%d", t.ID, len(t.Items)), Role: m.Role, Type: typ, Text: text})
			}
			if len(turns) > 0 {
				r.host.replaceHistory(r.localID, turns)
			}
		}
	}
	return r.native, nil
}
func (r *piRuntime) Models() []provider.Model {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]provider.Model{}, r.models...)
}
func (r *piRuntime) write(v any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return json.NewEncoder(r.process.in).Encode(v)
}
func (r *piRuntime) request(ctx context.Context, v map[string]any) (json.RawMessage, error) {
	id := device.Random()[:16]
	v["id"] = id
	ch := make(chan map[string]json.RawMessage, 1)
	r.mu.Lock()
	r.pending[id] = ch
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	if err := r.write(v); err != nil {
		return nil, err
	}
	select {
	case msg := <-ch:
		var ok bool
		_ = json.Unmarshal(msg["success"], &ok)
		if !ok {
			var message string
			_ = json.Unmarshal(msg["error"], &message)
			return nil, fmt.Errorf("Pi: %s", message)
		}
		return msg["data"], nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.done:
		return nil, errors.New("Pi RPC 进程已退出")
	}
}
func (r *piRuntime) Prompt(ctx context.Context, text string, files []provider.Attachment, paths []string, model string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	r.settled = make(chan struct{}, 1)
	settled := r.settled
	r.promptCancel = cancel
	r.promptContext = ctx
	r.failure = nil
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.settled = nil; r.promptCancel = nil; r.mu.Unlock() }()
	if model != "" {
		var chosen piModel
		if json.Unmarshal([]byte(model), &chosen) != nil || chosen.Provider == "" || chosen.ID == "" {
			return errors.New("Pi 模型标识无效")
		}
		state, err := r.state(ctx)
		if err != nil {
			return err
		}
		if chosen != state.Model {
			if _, err := r.request(ctx, map[string]any{"type": "set_model", "provider": chosen.Provider, "modelId": chosen.ID}); err != nil {
				return err
			}
		}
	}
	input := map[string]any{"type": "prompt", "message": text}
	images := []map[string]string{}
	for i, a := range files {
		if a.Kind != "image" {
			return errors.New("Pi 仅支持图片附件")
		}
		if i >= len(paths) {
			return errors.New("附件路径不存在")
		}
		data, err := os.ReadFile(paths[i])
		if err != nil {
			return err
		}
		if len(data) > 20<<20 {
			return errors.New("图片超过 20 MiB")
		}
		images = append(images, map[string]string{"type": "image", "mimeType": a.MIME, "data": base64.StdEncoding.EncodeToString(data)})
	}
	if len(images) > 0 {
		input["images"] = images
	}
	data, err := r.request(ctx, input)
	if err != nil {
		return err
	}
	var response struct{ Disposition string }
	_ = json.Unmarshal(data, &response)
	if response.Disposition == "handled" {
		return nil
	}
	select {
	case <-settled:
		r.mu.Lock()
		err = r.failure
		r.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return errors.New("Pi 在回复完成前退出")
	}
}
func (r *piRuntime) Cancel(ctx context.Context) error {
	_, err := r.request(ctx, map[string]any{"type": "abort"})
	r.mu.Lock()
	cancel := r.promptCancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return err
}
func (r *piRuntime) Steer(ctx context.Context, text string) error {
	_, err := r.request(ctx, map[string]any{"type": "steer", "message": text})
	return err
}
func (r *piRuntime) Close() error { return r.process.Close() }
func (r *piRuntime) read() {
	defer close(r.done)
	reader := bufio.NewScanner(r.process.out)
	reader.Buffer(make([]byte, 64<<10), 32<<20)
	for reader.Scan() {
		var msg map[string]json.RawMessage
		if json.Unmarshal(reader.Bytes(), &msg) != nil {
			continue
		}
		var typ, id string
		_ = json.Unmarshal(msg["type"], &typ)
		_ = json.Unmarshal(msg["id"], &id)
		if typ == "response" {
			r.mu.Lock()
			ch := r.pending[id]
			r.mu.Unlock()
			if ch != nil {
				select {
				case ch <- msg:
				default:
				}
			}
			continue
		}
		switch typ {
		case "message_update":
			var e struct{ Type, Delta string }
			_ = json.Unmarshal(msg["assistantMessageEvent"], &e)
			if e.Type == "text_delta" {
				r.host.text(r.localID, e.Delta)
			}
		case "message_end":
			var m struct{ Role, StopReason, ErrorMessage string }
			_ = json.Unmarshal(msg["message"], &m)
			if m.Role == "assistant" && m.StopReason == "error" {
				r.mu.Lock()
				r.failure = fmt.Errorf("Pi: %s", m.ErrorMessage)
				r.mu.Unlock()
			}
		case "tool_execution_start", "tool_execution_end":
			var toolID, name string
			_ = json.Unmarshal(msg["toolCallId"], &toolID)
			_ = json.Unmarshal(msg["toolName"], &name)
			status := "in_progress"
			if typ == "tool_execution_end" {
				status = "completed"
				var failed bool
				_ = json.Unmarshal(msg["isError"], &failed)
				if failed {
					status = "failed"
				}
			}
			r.host.tool(r.localID, toolID, name, status, "tool")
		case "agent_end", "agent_settled":
			r.mu.Lock()
			if r.settled != nil {
				select {
				case r.settled <- struct{}{}:
				default:
				}
			}
			r.mu.Unlock()
		case "extension_ui_request":
			go r.dialog(msg)
		}
	}
}
func (r *piRuntime) dialog(msg map[string]json.RawMessage) {
	var id, method, title, message string
	_ = json.Unmarshal(msg["id"], &id)
	_ = json.Unmarshal(msg["method"], &method)
	_ = json.Unmarshal(msg["title"], &title)
	_ = json.Unmarshal(msg["message"], &message)
	if id == "" {
		return
	}
	if method != "confirm" && method != "select" && method != "input" && method != "editor" {
		return
	}
	ctx, cancel := context.WithCancel(r.host.life)
	defer cancel()
	r.mu.Lock()
	promptContext := r.promptContext
	r.mu.Unlock()
	if promptContext != nil {
		stop := context.AfterFunc(promptContext, cancel)
		defer stop()
	}
	dialogDone := ctx.Done()
	go func() {
		select {
		case <-r.done:
			cancel()
		case <-dialogDone:
		}
	}()
	var timeout float64
	_ = json.Unmarshal(msg["timeout"], &timeout)
	if timeout > 0 {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		defer deadlineCancel()
	}
	response := map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true}
	options := []permissionOption{}
	if method == "confirm" {
		options = []permissionOption{{ID: "yes", Name: "允许一次", Kind: "allow_once"}, {ID: "no", Name: "拒绝", Kind: "reject_once"}}
	} else if method == "select" {
		var values []string
		_ = json.Unmarshal(msg["options"], &values)
		for i, v := range values {
			options = append(options, permissionOption{ID: fmt.Sprint(i), Name: v})
		}
	}
	if method == "input" || method == "editor" {
		initial := ""
		_ = json.Unmarshal(msg["prefill"], &initial)
		choice, err := r.host.requestPermission(ctx, r.localID, title+"\n"+message, nil, &initial)
		if err == nil {
			delete(response, "cancelled")
			response["value"] = choice
		}
	}
	if len(options) > 0 {
		choice, err := r.host.permission(ctx, r.localID, title+"\n"+message, options)
		if err == nil {
			delete(response, "cancelled")
			if method == "confirm" {
				response["confirmed"] = choice == "yes"
			} else {
				for _, o := range options {
					if choice == o.ID {
						response["value"] = o.Name
					}
				}
			}
		}
	}
	_ = r.write(response)
}
