package codex

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/events"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/rpc"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHistoryDoesNotResumeAndWriterConflictPreservesID(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			_, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var m rpc.Message
			_ = json.Unmarshal(raw, &m)
			if m.Method == "initialized" {
				continue
			}
			mu.Lock()
			methods = append(methods, m.Method)
			mu.Unlock()
			var result any = map[string]any{}
			switch m.Method {
			case "thread/read":
				result = map[string]any{"thread": map[string]any{"id": "original", "name": "Test", "model": "session-model", "status": map[string]string{"type": "notLoaded"}}}
			case "thread/turns/list":
				result = json.RawMessage(`{"data":[{"id":"turn-new","status":"completed","items":[{"id":"item-1","type":"agentMessage","phase":"final_answer","text":"answer"}]},{"id":"turn-old","status":"inProgress","items":[{"id":"item-2","type":"userMessage","content":[{"type":"text","text":"question"}]}]}],"nextCursor":"older"}`)
			case "thread/resume":
				if !strings.Contains(string(m.Params), "original") {
					t.Error("session ID changed")
				}
				response, _ := json.Marshal(map[string]any{"id": m.ID, "error": map[string]any{"code": 409, "message": "writer conflict"}})
				_ = ws.Write(r.Context(), websocket.MessageText, response)
				continue
			}
			response, _ := json.Marshal(map[string]any{"id": m.ID, "result": result})
			_ = ws.Write(r.Context(), websocket.MessageText, response)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	p := New(config.Config{}, "test", events.New())
	client, err := rpc.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), p.observe)
	if err != nil {
		t.Fatal(err)
	}
	p.client = client
	defer p.Close()
	transcript, err := p.Transcript(ctx, "original", "")
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Session.ID != "original" || transcript.NextCursor != "older" || transcript.Turns[0].ID != "turn-old" || transcript.Turns[1].Items[0].Text != "answer" {
		t.Fatalf("bad transcript: %+v", transcript)
	}
	if transcript.Session.Model != "session-model" {
		t.Fatalf("actual session model lost: %+v", transcript.Session)
	}
	if transcript.Session.ActiveTurnID != "" {
		t.Fatalf("stale historical turn treated as active: %+v", transcript.Session)
	}
	if err = p.Send(ctx, "original", "continue"); err == nil || !strings.Contains(err.Error(), "writer conflict") {
		t.Fatal("writer conflict not surfaced", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(methods, ",") != "initialize,thread/read,thread/turns/list,thread/resume" {
		t.Fatalf("unexpected methods: %v", methods)
	}
}

func TestApprovalClaimAndServerCleanup(t *testing.T) {
	p := New(config.Config{}, "test", events.New())
	var mu sync.Mutex
	answered := 0
	reply := func(context.Context, any) error { mu.Lock(); answered++; mu.Unlock(); return nil }
	msg := rpc.Message{ID: json.RawMessage(`10`), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn"}`)}
	id := p.addPending(msg, reply, "service")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.Decide(context.Background(), id, provider.Decision{Decision: "accept"}) }()
	}
	wg.Wait()
	if answered != 1 {
		t.Fatalf("answered %d times", answered)
	}
	p.addPending(msg, reply, "service")
	p.resolvePending("different-connection", rpc.Message{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"thread","requestId":10}`)})
	if len(p.Approvals()) != 1 {
		t.Fatal("another connection resolved an unrelated request")
	}
	p.observe(rpc.Message{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"thread","requestId":10}`)}, nil)
	if len(p.Approvals()) != 0 {
		t.Fatal("resolved request remained pending")
	}
	p.addPending(msg, reply, "service")
	p.observe(rpc.Message{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn"}}`)}, nil)
	if len(p.Approvals()) != 0 {
		t.Fatal("completed turn retained pending approval")
	}
}

func TestModelSelectionThroughControl(t *testing.T) {
	starts := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			_, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var m rpc.Message
			_ = json.Unmarshal(raw, &m)
			if m.Method == "initialized" {
				continue
			}
			result := any(map[string]any{})
			switch m.Method {
			case "model/list":
				var params map[string]any
				_ = json.Unmarshal(m.Params, &params)
				if params["cursor"] == "page-2" {
					result = json.RawMessage(`{"data":[{"model":"model-b","displayName":"Second model","isDefault":false}],"nextCursor":null}`)
				} else {
					result = json.RawMessage(`{"data":[{"model":"model-a","displayName":"First model","isDefault":true,"inputModalities":["text","image"]},{"model":"hidden","displayName":"Hidden model","hidden":true}],"nextCursor":"page-2"}`)
				}
			case "turn/start":
				var params map[string]any
				_ = json.Unmarshal(m.Params, &params)
				starts <- params
			}
			response, _ := json.Marshal(map[string]any{"id": m.ID, "result": result})
			_ = ws.Write(r.Context(), websocket.MessageText, response)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	p := New(config.Config{CodexCLI: "fixture"}, "", events.New())
	attachIPCFixture(t, p, func(m codexipc.Message) (codexipc.Message, bool) {
		if m.Method == "thread-owner-discovery" {
			return codexipc.Message{ResultType: "error", Error: "no-client-found"}, true
		}
		return codexipc.Message{}, false
	})
	client, err := rpc.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), p.observe)
	if err != nil {
		t.Fatal(err)
	}
	p.client = client
	defer p.Close()
	d := &control.Dispatcher{Providers: map[string]provider.Provider{"codex-app": p}}
	response := d.Handle(ctx, control.Request{Method: "models", Provider: "codex-app"})
	var models []provider.Model
	if response.Error != "" || json.Unmarshal(response.Result, &models) != nil || len(models) != 2 || models[0].ID != "model-a" || !models[0].Default || !models[0].ImageInput || models[1].ID != "model-b" || models[1].ImageInput {
		t.Fatalf("invalid paginated catalog: %+v %s", response, response.Result)
	}
	response = d.Handle(ctx, control.Request{Method: "send", Provider: "codex-app", Session: "fixture-thread", Text: "fixture message", Model: "model-b"})
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	params := <-starts
	if params["model"] != "model-b" || params["threadId"] != "fixture-thread" {
		t.Fatalf("model/session selection lost: %v", params)
	}
}
