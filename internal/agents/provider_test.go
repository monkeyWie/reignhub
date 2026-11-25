package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/provider"
)

// This is a real subprocess speaking the wire protocol, not an in-process mock
// of runtimeAgent. It exercises framing, reverse requests and process lifetime.
func TestAgentProcessFixture(t *testing.T) {
	protocol := os.Getenv("REIGN_AGENT_FIXTURE")
	if protocol == "" {
		return
	}
	var mu sync.Mutex
	write := func(v any) {
		data, _ := json.Marshal(v)
		data = append(data, '\n')
		mu.Lock()
		defer mu.Unlock()
		mid := len(data) / 2
		_, _ = os.Stdout.Write(data[:mid])
		_, _ = os.Stdout.Write(data[mid:])
	}
	stop := make(chan struct{}, 1)
	permission := make(chan json.RawMessage, 1)
	notify := func(session, kind string, extra map[string]any) {
		u := map[string]any{"sessionUpdate": kind}
		for k, v := range extra {
			u[k] = v
		}
		write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": session, "update": u}})
	}
	options := []map[string]any{{"type": "select", "id": "models-choice", "name": "Model", "category": "model", "currentValue": "model/a", "options": []map[string]any{{"value": "model/a", "name": "Model A"}, {"value": "model/b", "name": "Model B"}}}}
	options = append(options, map[string]any{"type": "select", "id": "trust", "category": "mode", "name": "Permissions", "currentValue": "ask", "options": []map[string]string{{"value": "ask", "name": "Ask"}, {"value": "accept", "name": "Accept edits"}}})
	effort := map[string]any{"type": "select", "id": "depth", "category": "thought_level", "name": "Thinking", "currentValue": "short", "options": []map[string]string{{"value": "short", "name": "Short"}, {"value": "deep", "name": "Deep"}}}
	options = append(options, effort)
	piLevel := "low"
	reader := bufio.NewScanner(os.Stdin)
	reader.Buffer(make([]byte, 65536), 32<<20)
	for reader.Scan() {
		var msg map[string]json.RawMessage
		if json.Unmarshal(reader.Bytes(), &msg) != nil {
			continue
		}
		var method, typ, id string
		_ = json.Unmarshal(msg["method"], &method)
		_ = json.Unmarshal(msg["type"], &typ)
		_ = json.Unmarshal(msg["id"], &id)
		if id == "permission-1" && method == "" {
			permission <- msg["result"]
			continue
		}
		if protocol == "pi" {
			var params map[string]any
			_ = json.Unmarshal(reader.Bytes(), &params)
			reply := func(data any) {
				write(map[string]any{"type": "response", "id": id, "command": typ, "success": true, "data": data})
			}
			switch typ {
			case "get_available_models":
				reply(map[string]any{"models": []map[string]any{{"provider": "provider/one", "id": "model/two", "name": "Model", "input": []string{"text", "image"}}}})
			case "get_state":
				reply(map[string]any{"isStreaming": false, "model": map[string]string{"provider": "provider/one", "id": "model/two"}, "thinkingLevel": piLevel})
			case "get_available_thinking_levels":
				reply(map[string]any{"levels": []string{"low", "high"}})
			case "set_thinking_level":
				level, ok := params["level"].(string)
				if !ok || level != "high" {
					os.Exit(9)
				}
				piLevel = level
				reply(nil)
			case "get_messages":
				reply(map[string]any{"messages": []map[string]any{{"role": "user", "content": []map[string]string{{"type": "text", "text": "old question"}}}, {"role": "assistant", "content": []map[string]string{{"type": "text", "text": "old answer"}}}}})
			case "set_model":
				if params["provider"] != "provider/one" || params["modelId"] != "model/two" {
					os.Exit(3)
				}
				reply(nil)
			case "prompt":
				reply(map[string]any{"disposition": "accepted"})
				go func() {
					write(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]string{"type": "text_delta", "delta": "Hello 🌏\u2028"}})
					time.Sleep(10 * time.Millisecond)
					write(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]string{"type": "text_delta", "delta": "world"}})
					write(map[string]any{"type": "agent_settled"})
				}()
			case "abort":
				reply(nil)
			case "steer":
				reply(nil)
			}
			continue
		}
		var params map[string]json.RawMessage
		_ = json.Unmarshal(msg["params"], &params)
		reply := func(result any) {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg["id"]), "result": result})
		}
		switch method {
		case "initialize":
			reply(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true, "promptCapabilities": map[string]bool{"image": false, "embeddedContext": true}, "sessionCapabilities": map[string]any{"list": map[string]any{}}}, "agentInfo": map[string]string{"name": "Fixture", "version": "1"}, "authMethods": []any{}})
		case "session/new":
			reply(map[string]any{"sessionId": "native-1", "configOptions": options})
		case "session/load":
			var native string
			_ = json.Unmarshal(params["sessionId"], &native)
			notify(native, "user_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "old question"}})
			notify(native, "agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "old answer"}})
			reply(map[string]any{"configOptions": options})
		case "session/list":
			rows := []any{}
			if os.Getenv("REIGN_AGENT_DISCOVER") == "1" {
				rows = append(rows, map[string]string{"sessionId": "external-1", "cwd": os.TempDir(), "title": "External chat", "updatedAt": "2026-11-16T00:00:00Z"})
			}
			reply(map[string]any{"sessions": rows})
		case "session/set_config_option":
			var id, value string
			_ = json.Unmarshal(params["configId"], &id)
			_ = json.Unmarshal(params["value"], &value)
			switch {
			case id == "models-choice" && value == "model/b":
				options[0]["currentValue"] = value
				options = options[:2] // Model B has no reasoning control.
			case id == "trust" && value == "accept":
				options[1]["currentValue"] = value
			case id == "depth" && value == "deep":
				effort["currentValue"] = value
			default:
				os.Exit(4)
			}
			reply(map[string]any{"configOptions": options})
		case "session/cancel":
			select {
			case stop <- struct{}{}:
			default:
			}
		case "session/prompt":
			capturedID := append(json.RawMessage{}, msg["id"]...)
			var native string
			_ = json.Unmarshal(params["sessionId"], &native)
			var blocks []struct{ Text string }
			_ = json.Unmarshal(params["prompt"], &blocks)
			text := ""
			if len(blocks) > 0 {
				text = blocks[0].Text
			}
			go func() {
				finish := func(reason string) {
					write(map[string]any{"jsonrpc": "2.0", "id": capturedID, "result": map[string]string{"stopReason": reason}})
				}
				notify(native, "agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "Hello 🌏"}})
				if text == "block" {
					<-stop
					finish("cancelled")
					return
				}
				if text == "exit" {
					os.Exit(7)
				}
				write(map[string]any{"jsonrpc": "2.0", "id": "permission-1", "method": "session/request_permission", "params": map[string]any{"sessionId": native, "toolCall": map[string]any{"toolCallId": "tool-1", "title": "Edit a file"}, "options": []map[string]string{{"optionId": "forever", "name": "Always", "kind": "allow_always"}, {"optionId": "once", "name": "Once", "kind": "allow_once"}, {"optionId": "deny", "name": "Deny", "kind": "reject_once"}}}})
				result := <-permission
				var answer struct {
					Outcome struct {
						OptionID string `json:"optionId"`
					}
				}
				_ = json.Unmarshal(result, &answer)
				if answer.Outcome.OptionID != "once" {
					finish("refusal")
					return
				}
				notify(native, "agent_thought_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "private reasoning"}})
				notify(native, "agent_message_chunk", map[string]any{"content": map[string]string{"type": "text", "text": "\u2028world"}})
				notify(native, "tool_call", map[string]any{"toolCallId": "tool-1", "title": "Edit a file", "kind": "edit", "status": "completed", "rawOutput": "secret raw output"})
				finish("end_turn")
			}()
		}
	}
	os.Exit(0)
}
func fixtureProvider(t *testing.T, protocol, dir string, discover bool) *Provider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"REIGN_AGENT_FIXTURE": protocol}
	if discover {
		env["REIGN_AGENT_DISCOVER"] = "1"
	}
	p := New(context.Background(), protocol+"-test", config.Agent{Name: "Fixture", Protocol: protocol, Command: exe, Args: []string{"-test.run=TestAgentProcessFixture", "--"}, Env: env}, dir, nil)
	t.Cleanup(func() { _ = p.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return p
}
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
func TestACPSubprocessLifecycle(t *testing.T) {
	dir := t.TempDir()
	p := fixtureProvider(t, "acp", dir, false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if !p.Status(ctx).Ready() || p.Status(ctx).Capabilities.ImageInput {
		t.Fatal("status ignored negotiated capabilities")
	}
	s, err := p.Create(ctx, provider.CreateOptions{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	models, err := p.SessionModels(ctx, s.ID)
	if err != nil || len(models) != 2 {
		t.Fatalf("models=%v, err=%v", models, err)
	}
	epoch, after := p.log.Position()
	request, cancelRequest := context.WithCancel(ctx)
	if err = p.SendWithModel(request, s.ID, "test", "model/b"); err != nil {
		t.Fatal(err)
	}
	cancelRequest()
	eventually(t, func() bool { return len(p.Approvals()) == 1 })
	started, _ := p.Transcript(ctx, s.ID, "")
	if started.Session.RecencyAt <= 0 {
		t.Fatal("submission did not record recency")
	}
	title, update := "Renamed during streaming", time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	p.metadata(s.ID, &title, &update)
	progress, _ := p.Transcript(ctx, s.ID, "")
	if progress.Session.RecencyAt != started.Session.RecencyAt {
		t.Fatal("metadata/progress changed conversation recency")
	}
	a := p.Approvals()[0]
	if err = p.Decide(ctx, a.ID, provider.Decision{Decision: "invented"}); err == nil {
		t.Fatal("accepted an option the Agent did not offer")
	}
	if err = p.Decide(ctx, a.ID, provider.Decision{Decision: "accept"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { tr, _ := p.Transcript(ctx, s.ID, ""); return tr.Session.Status == "completed" })
	transcript, err := p.Transcript(ctx, s.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Session.RecencyAt < started.Session.RecencyAt || transcript.Session.RecencyAt != transcript.Turns[len(transcript.Turns)-1].RecencyAt {
		t.Fatal("completion boundary is missing from directory/history")
	}
	raw, _ := json.Marshal(transcript)
	if strings.Contains(string(raw), "private reasoning") || strings.Contains(string(raw), "secret raw output") {
		t.Fatal("leaked native reasoning or raw tool output")
	}
	if !strings.Contains(string(raw), "Hello 🌏") || !strings.Contains(string(raw), "world") {
		t.Fatal(string(raw))
	}
	batch, err := p.Probe(ctx, s.ID, epoch, after)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range batch.Events {
		if e.Kind == "text" && e.Delta == "\u2028world" {
			found = true
			if e.Offset != 8 {
				t.Fatalf("UTF-16 offset=%d, want8", e.Offset)
			}
		}
	}
	if !found {
		t.Fatal("missing incremental text")
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := fixtureProvider(t, "acp", dir, false)
	page, err := restarted.Sessions(ctx, "")
	if err != nil || len(page.Sessions) != 1 {
		t.Fatalf("restart sessions=%v err=%v", page, err)
	}
	if _, err = restarted.SessionModels(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Transcript(ctx, s.ID, "")
	if err != nil || len(replayed.Turns) != 1 || replayed.Turns[0].Items[1].Text != "old answer" {
		t.Fatalf("restore=%+v err=%v", replayed, err)
	}
}
func TestACPDiscoverLazyHistoryAndCancel(t *testing.T) {
	p := fixtureProvider(t, "acp", t.TempDir(), true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	page, err := p.Sessions(ctx, "")
	if err != nil || len(page.Sessions) != 1 {
		t.Fatalf("page=%v err=%v", page, err)
	}
	id := page.Sessions[0].ID
	p.mu.Lock()
	loaded := len(p.sessions[id].Turns) > 0
	p.mu.Unlock()
	if loaded {
		t.Fatal("list eagerly loaded message history")
	}
	tr, err := p.Transcript(ctx, id, "")
	if err != nil || len(tr.Turns) != 1 {
		t.Fatalf("history=%v err=%v", tr, err)
	}
	if err = p.Send(ctx, id, "block"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		tr, _ := p.Transcript(ctx, id, "")
		return len(tr.Turns) == 2 && len(tr.Turns[1].Items) > 1
	})
	if err = p.Interrupt(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { tr, _ := p.Transcript(ctx, id, ""); return tr.Session.Status == "interrupted" })
}
func TestPiNativeRPC(t *testing.T) {
	p := fixtureProvider(t, "pi", t.TempDir(), false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, err := p.Create(ctx, provider.CreateOptions{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	models, err := p.Models(ctx)
	if err != nil || len(models) != 1 || !models[0].ImageInput {
		t.Fatalf("models=%v err=%v", models, err)
	}
	if err = p.SendWithModel(ctx, s.ID, "hi", models[0].ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { tr, _ := p.Transcript(ctx, s.ID, ""); return tr.Session.Status == "completed" })
	tr, _ := p.Transcript(ctx, s.ID, "")
	if got := tr.Turns[0].Items[1].Text; got != "Hello 🌏\u2028world" {
		t.Fatalf("Pi text=%q", got)
	}
}
func TestAgentExitFinishesPendingTurn(t *testing.T) {
	p := fixtureProvider(t, "acp", t.TempDir(), false)
	ctx := context.Background()
	s, err := p.Create(ctx, provider.CreateOptions{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Send(ctx, s.ID, "exit"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { tr, _ := p.Transcript(ctx, s.ID, ""); return tr.Session.Status == "failed" })
}
func TestCatalogUsesRealTransports(t *testing.T) {
	for id, def := range Catalog(nil) {
		if err := Validate(id, def); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if err := Validate("codex-app", config.Agent{Protocol: "acp", Command: "x"}); err == nil {
		t.Fatal("overrode Codex reserved ID")
	}
	if err := Validate("pi", config.Agent{Protocol: "pi", Command: "pi", Args: []string{"--no-extensions"}}); err == nil {
		t.Fatal("allowed disabling the Pi permission hook")
	}
}

func TestPermissionTextInputAndSingleDecision(t *testing.T) {
	p := New(context.Background(), "test", config.Agent{}, t.TempDir(), nil)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan string, 1)
	go func() {
		initial := "prefill"
		value, err := p.requestPermission(ctx, "s", "Answer", nil, &initial)
		if err != nil {
			result <- "error"
			return
		}
		result <- value
	}()
	eventually(t, func() bool { return len(p.Approvals()) == 1 })
	id := p.Approvals()[0].ID
	if err := p.Decide(ctx, id, provider.Decision{Decision: "accept"}); err == nil {
		t.Fatal("accepted boolean answer for text input")
	}
	decision := provider.Decision{Decision: "input"}
	decision.Answers = map[string]struct {
		Answers []string `json:"answers"`
	}{"value": {Answers: []string{"my response"}}}
	if err := p.Decide(ctx, id, decision); err != nil {
		t.Fatal(err)
	}
	if err := p.Decide(ctx, id, decision); err == nil {
		t.Fatal("answered same request twice")
	}
	if got := <-result; got != "my response" {
		t.Fatalf("input=%q", got)
	}
}
func TestExternalAgentHandshake(t *testing.T) {
	command := os.Getenv("REIGN_AGENT_SMOKE_COMMAND")
	if command == "" {
		t.Skip("set REIGN_AGENT_SMOKE_COMMAND for an installed Agent handshake")
	}
	protocol := os.Getenv("REIGN_AGENT_SMOKE_PROTOCOL")
	if protocol == "" {
		protocol = "acp"
	}
	var args []string
	if raw := os.Getenv("REIGN_AGENT_SMOKE_ARGS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			t.Fatal(err)
		}
	}
	p := New(context.Background(), "external-smoke", config.Agent{Name: "External smoke", Protocol: protocol, Command: command, Args: args, Env: map[string]string{"CLAUDE_AGENT_ACP_EXPERIMENTAL_V2": "0", "PI_CODING_AGENT_DIR": t.TempDir()}}, t.TempDir(), nil)
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	status := p.Status(ctx)
	if !status.Ready() {
		t.Fatalf("not ready: %+v", status)
	}
	if protocol == "pi" {
		s, err := p.Create(ctx, provider.CreateOptions{CWD: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.SessionModels(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%s initialization succeeded; capabilities=%+v", protocol, status.Capabilities)
}

func TestSessionComposerChoicesByAgentAndModel(t *testing.T) {
	for _, protocol := range []string{"acp", "pi"} {
		t.Run(protocol, func(t *testing.T) {
			p := fixtureProvider(t, protocol, t.TempDir(), false)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			session, err := p.Create(ctx, provider.CreateOptions{CWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			read := func() map[string]string {
				controls, err := p.ComposerControls(ctx, session.ID, "")
				if err != nil {
					t.Fatal(err)
				}
				out := map[string]string{}
				for _, c := range controls {
					if c.ID == "acp:depth" && c.Kind != "effort" {
						t.Fatalf("lost reasoning category for %q", c.ID)
					}
					out[c.ID] = c.Selected
				}
				return out
			}
			set := func(id, v, model string) error {
				return p.ConfigureComposer(ctx, session.ID, provider.ComposerChange{Control: id, Value: v, Model: model})
			}
			initial := read()
			if protocol == "pi" {
				if len(initial) != 2 || initial["effort"] != "low" {
					t.Fatal(initial)
				}
				if err := set("permission", "full", ""); err == nil {
					t.Fatal("unsupported Pi permission accepted")
				}
				if err := set("effort", "high", "another-model"); err == nil {
					t.Fatal("stale Pi model accepted")
				}
				if err := set("effort", "high", initial["model"]); err != nil {
					t.Fatal(err)
				}
				if read()["effort"] != "high" {
					t.Fatal("Pi did not apply thinking level")
				}
				return
			}
			if initial["acp:trust"] != "ask" || initial["acp:depth"] != "short" {
				t.Fatal(initial)
			}
			for _, c := range []provider.ComposerChange{{Control: "acp:trust", Value: "accept"}, {Control: "acp:depth", Value: "deep"}} {
				if err := p.ConfigureComposer(ctx, session.ID, c); err != nil {
					t.Fatal(err)
				}
				if read()[c.Control] != c.Value {
					t.Fatal("native option not updated", c)
				}
			}
			if err := set("model", "model/b", "model/a"); err != nil {
				t.Fatal(err)
			}
			after := read()
			if after["model"] != "model/b" || after["acp:depth"] != "" || after["acp:trust"] != "accept" {
				t.Fatal(after)
			}
			if err := set("acp:depth", "deep", "model/b"); err == nil {
				t.Fatal("removed reasoning choice accepted")
			}
		})
	}
}
